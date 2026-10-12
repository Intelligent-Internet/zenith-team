import { CanvasDialog } from "./canvas-dialog";
import { PageDocumentLoading } from "./page-document-loading";
import "./document-workspace.css";

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
