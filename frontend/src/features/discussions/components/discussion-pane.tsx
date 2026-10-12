import {
  ChannelAgentsProvider,
  ChannelAgentPanel,
  useChannelAgents,
} from "./channel-agents";
import { DiscussionSearch } from "./discussion-search";
import { DiscussionOptionsMenu } from "./discussion-options-menu";
import { useReadDiscussionNotifications } from "../model/use-read-discussion-notifications";
import type { components } from "@/shared/api/generated/api.generated";
import { useMemberPresence } from "../model/use-member-presence";
import store from "@/state/store";
import {
  setVisibleConversation,
  clearVisibleConversation,
} from "@/state/slice/conversation-dock";
import {
  useCallback,
  useId,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Circle,
  Search,
  Files,
  Hash,
  LockKeyhole,
  MessageSquare,
  Headphones,
  Pin,
  RadioIndicator,
  Users,
} from "@/shared/ui/icons";
import { discussionsApi } from "../api/discussions-api";
import {
  openHuddleStartWindow,
  prepareHuddleWindow,
} from "../../huddles/huddle-window";
import { useDiscussionUpdates } from "./discussion-provider";
import { DiscussionComposer } from "./discussion-composer";
import { DiscussionTimeline } from "./discussion-timeline";
import { DiscussionThread } from "./discussion-thread";
import { DiscussionArtifacts } from "./discussion-artifacts";
import {
  DiscussionPreviewProvider,
  DiscussionPreviewPanel,
  useDiscussionPreview,
} from "./discussion-preview";
import { DiscussionPins, useDiscussionPins } from "./discussion-pins";
import { GroupAboutDialog, GroupManagementPane } from "./group-about";
import { DiscussionAvatar } from "./discussion-identity";
import { discussionError } from "../model/discussion-runtime";
import type { DiscussionSelection } from "../model/use-discussion-navigation";
import { Tooltip } from "@/shared/ui/tooltip";

export function DiscussionPane({
  selection,
  onSelect,
  onClose,
}: {
  selection: DiscussionSelection;
  onSelect: (selection: DiscussionSelection) => void;
  onClose: () => void;
}) {
  if (selection.manage)
    return (
      <GroupManagementPane
        conversationId={selection.conversationId}
        onOpenChat={() => onSelect({ ...selection, manage: false })}
        onLeave={onClose}
      />
    );
  return (
    <ChannelAgentsProvider
      key={selection.conversationId}
      conversationId={selection.conversationId}
      enabled
    >
      <DiscussionPreviewProvider key={selection.conversationId}>
        <DiscussionChat selection={selection} onClose={onClose} />
      </DiscussionPreviewProvider>
    </ChannelAgentsProvider>
  );
}

function DiscussionChat({
  selection,
  onClose,
}: Omit<Parameters<typeof DiscussionPane>[0], "onSelect">) {
  const runtime = useDiscussionUpdates();
  const preview = useDiscussionPreview();
  const channelAgents = useChannelAgents();
  const id = selection.conversationId;
  const scope = useMemo(() => runtime.scope(id), [id, runtime]);
  const address = useMemo(
    () => ({ ...runtime.session, conversationId: id, type: selection.type }),
    [id, runtime, selection.type],
  );
  const detail = discussionsApi.useGetDiscussionDetailQuery(address);
  const history =
    discussionsApi.endpoints.getDiscussionHistory.useInfiniteQueryState(scope);
  const [about, setAbout] = useState(false);
  const [aboutTab, setAboutTab] = useState<"about" | "members" | "agents">(
    "about",
  );
  const [searchOpen, setSearchOpen] = useState(false);
  const [tab, setTab] = useState<"all" | "pinned" | "artifacts">("all");
  const [rootId, setRootId] = useState<string>();
  const [focusMessageId, setFocusMessageId] = useState<string>();
  const navigation = useRef(0);
  const deepLink = useRef<string | undefined>(undefined);
  const [preference, mutation] =
    discussionsApi.useUpdateDiscussionPersonalStateMutation();
  const [changeChannel, channelMutation] =
    discussionsApi.useChangeChatChannelMutation();
  const revealedOnOpen = useRef(false);
  const [error, setError] = useState("");
  useLayoutEffect(() => runtime.open(id), [id, runtime]);
  const status = runtime.status(id);
  const state = runtime.state(id);
  const data = detail.currentData;
  const presence = useMemberPresence(
    data?.members
      .filter((member) => member.active)
      .map((member) => member.userId) ?? [],
  );
  const ready =
    status?.visible === true &&
    status?.phase === "ready" &&
    Boolean(state?.capabilities.readHistory) &&
    !detail.isError;
  const conversation = ready ? data?.conversation : undefined;
  const canLeave =
    conversation?.type === "channel" &&
    !conversation.isGeneral &&
    Boolean(
      data?.members.some(
        (member) => member.userId === runtime.session.userId && member.active,
      ),
    );
  useEffect(() => {
    if (!ready || !state || revealedOnOpen.current) return;
    revealedOnOpen.current = true;
    if (selection.type !== "human_direct" || !state.userState?.hiddenAt) return;
    // Reopening a DM from search, a link, or the recipient picker returns it
    // to the chat list. Archiving an already open chat must not reveal it again.
    void preference({
      ...scope,
      body: { expectedRevision: state.userState.revision, hidden: false },
    })
      .unwrap()
      .then(() => {
        if (runtime.active) runtime.schedule(true);
      })
      .catch((cause) => {
        if (runtime.active) setError(discussionError(cause));
      });
  }, [ready, state, selection.type, scope, preference, runtime]);
  const artifactsVisible = tab === "artifacts";
  const panelId = useId();
  useEffect(() => {
    if (!ready || tab !== "all") return;
    store.dispatch(
      setVisibleConversation({
        companyId: runtime.session.companyId,
        conversationId: id,
        panelId,
        discussion: true,
        threadRootId: channelAgents.selected ? undefined : rootId,
      }),
    );
    return () => {
      store.dispatch(clearVisibleConversation(panelId));
    };
  }, [ready, tab, runtime, id, panelId, rootId, channelAgents.selected]);
  useReadDiscussionNotifications(
    runtime.session.companyId,
    id,
    panelId,
    ready && tab === "all",
    channelAgents.selected ? undefined : rootId,
  );
  const canSend = ready && Boolean(state?.capabilities.send);
  const pins = useDiscussionPins(id, ready);
  const openThread = useCallback(
    (root: string) => {
      setTab("all");
      preview.close();
      channelAgents.close();
      navigation.current += 1;
      setFocusMessageId(undefined);
      setRootId(root);
    },
    [channelAgents.close, preview.close],
  );
  const focusMessage = useCallback(
    async (messageId: string) => {
      const attempt = ++navigation.current;
      setError("");
      try {
        const context = await runtime.focusMessage(id, messageId);
        if (!context || attempt !== navigation.current || !runtime.active)
          return;
        setTab("all");
        setRootId(context.rootId);
        setFocusMessageId(messageId);
      } catch (cause) {
        if (attempt === navigation.current) setError(discussionError(cause));
      }
    },
    [id, runtime],
  );
  useEffect(() => {
    if (!ready) return;
    const key = selection.messageId ?? selection.threadRootId;
    if (!key || deepLink.current === key) return;
    deepLink.current = key;
    if (selection.messageId) void focusMessage(selection.messageId);
    else if (selection.threadRootId) openThread(selection.threadRootId);
  }, [
    ready,
    selection.messageId,
    selection.threadRootId,
    focusMessage,
    openThread,
  ]);
  useEffect(
    () => () => {
      navigation.current += 1;
    },
    [],
  );
  async function changePreference(body: {
    notificationLevel?: components["schemas"]["UpdateDiscussionStateRequest"]["notificationLevel"];
    pinned?: boolean;
    hidden?: boolean;
  }) {
    if (!ready || !state || mutation.isLoading || channelMutation.isLoading)
      return;
    setError("");
    try {
      await preference({
        ...scope,
        body: { ...body, expectedRevision: state.userState?.revision ?? 0 },
      }).unwrap();
      if (!runtime.active) return;
      runtime.schedule(true);
      if (body.hidden) onClose();
    } catch (cause) {
      setError(discussionError(cause));
    }
  }
  async function leaveChannel() {
    if (
      !canLeave ||
      !conversation ||
      mutation.isLoading ||
      channelMutation.isLoading
    )
      return;
    setError("");
    try {
      await changeChannel({
        ...runtime.session,
        conversationId: id,
        expectedRevision: conversation.configurationRevision ?? 0,
        action: "leave",
      }).unwrap();
      if (!runtime.active) return;
      runtime.revoke(id);
      runtime.schedule(true);
      onClose();
    } catch (cause) {
      if (runtime.active) setError(discussionError(cause));
    }
  }
  if (
    status?.phase === "revoked" ||
    (detail.isError &&
      "status" in detail.error &&
      [403, 404].includes(Number(detail.error.status)))
  )
    return (
      <div className="discussion-pane">
        <div className="discussion-empty" role="status">
          <LockKeyhole size={30} />
          <h2>Conversation unavailable</h2>
          <p>You may no longer have access to this conversation.</p>
          <button className="discussion-button" onClick={onClose}>
            Back to chats
          </button>
        </div>
      </div>
    );
  const peer = conversation?.peer;
  const peerPresence = peer?.active
    ? presence.currentData?.items.find(
        (member) => member.userId === peer.userId,
      )
    : undefined;
  const peerPresenceLabel = peerPresence
    ? peerPresence.online
      ? "Active"
      : "Away"
    : undefined;
  const PeerPresenceIcon = peerPresence?.online ? RadioIndicator : Circle;
  return (
    <div className="discussion-pane">
      <div
        className={`discussion-content artifacts-layout ${preview.isOpen ? "discussion-content--preview" : ""} ${(rootId || channelAgents.selected) && ready && !searchOpen && !artifactsVisible && !preview.isOpen ? "discussion-content--thread" : ""} ${searchOpen && ready && !artifactsVisible && !preview.isOpen ? "discussion-content--search" : ""}`}
      >
        <div className="discussion-main-stream">
          <header className="discussion-header">
            <button
              className="discussion-header-identity"
              onClick={() => {
                setAboutTab("about");
                setAbout(true);
              }}
              disabled={!ready || !data}
              aria-label={[
                "Open conversation details",
                peer?.displayName,
                peerPresenceLabel,
              ]
                .filter(Boolean)
                .join(", ")}
            >
              {selection.type === "channel" ||
              (conversation && !conversation.peer) ? (
                <span className="discussion-group-avatar">
                  {conversation?.isGeneral ? (
                    <Hash size={18} />
                  ) : (
                    <LockKeyhole size={18} />
                  )}
                </span>
              ) : (
                <span
                  className="discussion-header-avatar"
                  title={peerPresenceLabel}
                >
                  <DiscussionAvatar
                    name={peer?.displayName ?? conversation?.title ?? "Person"}
                    src={peer?.avatarUrl}
                    userId={peer?.userId}
                  />
                  {peerPresence ? (
                    <span
                      className="discussion-header-presence"
                      data-online={peerPresence.online || undefined}
                      aria-hidden="true"
                    >
                      <PeerPresenceIcon size={12} />
                    </span>
                  ) : null}
                </span>
              )}
              <span>
                <h2 title={conversation?.title}>
                  {conversation?.title ?? "Conversation"}
                </h2>
                {selection.type !== "channel" && peer?.active === false ? (
                  <small>No longer active · read-only</small>
                ) : null}
              </span>
            </button>
            <div className="discussion-header-actions">
              {!artifactsVisible ? (
                <Tooltip
                  label="Search messages"
                  disabled={!ready || !state?.capabilities.search}
                >
                  <button
                    className="discussion-icon-button"
                    disabled={!ready || !state?.capabilities.search}
                    aria-label="Search messages"
                    onClick={() => {
                      preview.close();
                      setSearchOpen((value) => !value);
                    }}
                  >
                    <Search size={18} aria-hidden="true" />
                  </button>
                </Tooltip>
              ) : null}
              <Tooltip label="View members and details" disabled={!ready}>
                <button
                  className="discussion-icon-button discussion-members-button"
                  disabled={!ready}
                  aria-label="View members and details"
                  onClick={() => {
                    setAboutTab("members");
                    setAbout(true);
                  }}
                >
                  <Users size={18} aria-hidden="true" />
                  {data ? (
                    <span>
                      {data.members.filter((member) => member.active).length +
                        (data.conversation.type !== "channel" &&
                        "agents" in data
                          ? data.agents.length
                          : 0)}
                    </span>
                  ) : null}
                </button>
              </Tooltip>
              <Tooltip label="Start Huddle" disabled={!ready}>
                <button
                  className="discussion-icon-button"
                  disabled={!ready}
                  aria-label="Start Huddle"
                  onClick={() => {
                    // The lobby creates the Huddle when Start is pressed, so
                    // nobody rings while the caller is still choosing devices.
                    const popup = prepareHuddleWindow();
                    void openHuddleStartWindow(
                      selection.conversationId,
                      popup,
                    ).then((opened) => {
                      if (!opened) {
                        setError(
                          "Your browser blocked the Huddle window. Allow popups for Zenith and try again.",
                        );
                      }
                    });
                  }}
                >
                  <Headphones size={17} aria-hidden="true" />
                </button>
              </Tooltip>
              <DiscussionOptionsMenu
                isChannel={selection.type === "channel"}
                pinned={Boolean(state?.userState?.pinnedAt)}
                notificationLevel={state?.userState?.notificationLevel ?? "all"}
                disabled={!ready}
                pending={mutation.isLoading || channelMutation.isLoading}
                onAbout={
                  selection.type === "channel"
                    ? () => {
                        setAboutTab("about");
                        setAbout(true);
                      }
                    : undefined
                }
                onAgents={
                  selection.type === "channel"
                    ? () => {
                        setAboutTab("agents");
                        setAbout(true);
                      }
                    : undefined
                }
                onTogglePin={() =>
                  void changePreference({ pinned: !state?.userState?.pinnedAt })
                }
                onNotificationChange={(notificationLevel) =>
                  void changePreference({ notificationLevel })
                }
                onLeave={canLeave ? () => void leaveChannel() : undefined}
              />
            </div>
          </header>
          {error ? (
            <p role="alert" className="discussion-error">
              {error}
            </p>
          ) : null}
          <nav className="discussion-view-tabs" aria-label="Conversation views">
            <button
              aria-current={tab === "all" ? "page" : undefined}
              onClick={() => {
                setTab("all");
                preview.close();
                setRootId(undefined);
              }}
            >
              All
            </button>
            <button
              aria-current={tab === "pinned" ? "page" : undefined}
              onClick={() => {
                setTab("pinned");
                preview.close();
                setRootId(undefined);
              }}
            >
              <Pin size={14} />
              Pinned
              {ready && pins.currentData
                ? ` (${pins.currentData.pages[0]?.totalCount ?? 0})`
                : ""}
            </button>
            <button
              aria-current={artifactsVisible ? "page" : undefined}
              onClick={() => {
                setTab("artifacts");
                setRootId(undefined);
                setSearchOpen(false);
                channelAgents.close();
              }}
            >
              <Files size={14} aria-hidden />
              Artifacts
            </button>
          </nav>

          {status?.phase === "error" || detail.isError ? (
            <div className="discussion-empty" role="alert">
              <MessageSquare size={28} />
              <h3>Couldn’t refresh this conversation</h3>
              <p>{status?.error ?? discussionError(detail.error)}</p>
              <button
                className="discussion-button"
                onClick={() => {
                  runtime.schedule(true);
                  void detail.refetch();
                }}
              >
                Try again
              </button>
            </div>
          ) : ready && artifactsVisible ? (
            <DiscussionArtifacts
              companyId={runtime.session.companyId}
              conversationId={id}
            />
          ) : ready && data && tab === "pinned" ? (
            <DiscussionPins
              query={pins}
              detail={data}
              onFocus={(messageId) => void focusMessage(messageId)}
              onOpenThread={openThread}
            />
          ) : ready && history.currentData && data ? (
            <DiscussionTimeline
              conversationId={id}
              detail={data}
              pages={history.currentData.pages}
              busy={Boolean(status?.busy)}
              key={`root:${status?.guard}:${focusMessageId ?? ""}`}
              focusMessageId={!rootId ? focusMessageId : undefined}
              onOpenThread={openThread}
            />
          ) : (
            <div className="discussion-empty" role="status">
              Checking conversation access…
            </div>
          )}
          {tab === "all" ? (
            <DiscussionComposer
              key={id}
              conversationType={conversation?.type}
              channelName={conversation?.title}
              conversationId={id}
              title={peer?.displayName ?? conversation?.title ?? "conversation"}
              mentionMembers={
                data
                  ? [
                      ...data.members
                        .filter((member) => member.active)
                        .map((member) => ({
                          id: member.userId,
                          name: member.displayName,
                          description: presence.currentData?.items.find(
                            (item) => item.userId === member.userId,
                          )?.online
                            ? "Online"
                            : presence.currentData?.items.some(
                                  (item) => item.userId === member.userId,
                                )
                              ? "Away"
                              : "Status unavailable",
                          avatarSrc: member.avatarUrl,
                          kind: "human" as const,
                        })),
                      ...("agents" in data
                        ? data.agents.map((agent) => ({
                            id: agent.agentId,
                            name: agent.name,
                            description: "Agent",
                            kind: "agent" as const,
                          }))
                        : []),
                    ]
                  : []
              }
              canSend={canSend}
              readOnlyReason={
                !ready
                  ? "Waiting for the connection and conversation access."
                  : conversation?.archivedAt
                    ? "This channel is archived. Restore it to send messages."
                    : peer?.active === false
                      ? "This person is no longer active. Your history is still available."
                      : "You can read this conversation. Sending is currently unavailable."
              }
            />
          ) : null}
        </div>
        {ready && preview.isOpen ? (
          <DiscussionPreviewPanel
            companyId={runtime.session.companyId}
            conversationId={id}
            backLabel={
              artifactsVisible ? "Back to artifacts" : "Back to conversation"
            }
          />
        ) : null}
        {searchOpen && !artifactsVisible && !preview.isOpen && ready && data ? (
          <DiscussionSearch
            conversationId={id}
            detail={data}
            onClose={() => setSearchOpen(false)}
            onOpenMessage={(messageId) => {
              setSearchOpen(false);
              void focusMessage(messageId);
            }}
          />
        ) : null}
        <div style={{ display: channelAgents.selected ? "none" : "contents" }}>
          {rootId &&
          !searchOpen &&
          !artifactsVisible &&
          !preview.isOpen &&
          ready &&
          data ? (
            <DiscussionThread
              key={rootId}
              conversationId={id}
              rootId={rootId}
              detail={data}
              focusMessageId={focusMessageId}
              onClose={() => {
                navigation.current += 1;
                setRootId(undefined);
              }}
            />
          ) : null}
        </div>
        {ready && !searchOpen && !artifactsVisible && !preview.isOpen ? (
          <ChannelAgentPanel
            conversationId={id}
            conversationTitle={conversation?.title ?? "conversation"}
            onBack={(threadId) => {
              if (threadId) setRootId(threadId);
            }}
          />
        ) : null}
      </div>
      {data && ready ? (
        <GroupAboutDialog
          detail={data}
          initialTab={aboutTab}
          open={about}
          onOpenChange={setAbout}
          onLeave={onClose}
        />
      ) : null}
    </div>
  );
}
