import { useRef, useState, type FormEvent } from "react";
import {
  FileText,
  ArrowUp,
  Layers,
  Link2,
  LoaderCircle,
  ArrowUpRight,
} from "@/shared/ui/icons";
import { useListWikiPagesQuery } from "@/features/company-wiki/wiki-api";
import { attachmentService } from "@/shared/api/attachment.service";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import {
  useGetCanvasQuery,
  type CanvasCommand,
  type CanvasScope,
} from "../api/canvas-api";
import { CanvasDialog } from "./canvas-dialog";
import { documentTemplates } from "../documents/document-templates";
export type ExecuteCanvasCommand = (
  command: Omit<CanvasCommand, "requestKey"> & { requestKey?: string },
) => Promise<{ id: string; sequence: number }>;
export function AddArtifact({
  scope,
  canEditDocuments,
  position,
  execute,
  onClose,
  initial = "document",
  artifactId,
  baseVersionId,
  revision,
  taskId,
}: {
  scope: CanvasScope;
  canEditDocuments: boolean;
  position: { x: number; y: number };
  execute: ExecuteCanvasCommand;
  onClose: () => void;
  initial?: "document" | "upload" | "wiki" | "output";
  artifactId?: string;
  baseVersionId?: string;
  revision?: number;
  taskId?: string;
}) {
  const { currentData: board } = useGetCanvasQuery(scope);
  const canManageFiles = board?.canManageFiles ?? false;
  const [selectedTab, setTab] = useState(initial);
  const tab =
    selectedTab === "upload" && !canManageFiles
      ? canEditDocuments
        ? "document"
        : "wiki"
      : selectedTab === "document" && !canEditDocuments
        ? canManageFiles
          ? "upload"
          : "wiki"
        : selectedTab;
  const canSubmit = tab === "upload" ? canManageFiles : !!board?.canEdit;
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [content, setContent] = useState<CanvasCommand["content"]>();
  const [file, setFile] = useState<File>();
  const [source, setSource] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [progress, setProgress] = useState<number>();
  const [search, setSearch] = useState("");
  const key = useRef(crypto.randomUUID());
  const upload = useRef<{ file: File; id: string } | undefined>(undefined);
  const pages = useListWikiPagesQuery(
    { companyId: scope.companyId, search },
    { skip: tab !== "wiki" },
  );
  const change = () => {
    key.current = crypto.randomUUID();
    setError("");
  };
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    if (!canSubmit) {
      setError("You no longer have permission to add this content.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const command: CanvasCommand = {
        type: artifactId ? "add_version" : "add_artifact",
        requestKey: key.current,
        title: title.trim(),
        ...position,
        artifactId,
        taskId: artifactId ? undefined : taskId,
        versionId: baseVersionId,
        expectedRevision: revision,
      };
      if (tab === "document") {
        command.type = "create_document";
        command.body = body;
        command.content = content;
      } else if (tab === "upload") {
        if (!file) throw new Error("Choose a file to add.");
        if (upload.current?.file !== file)
          upload.current = {
            file,
            id: (
              await attachmentService.uploadFile(
                scope.companyId,
                file,
                setProgress,
                key.current,
              )
            ).id,
          };
        command.attachmentId = upload.current.id;
        command.title ||= file.name;
      } else if (tab === "wiki") {
        if (!source) throw new Error("Choose a document.");
        command.wikiPageId = source;
        command.title ||=
          pages.data?.items.find((page) => page.id === source)?.title ??
          "Document";
      } else {
        const id = source
          .match(/[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}/gi)
          ?.at(-1);
        if (!id)
          throw new Error("Paste a saved artifact link from a conversation.");
        const output = await runDeliverableService.getArtifact(
          scope.companyId,
          id,
        );
        command.outputId = id;
        command.title ||= output.result.output?.title || "Saved output";
      }
      await execute(command);
      onClose();
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "This artifact could not be added. Check the source and try again.",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <CanvasDialog
      title={
        artifactId
          ? "Add a new version"
          : taskId
            ? "Add to task canvas"
            : "Bring your work together"
      }
      description={
        artifactId
          ? "Keep the earlier version and its feedback intact."
          : taskId
            ? "Create a document or add a file to this task."
            : "Create a shared document, or add something ready for review."
      }
      onClose={onClose}
      canClose={!busy}
    >
      <div
        className="pc-add-tabs grid grid-cols-[repeat(4,_1fr)] m-[22px_26px_0] gap-[6px] p-[4px] [border:1px_solid_var(--pc-line)] rounded-[8px] [background:var(--pc-paper)]"
        role="tablist"
        aria-label="Artifact source"
      >
        {(
          [
            { value: "document", label: "Create", Icon: FileText },
            { value: "upload", label: "Upload", Icon: ArrowUp },
            { value: "wiki", label: "From wiki", Icon: Layers },
            { value: "output", label: "Saved output", Icon: Link2 },
          ] as const
        )
          .filter((row) =>
            row.value === "upload"
              ? canManageFiles
              : board?.canEdit &&
                (row.value !== "document" || canEditDocuments),
          )
          .map(({ value, label, Icon }) => (
            <button
              key={value}
              role="tab"
              aria-selected={tab === value}
              disabled={busy}
              onClick={() => {
                setTab(value);
                setSource("");
                change();
              }}
            >
              <Icon size={18} />
              {label}
            </button>
          ))}
      </div>
      <form
        className="pc-add-form p-[22px_26px_24px] flex flex-col gap-[19px]"
        onSubmit={(event) => void submit(event)}
      >
        {tab === "upload" && canManageFiles && (
          <label className={`pc-upload-zone ${file ? "has-file" : ""}`}>
            <ArrowUp size={28} />
            <strong>{file?.name || "Choose a file"}</strong>
            <span>PDF, Markdown, Word, HTML, images and more</span>
            <input
              type="file"
              disabled={busy}
              onChange={(event) => {
                setFile(event.target.files?.[0]);
                change();
              }}
            />
          </label>
        )}
        {tab === "wiki" && (
          <div className="pc-source-picker">
            <label>
              Find a wiki document
              <input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="Search documents…"
              />
            </label>
            <div role="listbox" aria-label="Wiki documents">
              {pages.data?.items.map((page) => (
                <button
                  key={page.id}
                  type="button"
                  role="option"
                  aria-selected={source === page.id}
                  onClick={() => {
                    setSource(page.id);
                    change();
                  }}
                >
                  <FileText size={17} />
                  {page.title}
                  <ArrowUpRight size={14} />
                </button>
              ))}
              {pages.isLoading && <p>Loading documents…</p>}
              {pages.isError && (
                <p role="alert">Documents could not be loaded.</p>
              )}
              {pages.data?.items.length === 0 && <p>No documents found.</p>}
            </div>
          </div>
        )}
        {tab === "output" && (
          <label>
            Saved artifact link
            <input
              required
              type="text"
              value={source}
              placeholder="Paste a link to a saved conversation artifact"
              onChange={(event) => {
                setSource(event.target.value);
                change();
              }}
            />
            <small>
              Source permissions continue to apply to everyone reviewing it.
            </small>
          </label>
        )}
        <label>
          {artifactId ? "Version title (optional)" : "Title"}
          <input
            required={tab === "document"}
            autoFocus={tab === "document"}
            maxLength={240}
            value={title}
            placeholder={
              tab === "document" ? "e.g. Launch brief" : "Use the source title"
            }
            disabled={busy}
            onChange={(event) => {
              setTitle(event.target.value);
              change();
            }}
          />
        </label>
        {tab === "document" && (
          <>
            <label>
              Start from a template
              <select
                disabled={busy}
                defaultValue="0"
                onChange={(event) => {
                  const template =
                    documentTemplates[Number(event.target.value)];
                  setBody(template.key === "blank" ? "" : template.description);
                  setContent(
                    template.key === "blank" ? undefined : template.content,
                  );
                  if (!title) setTitle(template.title);
                  change();
                }}
              >
                {documentTemplates.map((template, index) => (
                  <option key={template.key} value={index}>
                    {template.title}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Import Markdown or native blocks
              <input
                type="file"
                accept=".md,.markdown,.txt,.json"
                disabled={busy}
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  if (!file) return;
                  change();
                  void file
                    .text()
                    .then((text) => {
                      if (text.length > 2_000_000)
                        throw new Error("This document is too large.");
                      if (file.name.endsWith(".json")) {
                        const value = JSON.parse(text);
                        const root =
                          value.content?.type === "doc" ? value.content : value;
                        if (root.type !== "doc")
                          throw new Error(
                            "Choose a native block document export.",
                          );
                        setContent(root);
                        setBody(
                          "Native document imported. All block identities will be remapped.",
                        );
                      } else {
                        setContent(undefined);
                        setBody(text);
                      }
                      setTitle(file.name.replace(/\.[^.]+$/, ""));
                    })
                    .catch((reason) =>
                      setError(
                        reason instanceof Error
                          ? reason.message
                          : "Import failed.",
                      ),
                    );
                }}
              />
            </label>
            <label>
              Start with a thought
              <textarea
                rows={6}
                maxLength={200000}
                value={body}
                placeholder="What are we making, and what should the team know?"
                disabled={busy || !!content}
                onChange={(event) => {
                  setBody(event.target.value);
                  change();
                }}
              />
              <small>
                Markdown supported. Teammates can edit the shared draft
                together.
              </small>
            </label>
          </>
        )}
        {error && (
          <p className="pc-inline-warning" role="alert">
            {error}
          </p>
        )}
        <footer>
          <button
            type="button"
            className="pc-button"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            className="pc-primary"
            type="submit"
            disabled={busy || !canSubmit}
          >
            {busy && <LoaderCircle size={16} className="pc-spin" />}
            {busy
              ? progress !== undefined
                ? `Uploading ${progress}%`
                : "Adding…"
              : artifactId
                ? "Add version"
                : tab === "document"
                  ? "Create document"
                  : "Add to canvas"}
          </button>
        </footer>
      </form>
    </CanvasDialog>
  );
}
