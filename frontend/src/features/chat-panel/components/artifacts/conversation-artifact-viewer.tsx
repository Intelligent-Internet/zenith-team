import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import { ArtifactAppView } from "@/features/chat-panel/components/artifacts/artifact-app-view";
import { ArtifactFileView } from "@/features/chat-panel/components/artifacts/artifact-file-view";
import { ArtifactViewerShell } from "@/features/chat-panel/components/artifacts/artifact-viewer-shell";
import type { ConversationArtifactReference } from "@/features/chat-panel/model/conversation-artifact";
import { outputDocumentLocation } from "@/features/project-canvas/output-document-location";
import { lazy, Suspense } from "react";

const OutputDocumentWorkspace = lazy(
  () => import("@/features/project-canvas/output-document-workspace"),
);

export function ConversationArtifactViewer({
  artifact,
  companyId,
  initialFileId,
  onBack,
  backLabel,
  onClose,
  onOpenStudio,
  onFeedback,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  initialFileId?: string;
  onBack: () => void;
  backLabel?: string;
  onClose?: () => void;
  onOpenStudio?: () => void;
  onFeedback?: (artifact: ConversationArtifactReference) => void;
}) {
  const location = outputDocumentLocation(artifact.artifact);
  // Submitted results keep their immutable evidence and Task review controls.
  if (location && !artifact.deliverable)
    return (
      <Suspense
        fallback={
          <div role="status" className="artifact-text-view">
            Opening document…
          </div>
        }
      >
        <OutputDocumentWorkspace
          key={artifact.artifact.id}
          companyId={companyId}
          location={location}
          initialOutputItemId={initialFileId}
          onClose={onBack}
        />
      </Suspense>
    );
  return (
    <ArtifactViewerShell
      artifact={artifact}
      companyId={companyId}
      onBack={onBack}
      backLabel={backLabel}
      onClose={onClose}
      onOpenStudio={onOpenStudio}
      onFeedback={onFeedback}
    >
      {artifact.artifact.type === "app" ? (
        <ArtifactAppView
          artifact={artifact}
          companyId={companyId}
          onEscape={onBack}
        />
      ) : (
        <ArtifactFileView
          key={`${artifact.artifact.id}:${initialFileId ?? ""}`}
          artifact={artifact}
          companyId={companyId}
          initialFileId={initialFileId}
        />
      )}
    </ArtifactViewerShell>
  );
}
