import { useCallback, type ReactNode } from "react";
import { ArtifactReview } from "../review/artifact-review";
import { useCanvasCommandMutation, useGetCanvasQuery } from "../api/canvas-api";
import type { ExecuteCanvasCommand } from "../canvas/add-artifact";
import "../canvas/project-canvas.css";
import "./document-workspace.css";

// Task Outputs carry a server-resolved Project destination. Direct/Schedule
// Outputs have no such destination and keep their existing file viewer.
type OutputDocumentLocation = {
  projectId: string;
  artifactId: string;
  versionId: string;
};

export default function OutputDocumentWorkspace({
  companyId,
  location,
  initialOutputItemId,
  onClose,
  fallback,
  reviewOnly = false,
}: {
  companyId: string;
  location: OutputDocumentLocation;
  initialOutputItemId?: string;
  onClose: () => void;
  fallback?: ReactNode;
  reviewOnly?: boolean;
}) {
  const scope = { companyId, projectId: location.projectId };
  const board = useGetCanvasQuery(scope);
  const [mutate] = useCanvasCommandMutation();
  const execute = useCallback<ExecuteCanvasCommand>(
    async (command) => {
      const result = await mutate({
        companyId,
        projectId: location.projectId,
        command: {
          ...command,
          requestKey: command.requestKey ?? crypto.randomUUID(),
        },
      }).unwrap();
      const refreshed = await board.refetch().unwrap();
      if (refreshed.sequence < result.sequence) await board.refetch().unwrap();
      return result;
    },
    [mutate, companyId, location.projectId, board.refetch],
  );
  const artifact = board.data?.artifacts.find(
    (row) => row.id === location.artifactId,
  );
  if (board.isError || (!board.isLoading && !artifact))
    return (
      fallback ?? (
        <div className="pc-output-workspace-state" role="alert">
          <p>This document is unavailable or your access has changed.</p>
          <button onClick={() => void board.refetch()}>Retry</button>
          <button onClick={onClose}>Back to artifacts</button>
        </div>
      )
    );
  if (!artifact || !board.data)
    return (
      <div className="pc-output-workspace-state" role="status">
        Opening document…
      </div>
    );
  return (
    <ArtifactReview
      embedded
      reviewOnly={reviewOnly}
      scope={scope}
      artifact={artifact}
      versionId={location.versionId}
      initialOutputItemId={initialOutputItemId}
      canEdit={board.data.canEdit}
      canEditDocuments={board.data.canEditDocuments}
      execute={execute}
      onClose={onClose}
    />
  );
}
