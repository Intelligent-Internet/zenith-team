import { ChatSourceCode, ChatSourcePreview } from "./chat-source-preview";
import {
  Download,
  File,
  FileCode,
  FileText,
  Image as ImageIcon,
  LoaderCircle,
  Music,
  Video,
  X,
} from "@/shared/ui/icons";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { Streamdown } from "streamdown";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import {
  attachmentKind,
  attachmentUrl,
  downloadChatAttachment,
  formatAttachmentSize,
  type ChatAttachment,
} from "./attachment-preview";
import { ChatImageViewer } from "./chat-image-viewer";
import ChatPresentationPreview from "./chat-presentation-preview";
import "./chat-attachments.css";

const ChatSpreadsheetPreview = lazy(() => import("./chat-spreadsheet-preview"));

const ChatPdfPreview = lazy(() => import("./chat-pdf-preview"));

export function ChatAttachmentIcon({
  file,
  size = 23,
}: {
  file: ChatAttachment;
  size?: number;
}) {
  const kind = attachmentKind(file);
  const Icon = {
    image: ImageIcon,
    video: Video,
    audio: Music,
    pdf: FileText,
    presentation: FileText,
    spreadsheet: FileText,
    markdown: FileCode,
    text: FileText,
    file: File,
  }[kind];
  return <Icon aria-hidden="true" size={size} />;
}

export function ChatAttachmentList({
  files,
}: {
  files: readonly ChatAttachment[];
}) {
  const images = files.filter((file) => attachmentKind(file) === "image");
  const others = files.filter((file) => attachmentKind(file) !== "image");
  const [imageIndex, setImageIndex] = useState<number | null>(null);
  const imageTriggerRef = useRef<HTMLButtonElement>(null);
  return (
    <div className="chat-attachments">
      {images.length > 0 ? (
        <div
          className="chat-attachments__gallery"
          data-single={images.length === 1}
        >
          {images.map((file, index) => (
            <button
              aria-label={`View ${file.name}`}
              className="chat-attachments__image"
              key={file.id}
              type="button"
              onClick={(event) => {
                imageTriggerRef.current = event.currentTarget;
                setImageIndex(index);
              }}
            >
              <ChatAttachmentThumbnail file={file} />
              <span>{file.name}</span>
            </button>
          ))}
        </div>
      ) : null}
      {others.map((file) => (
        <ChatAttachmentItem file={file} key={file.id} />
      ))}
      {imageIndex !== null ? (
        <ChatImageViewer
          files={images}
          index={imageIndex}
          onIndexChange={setImageIndex}
          onClose={() => setImageIndex(null)}
          returnFocusRef={imageTriggerRef}
        />
      ) : null}
    </div>
  );
}

export function ChatAttachmentThumbnail({ file }: { file: ChatAttachment }) {
  const [failedUrl, setFailedUrl] = useState<string>();
  const url = attachmentUrl(file.url);
  if (!url || failedUrl === url)
    return (
      <span className="chat-attachments__unavailable">
        <ImageIcon size={24} /> Image unavailable
      </span>
    );
  return (
    <img
      alt={file.name}
      src={url}
      loading="lazy"
      decoding="async"
      onError={() => setFailedUrl(url)}
    />
  );
}

export function ChatAttachmentItem({ file }: { file: ChatAttachment }) {
  const [open, setOpen] = useState(false);
  const [mediaFailed, setMediaFailed] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const kind = attachmentKind(file);
  const url = attachmentUrl(file.url);
  const isMedia = kind === "video" || kind === "audio";
  const detail = [
    formatAttachmentSize(file.sizeBytes),
    file.detail ||
      (kind === "file"
        ? file.mediaType || "File"
        : kind === "pdf"
          ? "PDF"
          : kind.charAt(0).toUpperCase() + kind.slice(1)),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className="chat-attachments__item">
      {isMedia && url ? (
        <div className="chat-attachments__player">
          {kind === "video" ? (
            <video
              controls
              preload="metadata"
              playsInline
              src={url}
              onError={() => setMediaFailed(true)}
            />
          ) : (
            <audio
              controls
              preload="metadata"
              src={url}
              onError={() => setMediaFailed(true)}
            />
          )}
          {mediaFailed ? (
            <p role="status">
              Playback is unavailable for this file. You can download it below.
            </p>
          ) : null}
        </div>
      ) : null}
      <div className="chat-attachments__file">
        <button
          aria-label={`Preview ${file.name}`}
          className="chat-attachments__file-open"
          onClick={() => setOpen(true)}
          ref={triggerRef}
          type="button"
        >
          <span className="chat-attachments__file-icon">
            <ChatAttachmentIcon file={file} />
          </span>
          <span className="chat-attachments__file-copy">
            <strong>{file.name}</strong>
            <span>{detail}</span>
          </span>
        </button>
        <ChatAttachmentDownload file={file} />
      </div>
      {kind === "text" || kind === "markdown" ? (
        <ChatSourcePreview file={file} onOpen={() => setOpen(true)} />
      ) : null}
      {open ? (
        <ChatAttachmentViewer
          file={file}
          onClose={() => setOpen(false)}
          returnFocusRef={triggerRef}
        />
      ) : null}
    </div>
  );
}

export function ChatAttachmentDownload({ file }: { file: ChatAttachment }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      aria-label={`Download ${file.name}`}
      className="chat-attachments__download"
      disabled={busy}
      title="Download file"
      type="button"
      onClick={() => {
        setBusy(true);
        void downloadChatAttachment(file)
          .catch((error: Error) => toast.error(error.message))
          .finally(() => setBusy(false));
      }}
    >
      {busy ? (
        <LoaderCircle className="animate-spin" size={17} />
      ) : (
        <Download size={17} />
      )}
    </button>
  );
}

export function ChatAttachmentViewer({
  file,
  onClose,
  returnFocusRef,
}: {
  file: ChatAttachment;
  onClose: () => void;
  returnFocusRef?: React.RefObject<HTMLButtonElement | null>;
}) {
  const kind = attachmentKind(file);
  const [preview, setPreview] = useState<{
    text?: string;
    url?: string;
    truncated?: boolean;
    error?: string;
  } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!["pdf", "markdown", "text"].includes(kind)) return;
    const controller = new AbortController();
    let objectUrl: string | undefined;
    setPreview(null);
    const load = async () => {
      let blob: Blob;
      if (file.content !== undefined && !file.url)
        blob = new Blob([file.content], {
          type: file.mediaType || "text/plain",
        });
      else {
        const url = attachmentUrl(file.url);
        if (!url) throw new Error("This file is no longer available.");
        const response = await fetch(url, {
          signal: controller.signal,
          credentials: "omit",
        });
        if (!response.ok) throw new Error("The preview could not be loaded.");
        blob = await response.blob();
      }
      if (controller.signal.aborted) return;
      if (kind === "pdf") {
        objectUrl = URL.createObjectURL(
          new Blob([blob], { type: "application/pdf" }),
        );
        setPreview({ url: objectUrl });
      } else {
        const text = await blob.slice(0, 1_000_000).text();
        if (!controller.signal.aborted)
          setPreview({ text, truncated: blob.size > 1_000_000 });
      }
    };
    void load().catch((error: Error) => {
      if (!controller.signal.aborted) setPreview({ error: error.message });
    });
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [file.content, file.url, file.mediaType, kind, attempt]);
  if (kind === "image")
    return (
      <ChatImageViewer
        files={[file]}
        index={0}
        onIndexChange={() => undefined}
        onClose={onClose}
        returnFocusRef={returnFocusRef}
      />
    );
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        className="chat-file-viewer"
        overlayClassName="chat-attachment-overlay"
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          if (returnFocusRef?.current) {
            event.preventDefault();
            returnFocusRef.current.focus({ preventScroll: true });
          }
        }}
      >
        <header>
          <div>
            <DialogTitle>{file.name}</DialogTitle>
            <DialogDescription>
              {[
                formatAttachmentSize(file.sizeBytes),
                file.detail || file.mediaType,
              ]
                .filter(Boolean)
                .join(" · ") || "File preview"}
            </DialogDescription>
          </div>
          <div className="chat-file-viewer__actions">
            <ChatAttachmentDownload file={file} />
            <DialogClose asChild>
              <button
                aria-label="Close file preview"
                title="Close preview"
                type="button"
              >
                <X aria-hidden="true" size={18} />
              </button>
            </DialogClose>
          </div>
        </header>
        <div className="chat-file-viewer__body">
          {kind === "file" ? (
            <div className="chat-file-viewer__empty">
              <ChatAttachmentIcon file={file} size={36} />
              <p>Preview isn’t available for this format.</p>
              <p>Download the file to open it.</p>
            </div>
          ) : kind === "presentation" ? (
            <ChatPresentationPreview
              url={file.url}
              sizeBytes={file.sizeBytes}
            />
          ) : kind === "spreadsheet" ? (
            <Suspense
              fallback={
                <p className="chat-file-viewer__notice">Loading spreadsheet…</p>
              }
            >
              <ChatSpreadsheetPreview
                url={file.url}
                sizeBytes={file.sizeBytes}
                format={
                  /\.tsv$/i.test(file.name) ||
                  file.mediaType === "text/tab-separated-values"
                    ? "tsv"
                    : /\.csv$/i.test(file.name) || file.mediaType === "text/csv"
                      ? "csv"
                      : "xlsx"
                }
              />
            </Suspense>
          ) : kind === "video" ? (
            <video
              controls
              preload="metadata"
              playsInline
              src={attachmentUrl(file.url)}
            />
          ) : kind === "audio" ? (
            <audio controls preload="metadata" src={attachmentUrl(file.url)} />
          ) : preview?.error ? (
            <div className="chat-file-viewer__empty" role="alert">
              <p>{preview.error}</p>
              <button
                onClick={() => setAttempt((value) => value + 1)}
                type="button"
              >
                Try again
              </button>
            </div>
          ) : !preview ? (
            <div className="chat-file-viewer__empty" role="status">
              <LoaderCircle className="animate-spin" />
              Loading preview…
            </div>
          ) : kind === "pdf" ? (
            <Suspense
              fallback={
                <div className="chat-file-viewer__empty" role="status">
                  Loading PDF viewer…
                </div>
              }
            >
              <ChatPdfPreview url={preview.url!} />
            </Suspense>
          ) : (
            <>
              {preview.truncated ? (
                <p className="chat-file-viewer__notice">
                  Showing the first 1 MB. Download the file for the full
                  content.
                </p>
              ) : null}
              {kind === "markdown" ? (
                <Streamdown
                  mode="static"
                  className="chat-file-viewer__markdown"
                  controls={false}
                >
                  {preview.text}
                </Streamdown>
              ) : (
                <ChatSourceCode text={preview.text ?? ""} file={file} />
              )}
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
