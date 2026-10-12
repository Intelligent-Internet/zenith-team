import { useEffect, useRef, useState } from "react";
import { OpenArtifactCanvasButton } from "@/features/run-deliverables/components/open-artifact-canvas-button";
import type { RunOutput } from "@/features/run-deliverables/model/run-deliverable";
import {
  Download,
  LoaderCircle,
  MoreHorizontal,
  Share,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { toast } from "@/shared/ui/sonner";
import { useLazyGetArtifactQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { ArtifactSharingControls } from "@/features/run-deliverables/components/artifact-sharing-controls";
import "@/features/run-deliverables/styles/artifact-sharing.css";
import {
  downloadArtifactFile,
  type ArtifactFileSelection,
} from "@/features/chat-panel/model/artifact-file-access";

export function ArtifactFileActions({
  companyId,
  file,
  output,
}: {
  companyId: string;
  file: ArtifactFileSelection;
  output: RunOutput;
}) {
  const [sharing, setSharing] = useState(false);
  const [isDownloading, setIsDownloading] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const downloadRef = useRef<AbortController | null>(null);
  const [getArtifact, detail] = useLazyGetArtifactQuery();

  useEffect(() => () => downloadRef.current?.abort(), []);

  const share = async () => {
    try {
      await getArtifact({ companyId, artifactId: file.artifactId }).unwrap();
      setSharing(true);
    } catch {
      toast.error("Sharing could not be loaded. Try again.");
    }
  };

  const download = async () => {
    if (downloadRef.current) return;
    const controller = new AbortController();
    downloadRef.current = controller;
    setIsDownloading(true);
    try {
      const blob = await downloadArtifactFile(
        companyId,
        file,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      const objectUrl = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = objectUrl;
      anchor.download =
        file.filename.replace(/[\/\\\u0000-\u001f]/g, "_") || "artifact";
      document.body.append(anchor);
      anchor.click();
      anchor.remove();
      window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1_000);
    } catch {
      if (!controller.signal.aborted) {
        toast.error("The artifact could not be downloaded. Try again.");
      }
    } finally {
      downloadRef.current = null;
      if (!controller.signal.aborted) setIsDownloading(false);
    }
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            ref={triggerRef}
            aria-label={`Actions for ${file.filename}`}
            aria-busy={detail.isFetching || isDownloading}
            className="artifact-action-button"
            disabled={detail.isFetching}
            title="File actions"
            type="button"
          >
            {detail.isFetching || isDownloading ? (
              <LoaderCircle
                aria-hidden
                className="artifact-spinner"
                size={17}
              />
            ) : (
              <MoreHorizontal aria-hidden size={17} />
            )}
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="end"
          className="z-[140]"
          aria-label={`Actions for ${file.filename}`}
        >
          <DropdownMenuItem onSelect={() => void share()}>
            <Share aria-hidden size={16} /> Share
          </DropdownMenuItem>
          <OpenArtifactCanvasButton
            companyId={companyId}
            output={output}
            outputItemId={file.id}
            menuItem
          />
          <DropdownMenuItem
            disabled={isDownloading}
            onSelect={() => void download()}
          >
            <Download aria-hidden size={16} />{" "}
            {isDownloading ? "Downloading…" : "Download"}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {sharing && detail.currentData ? (
        <ArtifactSharingControls
          triggerRef={triggerRef}
          companyId={companyId}
          artifactId={file.artifactId}
          access={detail.currentData.access}
          refreshing={detail.isFetching}
          url={detail.currentData.url}
          title={detail.currentData.result.output?.title ?? file.title}
          open={sharing}
          onOpenChange={setSharing}
        />
      ) : null}
    </>
  );
}
