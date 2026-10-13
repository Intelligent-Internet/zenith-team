import { lazy, Suspense, useLayoutEffect, useRef, useState } from "react";
import { FileViewerShell } from "@/features/chat-panel/components/artifacts/file-viewer-shell";
import type { Project } from "@/features/company-plan/api/project-api";
import { attachmentService } from "@/shared/api/attachment.service";
import { formatAttachmentSize } from "@/shared/chat/attachment-preview";
import { Icon } from "@/shared/ui/icon";
import "../canvas/project-canvas.css";
import "../documents/document-workspace.css";
import "../documents/page-document.css";
import "@/features/chat-panel/styles/conversation-artifacts.css";
import "../documents/project-files-library.css";

const FileViewer = lazy(() =>
  import("./artifact-file-viewer").then((module) => ({
    default: module.FileViewer,
  })),
);

export default function ProjectUploadedFiles({
  companyId,
  documents,
  canManageFiles,
  onManageFiles,
}: {
  companyId: string;
  documents: NonNullable<Project["files"]>["documents"];
  canManageFiles: boolean;
  onManageFiles: () => void;
}) {
  const [search, setSearch] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const previewTrigger = useRef<HTMLButtonElement | null>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const selectedFile = documents.find((file) => file.id === selectedId);
  useLayoutEffect(() => {
    if (selectedFile || !previewTrigger.current) return;
    const target = previewTrigger.current.isConnected
      ? previewTrigger.current
      : searchInput.current;
    target?.focus({ preventScroll: true });
    previewTrigger.current = null;
  }, [selectedFile]);
  const closePreview = () => setSelectedId(null);
  const files = documents.filter((file) =>
    file.filename.toLocaleLowerCase().includes(search.toLocaleLowerCase()),
  );
  return (
    <>
      <section
        className="po-document po-library"
        id="uploaded-files"
        aria-label="Uploaded Files"
        hidden={!!selectedFile}
      >
        <header className="po-library-heading">
          <div>
            <h2>Uploaded Files</h2>
            <p>Documents uploaded to this Project.</p>
          </div>
          {canManageFiles && (
            <button className="po-primary" onClick={onManageFiles}>
              <Icon name="folder-open" className="size-4" />
              Manage files
            </button>
          )}
        </header>
        <label className="po-library-search">
          <Icon name="search" className="size-4" />
          <input
            ref={searchInput}
            aria-label="Search uploaded files"
            placeholder="Search files…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        {files.length ? (
          <div className="flex flex-col [border-top:1px_solid_var(--pc-line)]">
            {files.map((file) => (
              <ProjectDocumentRow
                key={file.id}
                companyId={companyId}
                file={file}
                onPreview={(trigger) => {
                  previewTrigger.current = trigger;
                  setSelectedId(file.id);
                }}
              />
            ))}
          </div>
        ) : (
          <div className="po-state">
            <h3>{search ? "No matches" : "No files yet"}</h3>
            <p>
              {search
                ? "Try another name."
                : canManageFiles
                  ? "Add documents using Manage files."
                  : "Documents uploaded to this Project will appear here."}
            </p>
          </div>
        )}
      </section>
      {selectedFile && (
        <Suspense
          fallback={
            <div className="po-project-file-preview">
              <div
                className="pc-viewer-loading flex flex-col items-center justify-center gap-[14px] min-h-[260px] h-full p-[40px] text-center text-[var(--pc-muted)] text-[length:13px] leading-[1.7]"
                role="status"
              >
                Opening file…
              </div>
            </div>
          }
        >
          <FileViewer
            key={selectedFile.id}
            scope={{ companyId }}
            version={{ ...selectedFile, attachmentId: selectedFile.id }}
            toolbarButtonClassName="artifacts-icon-button"
            renderShell={(viewer, actions) => (
              <FileViewerShell
                title={selectedFile.filename}
                backLabel="Back to Files"
                onBack={closePreview}
                onClose={closePreview}
                actions={actions}
              >
                <div className="po-project-file-preview">{viewer}</div>
              </FileViewerShell>
            )}
          />
        </Suspense>
      )}
    </>
  );
}

function ProjectDocumentRow({
  companyId,
  file,
  onPreview,
}: {
  companyId: string;
  file: NonNullable<Project["files"]>["documents"][number];
  onPreview: (trigger: HTMLButtonElement) => void;
}) {
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");
  const download = async () => {
    setDownloading(true);
    setError("");
    try {
      const blob = await attachmentService.download(companyId, file.id);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = file.filename;
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
    } catch {
      setError(`${file.filename} could not be downloaded. Try again.`);
    } finally {
      setDownloading(false);
    }
  };
  return (
    <>
      <div className="po-project-file-row flex items-center [border-bottom:1px_solid_var(--pc-line)]">
        <button
          type="button"
          className="po-library-row"
          aria-label={`Preview ${file.filename}`}
          onClick={(event) => onPreview(event.currentTarget)}
        >
          <Icon name="document-normal" className="size-5" />
          <span>
            <strong>{file.filename}</strong>
            <small>
              Project document · {formatAttachmentSize(file.sizeBytes)}
            </small>
          </span>
          <span>Preview</span>
        </button>
        <button
          type="button"
          className="po-project-file-download"
          disabled={downloading}
          aria-label={`Download ${file.filename}`}
          title={downloading ? "Downloading…" : "Download"}
          onClick={() => void download()}
        >
          <Icon name="download" className="size-4" />
        </button>
      </div>
      {error && (
        <p role="alert" className="po-error">
          {error}
        </p>
      )}
    </>
  );
}
