import {
  ChevronLeft,
  ChevronRight,
  LoaderCircle,
  ZoomIn,
  ZoomOut,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  getDocument,
  GlobalWorkerOptions,
  type PDFDocumentProxy,
  type RenderTask,
} from "pdfjs-dist/legacy/build/pdf.mjs";
import workerUrl from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";

import "./chat-attachments.css";

// Both bundles include the polyfills needed by Safari and desktop webviews.
GlobalWorkerOptions.workerSrc = workerUrl;

export type PdfSurface = {
  pageIndex: number;
  width: number;
  height: number;
  toContent: (point: [number, number]) => [number, number];
  toView: (point: [number, number]) => [number, number];
};
export default function ChatPdfPreview({
  url,
  navigation,
  locked = false,
  overlay,
  onUnavailable,
}: {
  url: string;
  navigation?: { id: number; pageIndex: number };
  locked?: boolean;
  overlay?: (surface: PdfSurface) => ReactNode;
  onUnavailable?: (message: string) => void;
}) {
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null);
  const [pageNumber, setPageNumber] = useState(1);
  const [error, setError] = useState("");
  const [rendering, setRendering] = useState(true);
  const [pageText, setPageText] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [surface, setSurface] = useState<PdfSurface>();
  const [zoom, setZoom] = useState(1);
  const [rotation, setRotation] = useState(0);
  const [width, setWidth] = useState(600);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const observer = new ResizeObserver(() =>
      setWidth(Math.max(160, viewport.clientWidth - 32)),
    );
    observer.observe(viewport);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let active = true;
    setPdf(null);
    setError("");
    setPageNumber(1);
    setRendering(true);
    const task = getDocument({ url, useSystemFonts: true });
    void task.promise
      .then((document) => {
        if (active) setPdf(document);
      })
      .catch((reason: Error) => {
        if (active)
          setError(
            reason.name === "PasswordException"
              ? "This PDF needs a password. Download it to open it."
              : "This PDF could not be opened. Try again or download the file.",
          );
      });
    return () => {
      active = false;
      void task.destroy().catch(() => undefined);
    };
  }, [url, attempt]);

  useEffect(() => {
    if (!pdf || !navigation) return;
    if (navigation.pageIndex < 0 || navigation.pageIndex >= pdf.numPages) {
      onUnavailable?.("This PDF page is unavailable.");
      return;
    }
    onUnavailable?.("");
    setPageNumber(navigation.pageIndex + 1);
  }, [pdf, navigation?.id]);

  useEffect(() => {
    if (!pdf || !canvasRef.current) return;
    let active = true;
    let renderTask: RenderTask | undefined;
    const canvas = canvasRef.current;
    setError("");
    setRendering(true);
    setPageText("");
    setSurface(undefined);
    void pdf
      .getPage(pageNumber)
      .then(async (page) => {
        if (!active) return;
        const unscaled = page.getViewport({ scale: 1 });
        const viewport = page.getViewport({
          scale: Math.min(overlay ? 4 : 2, (width / unscaled.width) * zoom),
          rotation: page.rotate + rotation,
        });
        const outputScale = Math.min(
          window.devicePixelRatio || 1,
          2,
          Math.sqrt(16000000 / (viewport.width * viewport.height)),
        );
        canvas.width = Math.floor(viewport.width * outputScale);
        canvas.height = Math.floor(viewport.height * outputScale);
        canvas.style.width = `${Math.floor(viewport.width)}px`;
        canvas.style.height = `${Math.floor(viewport.height)}px`;
        renderTask = page.render({
          canvas,
          viewport,
          transform: [outputScale, 0, 0, outputScale, 0, 0],
        });
        await renderTask.promise;
        if (!active) return;
        setRendering(false);
        const [x0, y0, x1, y1] = page.view;
        setSurface({
          pageIndex: pageNumber - 1,
          width: Math.floor(viewport.width),
          height: Math.floor(viewport.height),
          toContent: ([x, y]) => {
            const p = viewport.convertToPdfPoint(
              x * viewport.width,
              y * viewport.height,
            );
            return [(p[0] - x0) / (x1 - x0), (y1 - p[1]) / (y1 - y0)];
          },
          toView: ([x, y]) => {
            const p = viewport.convertToViewportPoint(
              x0 + x * (x1 - x0),
              y1 - y * (y1 - y0),
            );
            return [p[0] / viewport.width, p[1] / viewport.height];
          },
        });
        // Text extraction is optional; a failure must not hide a rendered page.
        void page
          .getTextContent()
          .then((text) => {
            if (active)
              setPageText(
                text.items
                  .flatMap((item) => ("str" in item ? [item.str] : []))
                  .join(" "),
              );
          })
          .catch(() => undefined);
      })
      .catch(() => {
        if (active) {
          setError(
            "This page could not be rendered. Try again or download the PDF.",
          );
          setRendering(false);
        }
      });
    return () => {
      active = false;
      renderTask?.cancel();
    };
  }, [pdf, pageNumber, width, zoom, rotation]);

  return (
    <div className="chat-pdf-preview">
      <div className="chat-pdf-preview__page" ref={viewportRef}>
        {error ? (
          <div
            className="chat-file-viewer__empty flex min-h-[180px] flex-col items-center justify-center gap-[10px] p-[24px] text-workspace-muted text-[length:13px]"
            role="alert"
          >
            <p>{error}</p>
            <button
              onClick={() => setAttempt((value) => value + 1)}
              type="button"
            >
              Try again
            </button>
          </div>
        ) : !pdf || rendering ? (
          <span
            className="flex items-center justify-center gap-[8px] text-workspace-muted text-[length:12px]"
            role="status"
          >
            <LoaderCircle className="animate-spin" size={18} />
            Loading page…
          </span>
        ) : null}
        <div
          style={{
            position: "relative",
            width: surface?.width,
            height: surface?.height,
            flexShrink: 0,
          }}
        >
          <canvas
            aria-label={`PDF page ${pageNumber}`}
            hidden={Boolean(error) || !pdf}
            ref={canvasRef}
            role="img"
          />
          {!rendering &&
            surface?.pageIndex === pageNumber - 1 &&
            overlay?.(surface)}
        </div>
        <p className="sr-only">{pageText}</p>
      </div>
      <div className="chat-pdf-preview__toolbar">
        <button
          aria-label="Previous PDF page"
          disabled={locked || !pdf || pageNumber <= 1}
          onClick={() => setPageNumber((value) => value - 1)}
          type="button"
        >
          <ChevronLeft size={18} />
        </button>
        <span aria-live="polite">
          {pdf ? `Page ${pageNumber} of ${pdf.numPages}` : "PDF preview"}
        </span>
        <button
          aria-label="Next PDF page"
          disabled={locked || !pdf || pageNumber >= pdf.numPages}
          onClick={() => setPageNumber((value) => value + 1)}
          type="button"
        >
          <ChevronRight size={18} />
        </button>
        {overlay && (
          <>
            <button
              disabled={locked || zoom <= 0.5}
              onClick={() => setZoom((v) => Math.max(0.5, v - 0.25))}
              aria-label="Zoom out PDF"
            >
              <ZoomOut size={16} />
            </button>
            <span>{Math.round(zoom * 100)}%</span>
            <button
              disabled={locked || zoom >= 3}
              onClick={() => setZoom((v) => Math.min(3, v + 0.25))}
              aria-label="Zoom in PDF"
            >
              <ZoomIn size={16} />
            </button>
            <button
              disabled={locked}
              onClick={() => setRotation((v) => (v + 90) % 360)}
            >
              Rotate
            </button>
          </>
        )}
      </div>
    </div>
  );
}
