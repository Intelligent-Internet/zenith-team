import { useMemberPresence } from "../model/use-member-presence";
import { useEffect, useMemo, useRef } from "react";
import { X } from "@/shared/ui/icons";
import { discussionsApi, type DiscussionDetail } from "../api/discussions-api";
import { useDiscussionUpdates } from "./discussion-provider";
import { DiscussionMessageRow } from "./discussion-message-row";
import { DiscussionTimeline } from "./discussion-timeline";
import { DiscussionComposer } from "./discussion-composer";

export function DiscussionThread({
  conversationId,
  rootId,
  detail,
  focusMessageId,
  onClose,
}: {
  conversationId: string;
  rootId: string;
  detail: DiscussionDetail;
  focusMessageId?: string;
  onClose: () => void;
}) {
  const runtime = useDiscussionUpdates();
  const presence = useMemberPresence(
    detail.members
      .filter((member) => member.active)
      .map((member) => member.userId),
  );
  const scope = useMemo(
    () => runtime.scope(conversationId, rootId),
    [runtime, conversationId, rootId],
  );
  const history =
    discussionsApi.endpoints.getDiscussionThread.useInfiniteQueryState(scope);
  const panel = useRef<HTMLElement>(null);
  useEffect(
    () => runtime.openThread(conversationId, rootId),
    [conversationId, rootId, runtime],
  );
  useEffect(() => {
    const previous = document.activeElement;
    panel.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected)
        previous.focus();
    };
  }, []);
  const status = runtime.status(conversationId, rootId);
  const ready =
    status?.phase === "ready" &&
    runtime.status(conversationId)?.phase === "ready";
  const page = history.currentData?.pages.reduce((latest, item) =>
    item.conversationRevision >= latest.conversationRevision ? item : latest,
  );
  const mutating = runtime.isMutating(conversationId);
  const composer = (
    <DiscussionComposer
      conversationType={detail.conversation.type}
      channelName={detail.conversation.title}
      conversationId={conversationId}
      rootId={rootId}
      title="thread"
      mentionMembers={[
        ...detail.members
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
        ...("agents" in detail
          ? detail.agents.map((agent) => ({
              id: agent.agentId,
              name: agent.name,
              description: "Agent",
              kind: "agent" as const,
            }))
          : []),
      ]}
      canSend={
        ready && Boolean(runtime.state(conversationId)?.capabilities.send)
      }
      readOnlyReason={
        detail.conversation.archivedAt
          ? "This channel is archived. Replies are read-only."
          : "Replies are currently unavailable."
      }
    />
  );
  return (
    <aside
      ref={panel}
      tabIndex={-1}
      className="discussion-thread-panel"
      aria-label="Thread"
      onKeyDown={(event) => {
        if (
          event.key === "Escape" &&
          !event.defaultPrevented &&
          !event.nativeEvent.isComposing
        ) {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <header className="discussion-thread-header">
        <div className="discussion-thread-heading">
          <h3>Thread</h3>
          <span>{detail.conversation.title}</span>
        </div>
        <button
          className="discussion-icon-button"
          aria-label="Close thread"
          onClick={onClose}
        >
          <X size={18} />
        </button>
      </header>
      {ready && page?.root && history.currentData ? (
        <DiscussionTimeline
          key={`${rootId}:${status?.guard}`}
          header={
            <>
              <div className="discussion-thread-root">
                <DiscussionMessageRow
                  message={page.root}
                  detail={detail}
                  onOpenThread={() =>
                    panel.current
                      ?.querySelector<HTMLTextAreaElement>(
                        ".discussion-composer textarea",
                      )
                      ?.focus()
                  }
                  inThread
                />
              </div>
              <div className="discussion-thread-replies">
                <span>
                  {page.root.threadSummary?.replyCount ?? 0}{" "}
                  {page.root.threadSummary?.replyCount === 1
                    ? "reply"
                    : "replies"}
                </span>
              </div>
            </>
          }
          conversationId={conversationId}
          rootId={rootId}
          focusMessageId={focusMessageId}
          detail={detail}
          pages={history.currentData.pages}
          busy={Boolean(status.busy) || mutating}
          onOpenThread={() =>
            panel.current
              ?.querySelector<HTMLTextAreaElement>(
                ".discussion-composer textarea",
              )
              ?.focus()
          }
        />
      ) : (
        <>
          <div className="discussion-empty" role="status">
            {status?.error ?? "Loading thread…"}
            {status?.error ? (
              <button
                className="discussion-button"
                onClick={() => runtime.schedule()}
              >
                Try again
              </button>
            ) : null}
          </div>
        </>
      )}
      {composer}
    </aside>
  );
}
