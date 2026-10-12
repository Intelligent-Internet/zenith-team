import { useCallback, useEffect, useId, useRef, useState } from "react";
import { attachmentService } from "@/shared/api/attachment.service";
import type { AttachmentDto } from "@/shared/api/types";
import { Download, LoaderCircle, MoreHorizontal } from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { toast } from "@/shared/ui/sonner";
import { ArtifactFileContent } from "./artifact-file-preview";
import { ArtifactFileTabs } from "./artifact-file-tabs";
import { FileViewerShell } from "./file-viewer-shell";

export type AttachmentPreviewFile = Pick<
  AttachmentDto,
  "id" | "filename" | "contentType" | "sizeBytes"
>;

export function AttachmentFileView({
  companyId,
  file,
  onClose,
}: {
  companyId: string;
  file: AttachmentPreviewFile;
  onClose: () => void;
}) {
  const tabsId = useId();
  const [downloading, setDownloading] = useState(false);
  const downloadRef = useRef<AbortController | null>(null);
  const loadFile = useCallback(
    (signal: AbortSignal) =>
      attachmentService.download(companyId, file.id, signal),
    [companyId, file.id],
  );
  useEffect(() => () => downloadRef.current?.abort(), []);

  const download = async () => {
    if (downloadRef.current) return;
    const controller = new AbortController();
    downloadRef.current = controller;
    setDownloading(true);
    try {
      const blob = await loadFile(controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = file.filename;
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
    } catch {
      if (!controller.signal.aborted)
        toast.error(
          "This file could not be downloaded. Check your connection and access.",
        );
    } finally {
      downloadRef.current = null;
      if (!controller.signal.aborted) setDownloading(false);
    }
  };

  return (
    <FileViewerShell title={file.filename} onBack={onClose} onClose={onClose}>
      <div className="artifact-file-view">
        <div className="artifact-file-toolbar">
          <ArtifactFileTabs
            id={tabsId}
            files={[file]}
            selectedId={file.id}
            onSelect={() => undefined}
          />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                aria-label={`Actions for ${file.filename}`}
                aria-busy={downloading}
                className="artifact-action-button"
                title="File actions"
                type="button"
              >
                {downloading ? (
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
              aria-label={`Actions for ${file.filename}`}
            >
              <DropdownMenuItem
                disabled={downloading}
                onSelect={() => void download()}
              >
                <Download aria-hidden size={16} />{" "}
                {downloading ? "Downloading…" : "Download"}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
        <div
          aria-labelledby={`${tabsId}-${file.id}`}
          className="artifact-file-preview-pane"
          id={`${tabsId}-panel`}
          role="tabpanel"
          tabIndex={0}
        >
          <ArtifactFileContent
            file={{ ...file, title: file.filename }}
            loadFile={loadFile}
          />
        </div>
      </div>
    </FileViewerShell>
  );
}
