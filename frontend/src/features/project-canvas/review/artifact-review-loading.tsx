import { CanvasDialog } from "../canvas/canvas-dialog";
import { PageDocumentLoading } from "../documents/page-document-loading";
import "../documents/document-workspace.css";

export function ArtifactReviewLoading({
  title,
  onClose,
}: {
  title: string;
  onClose: () => void;
}) {
  return (
    <CanvasDialog
      title={title}
      description="Opening document…"
      wide
      className="pc-document-workspace"
      onClose={onClose}
    >
      <PageDocumentLoading />
    </CanvasDialog>
  );
}
