import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { ReviewAnchor } from "./annotation-overlay";
import type { ExecuteCanvasCommand } from "./add-artifact";
import type { CommentFilter, ReviewThreadData } from "./review-comments";
import { revealMarginComment } from "./review-navigation";
import { useCommentThreadExits } from "./use-comment-thread-exits";
import { useDocumentCommentHighlights } from "./use-document-comment-highlights";

/** Shared interaction state for Page, Overview and Canvas discussions. */
export function useDocumentComments({
  threads,
  content,
  open,
  onOpenChange,
  anchor,
  onAnchorChange,
  canCompose,
  initialThread = "",
}: {
  threads: ReviewThreadData[] | undefined;
  content: HTMLDivElement | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchor: ReviewAnchor;
  onAnchorChange: (anchor: ReviewAnchor) => void;
  canCompose: boolean;
  initialThread?: string;
}) {
  const panelId = useId();
  const rail = useRef<HTMLElement>(null);
  const composer = useRef<HTMLTextAreaElement>(null);
  const connectorLayer = useRef<SVGSVGElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  const [active, setActive] = useState(initialThread);
  const [hovered, setHovered] = useState("");
  const [filter, setFilter] = useState<CommentFilter>("open");
  const [composing, setComposing] = useState(false);
  const [postedThreadId, setPostedThreadId] = useState("");
  const [blockOrder, setBlockOrder] = useState<string[]>([]);
  useLayoutEffect(() => {
    const root = content;
    if (!root) return;
    const update = () => {
      const ids = Array.from(
        root.querySelectorAll<HTMLElement>("[data-block-id]"),
        (block) => block.dataset.blockId!,
      );
      setBlockOrder((previous) =>
        previous.length === ids.length &&
        previous.every((id, index) => id === ids[index])
          ? previous
          : ids,
      );
    };
    update();
    const observer = new MutationObserver(update);
    observer.observe(root, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["data-block-id"],
    });
    return () => observer.disconnect();
  }, [content]);
  const orderedThreads = useMemo(() => {
    const positions = new Map<string, number>();
    blockOrder.forEach((id, index) => {
      if (!positions.has(id)) positions.set(id, index);
    });
    const position = (thread: ReviewThreadData) => {
      const anchor = thread.anchor;
      return anchor.type !== "media" && anchor.blockId
        ? (positions.get(anchor.blockId) ?? blockOrder.length)
        : blockOrder.length;
    };
    // Document-wide and missing-block comments follow the located comments.
    // Same-block threads retain creation order, independent of viewport position.
    return threads
      ?.slice()
      .sort(
        (a, b) =>
          position(a) - position(b) ||
          a.createdAt.localeCompare(b.createdAt) ||
          a.id.localeCompare(b.id),
      );
  }, [threads, blockOrder]);
  const showComposer = canCompose && composing;
  const { visible: animatedThreads, finishExit } = useCommentThreadExits(
    orderedThreads,
    filter,
  );
  const select = useCallback((id: string) => {
    setActive(id);
    setHovered("");
  }, []);
  useLayoutEffect(() => {
    if (
      !postedThreadId ||
      !threads?.some((thread) => thread.id === postedThreadId)
    )
      return;
    setComposing(false);
    select(postedThreadId);
    setPostedThreadId("");
    onAnchorChange({ type: "version" });
  }, [threads, postedThreadId, select, onAnchorChange]);
  const anchors = useMemo(
    () => [
      ...animatedThreads.map(({ thread }) => ({
        id: thread.id,
        anchor: thread.anchor,
      })),
      ...(showComposer ? [{ id: "draft", anchor }] : []),
    ],
    [animatedThreads, showComposer, anchor],
  );
  const highlights = useDocumentCommentHighlights(
    content,
    rail,
    anchors,
    open,
    select,
    hovered || active,
    connectorLayer,
  );
  useEffect(() => {
    if (!open || !showComposer) return;
    revealMarginComment(rail.current, "draft");
    composer.current?.focus({ preventScroll: true });
  }, [open, showComposer, anchor]);
  const activeAvailable = threads?.some((thread) => thread.id === active);
  useEffect(() => {
    if (open && active) revealMarginComment(rail.current, active);
  }, [open, active, activeAvailable]);
  const start = useCallback(() => {
    select("");
    setComposing(true);
    onOpenChange(true);
  }, [select, onOpenChange]);
  const cancel = () => {
    setComposing(false);
    onAnchorChange({ type: "version" });
  };
  const reveal = (thread: ReviewThreadData) => {
    select(thread.id);
    onOpenChange(true);
    const status = thread.status === "resolved" ? "resolved" : "open";
    setFilter((current) => (current === "all" ? current : status));
  };
  const close = () => {
    onOpenChange(false);
    toggle.current?.focus();
  };
  const submit = async (
    execute: ExecuteCanvasCommand,
    target: { pageId: string } | { versionId: string },
    body: string,
    requestKey: string,
    agentIds?: string[],
  ) => {
    const result = await execute({
      type: "comment",
      agentIds,
      ...target,
      body,
      anchor,
      requestKey,
    });
    setPostedThreadId(result.id);
    setFilter((current) => (current === "resolved" ? "open" : current));
  };
  return {
    panelId,
    connectorLayer,
    rail,
    composer,
    toggle,
    active,
    setActive: select,
    hovered,
    setHovered,
    filter,
    setFilter,
    composing,
    setComposing,
    postedThreadId,
    showComposer,
    animatedThreads,
    finishExit,
    anchors,
    anchor,
    open,
    onOpenChange,
    canCompose,
    start,
    cancel,
    reveal,
    close,
    submit,
    openCount:
      threads?.filter((thread) => thread.status === "open").length ?? 0,
    ...highlights,
  };
}
export type DocumentCommentsController = ReturnType<typeof useDocumentComments>;
