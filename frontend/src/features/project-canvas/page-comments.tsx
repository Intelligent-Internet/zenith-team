import { CommentConnectors } from "./comment-connectors";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  DocumentCommentControls,
  DocumentCommentHighlights,
  DocumentCommentsHeader,
  DocumentCommentsPanel,
} from "./document-comments";
import { useDocumentComments } from "./use-document-comments";
import { usePageThreadsQuery, type PageScope } from "./page-api";
import { useCanvasCommandMutation } from "./canvas-api";
import type { ExecuteCanvasCommand } from "./add-artifact";
import type { ReviewAnchor } from "./annotation-overlay";
import { scrollToReviewAnchor } from "./review-navigation";

export function PageComments({
  scope,
  canComment,
  open,
  onOpenChange,
  anchor,
  onAnchorChange,
  toolbar,
  content,
  compactToolbar = false,
  requestedThreadId = "",
  suggestions,
}: {
  scope: PageScope;
  canComment: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchor: ReviewAnchor;
  onAnchorChange: (anchor: ReviewAnchor) => void;
  toolbar: HTMLElement | null;
  content: HTMLDivElement | null;
  compactToolbar?: boolean;
  requestedThreadId?: string;
  suggestions?: ReactNode;
}) {
  const query = usePageThreadsQuery(scope);
  const [mutate, mutation] = useCanvasCommandMutation();
  const [error, setError] = useState("");
  const comments = useDocumentComments({
    threads: query.isError ? undefined : query.currentData?.items,
    content,
    open,
    onOpenChange,
    anchor,
    onAnchorChange,
    canCompose: canComment,
  });
  const revealedRequest = useRef("");
  useEffect(() => {
    if (!requestedThreadId || revealedRequest.current === requestedThreadId)
      return;
    const thread = query.currentData?.items.find(
      (item) => item.id === requestedThreadId,
    );
    if (thread) {
      revealedRequest.current = requestedThreadId;
      comments.reveal(thread);
    }
  }, [requestedThreadId, query.currentData]);
  // The Page editor emits an anchor only when its Comment action is invoked.
  useEffect(() => {
    if (anchor.type !== "version") comments.start();
  }, [anchor, comments.start]);
  const execute: ExecuteCanvasCommand = async (command) => {
    setError("");
    try {
      return await mutate({
        companyId: scope.companyId,
        projectId: scope.projectId,
        command: {
          ...command,
          requestKey: command.requestKey ?? crypto.randomUUID(),
        },
      }).unwrap();
    } catch (reason) {
      setError(
        "The comment could not be saved. Your access or this thread may have changed.",
      );
      throw reason;
    }
  };
  return (
    <>
      <DocumentCommentHighlights comments={comments} />
      {toolbar &&
        createPortal(
          <DocumentCommentControls
            comments={comments}
            compact={compactToolbar}
          />,
          toolbar,
        )}

      <CommentConnectors
        layer={comments.connectorLayer}
        paths={comments.connectors}
        activeId={comments.hovered || comments.active}
        open={open}
      />
      <DocumentCommentsPanel
        suggestions={suggestions}
        agentScope={canComment ? scope : undefined}
        comments={comments}
        layout="page"
        page
        canComment={canComment}
        ready={!!query.currentData && !query.isError}
        failed={query.isError}
        retry={query.refetch}
        busy={mutation.isLoading}
        execute={execute}
        target={{ pageId: scope.pageId }}
        error={error}
        header={
          <DocumentCommentsHeader
            comments={comments}
            showAdd={compactToolbar}
          />
        }
        onNavigate={(thread) => {
          comments.reveal(thread);
          if (content) scrollToReviewAnchor(content, thread.anchor);
        }}
      />
    </>
  );
}
