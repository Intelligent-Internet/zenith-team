import { useEffect, useRef, useState } from "react";
import type { PptxViewer } from "@aiden0z/pptx-renderer";
import { ChevronLeft, ChevronRight, LoaderCircle } from "@/shared/ui/icons";
import { attachmentUrl } from "./attachment-preview";
import "./chat-presentation-preview.css";

const maximumPresentationBytes = 100 * 1024 * 1024;

export default function ChatPresentationPreview({
  url,
  sizeBytes,
}: {
  url?: string;
  sizeBytes?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewerRef = useRef<PptxViewer | null>(null);
  const [slideCount, setSlideCount] = useState(0);
  const [slideIndex, setSlideIndex] = useState(0);
  const [rendering, setRendering] = useState(true);
  const [error, setError] = useState("");
  const [incomplete, setIncomplete] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const controller = new AbortController();
    setSlideCount(0);
    setSlideIndex(0);
    setRendering(true);
    setError("");
    setIncomplete(false);
    let viewer: PptxViewer | undefined;
    const timeout = window.setTimeout(() => {
      controller.abort();
      viewer?.destroy();
      setRendering(false);
      setError(
        "This presentation is taking too long to open. Try again or download the file.",
      );
    }, 30_000);

    const load = async () => {
      const source = attachmentUrl(url);
      if (!source) throw new Error("This file is no longer available.");
      const tooLarge =
        "This presentation exceeds the 100 MB preview limit. Download it to open in PowerPoint.";
      if ((sizeBytes ?? 0) > maximumPresentationBytes)
        throw new Error(tooLarge);
      // Catch module-load failures here so they cannot escape to the chat route.
      const { PptxViewer, RECOMMENDED_ZIP_LIMITS } =
        await import("@aiden0z/pptx-renderer").catch(() => {
          throw new Error(
            "The PowerPoint viewer could not be loaded. Try again or download the file.",
          );
        });
      if (controller.signal.aborted) return;
      const response = await fetch(source, {
        credentials: "omit",
        referrerPolicy: "no-referrer",
        signal: controller.signal,
      });
      if (!response.ok)
        throw new Error("The presentation could not be loaded. Try again.");
      if (
        Number(response.headers.get("content-length")) >
        maximumPresentationBytes
      )
        throw new Error(tooLarge);
      const data = await response.arrayBuffer();
      if (controller.signal.aborted) return;
      if (data.byteLength > maximumPresentationBytes) throw new Error(tooLarge);
      viewer = new PptxViewer(container, {
        fitMode: "contain",
        zipLimits: RECOMMENDED_ZIP_LIMITS,
        lazySlides: true,
        lazyMedia: true,
        pdfjs: false,
        onSlideChange: (index) => {
          if (!controller.signal.aborted) setSlideIndex(index);
        },
        onRenderStart: () => {
          if (!controller.signal.aborted) setRendering(true);
        },
        onRenderComplete: () => {
          if (!controller.signal.aborted) setRendering(false);
        },
        onSlideError: () => {
          if (!controller.signal.aborted)
            setError(
              "This slide could not be displayed. Try another slide or download the file.",
            );
        },
        onNodeError: () => {
          if (!controller.signal.aborted) setIncomplete(true);
        },
      });
      viewerRef.current = viewer;
      try {
        await viewer.open(data, {
          renderMode: "slide",
          signal: controller.signal,
        });
      } catch {
        throw new Error(
          "This PowerPoint file could not be opened. Try again or download it.",
        );
      }
      if (controller.signal.aborted) return;
      if (!viewer.slideCount)
        throw new Error("This presentation has no slides.");
      setSlideCount(viewer.slideCount);
      setRendering(false);
    };
    void load()
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        viewer?.destroy();
        setRendering(false);
        setError(
          reason instanceof Error
            ? reason.message
            : "The presentation could not be opened. Try again or download it.",
        );
      })
      .finally(() => window.clearTimeout(timeout));
    return () => {
      controller.abort();
      window.clearTimeout(timeout);
      viewerRef.current = null;
      viewer?.destroy();
    };
  }, [url, sizeBytes, attempt]);

  const showSlide = (index: number) => {
    const viewer = viewerRef.current;
    if (!viewer || rendering || index < 0 || index >= slideCount) return;
    setError("");
    setIncomplete(false);
    void viewer.renderSlide(index).catch(() => {
      if (viewerRef.current !== viewer) return;
      setRendering(false);
      setError(
        "This slide could not be displayed. Try again or download the file.",
      );
    });
  };
  const loading = !error && (!slideCount || rendering);

  return (
    <div className="chat-presentation-preview flex min-w-0 min-h-[280px] flex-1 flex-col text-workspace-text">
      <div className="chat-presentation-preview__toolbar">
        <button
          aria-label="Previous slide"
          disabled={!slideCount || rendering || slideIndex === 0}
          onClick={() => showSlide(slideIndex - 1)}
          type="button"
        >
          <ChevronLeft aria-hidden size={18} />
        </button>
        <span aria-live="polite">
          {slideCount
            ? `Slide ${slideIndex + 1} of ${slideCount}`
            : "PowerPoint preview"}
        </span>
        <button
          aria-label="Next slide"
          disabled={!slideCount || rendering || slideIndex >= slideCount - 1}
          onClick={() => showSlide(slideIndex + 1)}
          type="button"
        >
          <ChevronRight aria-hidden size={18} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto overscroll-contain p-[16px] [background:var(--workspace-control-bg)]">
        {error ? (
          <div
            className="chat-presentation-preview__notice flex items-center justify-center gap-[8px] flex-wrap m-[16px_0] text-workspace-muted text-[length:13px] text-center"
            role="alert"
          >
            <p>{error}</p>
            <button
              type="button"
              onClick={() => setAttempt((value) => value + 1)}
            >
              Try again
            </button>
          </div>
        ) : loading ? (
          <div
            className="chat-presentation-preview__notice flex items-center justify-center gap-[8px] flex-wrap m-[16px_0] text-workspace-muted text-[length:13px] text-center"
            role="status"
          >
            <LoaderCircle aria-hidden className="animate-spin" size={18} />
            Loading presentation…
          </div>
        ) : null}
        <div
          aria-label={`Slide ${slideIndex + 1}`}
          aria-busy={loading}
          aria-hidden={Boolean(error) || loading}
          className="chat-presentation-preview__slide"
          ref={containerRef}
          role="region"
          style={{ visibility: error || loading ? "hidden" : undefined }}
          tabIndex={0}
          onKeyDown={(event) => {
            if (event.target !== event.currentTarget) return;
            const index = {
              ArrowLeft: slideIndex - 1,
              ArrowRight: slideIndex + 1,
              Home: 0,
              End: slideCount - 1,
            }[event.key];
            if (index === undefined) return;
            event.preventDefault();
            event.stopPropagation();
            showSlide(index);
          }}
        />
        {incomplete && !error ? (
          <p
            className="chat-presentation-preview__notice flex items-center justify-center gap-[8px] flex-wrap m-[16px_0] text-workspace-muted text-[length:13px] text-center"
            role="status"
          >
            Some elements could not be displayed. Download for the complete
            slide.
          </p>
        ) : null}
      </div>
    </div>
  );
}
