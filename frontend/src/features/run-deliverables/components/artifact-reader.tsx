import type { ReactNode } from "react";
import { FileText } from "@/shared/ui/icons";
import "../styles/artifact-sharing.css";

export function ArtifactReader({
  title,
  summary,
  files,
  selectedId,
  onSelect,
  artifactActions,
  fileActions,
  notice,
  inert,
  children,
}: {
  title: string;
  summary?: string;
  files: ReadonlyArray<{ id: string; label: string }>;
  selectedId?: string;
  onSelect: (id: string) => void;
  artifactActions?: ReactNode;
  fileActions?: ReactNode;
  notice?: ReactNode;
  inert?: boolean;
  children: ReactNode;
}) {
  const selected = files.find((file) => file.id === selectedId);
  return (
    <div className="artifact-share-container" inert={inert}>
      <aside className="artifact-share-sidebar">
        <header className="artifact-share-heading">
          <div>
            <h1 className="text-2xl font-semibold">{title}</h1>
            {summary ? (
              <details className="artifact-summary-details mt-[12px] text-[length:13px] text-[var(--text-secondary)]">
                <summary>About this artifact</summary>
                <p className="artifact-share-summary">{summary}</p>
              </details>
            ) : null}
          </div>
        </header>
        {artifactActions}
        <div className="artifact-sidebar-files-heading flex items-center justify-between mb-[12px] text-[length:12px] font-[600]">
          <h2>Files</h2>
          <span>{files.length}</span>
        </div>
        <nav className="artifact-file-list" aria-label="Artifact files">
          {files.map((file) => (
            <button
              key={file.id}
              type="button"
              aria-pressed={selectedId === file.id}
              onClick={() => onSelect(file.id)}
            >
              <FileText size={16} aria-hidden />
              <span>{file.label}</span>
            </button>
          ))}
        </nav>
      </aside>
      <div className="artifact-share-main">
        {notice}
        <div className="artifact-share-layout">
          <div className="min-w-0 grid gap-[24px]">
            {files.length ? (
              <section
                className="artifact-files-panel"
                aria-label="Artifact files"
              >
                {selected ? (
                  <header className="artifact-preview-heading">
                    <FileText size={18} aria-hidden />
                    <h2>{selected.label}</h2>
                    {fileActions}
                  </header>
                ) : null}
                {selected ? (
                  <div className="artifact-share-preview">{children}</div>
                ) : null}
              </section>
            ) : (
              <p role="status">This artifact has no files to display.</p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
