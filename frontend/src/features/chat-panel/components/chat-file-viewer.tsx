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
    <FileViewerShell
      title={file.title}
      onClose={onClose}
      actions={
        <button
          className="artifacts-icon-button"
          type="button"
          aria-label={
            busy ? `Downloading ${file.filename}` : `Download ${file.filename}`
          }
          title={busy ? "Downloading…" : "Download"}
          aria-busy={busy}
          disabled={busy}
          onClick={() => void save()}
        >
          {busy ? (
            <LoaderCircle aria-hidden size={18} className="artifact-spinner" />
          ) : (
            <Download aria-hidden size={18} />
          )}
        </button>
      }
    >
      <div className="artifact-file-view">
        <div className="artifact-file-preview-pane" tabIndex={0}>
          <ArtifactFileContent file={file} loadFile={loadFile} />
        </div>
      </div>
    </FileViewerShell>
  );
}
