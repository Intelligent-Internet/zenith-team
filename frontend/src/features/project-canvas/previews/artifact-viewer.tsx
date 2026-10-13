import { skipToken } from "@reduxjs/toolkit/query";
import { PageDocumentLoading } from "../documents/page-document-loading";
import { useOutputDocumentVersionQuery } from "../api/output-review-api";
import type { MediaReview } from "../review/annotation-overlay";
import { MarkdownView } from "./markdown-view";
import { BlockDocument } from "../documents/block-editor-schema";
import { DocumentTitle } from "../documents/document-title";
import { ArtifactFileViewer, ViewerMessage } from "./artifact-file-viewer";
import { useCanvasBodyQuery, type CanvasVersion } from "../api/canvas-api";
import type { CanvasScope } from "../api/canvas-scope";

export type ArtifactScope =
  CanvasScope | { companyId: string; outputId: string };

export function ArtifactViewer({
  scope,
  version,
  review,
  focusId,
  onEditDocument,
  editButtonContainer,
  autoEdit = false,
  initialOutputItemId,
  title,
}: {
  scope: ArtifactScope;
  version: CanvasVersion;
  review?: MediaReview;
  focusId?: string;
  onEditDocument?: (outputItemId: string, filename: string) => Promise<void>;
  editButtonContainer?: HTMLElement | null;
  autoEdit?: boolean;
  initialOutputItemId?: string;
  title?: string;
}) {
  if (version.sourceType === "wiki")
    return (
      <DocumentSnapshot
        scope={scope}
        version={version}
        focusId={focusId}
        title={title}
      />
    );
  return (
    <ArtifactFileViewer
      scope={scope}
      version={version}
      review={review}
      onEditDocument={onEditDocument}
      editButtonContainer={editButtonContainer}
      autoEdit={autoEdit}
      initialOutputItemId={initialOutputItemId}
    />
  );
}
function DocumentSnapshot({
  scope,
  version,
  focusId,
  title,
}: {
  scope: ArtifactScope;
  version: CanvasVersion;
  focusId?: string;
  title?: string;
}) {
  const projectVersion = useCanvasBodyQuery(
    "projectId" in scope ? { ...scope, versionId: version.id } : skipToken,
  );
  const outputVersion = useOutputDocumentVersionQuery(
    "outputId" in scope ? { ...scope, versionId: version.id } : skipToken,
  );
  const result = "outputId" in scope ? outputVersion : projectVersion;
  if (result.isLoading) return <PageDocumentLoading toolbar={false} />;
  if (result.isError)
    return (
      <ViewerMessage>
        This version is no longer available.{" "}
        <button onClick={() => void result.refetch()}>Retry</button>
      </ViewerMessage>
    );
  return (
    <div
      className="pc-reader-scroll"
      tabIndex={0}
      aria-label="Document content"
    >
      {title && (
        <header className="pc-document-heading">
          <DocumentTitle title={title} />
        </header>
      )}
      {result.data?.content?.type === "doc" &&
      ("projectId" in scope || version.outputItemId) ? (
        <BlockDocument
          showOutline
          scope={
            "projectId" in scope
              ? scope
              : {
                  companyId: scope.companyId,
                  outputId: scope.outputId,
                  outputItemId: version.outputItemId!,
                }
          }
          content={result.data.content}
          focusId={focusId}
        />
      ) : (
        <MarkdownView>
          {result.data?.body || "This document is empty."}
        </MarkdownView>
      )}
    </div>
  );
}
