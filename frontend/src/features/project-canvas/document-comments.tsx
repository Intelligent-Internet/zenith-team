import type { CanvasScope } from "./canvas-api";
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
} from "./review-comments";
import type { DocumentCommentsController } from "./use-document-comments";
import type { ExecuteCanvasCommand } from "./add-artifact";
import "./floating-comments.css";
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
      <option value="open">Open comments</option>
      <option value="resolved">Resolved comments</option>
      <option value="all">All comments</option>
    </select>
  );
}
export function DocumentCommentControls({
  comments,
  className = "po-secondary",
  addClassName = className,
  compact = false,
}: {
  comments: DocumentCommentsController;
  className?: string;
  addClassName?: string;
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
      {!compact && (
        <DocumentAddComment comments={comments} className={addClassName} />
      )}
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
  suggestions,
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
  suggestions?: ReactNode;
  agentScope?: CanvasScope;
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
          {suggestions}
          {!ready && !failed && <CommentsLoading />}
          {ready &&
            !failed &&
            comments.animatedThreads.length === 0 &&
            !comments.showComposer && (
              <p className="pc-comments-empty">
                {comments.filter === "resolved"
                  ? "No resolved comments."
                  : comments.filter === "open"
                    ? "No open comments."
                    : "No comments yet."}
              </p>
            )}
          {failed && (
            <div className="pc-comments-empty" role="alert">
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
                <div className="pc-animated-thread-content">
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
                </div>
              </div>
            ))}
          {comments.showComposer && (
            <div
              className={`pc-comment-composer po-page-comment-compose ${cardClass}`}
              data-page-comment="draft"
            >
              <div className="pc-margin-composer-author">
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
  showAdd = false,
}: {
  comments: DocumentCommentsController;
  showAdd?: boolean;
}) {
  return (
    <header>
      <DocumentCommentFilter comments={comments} />
      {showAdd && <DocumentAddComment comments={comments} />}
      <button
        type="button"
        aria-label="Close comments"
        onClick={comments.close}
      >
        <X size={16} />
      </button>
    </header>
  );
}
