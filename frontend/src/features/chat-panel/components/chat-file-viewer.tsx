import { useCallback, useEffect, useRef, useState } from "react";
import { Download, LoaderCircle } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import { ArtifactFileContent } from "./artifacts/artifact-file-preview";
import { FileViewerShell } from "./artifacts/file-viewer-shell";
import {
  chatFileDescription,
  downloadChatFile,
  loadChatFile,
  type ChatFileSource,
} from "../model/chat-file-source";

export function ChatFileViewer({
  source,
  onClose,
}: {
  source: ChatFileSource;
  onClose: () => void;
}) {
  const file = chatFileDescription(source);
  const loadFile = useCallback(
    (signal: AbortSignal) => loadChatFile(source, signal),
    [source],
  );
  const download = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => () => download.current?.abort(), []);
  const save = async () => {
    if (download.current) return;
    const controller = new AbortController();
    download.current = controller;
    setBusy(true);
    try {
      await downloadChatFile(source, controller.signal);
    } catch (error) {
      if (!controller.signal.aborted)
        toast.error(
          error instanceof Error
            ? error.message
            : "Unable to download this file.",
        );
    } finally {
      download.current = null;
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  return (
    <FileViewerShell title={file.title} onClose={onClose}>
      <div className="artifact-file-view">
        <div className="artifact-file-toolbar">
          <span
            className="min-w-0 flex-1 truncate px-3 text-sm"
            title={file.filename}
          >
            {file.filename}
          </span>
          <button
            className="artifact-action-button"
            type="button"
            aria-label={`Download ${file.filename}`}
            disabled={busy}
            onClick={() => void save()}
          >
            {busy ? (
              <LoaderCircle
                aria-hidden
                size={17}
                className="artifact-spinner"
              />
            ) : (
              <Download aria-hidden size={17} />
            )}
            Download
          </button>
        </div>
        <div className="artifact-file-preview-pane" tabIndex={0}>
          <ArtifactFileContent file={file} loadFile={loadFile} />
        </div>
      </div>
    </FileViewerShell>
  );
}
