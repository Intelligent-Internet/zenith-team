import type { DocumentAgentScope } from "../documents/use-document-agents";
import type { ReactNode } from "react";
import { MessageBubble, MessageAdd, X } from "@/shared/ui/icons";
import { useGetCurrentUserQuery } from "@/shared/api/account-api";
import { CommentsLoading } from "./comments-loading";
import {
  CommentAvatar,
  CommentComposer,
  ReviewThread,
  type CommentFilter,
  type ReviewThreadData,
} from "../review/review-comments";
import type { DocumentCommentsController } from "./use-document-comments";
import type { ExecuteCanvasCommand } from "../canvas/add-artifact";
import "../review/floating-comments.css";
import "./page-comments.css";

export function DocumentCommentHighlights({
  comments,
}: {
  comments: DocumentCommentsController;
}) {
  return (
    <style>{`
    ::highlight(${comments.highlightName}) { background-color: color-mix(in srgb, #e9b72f 22%, transparent); }
    ::highlight(${comments.selectedHighlightName}) { background-color: color-mix(in srgb, #e9b72f 44%, transparent); }
  `}</style>
  );
}
export function DocumentCommentFilter({
  comments,
}: {
  comments: DocumentCommentsController;
}) {
  return (
    <select
      aria-label="Filter comments"
      value={comments.filter}
      onChange={(event) =>
        comments.setFilter(event.target.value as CommentFilter)
      }
    >
      <option value="open">Open</option>
      <option value="resolved">Resolved</option>
      <option value="all">All</option>
    </select>
  );
}
export function DocumentCommentControls({
  comments,
  className = "po-secondary",
  compact = false,
}: {
  comments: DocumentCommentsController;
  className?: string;
  compact?: boolean;
}) {
  return (
    <>
      <button
        ref={comments.toggle}
        type="button"
        className={className}
        aria-label={comments.open ? "Hide comments" : "Show comments"}
        aria-expanded={comments.open}
        aria-controls={comments.panelId}
        onClick={() => comments.onOpenChange(!comments.open)}
      >
        <MessageBubble size={18} />
        <span>Comments</span>
        {(compact || comments.openCount > 0) && (
          <span className="po-comment-count">{comments.openCount}</span>
        )}
      </button>
    </>
  );
}
function DocumentAddComment({
  comments,
  className,
}: {
  comments: DocumentCommentsController;
  className?: string;
}) {
  if (!comments.canCompose) return null;
  return (
    <button
      type="button"
      className={className}
      aria-label="Add comment"
      title="Add comment"
      onClick={comments.start}
    >
      <MessageAdd size={18} />
      <span>New comment</span>
    </button>
  );
}

export function DocumentCommentsPanel({
  comments,
  ready,
  failed,
  retry,
  busy,
  canComment,
  page,
  execute,
  target,
  onNavigate,
  onCancel = comments.cancel,
  layout,
  error,
  header,
  agentScope,
  renderThreadContent,
}: {
  comments: DocumentCommentsController;
  ready: boolean;
  failed: boolean;
  retry: () => unknown;
  busy: boolean;
  canComment: boolean;
  page: boolean;
  execute: ExecuteCanvasCommand;
  target: { pageId: string } | { versionId: string };
  onNavigate: (thread: ReviewThreadData) => void;
  onCancel?: () => void;
  layout: "page" | "canvas";
  error?: string;
  header?: ReactNode;
  renderThreadContent?: (thread: ReviewThreadData) => ReactNode;
  agentScope?: DocumentAgentScope;
}) {
  const currentUser = useGetCurrentUserQuery();
  const cardClass =
    layout === "page" ? "po-page-comment-card" : "pc-floating-comment-card";
  return (
    <aside
      id={comments.panelId}
      ref={comments.rail}
      className={
        layout === "page" ? "po-page-comments" : "pc-floating-comments"
      }
      hidden={!comments.open}
      data-empty={ready && comments.anchors.length === 0}
      aria-label="Document discussion"
      onKeyDown={(event) => {
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          comments.close();
        }
      }}
    >
      {header ?? <DocumentCommentsHeader comments={comments} />}
      {error && (
        <p className="pc-comment-error" role="alert">
          {error}
        </p>
      )}
      <div className="pc-review-comments-panel">
        <div
          className="pc-thread-list po-page-comment-cards"
          onScroll={(event) => {
            if (!comments.active) return;
            const list = event.currentTarget;
            const card = list.querySelector<HTMLElement>(
              `[data-page-comment="${CSS.escape(comments.active)}"]`,
            );
            if (!card) return;
            const viewport = list.getBoundingClientRect();
            const bounds = card.getBoundingClientRect();
            if (bounds.bottom <= viewport.top || bounds.top >= viewport.bottom)
              comments.setActive("");
          }}
        >
          {!ready && !failed && <CommentsLoading />}
          {ready &&
            !failed &&
            comments.animatedThreads.length === 0 &&
            !comments.showComposer && (
              <p className="pc-comments-empty p-[42px_22px] text-center text-[var(--pc-soft)]">
                {comments.filter === "resolved"
                  ? "No resolved comments."
                  : comments.filter === "open"
                    ? "No open comments."
                    : "No comments yet."}
              </p>
            )}
          {failed && (
            <div
              className="pc-comments-empty p-[42px_22px] text-center text-[var(--pc-soft)]"
              role="alert"
            >
              <p>Comments are unavailable.</p>
              <button onClick={() => void retry()}>Retry</button>
            </div>
          )}
          {!failed &&
            comments.animatedThreads.map(({ thread, resolving }) => (
              <div
                key={thread.id}
                data-page-comment={thread.id}
                className={`${cardClass} pc-animated-thread ${resolving ? "is-resolving" : ""}`}
                inert={resolving}
                onAnimationEnd={(event) => {
                  if (
                    event.target === event.currentTarget &&
                    event.animationName === "pc-comment-resolved"
                  )
                    comments.finishExit(thread.id);
                }}
              >
                <div className="min-h-0 overflow-hidden">
                  <ReviewThread
                    agentScope={agentScope}
                    thread={thread}
                    page={page}
                    canEdit={canComment}
                    currentUserId={currentUser.data?.id}
                    active={comments.active === thread.id}
                    busy={busy}
                    execute={execute}
                    onAnchor={() => onNavigate(thread)}
                    onHover={(hovering) =>
                      comments.setHovered(hovering ? thread.id : "")
                    }
                  />
                  {comments.open &&
                    comments.active === thread.id &&
                    renderThreadContent?.(thread)}
                </div>
              </div>
            ))}
          {comments.showComposer && (
            <div
              className={`pc-comment-composer po-page-comment-compose ${cardClass}`}
              data-page-comment="draft"
            >
              <div className="flex items-center gap-[10px] p-[4px_0_14px] text-[length:13px]">
                <CommentAvatar name={currentUser.data?.displayName || "You"} />
                <strong>{currentUser.data?.displayName || "You"}</strong>
              </div>
              <CommentComposer
                agentScope={agentScope}
                key={"pageId" in target ? target.pageId : target.versionId}
                inputRef={comments.composer}
                clearOnSubmit={false}
                submitted={!!comments.postedThreadId}
                placeholder={
                  comments.postedThreadId
                    ? "Comment saved. Syncing…"
                    : "Add a comment…"
                }
                submitLabel="Comment"
                onCancel={onCancel}
                contextKey={JSON.stringify(comments.anchor)}
                disabled={!!comments.postedThreadId || busy || failed || !ready}
                onSubmit={(body, requestKey, agentIds) =>
                  comments.submit(execute, target, body, requestKey, agentIds)
                }
              />
            </div>
          )}
        </div>
      </div>
    </aside>
  );
}
export function DocumentCommentsHeader({
  comments,
}: {
  comments: DocumentCommentsController;
}) {
  return (
    <div className="pc-document-comments-header">
      <header className="pc-document-comments-heading">
        <strong>Comments</strong>
        <button
          type="button"
          aria-label="Close comments"
          onClick={comments.close}
        >
          <X size={16} />
        </button>
      </header>
      <div className="pc-document-comments-tools">
        <DocumentCommentFilter comments={comments} />
        {!comments.showComposer && (
          <DocumentAddComment
            comments={comments}
            className="pc-document-new-comment"
          />
        )}
      </div>
    </div>
  );
}
