import {
  ChevronLeft,
  ChevronRight,
  Download,
  RotateCcw,
  X,
  ZoomIn,
  ZoomOut,
} from "@/shared/ui/icons";
import { useRef, useState, type RefObject } from "react";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import {
  attachmentUrl,
  downloadChatAttachment,
  type ChatAttachment,
} from "./attachment-preview";
import "./chat-attachments.css";

export function ChatImageViewer({
  files,
  index,
  onIndexChange,
  onClose,
  returnFocusRef,
}: {
  files: readonly ChatAttachment[];
  index: number;
  onIndexChange: (index: number) => void;
  onClose: () => void;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
}) {
  const [zoom, setZoom] = useState(1);
  const [failed, setFailed] = useState(false);
  const viewportRef = useRef<HTMLDivElement>(null);
  const file = files[index];
  const navigate = (next: number) => {
    if (next < 0 || next >= files.length) return;
    setZoom(1);
    setFailed(false);
    viewportRef.current?.scrollTo(0, 0);
    onIndexChange(next);
  };
  if (!file) return null;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        className="chat-image-viewer"
        overlayClassName="chat-attachment-overlay"
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          if (returnFocusRef?.current) {
            event.preventDefault();
            returnFocusRef.current.focus({ preventScroll: true });
          }
        }}
        onKeyDown={(event) => {
          if (event.altKey || event.ctrlKey || event.metaKey) return;
          if (event.key === "ArrowLeft") {
            event.preventDefault();
            navigate(index - 1);
          }
          if (event.key === "ArrowRight") {
            event.preventDefault();
            navigate(index + 1);
          }
          if (event.key === "+" || event.key === "=") {
            event.preventDefault();
            setZoom((value) => Math.min(4, value + 0.5));
          }
          if (event.key === "-") {
            event.preventDefault();
            setZoom((value) => Math.max(1, value - 0.5));
          }
          if (event.key === "0") setZoom(1);
        }}
      >
        <header className="chat-image-viewer__header">
          <div>
            <DialogTitle>{file.name}</DialogTitle>
            <DialogDescription>
              {index + 1} / {files.length}
            </DialogDescription>
          </div>
          <div className="chat-image-viewer__controls">
            <button
              aria-label="Zoom out"
              disabled={zoom === 1}
              onClick={() => setZoom((value) => Math.max(1, value - 0.5))}
              type="button"
            >
              <ZoomOut size={18} />
            </button>
            <span aria-live="polite">{Math.round(zoom * 100)}%</span>
            <button
              aria-label="Zoom in"
              disabled={zoom === 4}
              onClick={() => setZoom((value) => Math.min(4, value + 0.5))}
              type="button"
            >
              <ZoomIn size={18} />
            </button>
            <button
              aria-label="Fit image"
              onClick={() => setZoom(1)}
              type="button"
            >
              <RotateCcw size={17} />
            </button>
            <button
              aria-label={`Download ${file.name}`}
              onClick={() =>
                void downloadChatAttachment(file).catch((error: Error) =>
                  toast.error(error.message),
                )
              }
              type="button"
            >
              <Download size={18} />
            </button>
            <DialogClose asChild>
              <button aria-label="Close image preview" type="button">
                <X size={20} />
              </button>
            </DialogClose>
          </div>
        </header>
        <div className="chat-image-viewer__viewport" ref={viewportRef}>
          {failed || !attachmentUrl(file.url) ? (
            <p role="status">
              This image could not be loaded. You can try downloading it.
            </p>
          ) : (
            <div
              className="chat-image-viewer__canvas"
              style={{ width: `${zoom * 100}%`, height: `${zoom * 100}%` }}
            >
              <img
                alt={file.name}
                key={file.id}
                onError={() => setFailed(true)}
                src={attachmentUrl(file.url)}
              />
            </div>
          )}
        </div>
        {files.length > 1 ? (
          <footer className="chat-image-viewer__navigation">
            <button
              aria-label="Previous image"
              disabled={index === 0}
              onClick={() => navigate(index - 1)}
              type="button"
            >
              <ChevronLeft size={18} /> Previous
            </button>
            <span>Use ← → to browse · Esc to close</span>
            <button
              aria-label="Next image"
              disabled={index === files.length - 1}
              onClick={() => navigate(index + 1)}
              type="button"
            >
              Next <ChevronRight size={18} />
            </button>
          </footer>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
