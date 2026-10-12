import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { discussionsApi } from "@/features/discussions/api/discussions-api";
import {
  ChannelAgentPanel,
  ChannelAgentsProvider,
  useChannelAgents,
} from "@/features/discussions/components/channel-agents";
import { CompactDiscussionProvider } from "@/features/discussions/components/discussion-compact";
import { DiscussionComposer } from "@/features/discussions/components/discussion-composer";
import {
  DiscussionPreviewPanel,
  DiscussionPreviewProvider,
  useDiscussionPreview,
} from "@/features/discussions/components/discussion-preview";
import { useDiscussionUpdates } from "@/features/discussions/components/discussion-provider";
import { DiscussionThread } from "@/features/discussions/components/discussion-thread";
import { DiscussionTimeline } from "@/features/discussions/components/discussion-timeline";
import { discussionError } from "@/features/discussions/model/discussion-runtime";
import { useMemberPresence } from "@/features/discussions/model/use-member-presence";
import { useReadDiscussionNotifications } from "@/features/discussions/model/use-read-discussion-notifications";
import "@/features/discussions/styles/discussions.css";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
import { LockKeyhole, MessageSquareText } from "@/shared/ui/icons";
import {
  clearVisibleConversation,
  setVisibleConversation,
} from "@/state/slice/conversation-dock";
import store from "@/state/store";
import { whiteboardApi, type BoardScope } from "./board-api";

type BoardChannel = components["schemas"]["BoardChannel"];

/** A message or thread a link asked the Board's chat to open. */
export type BoardChatFocus = {
  conversationId: string;
  messageId?: string;
  threadRootId?: string;
};

// Presence is read for at most this many people at once.
const presenceLimit = 49;

/**
 * The Board's chat, docked beside it: one channel the Board's readers share,
 * where they @mention each other and the Agents invited to the Board. An
 * Agent tagged here works where the person who tagged it is looking, and its
 * answer lands in the thread of the message that tagged it.
 */
export function BoardChatDock({
  scope,
  title,
  focus,
}: {
  scope: BoardScope;
  title: string;
  focus?: BoardChatFocus;
}) {
  const [join] = whiteboardApi.useJoinWhiteboardChannelMutation();
  const channelQuery = whiteboardApi.useWhiteboardChannelQuery(scope);
  const [error, setError] = useState<string | null>(null);
  // Bumped by "Try again" to open the chat once more.
  const [attempt, setAttempt] = useState(0);
  const { companyId, boardId } = scope;
  useEffect(() => {
    let cancelled = false;
    setError(null);
    join({ companyId, boardId })
      .unwrap()
      .catch((cause) => {
        if (!cancelled)
          setError(
            getBoardlyQueryErrorMessage(
              cause,
              "The Board's chat could not be opened.",
            ),
          );
      });
    return () => {
      cancelled = true;
    };
  }, [join, companyId, boardId, attempt]);
  const channel = channelQuery.currentData;
  const conversationId = channel?.joined ? channel.conversationId : null;
  // Reading the chat fails for good once the Board is out of reach; any
  // other failure, of the read or of opening, can be tried again.
  const readError = channelQuery.isError ? channelQuery.error : undefined;
  const lost =
    !!readError &&
    "status" in readError &&
    (readError.status === 403 || readError.status === 404);
  const failure =
    error ??
    (readError
      ? getBoardlyQueryErrorMessage(
          readError,
          "The Board's chat could not be loaded.",
        )
      : null);
  return (
    <aside className="wb-chat-dock" aria-label="Board chat">
      {lost ? (
        <BoardChatUnavailable />
      ) : failure ? (
        <div className="discussion-pane wb-chat-dock__pane">
          <div className="discussion-empty" role="alert">
            <p>{failure}</p>
            <button
              type="button"
              className="discussion-button"
              onClick={() => {
                setAttempt((current) => current + 1);
                void channelQuery.refetch();
              }}
            >
              Try again
            </button>
          </div>
        </div>
      ) : conversationId && channel ? (
        <CompactDiscussionProvider value>
          <ChannelAgentsProvider
            key={conversationId}
            conversationId={conversationId}
            enabled
          >
            <DiscussionPreviewProvider key={conversationId}>
              <BoardChatPanel
                title={title}
                channel={channel}
                conversationId={conversationId}
                focus={
                  focus?.conversationId === conversationId ? focus : undefined
                }
              />
            </DiscussionPreviewProvider>
          </ChannelAgentsProvider>
        </CompactDiscussionProvider>
      ) : (
        <p className="wb-chat-dock__status" role="status">
          Opening the chat…
        </p>
      )}
    </aside>
  );
}

/** The chat of a Board the reader can no longer reach. */
function BoardChatUnavailable() {
  return (
    <div className="discussion-pane wb-chat-dock__pane">
      <div className="discussion-empty" role="status">
        <LockKeyhole size={30} />
        <h2>Chat unavailable</h2>
        <p>You may no longer have access to this Board.</p>
      </div>
    </div>
  );
}

function BoardChatPanel({
  title,
  channel,
  conversationId: id,
  focus,
}: {
  title: string;
  channel: BoardChannel;
  conversationId: string;
  focus?: BoardChatFocus;
}) {
  const runtime = useDiscussionUpdates();
  const preview = useDiscussionPreview();
  const channelAgents = useChannelAgents();
  const scopeKey = useMemo(() => runtime.scope(id), [id, runtime]);
  const address = useMemo(
    () => ({
      ...runtime.session,
      conversationId: id,
      type: "channel" as const,
    }),
    [id, runtime],
  );
  const detail = discussionsApi.useGetDiscussionDetailQuery(address);
  const history =
    discussionsApi.endpoints.getDiscussionHistory.useInfiniteQueryState(
      scopeKey,
    );
  const [rootId, setRootId] = useState<string>();
  const [focusMessageId, setFocusMessageId] = useState<string>();
  const [error, setError] = useState("");
  const navigation = useRef(0);
  const deepLink = useRef<string | undefined>(undefined);
  useLayoutEffect(() => runtime.open(id), [id, runtime]);
  const status = runtime.status(id);
  const state = runtime.state(id);
  const data = detail.currentData;
  const ready =
    status?.visible === true &&
    status?.phase === "ready" &&
    Boolean(state?.capabilities.readHistory) &&
    !detail.isError;
  const conversation = ready ? data?.conversation : undefined;
  const panelId = useId();
  useEffect(() => {
    if (!ready) return;
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
  }, [ready, runtime, id, panelId, rootId, channelAgents.selected]);
  useReadDiscussionNotifications(
    runtime.session.companyId,
    id,
    panelId,
    ready,
    channelAgents.selected ? undefined : rootId,
  );
  const openThread = useCallback(
    (root: string) => {
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
    const key = focus?.messageId ?? focus?.threadRootId;
    if (!key || deepLink.current === key) return;
    deepLink.current = key;
    if (focus?.messageId) void focusMessage(focus.messageId);
    else if (focus?.threadRootId) openThread(focus.threadRootId);
  }, [ready, focus?.messageId, focus?.threadRootId, focusMessage, openThread]);
  // No URL change on unmount: it would resolve against the Board's route and
  // replace the page being navigated to. Closing the chat clears the preview.
  useEffect(
    () => () => {
      navigation.current += 1;
    },
    [],
  );

  // Everyone who can read the Board can be mentioned; mentioning someone who
  // has not opened the chat yet brings them in.
  const others = useMemo(
    () =>
      channel.people.filter(
        (person) => person.userId !== runtime.session.userId,
      ),
    [channel.people, runtime.session.userId],
  );
  const presence = useMemberPresence(
    others.slice(0, presenceLimit).map((person) => person.userId),
  );
  const mentionMembers = useMemo<ChatMentionMember[]>(
    () =>
      others.map((person) => {
        const seen = presence.currentData?.items.find(
          (item) => item.userId === person.userId,
        );
        return {
          id: person.userId,
          name: person.displayName,
          description: seen
            ? seen.online
              ? "Online"
              : "Away"
            : person.joined
              ? "In this chat"
              : "Can view this Board",
          avatarSrc: person.avatarUrl,
          kind: "human",
        };
      }),
    [others, presence.currentData],
  );
  const canSend = ready && Boolean(state?.capabilities.send);
  const readOnlyReason = !ready
    ? "Waiting for the connection and chat access."
    : "This Board is archived, so its chat is read-only.";

  if (
    status?.phase === "revoked" ||
    (detail.isError &&
      "status" in detail.error &&
      [403, 404].includes(Number(detail.error.status)))
  )
    return <BoardChatUnavailable />;
  let body: React.ReactNode;
  if (ready && preview.isOpen) {
    body = (
      <DiscussionPreviewPanel
        companyId={runtime.session.companyId}
        conversationId={id}
        backLabel="Back to chat"
      />
    );
  } else if (ready && channelAgents.selected) {
    body = (
      <ChannelAgentPanel
        conversationId={id}
        conversationTitle={title}
        onBack={(threadId) => {
          if (threadId) setRootId(threadId);
        }}
      />
    );
  } else if (ready && rootId && data) {
    body = (
      <DiscussionThread
        key={rootId}
        conversationId={id}
        rootId={rootId}
        detail={data}
        focusMessageId={focusMessageId}
        mentionMembers={mentionMembers}
        title={title}
        onClose={() => {
          navigation.current += 1;
          setRootId(undefined);
        }}
      />
    );
  } else {
    body = (
      <div className="discussion-main-stream">
        {error ? (
          <p className="wb-chat-dock__status" role="alert">
            {error}
          </p>
        ) : null}
        {status?.phase === "error" || detail.isError ? (
          <div className="discussion-empty" role="alert">
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
        ) : ready && history.currentData && data ? (
          <DiscussionTimeline
            conversationId={id}
            detail={data}
            pages={history.currentData.pages}
            busy={Boolean(status?.busy)}
            key={`root:${status?.guard}:${focusMessageId ?? ""}`}
            focusMessageId={!rootId ? focusMessageId : undefined}
            onOpenThread={openThread}
            empty={
              <>
                <MessageSquareText size={30} aria-hidden="true" />
                <h3>Chat about this Board</h3>
                <p>Type @ to mention a teammate or an Agent.</p>
              </>
            }
          />
        ) : (
          <div className="discussion-empty" role="status">
            Opening the chat…
          </div>
        )}
        <DiscussionComposer
          key={id}
          placeholder="Message, or type @ to mention someone"
          conversationType={conversation?.type}
          channelName={title}
          conversationId={id}
          title={title}
          mentionMembers={mentionMembers}
          canSend={canSend}
          readOnlyReason={readOnlyReason}
        />
      </div>
    );
  }
  return (
    <div className="discussion-pane wb-chat-dock__pane">
      <div className="discussion-content">{body}</div>
    </div>
  );
}
