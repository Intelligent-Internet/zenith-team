import { useLayoutEffect, useState } from "react";
import type { CommentFilter, ReviewThreadData } from "./review-comments";

type VisibleThread = { thread: ReviewThreadData; resolving: boolean };

/** Keep confirmed resolutions mounted briefly so their cards can leave the margin. */
export function useCommentThreadExits(
  threads: ReviewThreadData[] | undefined,
  filter: CommentFilter,
) {
  const [visible, setVisible] = useState<VisibleThread[]>([]);
  useLayoutEffect(() => {
    const reducedMotion = window.matchMedia(
      "(prefers-reduced-motion: reduce)",
    ).matches;
    setVisible((previous) => {
      const previousById = new Map(previous.map((row) => [row.thread.id, row]));
      const next = (threads ?? []).flatMap<VisibleThread>((thread) => {
        if (filter === "all" || thread.status === filter)
          return [{ thread, resolving: false }];
        const before = previousById.get(thread.id);
        if (
          !reducedMotion &&
          filter === "open" &&
          thread.status === "resolved" &&
          (before?.thread.status === "open" || before?.resolving)
        )
          return [{ thread, resolving: true }];
        return [];
      });
      return next.length === previous.length &&
        next.every(
          (row, index) =>
            row.thread === previous[index].thread &&
            row.resolving === previous[index].resolving,
        )
        ? previous
        : next;
    });
  }, [threads, filter]);

  const exiting = visible.some((row) => row.resolving);
  useLayoutEffect(() => {
    if (!exiting) return;
    // Also finish when animation events are unavailable or the margin is hidden.
    const timeout = window.setTimeout(() => {
      setVisible((rows) => rows.filter((row) => !row.resolving));
    }, 320);
    return () => window.clearTimeout(timeout);
  }, [exiting, threads]);

  const finishExit = (id: string) =>
    setVisible((rows) =>
      rows.filter((row) => row.thread.id !== id || !row.resolving),
    );
  return { visible, finishExit };
}
