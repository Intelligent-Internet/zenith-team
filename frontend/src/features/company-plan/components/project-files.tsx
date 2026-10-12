import {
  useEffect,
  useId,
  useState,
  type DragEvent,
  type ReactNode,
} from "react";
import type { components } from "@/shared/api/generated/api.generated";
import { attachmentService } from "@/shared/api/attachment.service";
import { formatAttachmentSize } from "@/shared/chat/attachment-preview";
import {
  Image,
  Paperclip,
  X,
  FileText,
  RefreshCw,
  LoaderCircle,
} from "@/shared/ui/icons";
import type { Project } from "../api/project-api";

type ProjectFile = components["schemas"]["ProjectFile"];
type FilesInput = components["schemas"]["ProjectFilesInput"];
type UploadRow = {
  localId: string;
  kind: "logo" | "document";
  name: string;
  size: number;
  file?: File;
  attachment?: ProjectFile;
  status: "ready" | "uploading" | "error";
  progress: number;
};

export function ProjectLogo({
  companyId,
  file,
  title,
}: {
  companyId: string;
  file?: ProjectFile | null;
  title: string;
}) {
  const [url, setUrl] = useState("");
  useEffect(() => {
    setUrl("");
    if (!file) return;
    const controller = new AbortController();
    let objectUrl = "";
    void attachmentService
      .download(companyId, file.id, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return;
        objectUrl = URL.createObjectURL(blob);
        setUrl(objectUrl);
      })
      .catch(() => {});
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [companyId, file?.id]);
  return (
    <span className="project-logo">
      {url ? (
        <img src={url} alt="" onError={() => setUrl("")} />
      ) : (
        <span aria-hidden="true">
          {title
            .trim()
            .split(/\s+/)
            .slice(0, 2)
            .map((word) => word[0])
            .join("")
            .toUpperCase() || "P"}
        </span>
      )}
    </span>
  );
}

function ProjectLogoPicker({
  companyId,
  row,
  disabled,
  onFiles,
  onRemove,
  onRetry,
}: {
  companyId: string;
  row?: UploadRow;
  disabled: boolean;
  onFiles: (files: File[]) => void;
  onRemove: () => void;
  onRetry: () => void;
}) {
  const statusId = useId();
  const [previewUrl, setPreviewUrl] = useState("");
  const file = row?.file;
  const uploading = row?.status === "uploading";
  const failed = row?.status === "error";

  useEffect(() => {
    if (!file) {
      setPreviewUrl("");
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewUrl(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  return (
    <div className="project-logo-picker">
      <div className="project-logo-picker__control">
        <label
          className="project-logo-upload"
          data-uploading={uploading || undefined}
          data-error={failed || undefined}
          aria-busy={uploading || undefined}
          title="Choose a project image · PNG, JPG or WebP · Up to 5 MB"
        >
          {file && previewUrl ? (
            <img
              className="project-logo-upload__preview"
              src={previewUrl}
              alt=""
            />
          ) : row?.attachment ? (
            <ProjectLogo
              companyId={companyId}
              file={row.attachment}
              title="Project"
            />
          ) : (
            <Image size={24} aria-hidden="true" />
          )}
          {uploading ? (
            <span
              className="project-logo-upload__progress"
              role="progressbar"
              aria-label="Uploading project image"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={row.progress || undefined}
            >
              <LoaderCircle
                size={20}
                className="animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
              {row.progress > 0 ? (
                <span aria-hidden="true">{row.progress}%</span>
              ) : null}
            </span>
          ) : null}
          <input
            aria-label={
              row ? "Change project image" : "Upload project image (optional)"
            }
            aria-describedby={statusId}
            disabled={disabled || uploading}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            onChange={(event) => {
              onFiles(Array.from(event.target.files ?? []));
              event.target.value = "";
            }}
          />
        </label>
        {row ? (
          <button
            className="project-logo-picker__remove"
            type="button"
            aria-label="Remove project image"
            title="Remove image"
            disabled={disabled}
            onClick={onRemove}
          >
            <span>
              <X size={10} aria-hidden="true" />
            </span>
          </button>
        ) : null}
      </div>
      <span
        id={statusId}
        className={
          uploading || failed ? "project-logo-picker__status" : "sr-only"
        }
        data-error={failed || undefined}
        role={failed ? "alert" : "status"}
      >
        {uploading
          ? "Uploading…"
          : failed
            ? "Upload failed"
            : row
              ? "Project image selected."
              : ""}
      </span>
      {failed ? (
        <button
          className="project-logo-picker__retry"
          type="button"
          aria-label="Retry project image upload"
          disabled={disabled}
          onClick={onRetry}
        >
          Retry
        </button>
      ) : null}
    </div>
  );
}

export function ProjectFilesEditor({
  companyId,
  initial,
  disabled,
  onChange,
  onReadyChange,
  identity,
  children,
}: {
  companyId: string;
  identity: ReactNode;
  children: ReactNode;
  initial?: Project["files"];
  disabled: boolean;
  onChange: (files: FilesInput) => void;
  onReadyChange: (ready: boolean) => void;
}) {
  const [rows, setRows] = useState<UploadRow[]>(() => [
    ...(initial?.logo
      ? [
          {
            localId: initial.logo.id,
            kind: "logo" as const,
            name: initial.logo.filename,
            size: initial.logo.sizeBytes,
            attachment: initial.logo,
            status: "ready" as const,
            progress: 100,
          },
        ]
      : []),
    ...(initial?.documents ?? []).map((file) => ({
      localId: file.id,
      kind: "document" as const,
      name: file.filename,
      size: file.sizeBytes,
      attachment: file,
      status: "ready" as const,
      progress: 100,
    })),
  ]);
  const logo = rows.find((row) => row.kind === "logo");
  const [error, setError] = useState("");
  useEffect(() => {
    onReadyChange(rows.every((row) => row.status === "ready"));
    onChange({
      logoId: rows.find((row) => row.kind === "logo")?.attachment?.id ?? null,
      documentIds: rows
        .filter((row) => row.kind === "document" && row.attachment)
        .map((row) => row.attachment!.id),
    });
  }, [rows, onChange, onReadyChange]);
  async function upload(row: UploadRow) {
    if (!row.file) return;
    setRows((current) =>
      current.map((item) =>
        item.localId === row.localId
          ? { ...item, status: "uploading", progress: 0 }
          : item,
      ),
    );
    try {
      const attachment = await attachmentService.uploadFile(
        companyId,
        row.file,
        (progress) =>
          setRows((current) =>
            current.map((item) =>
              item.localId === row.localId
                ? { ...item, progress: progress ?? 0 }
                : item,
            ),
          ),
        row.localId,
      );
      setRows((current) =>
        current.map((item) =>
          item.localId === row.localId
            ? { ...item, attachment, status: "ready", progress: 100 }
            : item,
        ),
      );
    } catch {
      setRows((current) =>
        current.map((item) =>
          item.localId === row.localId ? { ...item, status: "error" } : item,
        ),
      );
    }
  }
  function add(files: File[], kind: UploadRow["kind"]) {
    if (disabled) return;
    setError("");
    if (
      kind === "document" &&
      rows.filter((row) => row.kind === "document").length + files.length > 10
    ) {
      setError("Choose up to 10 documents.");
      return;
    }
    if (kind === "logo" && files.length > 1) {
      setError("Choose one logo.");
      return;
    }
    const created: UploadRow[] = [];
    for (const file of files) {
      const extension = file.name.split(".").pop()?.toLowerCase() ?? "";
      const types: Record<string, string> = {
        pdf: "application/pdf",
        docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        md: "text/markdown",
        markdown: "text/markdown",
        txt: "text/plain",
      };
      const valid =
        kind === "logo"
          ? ["image/png", "image/jpeg", "image/webp"].includes(file.type)
          : !!types[extension];
      const limit = kind === "logo" ? 5 : 20;
      if (!valid || file.size === 0 || file.size > limit * 1024 * 1024) {
        setError(
          kind === "logo"
            ? "Choose a PNG, JPG or WebP image up to 5 MB."
            : "Choose a PDF, DOCX, Markdown or TXT document up to 20 MB.",
        );
        return;
      }
      const normalized =
        kind === "document"
          ? new File([file], file.name, { type: types[extension] })
          : file;
      created.push({
        localId: crypto.randomUUID(),
        kind,
        name: file.name,
        size: file.size,
        file: normalized,
        status: "uploading",
        progress: 0,
      });
    }
    setRows((current) => [
      ...(kind === "logo"
        ? current.filter((row) => row.kind !== "logo")
        : current),
      ...created,
    ]);
    for (const row of created) void upload(row);
  }
  function drop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    add(Array.from(event.dataTransfer.files), "document");
  }
  function rowView(row: UploadRow) {
    return (
      <div className="project-file-row" key={row.localId}>
        <FileText size={18} />
        <span className="project-file-row__name">
          {row.name}
          <small>
            {formatAttachmentSize(row.size)} ·{" "}
            {row.status === "uploading"
              ? `Uploading ${row.progress}%`
              : row.status === "error"
                ? "Upload failed"
                : "Ready"}
          </small>
        </span>
        {row.status === "error" ? (
          <button
            type="button"
            disabled={disabled}
            onClick={() => void upload(row)}
            aria-label={`Retry ${row.name}`}
          >
            <RefreshCw size={16} />
          </button>
        ) : null}
        <button
          type="button"
          disabled={disabled}
          onClick={() =>
            setRows((current) =>
              current.filter((item) => item.localId !== row.localId),
            )
          }
          aria-label={`Remove ${row.name}`}
        >
          <X size={16} />
        </button>
      </div>
    );
  }
  return (
    <div className="project-files-editor">
      <div className="project-editor__identity">
        <ProjectLogoPicker
          companyId={companyId}
          row={logo}
          disabled={disabled}
          onFiles={(files) => add(files, "logo")}
          onRemove={() =>
            setRows((current) => current.filter((row) => row.kind !== "logo"))
          }
          onRetry={() => {
            if (logo) void upload(logo);
          }}
        />
        {identity}
      </div>
      {children}
      <div className="project-files-editor__documents">
        <span className="project-editor__field-heading">
          Documents <small>Optional</small>
        </span>
        <div onDragOver={(event) => event.preventDefault()} onDrop={drop}>
          <label
            className="project-document-drop"
            title="Up to 10 files, 20 MB each. Scanned PDFs need OCR."
          >
            <Paperclip size={22} />
            <span>
              Add documents<small>PDF, DOCX, MD, TXT</small>
            </span>
            <input
              aria-label="Add project documents (optional)"
              disabled={disabled}
              type="file"
              multiple
              accept=".pdf,.docx,.md,.markdown,.txt"
              onChange={(event) => {
                add(Array.from(event.target.files ?? []), "document");
                event.target.value = "";
              }}
            />
          </label>
        </div>
      </div>
      {rows.filter((row) => row.kind === "document").map(rowView)}
      {error ? <p role="alert">{error}</p> : null}
    </div>
  );
}
