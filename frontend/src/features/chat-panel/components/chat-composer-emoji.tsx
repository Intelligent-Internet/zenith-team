import { Smile } from "@/shared/ui/icons";
import { useRef, type RefObject } from "react";

import { ChatEmojiPicker } from "@/shared/chat/chat-emoji-picker";
import type { ChatTextSelection } from "@/shared/chat/chat-session-memory";
import { cx } from "@/shared/lib/cx";
import "@/features/chat-panel/styles/chat-composer-emoji.css";

export function ChatComposerEmoji({
  textareaRef,
  onDraftChange,
  onSelectionChange,
  disabled = false,
  className,
  ariaLabel = "Add emoji",
}: {
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  onDraftChange: (value: string) => void;
  onSelectionChange?: (selection: ChatTextSelection) => void;
  disabled?: boolean;
  className?: string;
  ariaLabel?: string;
}) {
  const selectionRef = useRef<ChatTextSelection | null>(null);
  const pendingCaretRef = useRef<number | null>(null);

  return (
    <ChatEmojiPicker
      align="start"
      label="Choose an emoji"
      onOpenChange={(open) => {
        if (!open) return;
        pendingCaretRef.current = null;
        const textarea = textareaRef.current;
        selectionRef.current = textarea
          ? { start: textarea.selectionStart, end: textarea.selectionEnd }
          : null;
      }}
      onSelect={(emoji) => {
        const textarea = textareaRef.current;
        if (disabled || !textarea) return;
        const value = textarea.value;
        const selection = selectionRef.current;
        const start = Math.min(selection?.start ?? value.length, value.length);
        const end = Math.min(selection?.end ?? start, value.length);
        const caret = start + emoji.length;
        onDraftChange(value.slice(0, start) + emoji + value.slice(end));
        onSelectionChange?.({ start: caret, end: caret });
        pendingCaretRef.current = caret;
      }}
      onCloseAutoFocus={(event) => {
        const caret = pendingCaretRef.current;
        if (caret === null) return;
        event.preventDefault();
        pendingCaretRef.current = null;
        const textarea = textareaRef.current;
        window.requestAnimationFrame(() => {
          if (!textarea?.isConnected || textareaRef.current !== textarea)
            return;
          textarea.focus({ preventScroll: true });
          textarea.setSelectionRange(caret, caret);
        });
      }}
    >
      <button
        aria-label={ariaLabel}
        className={cx("chat-composer-emoji-button", className)}
        disabled={disabled}
        onMouseDown={(event) => event.preventDefault()}
        title={ariaLabel}
        type="button"
      >
        <Smile aria-hidden="true" size={18} />
      </button>
    </ChatEmojiPicker>
  );
}
