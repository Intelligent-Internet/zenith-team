import { useLayoutEffect, type RefObject } from "react";

export function useComposerFocusRequest({
  focusRequestKey,
  textareaRef,
}: {
  focusRequestKey: number;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
}) {
  useLayoutEffect(() => {
    const frameId = window.requestAnimationFrame(() => {
      textareaRef.current?.focus({ preventScroll: true });
    });

    return () => window.cancelAnimationFrame(frameId);
  }, [focusRequestKey, textareaRef]);
}
