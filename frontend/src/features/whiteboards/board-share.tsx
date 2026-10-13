import { useRef, useState } from "react";
import { Users } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import { useLazyGetArtifactQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { ArtifactSharingControls } from "@/features/run-deliverables/components/artifact-sharing-controls";
import "@/features/run-deliverables/styles/artifact-sharing.css";
import type { BoardScope } from "./board-api";

/**
 * A Whiteboard is an Artifact, so it is shared, and published as a public
 * link, with the controls every Artifact uses.
 */
export function BoardShareButton({
  scope,
  title,
  compact,
}: {
  scope: BoardScope;
  title: string;
  compact: boolean;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const [getArtifact, detail] = useLazyGetArtifactQuery();
  const share = async () => {
    try {
      await getArtifact({
        companyId: scope.companyId,
        artifactId: scope.boardId,
      }).unwrap();
      setOpen(true);
    } catch {
      toast.error("Sharing could not be loaded. Try again.");
    }
  };
  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={compact ? "wb-icon-button" : "wb-primary wb-share-button"}
        aria-label={compact ? "Share whiteboard" : undefined}
        title={compact ? "Share" : undefined}
        aria-busy={detail.isFetching}
        disabled={detail.isFetching}
        onClick={() => void share()}
      >
        <Users size={18} aria-hidden />
        {compact ? null : "Share"}
      </button>
      {open && detail.currentData ? (
        <ArtifactSharingControls
          triggerRef={trigger}
          companyId={scope.companyId}
          artifactId={scope.boardId}
          access={detail.currentData.access}
          refreshing={detail.isFetching}
          url={detail.currentData.url}
          title={detail.currentData.result.output?.title ?? title}
          open={open}
          onOpenChange={setOpen}
          whiteboard
        />
      ) : null}
    </>
  );
}
