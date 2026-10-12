import { useState } from "react";
import {
  MessageSquare,
  X,
  ArrowUpRight,
  Check,
  LoaderCircle,
} from "@/shared/ui/icons";
import {
  CommentAvatar,
  CommentFilters,
  commentLocation,
  reviewDate,
  type CommentFilter,
} from "./review-comments";
import {
  useCanvasThreadsQuery,
  type CanvasScope,
  type CanvasVersion,
} from "./canvas-api";

type NodeCommentsProps = {
  version: CanvasVersion;
  title: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  onOpen: (threadId: string) => void;
};

export function NodeComments({
  version,
  title,
  open = false,
  onOpenChange,
  onOpen,
}: NodeCommentsProps) {
  return (
    <button
      type="button"
      className={`pc-node-comments-badge nodrag nopan ${version.openComments ? "has-comments" : ""}`}
      aria-label={`${version.openComments} open comments on ${title}`}
      aria-expanded={open}
      title="Show comments"
      onClick={() => (onOpenChange ? onOpenChange(!open) : onOpen(""))}
      onDoubleClick={(event) => event.stopPropagation()}
    >
      {version.openComments > 0 ? (
        <span>{version.openComments > 99 ? "99+" : version.openComments}</span>
      ) : (
        <MessageSquare size={14} aria-hidden />
      )}
    </button>
  );
}

export function NodeCommentsContent({
  scope,
  version,
  title,
  onClose,
  onOpen,
}: Pick<NodeCommentsProps, "version" | "title" | "onOpen"> & {
  scope: CanvasScope;
  onClose: () => void;
}) {
  const openThread = (threadId: string) => {
    onClose();
    onOpen(threadId);
  };
  return (
    <>
      <header>
        <div>
          <strong>
            Discussion <span className="pc-count">v{version.number}</span>
          </strong>
          <span title={title}>{title}</span>
        </div>
        <button
          type="button"
          className="pc-icon-button"
          aria-label="Close comments"
          onClick={onClose}
        >
          <X size={17} />
        </button>
      </header>
      <NodeThreadList
        key={version.id}
        scope={scope}
        version={version}
        onOpen={openThread}
      />
      <footer className="pc-node-comments-footer">
        <span>Comments stay with this version</span>
        <button type="button" onClick={() => openThread("")}>
          Open artifact <ArrowUpRight size={15} />
        </button>
      </footer>
    </>
  );
}

function NodeThreadList({
  scope,
  version,
  onOpen,
}: {
  scope: CanvasScope;
  version: CanvasVersion;
  onOpen: (threadId: string) => void;
}) {
  const [filter, setFilter] = useState<CommentFilter>("open");
  const query = useCanvasThreadsQuery(
    { ...scope, versionId: version.id },
    { refetchOnMountOrArgChange: true },
  );
  const threads = !query.isError ? query.currentData?.items : undefined;
  const filtered = threads?.filter(
    (thread) => filter === "all" || thread.status === filter,
  );
  return (
    <>
      {threads && (
        <CommentFilters threads={threads} value={filter} onChange={setFilter} />
      )}
      <div className="pc-node-thread-list">
        {query.isError ? (
          <div className="pc-node-comments-state" role="alert">
            <p>Comments could not be loaded.</p>
            <button onClick={() => void query.refetch()}>Try again</button>
          </div>
        ) : !threads ? (
          <div className="pc-node-comments-state" role="status">
            <LoaderCircle size={22} className="pc-spin" />
            Loading comments…
          </div>
        ) : !filtered?.length ? (
          <div className="pc-node-comments-state">
            {filter === "resolved" ? (
              <Check size={24} />
            ) : (
              <MessageSquare size={24} />
            )}
            <strong>
              {filter === "resolved"
                ? "No resolved threads"
                : threads.length
                  ? "All caught up"
                  : "No comments yet"}
            </strong>
            <p>
              {threads.length
                ? "Choose another filter to see more conversations."
                : "Open the artifact to start a conversation or mark a specific area."}
            </p>
          </div>
        ) : (
          filtered.map((thread) => {
            const first = thread.comments.find((comment) => !comment.deleted);
            return (
              <button
                key={thread.id}
                type="button"
                className="pc-node-thread"
                onClick={() => onOpen(thread.id)}
              >
                <span className="pc-node-thread-heading">
                  <CommentAvatar name={first?.authorName ?? "?"} />
                  <strong>{first?.authorName ?? "Removed comment"}</strong>
                  {first && (
                    <time
                      dateTime={first.createdAt}
                      title={reviewDate(first.createdAt)}
                    >
                      {new Date(first.createdAt).toLocaleDateString(undefined, {
                        month: "short",
                        day: "numeric",
                      })}
                    </time>
                  )}
                  <ArrowUpRight size={14} />
                </span>
                <p>{first?.body ?? "This comment was removed."}</p>
                <span className="pc-node-thread-meta">
                  <span>
                    {thread.status === "resolved" && <Check size={12} />}
                    {commentLocation(thread.anchor)}
                  </span>
                  {thread.comments.length > 1 && (
                    <span>
                      {thread.comments.length - 1}{" "}
                      {thread.comments.length === 2 ? "reply" : "replies"}
                    </span>
                  )}
                </span>
              </button>
            );
          })
        )}
      </div>
    </>
  );
}
