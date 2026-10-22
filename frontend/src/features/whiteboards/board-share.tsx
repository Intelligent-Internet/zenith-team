import { Users } from "@/shared/ui/icons";
import { ArtifactSharing } from "@/features/project-canvas/documents/artifact-sharing";
import type { BoardScope } from "./board-api";

export function BoardShareButton({
  scope,
  compact,
}: {
  scope: BoardScope;
  compact: boolean;
}) {
  return (
    <ArtifactSharing
      scope={{ companyId: scope.companyId, artifactId: scope.boardId }}
      noun="Whiteboard"
      trigger={
        <button
          type="button"
          className={compact ? "wb-icon-button" : "wb-primary wb-share-button"}
          aria-label={compact ? "Share whiteboard" : undefined}
          title={compact ? "Share" : undefined}
        >
          <Users size={18} aria-hidden />
          {compact ? null : "Share"}
        </button>
      }
    />
  );
}
