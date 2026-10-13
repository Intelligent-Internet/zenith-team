import { useEffect, useId, useRef, useState } from "react";
import { EditorContent, useEditor, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Placeholder from "@tiptap/extension-placeholder";
import { Markdown } from "@tiptap/markdown";
import {
  Bold,
  Italic,
  Strikethrough,
  Link,
  List,
  Code,
  Smile,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import { ChatEmojiPicker } from "@/shared/chat/chat-emoji-picker";
import { ChatMentionSuggestions } from "@/features/chat-panel/components/chat-mention-suggestions";
import {
  filterChatMentionMembers,
  findChatMentionQuery,
  type ChatMentionMember,
} from "@/features/chat-panel/model/chat-mentions";
import {
  DiscussionMention,
  richDiscussionDocument,
  serializeDiscussionEditor,
} from "../model/discussion-rich-text";
import type { DiscussionMessage } from "../api/discussions-api";
import "../styles/discussion-rich-editor.css";

type RichDraft = {
  text: string;
  references: NonNullable<DiscussionMessage["body"]>["references"];
};

type EditorOptions = {
  sessionKey: string;
  draft: RichDraft;
  disabled: boolean;
  label: string;
  placeholder: string;
  describedBy?: string;
  members: readonly ChatMentionMember[];
  onChange: (draft: RichDraft) => void;
  onSend: () => void;
  onUnavailableSelect: (member: ChatMentionMember) => void;
};

export function useDiscussionRichEditor(options: EditorOptions) {
  const latest = useRef(options);
  latest.current = options;
  const lastPublished = useRef<string | null>(null);
  const listId = useId();
  const anchor = useRef<HTMLDivElement>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const [dismissed, setDismissed] = useState<string | null>(null);
  const keys = useRef<(event: KeyboardEvent) => boolean>(() => false);
  const editor = useEditor(
    {
      extensions: [
        StarterKit.configure({
          heading: false,
          horizontalRule: false,
          underline: false,
          link: { openOnClick: false, autolink: true },
        }),
        Markdown.configure({ markedOptions: { gfm: true, breaks: true } }),
        Placeholder.configure({
          placeholder: () => latest.current.placeholder,
        }),
        DiscussionMention,
      ],
      editable: !options.disabled,
      shouldRerenderOnTransaction: true,
      editorProps: {
        attributes: {
          class: "discussion-rich-input",
          role: "textbox",
          "aria-multiline": "true",
          "aria-label": options.label,
        },
        handleKeyDown: (_view, event) => keys.current(event),
      },
      onCreate: ({ editor }) => {
        const draft = latest.current.draft;
        editor.commands.setContent(
          richDiscussionDocument(editor, draft.text, draft.references),
          { emitUpdate: false },
        );
        lastPublished.current = JSON.stringify(draft);
      },
      onUpdate: ({ editor }) => {
        const next = serializeDiscussionEditor(editor);
        lastPublished.current = JSON.stringify(next);
        latest.current.onChange(next);
      },
    },
    [options.sessionKey],
  );

  useEffect(() => {
    if (!editor || !editor.markdown) return;
    const snapshot = JSON.stringify({
      text: options.draft.text,
      references: options.draft.references,
    });
    if (snapshot === lastPublished.current) return;
    editor.commands.setContent(
      richDiscussionDocument(
        editor,
        options.draft.text,
        options.draft.references,
      ),
      { emitUpdate: false },
    );
    lastPublished.current = snapshot;
  }, [editor, options.draft.text, options.draft.references]);
  useEffect(() => {
    editor?.setEditable(!options.disabled, false);
  }, [editor, options.disabled]);

  const selection = editor?.state.selection;
  const before =
    selection?.$from.parent.textBetween(
      0,
      selection.$from.parentOffset,
      "",
      "\uFFFC",
    ) ?? "";
  const query =
    selection?.empty &&
    !editor?.isActive("codeBlock") &&
    !editor?.isActive("code")
      ? findChatMentionQuery(before, before.length, before.length)
      : null;
  const signature =
    query && selection ? `${selection.from}:${query.text}` : null;
  const isOpen = !options.disabled && !!query && signature !== dismissed;
  const matches = query
    ? filterChatMentionMembers(options.members, query.text)
    : [];
  const index = Math.min(activeIndex, Math.max(0, matches.length - 1));
  useEffect(() => {
    setActiveIndex(0);
  }, [signature]);
  useEffect(() => {
    const element = anchor.current?.querySelector('[role="textbox"]');
    if (!element) return;
    element.setAttribute("aria-label", options.label);
    for (const [key, value] of Object.entries({
      "aria-describedby": options.describedBy,
      "aria-controls": isOpen ? listId : undefined,
      "aria-activedescendant":
        isOpen && matches.length ? `${listId}-${index}` : undefined,
    })) {
      if (value) element.setAttribute(key, value);
      else element.removeAttribute(key);
    }
  }, [
    editor,
    options.label,
    options.describedBy,
    isOpen,
    listId,
    index,
    matches.length,
  ]);

  const selectMember = (member: ChatMentionMember) => {
    if (!editor || !query || !selection || options.disabled) return;
    setDismissed(signature);
    if (member.unavailable) {
      options.onUnavailableSelect(member);
      return;
    }
    const reference = member.broadcastScope
      ? { type: "broadcast", scope: member.broadcastScope }
      : member.kind === "agent"
        ? { type: "agent", agentId: member.id }
        : { type: "user", userId: member.id };
    editor
      .chain()
      .focus()
      .insertContentAt(
        {
          from: selection.from - (query.end - query.start),
          to: selection.from,
        },
        [
          {
            type: "discussionMention",
            attrs: { label: `@${member.name}`, reference },
          },
          { type: "text", text: " " },
        ],
      )
      .run();
  };
  keys.current = (event) => {
    if (event.isComposing || event.keyCode === 229 || editor?.view.composing)
      return false;
    if (isOpen) {
      if (event.key === "Escape") {
        setDismissed(signature);
        return true;
      }
      if (["ArrowDown", "ArrowUp"].includes(event.key) && matches.length) {
        setActiveIndex(
          (index + (event.key === "ArrowDown" ? 1 : -1) + matches.length) %
            matches.length,
        );
        return true;
      }
      if (["Enter", "Tab"].includes(event.key) && matches[index]) {
        selectMember(matches[index]);
        return true;
      }
      if (event.key === "Enter") return true;
    }
    if (event.key !== "Enter") return false;
    if (
      event.shiftKey &&
      !(event.metaKey || event.ctrlKey) &&
      editor?.isActive("codeBlock")
    ) {
      // Bypass StarterKit's Shift-Enter hard-break command, which exits code.
      editor.view.dispatch(editor.state.tr.insertText("\n").scrollIntoView());
      return true;
    }
    if (editor && selection?.empty && !(event.metaKey || event.ctrlKey)) {
      const { $from, from } = selection;
      const atEnd = $from.parentOffset === $from.parent.content.size;
      const fence = /^```([\w+-]*)$/.exec($from.parent.textContent);
      if (atEnd && fence && editor.isActive("paragraph")) {
        editor
          .chain()
          .deleteRange({ from: $from.start(), to: from })
          .setCodeBlock({ language: fence[1] })
          .run();
        return true;
      }
      if (atEnd && editor.isActive("codeBlock")) {
        const closingFence = /(?:^|\n)```$/.exec($from.parent.textContent);
        if (closingFence) {
          editor
            .chain()
            .deleteRange({ from: from - closingFence[0].length, to: from })
            .exitCode()
            .run();
          return true;
        }
      }
    }
    if (event.shiftKey) return false;
    if (
      !(event.metaKey || event.ctrlKey) &&
      (editor?.isActive("listItem") || editor?.isActive("codeBlock"))
    )
      return false;
    latest.current.onSend();
    return true;
  };
  return {
    editor,
    focus: () => editor?.commands.focus(),
    insertMention: () => {
      setDismissed(null);
      editor
        ?.chain()
        .focus()
        .insertContent({
          type: "text",
          text: before && !/\s$/.test(before) ? " @" : "@",
        })
        .run();
    },
    input: (
      <div ref={anchor}>
        <EditorContent editor={editor} />
      </div>
    ),
    suggestions: isOpen ? (
      <ChatMentionSuggestions
        activeIndex={index}
        listId={listId}
        members={matches}
        onActiveIndexChange={setActiveIndex}
        onSelect={selectMember}
        textareaRef={anchor}
      />
    ) : null,
  };
}

export function DiscussionRichFormatting({
  editor,
  disabled,
}: {
  editor: Editor | null;
  disabled: boolean;
}) {
  const [link, setLink] = useState<string | null>(null);
  const [linkError, setLinkError] = useState("");
  const range = useRef<{ from: number; to: number } | null>(null);
  const actions = [
    {
      label: "Bold",
      icon: Bold,
      active: editor?.isActive("bold"),
      apply: () => editor?.chain().focus().toggleBold().run(),
    },
    {
      label: "Italic",
      icon: Italic,
      active: editor?.isActive("italic"),
      apply: () => editor?.chain().focus().toggleItalic().run(),
    },
    {
      label: "Strikethrough",
      icon: Strikethrough,
      active: editor?.isActive("strike"),
      apply: () => editor?.chain().focus().toggleStrike().run(),
    },
    {
      label: "Insert link",
      icon: Link,
      active: editor?.isActive("link"),
      apply: () => {
        range.current = editor
          ? { from: editor.state.selection.from, to: editor.state.selection.to }
          : null;
        setLink(editor?.getAttributes("link").href ?? "");
        setLinkError("");
      },
    },
    {
      label: "Bulleted list",
      icon: List,
      active: editor?.isActive("bulletList"),
      apply: () => editor?.chain().focus().toggleBulletList().run(),
    },
    {
      label: "Code",
      icon: Code,
      active: editor?.isActive("code") || editor?.isActive("codeBlock"),
      apply: () => {
        if (!editor) return;
        const { from, to } = editor.state.selection;
        if (
          editor.isActive("codeBlock") ||
          editor.state.doc.textBetween(from, to, "\n").includes("\n")
        )
          editor.chain().focus().toggleCodeBlock().run();
        else editor.chain().focus().toggleCode().run();
      },
    },
  ];
  const applyLink = () => {
    if (!editor || disabled || link === null || !range.current) return;
    if (link && !/^(https?:\/\/|mailto:)[^\s]+$/i.test(link)) {
      setLinkError("Enter an https://, http:// or mailto: link.");
      return;
    }
    const chain = editor.chain().focus().setTextSelection(range.current);
    if (!link) chain.extendMarkRange("link").unsetLink().run();
    else if (
      range.current.from === range.current.to &&
      !editor.isActive("link")
    )
      chain
        .insertContent({
          type: "text",
          text: link,
          marks: [{ type: "link", attrs: { href: link } }],
        })
        .run();
    else chain.extendMarkRange("link").setLink({ href: link }).run();
    setLink(null);
  };
  return (
    <>
      <div
        className="discussion-rich-formatting flex gap-[4px] p-[4px_12px_0] min-h-[40px] items-center"
        role="group"
        aria-label="Message formatting"
      >
        {actions.map(({ label, icon: Icon, active, apply }) => (
          <Tooltip key={label} label={label}>
            <button
              type="button"
              aria-label={label}
              aria-pressed={Boolean(active)}
              disabled={disabled || !editor}
              onMouseDown={(event) => event.preventDefault()}
              onClick={apply}
            >
              <Icon size={18} aria-hidden />
            </button>
          </Tooltip>
        ))}
      </div>
      {link !== null ? (
        <div
          className="discussion-rich-link flex flex-wrap gap-[8px] p-[8px_14px] text-[length:13px]"
          role="group"
          aria-label="Edit link"
        >
          <input
            autoFocus
            aria-label="Link URL"
            placeholder="https://example.com"
            value={link}
            onChange={(event) => setLink(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                applyLink();
              }
              if (event.key === "Escape") {
                event.preventDefault();
                setLink(null);
                editor?.commands.focus();
              }
            }}
          />
          <button type="button" disabled={disabled} onClick={applyLink}>
            Apply
          </button>
          <button
            type="button"
            onClick={() => {
              setLink(null);
              editor?.commands.focus();
            }}
          >
            Cancel
          </button>
          {linkError ? <span role="alert">{linkError}</span> : null}
        </div>
      ) : null}
    </>
  );
}

export function DiscussionRichEmoji({
  editor,
  disabled,
}: {
  editor: Editor | null;
  disabled: boolean;
}) {
  return (
    <ChatEmojiPicker
      align="start"
      label="Choose an emoji"
      onSelect={(emoji) => {
        if (!disabled)
          editor
            ?.chain()
            .focus()
            .insertContent({ type: "text", text: emoji })
            .run();
      }}
      onCloseAutoFocus={(event) => {
        event.preventDefault();
        editor?.commands.focus();
      }}
    >
      <button
        className="discussion-icon-button"
        type="button"
        aria-label="Add emoji"
        disabled={disabled}
        onMouseDown={(event) => event.preventDefault()}
      >
        <Smile size={18} aria-hidden />
      </button>
    </ChatEmojiPicker>
  );
}
