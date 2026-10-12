import { createPortal } from "react-dom";
import { PageDocumentLoading } from "./page-document-loading";
import { skipToken } from "@reduxjs/toolkit/query";
import { useOutputDocumentVersionQuery } from "./output-review-api";
import { previewContentType } from "./preview-content-type";
import { MediaReviewViewer } from "./media-review";
import { mediaAnchor, type MediaReview } from "./annotation-overlay";
import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { MarkdownView } from "./markdown-view";
import { BlockDocument } from "./block-editor-schema";
import { DocumentTitle } from "./document-title";
import { apiClient } from "@/lib/api-client";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import { attachmentService } from "@/shared/api/attachment.service";
import { OpenArtifactCanvasButton } from "@/features/run-deliverables/components/open-artifact-canvas-button";
import {
  Download,
  FileText,
  LoaderCircle,
  FileCode2,
  Monitor,
  Square,
  Pencil,
} from "@/shared/ui/icons";
import type { components } from "@/shared/api/generated/api.generated";
import type { PreviewRequest, PreviewResponse } from "./preview-worker.types";
import {
  useCanvasBodyQuery,
  type CanvasScope,
  type CanvasVersion,
} from "./canvas-api";
export type ArtifactScope =
  CanvasScope | { companyId: string; outputId: string };
const PDFReader = lazy(() => import("@/shared/chat/chat-pdf-preview"));

export function ArtifactViewer({
  scope,
  version,
  review,
  focusId,
  onEditDocument,
  editButtonContainer,
  autoEdit = false,
  initialOutputItemId,
  title,
}: {
  scope: ArtifactScope;
  version: CanvasVersion;
  review?: MediaReview;
  focusId?: string;
  onEditDocument?: (outputItemId: string, filename: string) => Promise<void>;
  editButtonContainer?: HTMLElement | null;
  autoEdit?: boolean;
  initialOutputItemId?: string;
  title?: string;
}) {
  if (version.sourceType === "wiki")
    return (
      <DocumentSnapshot
        scope={scope}
        version={version}
        focusId={focusId}
        title={title}
      />
    );
  if (version.sourceType === "output" && version.outputId)
    return (
      <OutputViewer
        scope={scope}
        version={version}
        review={review}
        onEditDocument={onEditDocument}
        editButtonContainer={editButtonContainer}
        autoEdit={autoEdit}
        initialOutputItemId={initialOutputItemId}
      />
    );
  return (
    <FileViewer
      key={version.id}
      scope={scope}
      version={version}
      review={review}
    />
  );
}
function DocumentSnapshot({
  scope,
  version,
  focusId,
  title,
}: {
  scope: ArtifactScope;
  version: CanvasVersion;
  focusId?: string;
  title?: string;
}) {
  const projectVersion = useCanvasBodyQuery(
    "projectId" in scope ? { ...scope, versionId: version.id } : skipToken,
  );
  const outputVersion = useOutputDocumentVersionQuery(
    "outputId" in scope ? { ...scope, versionId: version.id } : skipToken,
  );
  const result = "outputId" in scope ? outputVersion : projectVersion;
  if (result.isLoading) return <PageDocumentLoading toolbar={false} />;
  if (result.isError)
    return (
      <ViewerMessage>
        This version is no longer available.{" "}
        <button onClick={() => void result.refetch()}>Retry</button>
      </ViewerMessage>
    );
  return (
    <div
      className="pc-reader-scroll"
      tabIndex={0}
      aria-label="Document content"
    >
      {title && (
        <header className="pc-document-heading">
          <DocumentTitle title={title} />
        </header>
      )}
      {result.data?.content?.type === "doc" &&
      ("projectId" in scope || version.outputItemId) ? (
        <BlockDocument
          showOutline
          scope={
            "projectId" in scope
              ? scope
              : {
                  companyId: scope.companyId,
                  outputId: scope.outputId,
                  outputItemId: version.outputItemId!,
                }
          }
          content={result.data.content}
          focusId={focusId}
        />
      ) : (
        <MarkdownView>
          {result.data?.body || "This document is empty."}
        </MarkdownView>
      )}
    </div>
  );
}
function OutputViewer({
  scope,
  version,
  review,
  onEditDocument,
  editButtonContainer,
  autoEdit,
  initialOutputItemId,
}: {
  scope: { companyId: string };
  version: CanvasVersion;
  review?: MediaReview;
  onEditDocument?: (outputItemId: string, filename: string) => Promise<void>;
  editButtonContainer?: HTMLElement | null;
  autoEdit?: boolean;
  initialOutputItemId?: string;
}) {
  const [output, setOutput] =
    useState<components["schemas"]["ArtifactDetail"]>();
  const [error, setError] = useState(false);
  const [locationUnavailable, setLocationUnavailable] = useState(false);
  const [ordinal, setOrdinal] = useState<number>();
  const [opening, setOpening] = useState(false);
  const [importError, setImportError] = useState("");
  const attempted = useRef(new Set<string>());
  useEffect(() => {
    const controller = new AbortController();
    setError(false);
    setOutput(undefined);
    setOrdinal(undefined);
    void apiClient
      .get<components["schemas"]["ArtifactDetail"]>(
        `/companies/${scope.companyId}/artifacts/${version.outputId}`,
        { signal: controller.signal },
      )
      .then((value) => {
        if (!controller.signal.aborted) {
          setOutput(value);
          const files = value.result.output?.items ?? [];
          setOrdinal(
            (
              files.find((row) => row.id === initialOutputItemId) ??
              (value.result.output?.type === "app"
                ? files.find((row) => /\.html?$/i.test(row.displayName))
                : undefined) ??
              files.find((row) => row.role === "primary") ??
              files[0]
            )?.ordinal,
          );
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [scope.companyId, version.outputId, initialOutputItemId]);
  useEffect(() => {
    const selected = mediaAnchor(review?.navigation?.anchor)?.target
      .outputItemId;
    if (!selected || !output) return;
    const file = output.result.output?.items.find((row) => row.id === selected);
    setLocationUnavailable(!file);
    if (file) setOrdinal(file.ordinal);
  }, [review?.navigation?.id, output]);
  const item = output?.result.output?.items.find(
    (row) => row.ordinal === ordinal,
  );
  const editable =
    item &&
    output?.result.output?.type !== "app" &&
    !output?.result.output?.presentationUrl &&
    (/\.(md|markdown)$/i.test(item.displayName) ||
      /^text\/(x-)?markdown$/i.test(item.contentType));
  const tooLarge = !!item && item.sizeBytes > 262144;
  const openDocument = async () => {
    if (!item || !onEditDocument || opening) return;
    attempted.current.add(item.id);
    setOpening(true);
    setImportError("");
    try {
      await onEditDocument(item.id, item.displayName);
    } catch {
      setImportError(
        "This file could not be opened for editing. The original is still available below.",
      );
    } finally {
      setOpening(false);
    }
  };
  useEffect(() => {
    if (
      autoEdit &&
      editable &&
      !tooLarge &&
      onEditDocument &&
      item &&
      !attempted.current.has(item.id)
    )
      void openDocument();
  }, [autoEdit, item, editable, tooLarge, onEditDocument]);
  if (error)
    return (
      <ViewerMessage>
        The source is unavailable or access has changed.
      </ViewerMessage>
    );
  if (!output) return <PageDocumentLoading toolbar={false} />;
  const editButton = (
    <button
      className="pc-document-edit-button"
      title="Edit the working draft. The submitted output stays unchanged."
      disabled={opening || tooLarge}
      onClick={() => void openDocument()}
    >
      {opening ? (
        <LoaderCircle size={15} className="pc-spin" />
      ) : (
        <Pencil size={15} />
      )}
      {opening ? "Opening…" : importError ? "Retry editing" : "Edit"}
    </button>
  );
  return (
    <div className="pc-output-viewer">
      {output.result.output?.presentationUrl && (
        <div className="pc-output-document-action">
          <span>Saved Project presentation · exact content and evidence</span>
          <OpenArtifactCanvasButton
            companyId={scope.companyId}
            output={output.result.output}
          />
        </div>
      )}
      {locationUnavailable && (
        <p role="status" className="pc-annotation-notice">
          This annotation’s file is unavailable.
        </p>
      )}
      {(output.result.output?.items.length ?? 0) > 1 && (
        <div className="pc-file-tabs">
          {output.result.output?.items.map((row) => (
            <button
              key={row.id}
              disabled={
                Boolean(review && mediaAnchor(review.anchor)) || review?.busy
              }
              aria-pressed={row.ordinal === ordinal}
              onClick={() => setOrdinal(row.ordinal)}
            >
              {row.displayName}
            </button>
          ))}
        </div>
      )}
      {editable && onEditDocument && (
        <div
          className="pc-output-document-action pc-output-draft-action"
          role="status"
        >
          {(opening || tooLarge || importError) && (
            <span>
              {opening
                ? "Opening working draft…"
                : tooLarge
                  ? "This file exceeds the 256 KB editing limit. The original is available for review and download."
                  : importError}
            </span>
          )}
          {editButtonContainer
            ? createPortal(editButton, editButtonContainer)
            : editButton}
        </div>
      )}
      {item &&
      (opening ||
        (autoEdit &&
          editable &&
          !tooLarge &&
          onEditDocument &&
          !attempted.current.has(item.id))) ? (
        <PageDocumentLoading toolbar={false} />
      ) : item ? (
        <FileViewer
          key={item.id}
          scope={scope}
          version={{
            ...version,
            assetVersionId: item.assetVersionId,
            filename: item.displayName,
            contentType: item.contentType,
            sizeBytes: item.sizeBytes,
          }}
          outputOrdinal={item.ordinal}
          outputItemId={item.id}
          review={review}
        />
      ) : (
        <MarkdownView>
          {output.result.output?.summary || "This output has no files."}
        </MarkdownView>
      )}
    </div>
  );
}
export function FileViewer({
  scope,
  version,
  outputOrdinal,
  outputItemId,
  review,
  renderShell,
  toolbarButtonClassName = "pc-icon-button",
}: {
  scope: { companyId: string };
  version: Pick<
    CanvasVersion,
    "id" | "filename" | "contentType" | "sizeBytes"
  > &
    Partial<
      Pick<CanvasVersion, "attachmentId" | "outputId" | "assetVersionId">
    >;
  outputOrdinal?: number;
  outputItemId?: string;
  review?: MediaReview;
  renderShell?: (viewer: ReactNode, actions: ReactNode) => ReactNode;
  toolbarButtonClassName?: string;
}) {
  const [blob, setBlob] = useState<Blob>();
  const [url, setUrl] = useState("");
  const [content, setContent] = useState<string>();
  const [error, setError] = useState("");
  const [source, setSource] = useState(false);
  const [mobile, setMobile] = useState(false);
  const type = previewContentType(version.contentType, version.filename),
    filename = version.filename ?? "artifact";
  const html = /html/.test(type) || /\.html?$/i.test(filename),
    word = /wordprocessingml/.test(type) || /\.docx$/i.test(filename);
  const binary = /^(image|video|audio)\//.test(type) || /pdf/.test(type);
  const text =
    /^(text\/|application\/(json|javascript|xml))/.test(type) ||
    /\.(md|txt|csv|json|css|js|tsx?|jsx|ya?ml)$/i.test(filename);
  useEffect(() => {
    const controller = new AbortController();
    let worker: Worker | undefined,
      objectUrl = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    setError("");
    setContent(undefined);
    setBlob(undefined);
    setUrl("");
    const load = async () => {
      if ((version.sizeBytes ?? 0) > 100 * 1024 * 1024) {
        setError("This file is larger than the 100 MB preview limit.");
        return;
      }
      const file =
        outputOrdinal !== undefined
          ? await runDeliverableService.downloadArtifactFile(
              scope.companyId,
              version.outputId!,
              outputOrdinal,
              controller.signal,
            )
          : version.attachmentId
            ? await attachmentService.download(
                scope.companyId,
                version.attachmentId,
                controller.signal,
              )
            : null;
      if (controller.signal.aborted) return;
      if (!file) throw new Error("Source file unavailable");
      setBlob(file);
      objectUrl = URL.createObjectURL(
        file.slice(0, file.size, type || file.type),
      );
      setUrl(objectUrl);
      if (binary) return;
      if (!html && !word && !text) {
        setError(
          "Preview is not available for this format. Download the original to open it.",
        );
        return;
      }
      const limit = word ? 20 * 1024 * 1024 : 1024 * 1024;
      if (file.size > limit) {
        setError(
          `This file is too large for reading view (${word ? "20" : "1"} MB limit). Download the original to read it.`,
        );
        return;
      }
      const buffer = await file.arrayBuffer();
      if (controller.signal.aborted) return;
      worker = new Worker(new URL("./preview.worker.ts", import.meta.url), {
        type: "module",
      });
      worker.onmessage = ({ data }: MessageEvent<PreviewResponse>) => {
        if (controller.signal.aborted || data.requestId !== 1) return;
        clearTimeout(timer);
        if (data.error) setError(data.error);
        else setContent(data.content);
        worker?.terminate();
      };
      worker.onerror = () => {
        setError(
          "Preview could not be prepared. Download the original to open it.",
        );
        clearTimeout(timer);
        worker?.terminate();
      };
      timer = setTimeout(() => {
        setError("Preview took too long. Download the original to open it.");
        worker?.terminate();
      }, 10000);
      worker.postMessage(
        {
          requestId: 1,
          format: html ? "html" : word ? "docx" : "text",
          buffer,
        } satisfies PreviewRequest,
        [buffer],
      );
    };
    void load().catch(() => {
      if (!controller.signal.aborted)
        setError("This file could not be opened. Access may have changed.");
    });
    return () => {
      controller.abort();
      worker?.terminate();
      clearTimeout(timer);
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [
    scope.companyId,
    version.id,
    version.attachmentId,
    version.outputId,
    outputOrdinal,
    binary,
    html,
    word,
    text,
    version.sizeBytes,
    type,
  ]);
  const download = async () => {
    try {
      const file =
        blob ??
        (outputOrdinal !== undefined
          ? await runDeliverableService.downloadArtifactFile(
              scope.companyId,
              version.outputId!,
              outputOrdinal,
            )
          : version.attachmentId
            ? await attachmentService.download(
                scope.companyId,
                version.attachmentId,
              )
            : null);
      if (!file) return;
      const href = URL.createObjectURL(file);
      const link = document.createElement("a");
      link.href = href;
      link.download = filename;
      link.click();
      setTimeout(() => URL.revokeObjectURL(href), 1000);
    } catch {
      setError("Download failed. Please try again.");
    }
  };
  const csp =
    "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src 'none'; connect-src 'none'; form-action 'none'; base-uri 'none'";
  const actions = (
    <>
      {html && (
        <>
          <button
            className={toolbarButtonClassName}
            aria-label={source ? "View design" : "View source"}
            onClick={() => setSource(!source)}
          >
            <FileCode2 size={17} />
          </button>
          <button
            className={toolbarButtonClassName}
            aria-label={mobile ? "Desktop viewport" : "Mobile viewport"}
            onClick={() => setMobile(!mobile)}
          >
            {mobile ? <Monitor size={17} /> : <Square size={17} />}
          </button>
        </>
      )}
      <button
        className={toolbarButtonClassName}
        aria-label="Download original"
        title={
          word
            ? "Word reading view · Download the original to keep its formatting"
            : "Download original"
        }
        onClick={() => void download()}
      >
        <Download size={17} />
      </button>
    </>
  );
  const viewer = (
    <div
      className={`pc-file-viewer ${type.startsWith("image/") ? "is-image" : ""}`}
    >
      {!renderShell && (
        <div className="pc-file-toolbar">
          {!type.startsWith("image/") && (
            <span>
              <FileText size={15} />
              {filename}
            </span>
          )}
          {actions}
        </div>
      )}
      <div className="pc-file-content">
        {error ? (
          <ViewerMessage>{error}</ViewerMessage>
        ) : !blob || (!binary && content === undefined) ? (
          <ViewerMessage loading>Preparing preview…</ViewerMessage>
        ) : html ? (
          <>
            {source ? (
              <pre className="pc-source-code">{content}</pre>
            ) : (
              <iframe
                className={`pc-html-preview ${mobile ? "is-mobile" : ""}`}
                title={filename}
                sandbox=""
                referrerPolicy="no-referrer"
                srcDoc={`<!doctype html><meta http-equiv="Content-Security-Policy" content="${csp}"><meta name="referrer" content="no-referrer">${content}`}
              />
            )}
            <p className="pc-viewer-caption">
              Static design review · Scripts and external resources are disabled
            </p>
          </>
        ) : review &&
          version.assetVersionId &&
          (/pdf/.test(type) ||
            type.startsWith("image/") ||
            type.startsWith("video/")) ? (
          <MediaReviewViewer
            url={url}
            filename={filename}
            surface={
              /pdf/.test(type)
                ? "pdf"
                : type.startsWith("image/")
                  ? "image"
                  : "video"
            }
            target={{
              assetVersionId: version.assetVersionId,
              ...(outputItemId ? { outputItemId } : {}),
            }}
            review={review}
          />
        ) : /pdf/.test(type) ? (
          <Suspense
            fallback={<ViewerMessage loading>Opening PDF…</ViewerMessage>}
          >
            <PDFReader url={url} />
          </Suspense>
        ) : type.startsWith("image/") ? (
          <div className="pc-image-viewer">
            <img src={url} alt={filename} />
          </div>
        ) : type.startsWith("video/") ? (
          <video controls src={url} className="pc-media-viewer" />
        ) : type.startsWith("audio/") ? (
          <audio controls src={url} className="pc-media-viewer" />
        ) : word || /markdown/.test(type) || /\.md$/i.test(filename) ? (
          <>
            {word && !renderShell && (
              <p className="pc-viewer-caption">
                Word reading view · Original formatting is preserved in the
                download
              </p>
            )}
            <MarkdownView>{content ?? ""}</MarkdownView>
          </>
        ) : (
          <pre className="pc-source-code">{content}</pre>
        )}
      </div>
    </div>
  );
  return renderShell ? renderShell(viewer, actions) : viewer;
}
export function ViewerMessage({
  children,
  loading,
}: {
  children: React.ReactNode;
  loading?: boolean;
}) {
  return (
    <div className="pc-viewer-loading" role="status">
      {loading && <LoaderCircle className="pc-spin" size={24} />}
      {children}
    </div>
  );
}
