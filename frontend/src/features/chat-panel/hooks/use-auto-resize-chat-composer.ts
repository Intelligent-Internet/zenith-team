import { useEffect, useRef, useState, type CSSProperties } from "react";
import {
  promptAttachmentVerticalSpace,
  promptInputMaxHeight,
  promptInputMinHeight,
  promptInputVerticalChrome,
  promptTextareaMaxHeight,
  promptTextareaMinHeight,
} from "@/features/chat-panel/constants";
import { clampValue } from "@/features/chat-panel/utils";

export function useAutoResizeChatComposer({
  additionalVerticalSpace = 0,
  draftMessage,
  hasPromptAttachments,
}: {
  additionalVerticalSpace?: number;
  draftMessage: string;
  hasPromptAttachments: boolean;
}) {
  const [size, setSize] = useState({
    promptInputHeight: promptInputMinHeight,
    promptTextareaHeight: promptTextareaMinHeight,
  });
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    const animationFrame = window.requestAnimationFrame(() => {
      const textarea = textareaRef.current;

      if (!textarea) {
        return;
      }

      textarea.style.height = `${promptTextareaMinHeight}px`;
      const measuredScrollHeight = textarea.scrollHeight;
      const nextTextareaHeight =
        measuredScrollHeight <= promptTextareaMinHeight + 2
          ? promptTextareaMinHeight
          : clampValue(
              measuredScrollHeight,
              promptTextareaMinHeight,
              promptTextareaMaxHeight,
            );
      const nextInputHeight = clampValue(
        nextTextareaHeight +
          promptInputVerticalChrome +
          (hasPromptAttachments ? promptAttachmentVerticalSpace : 0) +
          additionalVerticalSpace,
        promptInputMinHeight,
        promptInputMaxHeight + additionalVerticalSpace,
      );

      textarea.style.height = `${nextTextareaHeight}px`;
      textarea.style.overflowY =
        measuredScrollHeight > promptTextareaMaxHeight ? "auto" : "hidden";
      setSize((current) =>
        current.promptInputHeight === nextInputHeight &&
        current.promptTextareaHeight === nextTextareaHeight
          ? current
          : {
              promptInputHeight: nextInputHeight,
              promptTextareaHeight: nextTextareaHeight,
            },
      );
    });

    return () => window.cancelAnimationFrame(animationFrame);
  }, [additionalVerticalSpace, draftMessage, hasPromptAttachments]);

  return {
    promptInputHeight: size.promptInputHeight,
    textareaRef,
    textareaStyle: {
      fieldSizing: "fixed",
      flex: "0 0 auto",
      height: size.promptTextareaHeight,
      maxHeight: promptTextareaMaxHeight,
      minHeight: promptTextareaMinHeight,
      padding: 0,
      width: "calc(100% - 24px)",
    } as CSSProperties,
  };
}
