import { FileViewerShell } from "./file-viewer-shell";
import { OpenArtifactCanvasButton } from "@/features/run-deliverables/components/open-artifact-canvas-button";
import type { ReactNode } from "react";
import { MessageSquareText, Studio } from "@/shared/ui/icons";

import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import type { ConversationArtifactReference } from "@/features/chat-panel/model/conversation-artifact";

export function ArtifactViewerShell({
  artifact,
  companyId,
  children,
  onBack,
  backLabel = "Back to artifacts",
  onClose,
  onOpenStudio,
  onFeedback,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  children: ReactNode;
  onBack: () => void;
  backLabel?: string;
  onClose?: () => void;
  onOpenStudio?: () => void;
  onFeedback?: (artifact: ConversationArtifactReference) => void;
}) {
  const reference: ConversationArtifactReference = {
    artifactId: artifact.artifact.id,
    runId: artifact.runId,
    title: artifact.artifact.title,
    type: artifact.artifact.type,
  };
  return (
    <FileViewerShell
      title={artifact.artifact.title}
      label={`${artifact.artifact.title} artifact`}
      type={artifact.artifact.type}
      onBack={onBack}
      backLabel={backLabel}
      onClose={onClose}
      closeLabel="Close artifacts"
      actions={
        <>
          {artifact.artifact.type === "media" && onOpenStudio ? (
            <button
              className="artifact-action-button"
              onClick={onOpenStudio}
              aria-label="Open in Studio"
              type="button"
            >
              <Studio aria-hidden size={15} />
              Studio
            </button>
          ) : null}
          <OpenArtifactCanvasButton
            companyId={companyId}
            output={artifact.artifact}
            onOpen={onClose}
          />
          {onFeedback ? (
            <button
              className="artifact-viewer__feedback"
              onClick={() => onFeedback(reference)}
              aria-label="Request Change"
              type="button"
            >
              <MessageSquareText aria-hidden size={15} />
              Request Change
            </button>
          ) : null}
        </>
      }
    >
      {children}
    </FileViewerShell>
  );
}
