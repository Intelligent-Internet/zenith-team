import { useEffect, type RefObject } from "react";
import store from "@/state/store";
import { useDiscussionRuntime } from "../components/discussion-provider";

// Virtuoso mounts overscan rows too. Only intersecting rows count as read.
export function useDiscussionReadState(
  conversationId: string,
  rootId: string | undefined,
  scroller: RefObject<HTMLElement | null>,
) {
  const runtime = useDiscussionRuntime();
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const read = () => {
      const visible = store.getState().conversationDock.visibleConversation;
      const element = scroller.current;
      if (
        !element ||
        !runtime.active ||
        document.visibilityState !== "visible" ||
        !document.hasFocus() ||
        visible?.companyId !== runtime.session.companyId ||
        visible.conversationId !== conversationId ||
        !visible.discussion ||
        (rootId && visible.threadRootId !== rootId)
      )
        return;
      const bounds = element.getBoundingClientRect();
      let seq = 0;
      for (const row of element.querySelectorAll<HTMLElement>(
        "[data-discussion-seq]",
      )) {
        const rect = row.getBoundingClientRect();
        if (
          rect.bottom > bounds.top &&
          rect.top < bounds.bottom &&
          Math.min(rect.bottom, bounds.bottom) -
            Math.max(rect.top, bounds.top) >=
            Math.min(rect.height, bounds.height) / 2
        )
          seq = Math.max(seq, Number(row.dataset.discussionSeq));
      }
      if (seq > 0) void runtime.readThrough(conversationId, seq, rootId);
    };
    const schedule = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(read, 300);
    };
    const unsubscribe = runtime.subscribe(schedule);
    const interval = setInterval(read, 1000);
    window.addEventListener("focus", schedule);
    document.addEventListener("visibilitychange", schedule);
    schedule();
    return () => {
      unsubscribe();
      clearInterval(interval);
      if (timer) clearTimeout(timer);
      window.removeEventListener("focus", schedule);
      document.removeEventListener("visibilitychange", schedule);
    };
  }, [conversationId, rootId, runtime, scroller]);
}
