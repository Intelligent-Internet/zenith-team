import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type RefObject,
} from "react";
import {
  filterChatMentionMembers,
  findChatMentionQuery,
  type ChatMentionMember,
  type ChatMentionQuery,
} from "@/features/chat-panel/model/chat-mentions";

export function useChatMentions({
  disabled,
  members,
  onDraftChange,
  textareaRef,
  onMentionInserted,
  onUnavailableSelect,
}: {
  disabled: boolean;
  members: readonly ChatMentionMember[] | undefined;
  onDraftChange: (text: string) => void;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  onMentionInserted?: (
    member: ChatMentionMember,
    range: { start: number; end: number },
  ) => void;
  // A member listed as `unavailable` was chosen: the list closes and the
  // draft is left as typed, so the caller can offer what would make the
  // member taggable.
  onUnavailableSelect?: (member: ChatMentionMember) => void;
}) {
  const listId = useId();
  const [query, setQuery] = useState<ChatMentionQuery | null>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const dismissedQuery = useRef<string | null>(null);
  const composing = useRef(false);
  const pendingCaret = useRef<{
    text: string;
    caret: number;
    openSuggestions: boolean;
  } | null>(null);
  const matches = query
    ? filterChatMentionMembers(
        (members ?? []).filter((member) =>
          query.trigger === "$" ? !!member.skill : !member.skill,
        ),
        query.text,
      )
    : [];
  const isOpen = !disabled && query !== null;
  const selectedIndex = Math.min(activeIndex, Math.max(0, matches.length - 1));

  const syncSelection = (textarea: HTMLTextAreaElement) => {
    if (disabled || composing.current) return;
    const nextQuery = findChatMentionQuery(
      textarea.value,
      textarea.selectionStart,
      textarea.selectionEnd,
    );
    const signature = nextQuery
      ? `${nextQuery.trigger}:${nextQuery.start}:${nextQuery.end}:${nextQuery.text}`
      : null;
    if (signature && signature === dismissedQuery.current) return;
    dismissedQuery.current = null;
    if (
      query?.trigger === nextQuery?.trigger &&
      query?.start === nextQuery?.start &&
      query?.end === nextQuery?.end &&
      query?.text === nextQuery?.text
    )
      return;
    setActiveIndex(0);
    setQuery(nextQuery);
  };

  const dismiss = () => {
    if (query) {
      dismissedQuery.current = `${query.trigger}:${query.start}:${query.end}:${query.text}`;
    }
    setQuery(null);
  };

  const placeCaret = (
    text: string,
    caret: number,
    openSuggestions: boolean,
  ) => {
    pendingCaret.current = { text, caret, openSuggestions };
    onDraftChange(text);
  };

  useLayoutEffect(() => {
    const pending = pendingCaret.current;
    const textarea = textareaRef.current;
    if (!pending || !textarea || textarea.value !== pending.text) return;
    pendingCaret.current = null;
    textarea.focus({ preventScroll: true });
    textarea.setSelectionRange(pending.caret, pending.caret);
    if (pending.openSuggestions) syncSelection(textarea);
  });

  const selectMember = (member: ChatMentionMember) => {
    const textarea = textareaRef.current;
    if (!textarea || !query) return;
    if (member.unavailable) {
      dismiss();
      onUnavailableSelect?.(member);
      return;
    }
    const before = textarea.value.slice(0, query.start);
    const after = textarea.value.slice(query.end);
    const mention = `${member.skill ? "$" : "@"}${member.name}`;
    const separator = after.startsWith(" ") ? "" : " ";
    setQuery(null);
    dismissedQuery.current = null;
    placeCaret(
      `${before}${mention}${separator}${after}`,
      before.length + mention.length + 1,
      false,
    );
    onMentionInserted?.(member, {
      start: before.length,
      end: before.length + mention.length,
    });
  };

  const insertTrigger = () => {
    const textarea = textareaRef.current;
    if (!textarea || disabled || !members?.length) return;
    dismissedQuery.current = null;
    const existingQuery = findChatMentionQuery(
      textarea.value,
      textarea.selectionStart,
      textarea.selectionEnd,
    );
    if (existingQuery) {
      textarea.focus({ preventScroll: true });
      syncSelection(textarea);
      return;
    }
    const before = textarea.value.slice(0, textarea.selectionStart);
    const after = textarea.value.slice(textarea.selectionEnd);
    const trigger = /(?:^|[\s([{])$/.test(before) ? "@" : " @";
    placeCaret(
      `${before}${trigger}${after}`,
      before.length + trigger.length,
      true,
    );
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (
      !isOpen ||
      composing.current ||
      event.nativeEvent.isComposing ||
      event.nativeEvent.keyCode === 229
    )
      return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      dismiss();
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (matches.length) {
        const direction = event.key === "ArrowDown" ? 1 : -1;
        setActiveIndex(
          (selectedIndex + direction + matches.length) % matches.length,
        );
      }
      return;
    }
    if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey) {
      if (matches[selectedIndex]) {
        event.preventDefault();
        selectMember(matches[selectedIndex]);
      } else if (event.key === "Enter") {
        event.preventDefault();
        dismiss();
      }
    }
  };

  return {
    trigger: query?.trigger,
    activeIndex: selectedIndex,
    activeOptionId:
      isOpen && matches.length ? `${listId}-${selectedIndex}` : undefined,
    dismiss,
    insertTrigger,
    isOpen,
    listId,
    matches,
    onCompositionStart: () => {
      composing.current = true;
      setQuery(null);
    },
    onCompositionEnd: (textarea: HTMLTextAreaElement) => {
      composing.current = false;
      syncSelection(textarea);
    },
    onKeyDown,
    selectMember,
    setActiveIndex,
    syncSelection,
  };
}
