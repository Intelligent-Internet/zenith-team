import {
  Bold,
  Code,
  Italic,
  Link,
  List,
  Strikethrough,
} from "@/shared/ui/icons";
import type { RefObject } from "react";
import type { ChatTextSelection } from "@/shared/chat/chat-session-memory";
import { PromptInputButton } from "@/shared/ui/core/ai-elements/prompt-input";

const formattingActions = [
  { format: "bold", label: "Bold", icon: Bold },
  { format: "italic", label: "Italic", icon: Italic },
  { format: "strikethrough", label: "Strikethrough", icon: Strikethrough },
  { format: "link", label: "Insert link", icon: Link },
  { format: "list", label: "Bulleted list", icon: List },
  { format: "code", label: "Code", icon: Code },
] as const;

type TextFormat = (typeof formattingActions)[number]["format"];

function formatSelection(
  value: string,
  selection: ChatTextSelection,
  format: TextFormat,
) {
  let { start, end } = selection;
  const isCodeBlock =
    format === "code" && value.slice(start, end).includes("\n");
  if (format === "list" || isCodeBlock) {
    start = value.slice(0, start).lastIndexOf("\n") + 1;
    const lastSelectedCharacter =
      end > selection.start && value[end - 1] === "\n" ? end - 1 : end;
    const nextLine = value.indexOf("\n", lastSelectedCharacter);
    end = nextLine === -1 ? value.length : nextLine;
  }

  const selectedText = value.slice(start, end);
  let replacement: string;
  let nextSelection: ChatTextSelection;

  if (format === "list") {
    replacement = selectedText
      .split("\n")
      .map((line) => `- ${line}`)
      .join("\n");
    nextSelection = {
      start: start + 2,
      end: start + replacement.length,
    };
  } else if (format === "link") {
    const label = selectedText || "link text";
    replacement = `[${label}](https://)`;
    nextSelection = {
      start: start + label.length + 3,
      end: start + replacement.length - 1,
    };
  } else {
    const text = selectedText || "text";
    let marker = {
      bold: "**",
      italic: "*",
      strikethrough: "~~",
      code: "`",
    }[format];
    let padding = "";
    if (format === "code") {
      const longestBacktickRun = (text.match(/`+/g) ?? []).reduce(
        (longest, run) => Math.max(longest, run.length),
        0,
      );
      marker = "`".repeat(
        Math.max(isCodeBlock ? 3 : 1, longestBacktickRun + 1),
      );
      const needsInlinePadding =
        text.startsWith("`") ||
        text.endsWith("`") ||
        (text.startsWith(" ") && text.endsWith(" ") && text.trim().length > 0);
      padding = isCodeBlock ? "\n" : needsInlinePadding ? " " : "";
    }
    replacement = `${marker}${padding}${text}${padding}${marker}`;
    nextSelection = {
      start: start + marker.length + padding.length,
      end: start + marker.length + padding.length + text.length,
    };
  }

  return {
    value: value.slice(0, start) + replacement + value.slice(end),
    selection: nextSelection,
  };
}

export function ChatComposerFormatting({
  disabled,
  onDraftChange,
  onSelectionChange,
  textareaRef,
}: {
  disabled: boolean;
  onDraftChange: (value: string) => void;
  onSelectionChange?: (selection: ChatTextSelection) => void;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
}) {
  const applyFormat = (format: TextFormat) => {
    const textarea = textareaRef.current;
    if (disabled || !textarea) return;

    const result = formatSelection(
      textarea.value,
      { start: textarea.selectionStart, end: textarea.selectionEnd },
      format,
    );
    onDraftChange(result.value);
    onSelectionChange?.(result.selection);
    window.requestAnimationFrame(() => {
      if (textareaRef.current !== textarea) return;
      textarea.focus({ preventScroll: true });
      textarea.setSelectionRange(result.selection.start, result.selection.end);
    });
  };

  return (
    <div
      aria-label="Message formatting"
      className="chat-composer-formatting relative z-[2] flex min-h-10 shrink-0 items-center gap-1 px-3 pt-1"
      role="group"
    >
      {formattingActions.map(({ format, label, icon: Icon }) => (
        <PromptInputButton
          aria-label={label}
          className="chat-composer-formatting__button size-8 rounded-md text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]"
          disabled={disabled}
          key={format}
          onClick={() => applyFormat(format)}
          onMouseDown={(event) => event.preventDefault()}
          title={label}
        >
          <Icon aria-hidden="true" size={18} />
        </PromptInputButton>
      ))}
    </div>
  );
}
