import { useDiscussionReadState } from "../model/use-discussion-read-state";
import { toast } from "@/shared/ui/sonner";
import {
  memo,
  type ReactNode,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import { ArrowDown, MessageSquareText } from "@/shared/ui/icons";
import type {
  DiscussionDetail,
  DiscussionMessage,
  DiscussionPage,
} from "../api/discussions-api";
import { useDiscussionRuntime } from "./discussion-provider";
import { DiscussionMessageRow } from "./discussion-message-row";

type TimelineContext = { header?: ReactNode; footer?: ReactNode };

function TimelineHeader({ context }: { context?: TimelineContext }) {
  return <>{context?.header}</>;
}

function TimelineFooter({ context }: { context?: TimelineContext }) {
  return <>{context?.footer}</>;
}

const timelineComponents = { Header: TimelineHeader, Footer: TimelineFooter };

export const DiscussionTimeline = memo(function DiscussionTimeline({
  conversationId,
  pages,
  detail,
  busy,
  rootId,
  focusMessageId,
  onOpenThread,
  header,
  footer,
}: {
  header?: ReactNode;
  footer?: ReactNode;
  conversationId: string;
  pages: DiscussionPage[];
  detail: DiscussionDetail;
  busy: boolean;
  rootId?: string;
  focusMessageId?: string;
  onOpenThread: (rootId: string) => void;
}) {
  const runtime = useDiscussionRuntime();
  const list = useRef<VirtuosoHandle>(null);
  const scroller = useRef<HTMLElement | null>(null);
  useDiscussionReadState(conversationId, rootId, scroller);
  const readState = runtime.readState(conversationId, rootId);
  const [atBottom, setAtBottom] = useState(
    runtime.anchor(conversationId, rootId)?.atBottom ?? true,
  );
  const rows = useMemo(() => {
    const byId = new Map<
      string,
      { item: DiscussionMessage; revision: number }
    >();
    for (const page of pages)
      for (const item of page.items) {
        if ((byId.get(item.id)?.revision ?? -1) <= page.conversationRevision)
          byId.set(item.id, { item, revision: page.conversationRevision });
      }
    return [...byId.values()]
      .map((value) => value.item)
      .sort((a, b) => a.seq - b.seq);
  }, [pages]);
  const position = useRef({ first: 1_000_000, rows });
  if (position.current.rows !== rows) {
    const old = position.current.rows;
    const oldFirst = rows.findIndex((item) => item.id === old[0]?.id);
    const newFirst = old.findIndex((item) => item.id === rows[0]?.id);
    if (oldFirst >= 0) position.current.first -= oldFirst;
    else if (newFirst >= 0) position.current.first += newFirst;
    position.current.rows = rows;
  }
  const firstItemIndex = position.current.first;
  const anchor = useRef(runtime.anchor(conversationId, rootId));
  // A layout change can move the bottom without the user choosing to scroll away.
  const followLatest = useRef(anchor.current?.atBottom !== false);
  const bottomFrame = useRef<number | null>(null);
  const previousScrollTop = useRef(0);
  const initialIndex = anchor.current
    ? rows.findIndex((row) => row.id === anchor.current?.id)
    : -1;
  const latestLoadedSeq = rows.at(-1)?.seq ?? 0;
  const keepLatestVisible = useCallback(() => {
    if (!followLatest.current || bottomFrame.current !== null) return;
    bottomFrame.current = requestAnimationFrame(() => {
      bottomFrame.current = null;
      if (followLatest.current)
        list.current?.scrollToIndex({ index: "LAST", align: "end" });
    });
  }, []);
  useLayoutEffect(() => {
    keepLatestVisible();
  }, [rows, keepLatestVisible]);
  useLayoutEffect(
    () => () => {
      if (bottomFrame.current !== null)
        cancelAnimationFrame(bottomFrame.current);
      bottomFrame.current = null;
    },
    [],
  );
  const moreNew = latestLoadedSeq < (pages.at(-1)?.streamHeadSeq ?? 0);
  const remember = useCallback(() => {
    const element = scroller.current;
    if (!element) return;
    const top = element.getBoundingClientRect().top;
    const row = [
      ...element.querySelectorAll<HTMLElement>("[data-discussion-message]"),
    ].find((node) => node.getBoundingClientRect().bottom > top);
    const id = row?.dataset.discussionMessage;
    const page = pages.find((value) =>
      value.items.some((item) => item.id === id),
    );
    if (row && id && page)
      runtime.setAnchor(
        conversationId,
        {
          id,
          offset: row.getBoundingClientRect().top - top,
          cursor: page.refreshCursor,
          atBottom: followLatest.current,
        },
        rootId,
      );
  }, [conversationId, pages, rootId, runtime]);
  const rememberRef = useRef(remember);
  rememberRef.current = remember;
  useLayoutEffect(() => () => rememberRef.current(), []);
  const attachScroller = useCallback((element: HTMLElement | Window | null) => {
    if (scroller.current) {
      rememberRef.current();
      scroller.current.removeEventListener("scroll", captureScroll);
    }
    scroller.current = element instanceof HTMLElement ? element : null;
    previousScrollTop.current = scroller.current?.scrollTop ?? 0;
    scroller.current?.addEventListener("scroll", captureScroll, {
      passive: true,
    });
  }, []);
  function captureScroll() {
    const element = scroller.current;
    if (!element) return;
    const nearBottom =
      element.scrollHeight - element.scrollTop - element.clientHeight <= 48;
    if (nearBottom) followLatest.current = true;
    else if (element.scrollTop < previousScrollTop.current)
      followLatest.current = false;
    previousScrollTop.current = element.scrollTop;
    rememberRef.current();
  }
  async function jumpLatest() {
    if (!moreNew) {
      followLatest.current = true;
      keepLatestVisible();
      return;
    }
    runtime.clearAnchor(conversationId, rootId);
    await runtime.jumpLatest(conversationId, rootId);
  }
  if (!rows.length && !rootId)
    return (
      <div className="discussion-empty">
        <MessageSquareText size={30} aria-hidden="true" />
        <h3>Start the conversation</h3>
        <p>
          {detail.conversation.type === "channel"
            ? "A shared space for your team to talk and work together."
            : `Send a message to ${detail.conversation.peer?.displayName ?? detail.conversation.title}.`}
        </p>
      </div>
    );
  return (
    <div className="discussion-timeline">
      {runtime.readsPaused(conversationId, rootId) ? (
        <div className="discussion-history-toolbar" role="status">
          Marked as unread
          <button
            onClick={() =>
              void runtime
                .resumeReading(conversationId, rootId)
                .catch(() => toast.error("Read state could not be saved."))
            }
          >
            Resume reading
          </button>
        </div>
      ) : null}
      {pages[0]?.olderCursor || moreNew ? (
        <div className="discussion-history-toolbar">
          {pages[0]?.olderCursor ? (
            <button
              disabled={busy}
              onClick={() =>
                void runtime.page(conversationId, "backward", rootId)
              }
            >
              {busy ? "Loading…" : "Load older messages"}
            </button>
          ) : null}
          {moreNew ? (
            <button
              disabled={busy}
              onClick={() =>
                void runtime.page(conversationId, "forward", rootId)
              }
            >
              Load newer messages
            </button>
          ) : null}
        </div>
      ) : null}
      <Virtuoso
        ref={list}
        components={timelineComponents}
        context={{ header, footer }}
        data={rows}
        firstItemIndex={firstItemIndex}
        computeItemKey={(_index, row) => row.id}
        scrollerRef={attachScroller}
        className="discussion-virtual-list"
        aria-label={rootId ? "Thread replies" : "Messages"}
        alignToBottom={!rootId}
        atBottomThreshold={48}
        initialTopMostItemIndex={
          initialIndex >= 0 && anchor.current?.atBottom === false
            ? {
                index: initialIndex,
                align: "start",
                offset: -(anchor.current?.offset ?? 0),
              }
            : { index: "LAST", align: "end" }
        }
        increaseViewportBy={{ top: 160, bottom: 160 }}
        totalListHeightChanged={keepLatestVisible}
        followOutput={() => (followLatest.current ? "auto" : false)}
        atBottomStateChange={(bottom) => {
          setAtBottom(bottom);
          const current = runtime.anchor(conversationId, rootId);
          if (current)
            runtime.setAnchor(
              conversationId,
              { ...current, atBottom: followLatest.current },
              rootId,
            );
        }}
        itemContent={(index, row) => {
          const previous = rows[index - firstItemIndex - 1];
          const day = new Date(row.createdAt).toLocaleDateString();
          const newDay =
            !previous ||
            new Date(previous.createdAt).toLocaleDateString() !== day;
          const compact =
            !newDay &&
            previous?.type === "user_message" &&
            row.type === "user_message" &&
            previous.author.id === row.author.id &&
            Date.parse(row.createdAt) - Date.parse(previous.createdAt) <
              300_000;
          return (
            <div data-discussion-message={row.id} data-discussion-seq={row.seq}>
              {readState &&
              row.seq >
                (readState.manualUnreadSeq
                  ? Math.min(
                      readState.readThroughSeq,
                      readState.manualUnreadSeq - 1,
                    )
                  : readState.readThroughSeq) &&
              (!previous ||
                previous.seq <=
                  (readState.manualUnreadSeq
                    ? Math.min(
                        readState.readThroughSeq,
                        readState.manualUnreadSeq - 1,
                      )
                    : readState.readThroughSeq)) &&
              Boolean(readState.unreadCount) ? (
                <div
                  className="discussion-day"
                  role="separator"
                  aria-label="Unread messages"
                >
                  <span>New messages</span>
                </div>
              ) : null}
              {newDay && (!rootId || previous) ? (
                <div className="discussion-day">
                  <span>
                    {new Date(row.createdAt).toLocaleDateString(undefined, {
                      weekday: "long",
                      month: "short",
                      day: "numeric",
                      year: "numeric",
                    })}
                  </span>
                </div>
              ) : null}
              <DiscussionMessageRow
                message={row}
                compact={compact}
                detail={detail}
                onOpenThread={onOpenThread}
                inThread={Boolean(rootId)}
                focused={row.id === focusMessageId}
              />
            </div>
          );
        }}
      />
      {!atBottom || moreNew ? (
        <button className="discussion-jump" onClick={() => void jumpLatest()}>
          <ArrowDown size={15} />
          {moreNew ? "New messages" : "Jump to latest"}
        </button>
      ) : null}
      <span className="sr-only" role="status" aria-live="polite">
        {moreNew
          ? "New messages available. Use Jump to latest to read them."
          : ""}
      </span>
    </div>
  );
});
