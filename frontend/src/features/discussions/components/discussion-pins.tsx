import { useEffect, useRef } from "react";
import { Pin, MessageSquare } from "@/shared/ui/icons";
import store from "@/state/store";
import { discussionsApi, type DiscussionDetail } from "../api/discussions-api";
import { discussionError } from "../model/discussion-runtime";
import { useDiscussionUpdates } from "./discussion-provider";
import { DiscussionMessageRow } from "./discussion-message-row";

export function useDiscussionPins(conversationId: string, ready: boolean) {
  const runtime = useDiscussionUpdates();
  const args = { ...runtime.session, conversationId };
  const query = discussionsApi.useGetDiscussionPinsInfiniteQuery(args, {
    skip: !ready,
  });
  const revision = runtime.state(conversationId)?.conversationRevision;
  const busy = runtime.isMutating(conversationId);
  const denied =
    query.isError &&
    "status" in query.error &&
    [403, 404].includes(Number(query.error.status));
  useEffect(() => {
    if (denied) runtime.revoke(conversationId);
  }, [denied, runtime, conversationId]);
  const attempted = useRef<number | undefined>(undefined);
  const received = query.currentData?.pages[0]?.conversationRevision;
  useEffect(() => {
    if (
      !ready ||
      busy ||
      query.isFetching ||
      !received ||
      !revision ||
      received === revision ||
      attempted.current === revision
    )
      return;
    attempted.current = revision;
    // Pins are a collection: start at its head after a mutation even when the
    // bounded page window previously evicted its first page.
    store.dispatch(
      discussionsApi.util.updateQueryData(
        "getDiscussionPins",
        { ...runtime.session, conversationId },
        (data) => {
          data.pageParams[0] = null;
        },
      ),
    );
    void query.refetch();
  }, [
    ready,
    busy,
    query.isFetching,
    query.refetch,
    received,
    revision,
    runtime,
    conversationId,
  ]);
  return query;
}

export function DiscussionPins({
  query,
  detail,
  onFocus,
  onOpenThread,
}: {
  query: ReturnType<typeof useDiscussionPins>;
  detail: DiscussionDetail;
  onFocus: (messageId: string) => void;
  onOpenThread: (rootId: string) => void;
}) {
  const runtime = useDiscussionUpdates();
  const busy = runtime.isMutating(detail.conversation.id);
  const denied =
    query.isError &&
    "status" in query.error &&
    [403, 404].includes(Number(query.error.status));
  const pins = denied
    ? []
    : (query.currentData?.pages.flatMap((page) => page.items) ?? []);
  return (
    <div className="discussion-pins" aria-label="Pinned messages">
      <p className="discussion-pins-explanation">
        <Pin size={15} /> Messages pinned here are shared with everyone in this
        conversation.
      </p>
      {pins.map((pin) => (
        <div key={pin.message.id} className="discussion-pin-card">
          {pin.root ? (
            <p className="discussion-pin-context">
              Reply to {pin.root.author.displayName}:{" "}
              {pin.root.deletedAt
                ? "Deleted message"
                : pin.root.body?.markdown.slice(0, 140)}
            </p>
          ) : null}
          <DiscussionMessageRow
            message={pin.message}
            detail={detail}
            onOpenThread={onOpenThread}
            inThread={Boolean(pin.root)}
          />
          <button
            className="discussion-pin-jump"
            onClick={() => onFocus(pin.message.id)}
            disabled={busy}
          >
            <MessageSquare size={14} />
            {pin.root ? "Open in thread" : "View in conversation"}
          </button>
        </div>
      ))}
      {query.isError ? (
        <p className="discussion-error" role="alert">
          {discussionError(query.error)}{" "}
          <button disabled={busy} onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      ) : query.isLoading ? (
        <p role="status">Loading pinned messages…</p>
      ) : !pins.length ? (
        <div className="discussion-empty">
          <Pin size={28} />
          <h3>No pinned messages</h3>
          <p>
            Pin a message from its actions menu to keep it here for everyone.
          </p>
        </div>
      ) : null}
      {query.hasNextPage ? (
        <button
          className="discussion-button"
          disabled={query.isFetching || busy}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetching ? "Loading…" : "Load more pins"}
        </button>
      ) : null}
    </div>
  );
}
