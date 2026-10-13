import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import { LoaderCircle, RotateCw } from "@/shared/ui/icons";
import { Streamdown } from "streamdown";
import { ArtifactMediaPlayer } from "./artifact-media-player";
import { ImagePreview } from "@/shared/ui/image-preview";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import ChatPresentationPreview from "@/shared/chat/chat-presentation-preview";

import {
  artifactAppUrl,
  createArtifactFilePreview,
  maximumArtifactPreviewBytes,
  type ArtifactFilePreview as ArtifactFilePreviewValue,
  type ArtifactFileSelection,
  type ArtifactPreviewFile,
} from "@/features/chat-panel/model/artifact-file-access";

const ChatPdfPreview = lazy(() => import("@/shared/chat/chat-pdf-preview"));
const ChatSpreadsheetPreview = lazy(
  () => import("@/shared/chat/chat-spreadsheet-preview"),
);

type PreviewState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; preview: ArtifactFilePreviewValue };

export type ArtifactDocumentPosition = {
  top: number;
  left: number;
  outlineOpen: boolean;
};

export function ArtifactFilePreview({
  companyId,
  file,
  documentPosition,
  onDocumentScroll,
}: {
  companyId: string;
  file: ArtifactFileSelection;
  documentPosition?: ArtifactDocumentPosition;
  onDocumentScroll?: (position: ArtifactDocumentPosition) => void;
}) {
  const { runId, artifactId, ordinal } = file;
  const loadFile = useCallback(
    (signal: AbortSignal) =>
      runDeliverableService.downloadItem(
        companyId,
        runId,
        artifactId,
        ordinal,
        signal,
      ),
    [companyId, runId, artifactId, ordinal],
  );
  return (
    <ArtifactFileContent
      key={`${companyId}:${runId}:${artifactId}:${ordinal}`}
      file={file}
      loadFile={loadFile}
      documentPosition={documentPosition}
      onDocumentScroll={onDocumentScroll}
    />
  );
}

export function ArtifactFileContent({
  file,
  loadFile,
  documentPosition,
  onDocumentScroll,
}: {
  file: ArtifactPreviewFile;
  loadFile: (
    signal: AbortSignal,
  ) => Promise<Blob | { blob: Blob; truncated?: boolean }>;
  documentPosition?: ArtifactDocumentPosition;
  onDocumentScroll?: (position: ArtifactDocumentPosition) => void;
}) {
  const [state, setState] = useState<PreviewState>({ status: "loading" });
  const [retryKey, setRetryKey] = useState(0);
  const { id, title, filename, contentType, sizeBytes } = file;

  useEffect(() => {
    const controller = new AbortController();
    if (sizeBytes > maximumArtifactPreviewBytes) {
      setState({
        status: "ready",
        preview: {
          format: "unsupported",
          reason:
            "This file is larger than the 100 MB preview limit. Download it to view the complete file.",
        },
      });
      return;
    }
    const requestedFile: ArtifactPreviewFile = {
      id,
      title,
      filename,
      contentType,
      sizeBytes,
    };
    setState({ status: "loading" });
    void loadFile(controller.signal)
      .then(async (loaded) => {
        if (controller.signal.aborted) return null;
        const blob = loaded instanceof Blob ? loaded : loaded.blob;
        const preview = await createArtifactFilePreview(blob, requestedFile);
        if (
          !(loaded instanceof Blob) &&
          loaded.truncated &&
          ("truncated" in preview || preview.format === "spreadsheet")
        )
          preview.truncated = true;
        return preview;
      })
      .then((preview) => {
        if (!controller.signal.aborted && preview) {
          setState({ status: "ready", preview });
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setState({
            status: "error",
            message:
              error instanceof Error
                ? error.message
                : "This file could not be loaded. Try again.",
          });
        }
      });
    return () => controller.abort();
  }, [loadFile, contentType, filename, id, retryKey, sizeBytes, title]);

  if (state.status === "loading") {
    return (
      <ArtifactPreviewMessage loading>Loading preview…</ArtifactPreviewMessage>
    );
  }
  if (state.status === "error") {
    return (
      <ArtifactPreviewMessage>
        <p>{state.message}</p>
        <button
          className="artifact-action-button"
          onClick={() => setRetryKey((key) => key + 1)}
          type="button"
        >
          <RotateCw aria-hidden size={15} /> Retry
        </button>
      </ArtifactPreviewMessage>
    );
  }
  const { preview } = state;
  if (preview.format === "unsupported") {
    return <ArtifactPreviewMessage>{preview.reason}</ArtifactPreviewMessage>;
  }
  if (
    preview.format === "image" ||
    preview.format === "video" ||
    preview.format === "audio" ||
    preview.format === "pdf" ||
    preview.format === "presentation" ||
    preview.format === "spreadsheet"
  ) {
    return (
      <ArtifactBlobPreview
        title={title}
        filename={filename}
        preview={preview}
      />
    );
  }
  return (
    <ArtifactDocument
      preview={preview}
      initialPosition={documentPosition}
      onScroll={onDocumentScroll}
    />
  );
}

function ArtifactDocument({
  preview,
  initialPosition,
  onScroll,
}: {
  preview: Extract<ArtifactFilePreviewValue, { content: string }>;
  initialPosition?: ArtifactDocumentPosition;
  onScroll?: (position: ArtifactDocumentPosition) => void;
}) {
  const documentRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!initialPosition) return;
    // Restore after the document outline has joined the layout.
    const frame = requestAnimationFrame(() => {
      documentRef.current?.scrollTo(initialPosition);
    });
    return () => cancelAnimationFrame(frame);
  }, [initialPosition]);
  const rememberPosition = () => {
    const document = documentRef.current;
    if (!document) return;
    onScroll?.({
      top: document.scrollTop,
      left: document.scrollLeft,
      outlineOpen:
        document.querySelector<HTMLDetailsElement>(".artifact-outline")?.open ??
        false,
    });
  };
  return (
    <div
      ref={documentRef}
      className="artifact-document"
      onScroll={rememberPosition}
    >
      {preview.truncated ? (
        <p className="m-[0_0_16px] [border:1px_solid_var(--workspace-divider,_#dedbd2)] rounded-[8px] p-[9px_11px] text-[var(--workspace-native-text-muted,_#697168)] text-[length:12px]">
          Showing the first 1 MB. Download to read the complete artifact.
        </p>
      ) : null}
      {preview.format === "markdown" ? (
        <ArtifactMarkdown
          content={preview.content}
          outlineOpen={initialPosition?.outlineOpen}
          onOutlineToggle={rememberPosition}
        />
      ) : (
        <pre className="artifact-source">{preview.content}</pre>
      )}
    </div>
  );
}

function ArtifactMarkdown({
  content,
  outlineOpen,
  onOutlineToggle,
}: {
  content: string;
  outlineOpen?: boolean;
  onOutlineToggle: () => void;
}) {
  const documentRef = useRef<HTMLDivElement>(null);
  const [headings, setHeadings] = useState<HTMLElement[]>([]);
  useLayoutEffect(() => {
    const root = documentRef.current;
    if (!root) return;
    setHeadings(Array.from(root.querySelectorAll<HTMLElement>("h1, h2, h3")));
  }, [content]);
  return (
    <>
      {headings.length > 2 ? (
        <details
          className="artifact-outline"
          open={outlineOpen}
          onToggle={onOutlineToggle}
        >
          <summary>On this page</summary>
          <nav aria-label="Document contents">
            {headings.map((heading, index) => (
              <button
                key={index}
                type="button"
                onClick={() => {
                  heading.scrollIntoView({ block: "start" });
                  heading.tabIndex = -1;
                  heading.focus({ preventScroll: true });
                }}
              >
                {heading.textContent}
              </button>
            ))}
          </nav>
        </details>
      ) : null}
      <div ref={documentRef}>
        <Streamdown
          className="artifact-markdown"
          components={{
            a: SafeArtifactLink,
            code: ({ children }) => <code>{children}</code>,
            pre: ({ children }) => <pre>{children}</pre>,
            img: ({ alt }) => (
              <span className="m-[0_0_16px] [border:1px_solid_var(--workspace-divider,_#dedbd2)] rounded-[8px] p-[9px_11px] text-[var(--workspace-native-text-muted,_#697168)] text-[length:12px]">
                {alt ? `Image: ${alt}` : "Embedded image"}
              </span>
            ),
          }}
          controls={false}
          mode="static"
          rehypePlugins={[]}
          remarkRehypeOptions={{ allowDangerousHtml: false }}
        >
          {content}
        </Streamdown>
      </div>
    </>
  );
}

function ArtifactBlobPreview({
  title,
  filename,
  preview,
}: {
  title: string;
  filename: string;
  preview: Extract<ArtifactFilePreviewValue, { blob: Blob }>;
}) {
  const [objectUrl, setObjectUrl] = useState<string>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const url = URL.createObjectURL(preview.blob);
    setObjectUrl(url);
    setFailed(false);
    return () => URL.revokeObjectURL(url);
  }, [preview.blob]);
  if (!objectUrl) {
    return (
      <ArtifactPreviewMessage loading>
        Preparing preview…
      </ArtifactPreviewMessage>
    );
  }
  if (
    preview.format === "pdf" ||
    preview.format === "presentation" ||
    preview.format === "spreadsheet"
  ) {
    return (
      <Suspense
        fallback={
          <ArtifactPreviewMessage loading>
            Loading preview…
          </ArtifactPreviewMessage>
        }
      >
        {preview.format === "pdf" ? (
          <ChatPdfPreview url={objectUrl} />
        ) : preview.format === "presentation" ? (
          <ChatPresentationPreview
            url={objectUrl}
            sizeBytes={preview.blob.size}
          />
        ) : (
          <>
            {preview.truncated ? (
              <p className="m-[0_0_16px] [border:1px_solid_var(--workspace-divider,_#dedbd2)] rounded-[8px] p-[9px_11px] text-[var(--workspace-native-text-muted,_#697168)] text-[length:12px]">
                Showing the first 1 MB. Download to view the complete file.
              </p>
            ) : null}
            <ChatSpreadsheetPreview
              url={objectUrl}
              sizeBytes={preview.blob.size}
              format={preview.spreadsheetFormat}
            />
          </>
        )}
      </Suspense>
    );
  }
  if (failed) {
    return (
      <ArtifactPreviewMessage>
        This media could not be displayed. Download it to open it elsewhere.
      </ArtifactPreviewMessage>
    );
  }
  if (preview.format === "video" || preview.format === "audio") {
    return (
      <ArtifactMediaPlayer
        key={objectUrl}
        source={objectUrl}
        format={preview.format}
        title={title || filename}
        filename={filename}
        onError={() => setFailed(true)}
      />
    );
  }
  return <ImagePreview src={objectUrl} alt={filename} />;
}

function SafeArtifactLink({ children, href }: ComponentProps<"a">) {
  const safeUrl = href ? artifactAppUrl(href) : null;
  return safeUrl ? (
    <a href={safeUrl} rel="noopener noreferrer" target="_blank">
      {children}
    </a>
  ) : (
    <span>{children}</span>
  );
}

export function ArtifactPreviewMessage({
  children,
  loading = false,
}: {
  children: ReactNode;
  loading?: boolean;
}) {
  return (
    <div className="artifact-preview-message" role="status">
      {loading ? <LoaderCircle className="artifact-spinner" size={22} /> : null}
      <div>{children}</div>
    </div>
  );
}
