import { useCallback } from "react";
import { ArtifactReview } from "./artifact-review";
import {
  useOutputReviewQuery,
  useOutputReviewCommandMutation,
} from "../api/output-review-api";
import type { ExecuteCanvasCommand } from "../canvas/add-artifact";
import "../canvas/project-canvas.css";
import "../documents/document-workspace.css";

export default function OutputReviewWorkspace({
  companyId,
  outputId,
  initialOutputItemId,
  initialVersionId,
  onClose,
}: {
  companyId: string;
  outputId: string;
  initialOutputItemId?: string;
  initialVersionId?: string;
  onClose: () => void;
}) {
  const review = useOutputReviewQuery(
    { companyId, outputId },
    { refetchOnMountOrArgChange: true },
  );
  const [mutate] = useOutputReviewCommandMutation();
  const execute = useCallback<ExecuteCanvasCommand>(
    async (command) => {
      const result = await mutate({
        companyId,
        outputId,
        command: {
          ...command,
          requestKey: command.requestKey ?? crypto.randomUUID(),
        },
      }).unwrap();
      await review.refetch().unwrap();
      return result;
    },
    [companyId, outputId, mutate, review.refetch],
  );
  if (review.isError)
    return (
      <div
        className="grid [place-content:center] gap-[12px] h-full p-[32px] text-workspace-muted"
        role="alert"
      >
        <p>This artifact is unavailable or your access has changed.</p>
        <button onClick={() => void review.refetch()}>Retry</button>
        <button onClick={onClose}>Back to workspace</button>
      </div>
    );
  if (!review.currentData)
    return (
      <div
        className="grid [place-content:center] gap-[12px] h-full p-[32px] text-workspace-muted"
        role="status"
      >
        Opening canvas…
      </div>
    );
  return (
    <ArtifactReview
      embedded
      initialSharedView={!initialVersionId}
      reviewOnly={!!initialVersionId}
      scope={{ companyId, outputId }}
      outputId={outputId}
      artifact={review.currentData.artifact}
      initialOutputItemId={initialOutputItemId}
      versionId={
        initialVersionId ??
        (initialOutputItemId
          ? (review.currentData.versions.find(
              (version) => version.outputItemId === initialOutputItemId,
            )?.id ?? outputId)
          : undefined)
      }
      canEdit={false}
      canComment={review.currentData.canComment}
      canEditDocuments={review.currentData.canEdit}
      execute={execute}
      onClose={onClose}
    />
  );
}
