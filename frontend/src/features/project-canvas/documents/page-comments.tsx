import { PageRunPreviews } from "./page-run-previews";
import { CommentConnectors } from "./comment-connectors";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  DocumentCommentControls,
  DocumentCommentHighlights,
  DocumentCommentsHeader,
  DocumentCommentsPanel,
} from "./document-comments";
import { useDocumentComments } from "./use-document-comments";
import {
  usePageReviewQuery,
  useMutatePageReviewMutation,
} from "../api/page-api";
import type { ExecuteCanvasCommand } from "../canvas/add-artifact";
import type { ReviewAnchor } from "../review/annotation-overlay";
import { scrollToReviewAnchor } from "../review/review-navigation";

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
}: {
  scope: { companyId: string; pageId: string; projectId?: string };
  canComment: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchor: ReviewAnchor;
  onAnchorChange: (anchor: ReviewAnchor) => void;
  toolbar: HTMLElement | null;
  content: HTMLDivElement | null;
  compactToolbar?: boolean;
  requestedThreadId?: string;
}) {
  const query = usePageReviewQuery(scope);
  const [mutate, mutation] = useMutatePageReviewMutation();
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
        ...scope,
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
        renderThreadContent={(thread) =>
          thread.agentRuns?.map((run) => (
            <PageRunPreviews
              key={`${scope.companyId}:${scope.pageId}:${run.runId}`}
              scope={scope}
              runId={run.runId}
            />
          ))
        }
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
        header={<DocumentCommentsHeader comments={comments} />}
        onNavigate={(thread) => {
          comments.reveal(thread);
          if (content) scrollToReviewAnchor(content, thread.anchor);
        }}
      />
    </>
  );
}
