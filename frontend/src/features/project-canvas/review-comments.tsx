import type { CanvasScope } from "./canvas-api";
import { useChatMentions } from "@/features/chat-panel/hooks/use-chat-mentions";
import { ChatMentionSuggestions } from "@/features/chat-panel/components/chat-mention-suggestions";
import {
  findChatMentionRanges,
  type ChatMentionMember,
} from "@/features/chat-panel/model/chat-mentions";
import { useDocumentAgents } from "./use-document-agents";
import { useEffect, useRef, useState, type RefObject } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import {
  Check,
  ChevronDown,
  ChevronRight,
  MessageBubble,
  MoreHorizontal,
  Pencil,
  Pin,
  Send,
  Trash2,
  Undo2,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { anchorLabel } from "./annotation-overlay";
import type { ExecuteCanvasCommand } from "./add-artifact";

export type ReviewThreadData = components["schemas"]["CanvasThread"];
export type CommentFilter = "open" | "resolved" | "all";

const timestamp = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
});
const shortDate = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
});
export const reviewDate = (value: string) => timestamp.format(new Date(value));

export function CommentAvatar({ name }: { name: string }) {
  return (
    <span className="pc-comment-avatar" aria-hidden>
      {name
        .trim()
        .split(/\s+/)
        .slice(0, 2)
        .map((part) => part[0])
        .join("")
        .toUpperCase() || "?"}
    </span>
  );
}

export function commentLocation(anchor: ReviewThreadData["anchor"]) {
  if (anchor.type === "media") return anchorLabel(anchor);
  if (anchor.type === "textQuote") return "Selected text";
  if (anchor.type === "block") return "Selected block";
  if (anchor.type === "point") return "Pinned location";
  return "Whole artifact";
}

export function CommentFilters({
  threads,
  value,
  onChange,
}: {
  threads: ReviewThreadData[];
  value: CommentFilter;
  onChange: (value: CommentFilter) => void;
}) {
  const open = threads.filter((thread) => thread.status === "open").length;
  return (
    <div
      className="pc-comment-filters"
      role="group"
      aria-label="Filter comments"
    >
      {(
        [
          ["open", "Open", open],
          ["resolved", "Resolved", threads.length - open],
          ["all", "All", threads.length],
        ] as const
      ).map(([key, label, count]) => (
        <button
          type="button"
          key={key}
          aria-pressed={value === key}
          onClick={() => onChange(key)}
        >
          {label}
          <span>{count}</span>
        </button>
      ))}
    </div>
  );
}

export function CommentComposer({
  onSubmit,
  placeholder,
  initialBody = "",
  contextKey = "",
  inputRef,
  onCancel,
  submitLabel = "Post",
  disabled = false,
  clearOnSubmit = true,
  submitted = false,
  agentScope,
}: {
  onSubmit: (
    body: string,
    requestKey: string,
    agentIds?: string[],
  ) => Promise<void>;
  agentScope?: CanvasScope;
  placeholder: string;
  initialBody?: string;
  contextKey?: string;
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  onCancel?: () => void;
  submitLabel?: string;
  disabled?: boolean;
  clearOnSubmit?: boolean;
  submitted?: boolean;
}) {
  const [body, setBody] = useState(initialBody);
  const ownRef = useRef<HTMLTextAreaElement>(null);
  const textareaRef = inputRef ?? ownRef;
  const directory = useDocumentAgents(agentScope);
  const [tagged, setTagged] = useState<ChatMentionMember[]>([]);
  const mentions = useChatMentions({
    disabled: disabled || submitted || !directory.canInvoke,
    members: directory.agents,
    textareaRef,
    onDraftChange: (value) => {
      setBody(value);
      key.current = crypto.randomUUID();
    },
    onMentionInserted: (member) =>
      setTagged((current) => [
        ...current.filter((a) => a.id !== member.id),
        member,
      ]),
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const key = useRef(crypto.randomUUID());
  useEffect(() => {
    key.current = crypto.randomUUID();
  }, [contextKey]);
  const send = async () => {
    if (!body.trim() || busy || disabled || submitted) return;
    setBusy(true);
    setError("");
    try {
      const agentIds = (directory.canInvoke ? tagged : [])
        .filter((member) => findChatMentionRanges(body, [member]).length > 0)
        .map((member) => member.id);
      if (agentIds.length > 5)
        throw new Error("Mention up to five agents at a time.");
      await onSubmit(
        body.trim(),
        key.current,
        agentIds.length ? agentIds : undefined,
      );
      if (clearOnSubmit) setBody("");
      key.current = crypto.randomUUID();
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "Comment not sent. Your text is still here.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="pc-comment-form"
      aria-busy={busy}
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      <textarea
        ref={textareaRef}
        aria-autocomplete={directory.canInvoke ? "list" : undefined}
        aria-controls={mentions.isOpen ? mentions.listId : undefined}
        aria-activedescendant={mentions.activeOptionId}
        onSelect={(event) => mentions.syncSelection(event.currentTarget)}
        onCompositionStart={mentions.onCompositionStart}
        onCompositionEnd={(event) =>
          mentions.onCompositionEnd(event.currentTarget)
        }
        aria-label={placeholder}
        placeholder={placeholder}
        maxLength={4000}
        rows={2}
        value={body}
        disabled={busy || disabled || submitted}
        autoFocus={!!onCancel && !inputRef}
        onChange={(event) => {
          setBody(event.target.value);
          key.current = crypto.randomUUID();
          mentions.syncSelection(event.currentTarget);
        }}
        onKeyDown={(event) => {
          mentions.onKeyDown(event);
          if (event.defaultPrevented) return;
          if (
            (event.metaKey || event.ctrlKey) &&
            event.key === "Enter" &&
            !event.nativeEvent.isComposing
          ) {
            event.preventDefault();
            void send();
          }
          if (event.key === "Escape" && onCancel && !busy && !submitted) {
            event.preventDefault();
            event.stopPropagation();
            onCancel();
          }
        }}
      />
      {mentions.isOpen && (
        <ChatMentionSuggestions
          activeIndex={mentions.activeIndex}
          listId={mentions.listId}
          members={mentions.matches}
          onActiveIndexChange={mentions.setActiveIndex}
          onSelect={mentions.selectMember}
          textareaRef={textareaRef}
        />
      )}
      {directory.canInvoke && (
        <small>
          Tag an agent with @ to ask for a reply or suggested edits.
        </small>
      )}
      {directory.failed && (
        <button type="button" onClick={() => void directory.retry()}>
          Retry loading agents
        </button>
      )}
      {error && (
        <p className="pc-comment-error" role="alert">
          {error}
        </p>
      )}
      <footer>
        {onCancel ? (
          <button
            type="button"
            className="pc-composer-cancel"
            disabled={busy || submitted}
            onClick={onCancel}
          >
            Cancel
          </button>
        ) : (
          <small>⌘ / Ctrl + Enter</small>
        )}
        <button
          className="pc-primary pc-send-comment"
          disabled={busy || disabled || submitted || !body.trim()}
        >
          {submitted ? "Saved" : busy ? "Sending…" : submitLabel}
          <Send size={14} />
        </button>
      </footer>
    </form>
  );
}

export function ReviewThread({
  thread,
  canEdit,
  currentUserId,
  active,
  busy,
  execute,
  onAnchor,
  onHover,
  page = false,
  agentScope,
}: {
  page?: boolean;
  agentScope?: CanvasScope;
  thread: ReviewThreadData;
  canEdit: boolean;
  currentUserId?: string;
  active: boolean;
  busy: boolean;
  execute: ExecuteCanvasCommand;
  onAnchor: () => void;
  onHover: (hovering: boolean) => void;
}) {
  const [replying, setReplying] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [editId, setEditId] = useState("");
  const showReplies = expanded || replying || !!editId;
  const comments = showReplies ? thread.comments : thread.comments.slice(0, 1);
  const replies = Math.max(0, thread.comments.length - 1);
  return (
    <article
      id={`canvas-thread-${thread.id}`}
      className={`pc-thread ${thread.status === "resolved" ? "is-resolved" : ""} ${active ? "is-active" : ""}`}
      onClick={(event) => {
        if (
          (event.target as Element).closest(
            "button, a, input, textarea, select, [contenteditable], [role=menuitem]",
          )
        )
          return;
        const selection = window.getSelection();
        if (
          selection &&
          !selection.isCollapsed &&
          event.currentTarget.contains(selection.anchorNode)
        )
          return;
        onAnchor();
      }}
      onPointerEnter={() => onHover(true)}
      onPointerLeave={() => onHover(false)}
      onFocus={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) onHover(true);
      }}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) onHover(false);
      }}
    >
      <div className="pc-thread-context">
        <button
          className="pc-thread-anchor"
          onClick={onAnchor}
          title="Show comment location"
        >
          {thread.anchor.type === "version" ? (
            <MessageBubble size={13} />
          ) : (
            <Pin size={13} />
          )}
          {page && thread.anchor.type === "version"
            ? "Whole Page"
            : commentLocation(thread.anchor)}
          <ChevronRight size={12} />
        </button>
        {thread.status === "resolved" && (
          <span className="pc-thread-resolved">
            <Check size={12} /> Resolved
          </span>
        )}
      </div>
      {comments.map((comment, index) => (
        <div
          className={`pc-comment ${index > 0 ? "is-reply" : ""}`}
          key={comment.id}
        >
          <header>
            <CommentAvatar name={comment.authorName} />
            <strong>{comment.authorName}</strong>
            <time
              dateTime={comment.createdAt}
              title={reviewDate(comment.createdAt)}
            >
              {shortDate.format(new Date(comment.createdAt))}
            </time>
            {canEdit &&
              !comment.deleted &&
              !comment.authorAgentId &&
              comment.authorId === currentUserId && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <button
                      className="pc-comment-menu-trigger"
                      aria-label={`Actions for ${comment.authorName}'s comment`}
                      disabled={busy}
                    >
                      <MoreHorizontal size={16} />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    align="end"
                    className="z-[140]"
                    onCloseAutoFocus={(event) => {
                      if (editId === comment.id) event.preventDefault();
                    }}
                  >
                    <DropdownMenuItem onSelect={() => setEditId(comment.id)}>
                      <Pencil size={14} /> Edit comment
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onSelect={() =>
                        void execute({
                          type: "delete_comment",
                          id: comment.id,
                          threadId: thread.id,
                          expectedRevision: comment.revision,
                        }).catch(() => {})
                      }
                    >
                      <Trash2 size={14} /> Delete comment
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
          </header>
          {editId === comment.id ? (
            <CommentComposer
              placeholder="Edit your comment…"
              initialBody={comment.body}
              submitLabel="Save"
              onCancel={() => setEditId("")}
              disabled={busy}
              onSubmit={async (body, requestKey) => {
                await execute({
                  type: "edit_comment",
                  id: comment.id,
                  threadId: thread.id,
                  expectedRevision: comment.revision,
                  body,
                  requestKey,
                });
                setEditId("");
              }}
            />
          ) : (
            <button
              type="button"
              className="pc-comment-body"
              title="Show comment location"
              onClick={(event) => {
                const selection = window.getSelection();
                if (
                  event.detail > 0 &&
                  selection &&
                  !selection.isCollapsed &&
                  event.currentTarget.contains(selection.anchorNode)
                )
                  return;
                onAnchor();
              }}
            >
              {comment.deleted ? (
                <span className="pc-comment-deleted">Comment removed</span>
              ) : (
                comment.body
              )}
            </button>
          )}
          {comment.editedAt && !comment.deleted && (
            <small className="pc-comment-edited">Edited</small>
          )}
        </div>
      ))}
      <footer className="pc-thread-actions">
        <div>
          {replies > 0 && (
            <button
              aria-expanded={showReplies}
              onClick={() => setExpanded(!expanded)}
            >
              <ChevronDown
                size={13}
                className={showReplies ? "is-expanded" : ""}
              />
              {showReplies
                ? "Hide replies"
                : `${replies} ${replies === 1 ? "reply" : "replies"}`}
            </button>
          )}
          {canEdit && (
            <button disabled={busy} onClick={() => setReplying(!replying)}>
              <MessageBubble size={13} /> Reply
            </button>
          )}
        </div>
        {canEdit && (
          <button
            disabled={busy}
            onClick={() =>
              void execute({
                type: "resolve",
                threadId: thread.id,
                expectedRevision: thread.revision,
                action: thread.status === "resolved" ? "reopen" : "resolve",
              }).catch(() => {})
            }
          >
            {thread.status === "resolved" ? (
              <>
                <Undo2 size={13} /> Reopen
              </>
            ) : (
              <>
                <Check size={13} /> Resolve
              </>
            )}
          </button>
        )}
      </footer>
      {thread.agentRuns
        ?.filter((run) => run.status !== "completed")
        .map((run) => (
          <p className="pc-comment-agent-status" role="status" key={run.runId}>
            {run.agentName}:{" "}
            {run.status === "failed"
              ? "Could not finish. Mention again to retry."
              : run.status === "running"
                ? "Working…"
                : run.status.replaceAll("_", " ")}
          </p>
        ))}
      {replying && (
        <CommentComposer
          agentScope={agentScope}
          placeholder="Write a reply…"
          submitLabel="Reply"
          disabled={busy}
          onCancel={() => setReplying(false)}
          onSubmit={async (body, requestKey, agentIds) => {
            await execute({
              type: "reply",
              agentIds,
              threadId: thread.id,
              body,
              requestKey,
            });
            setReplying(false);
          }}
        />
      )}
    </article>
  );
}
