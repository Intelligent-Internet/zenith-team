import { SelectionActions } from "./selection-actions";
import { ImageReviewViewer } from "./image-review";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import {
  Pin,
  Pencil,
  Square,
  Circle,
  Cursor,
  Undo2,
  Maximize2,
  Hand,
} from "@/shared/ui/icons";
import {
  AnnotationOverlay,
  anchorLabel,
  mediaAnchor,
  sameTarget,
  type MediaAnchor,
  type MediaReview,
  type AnnotationTool,
  type AnchorShape,
} from "./annotation-overlay";
const PDFReader = lazy(() => import("@/shared/chat/chat-pdf-preview"));

export function MediaReviewViewer({
  url,
  filename,
  surface: surfaceType,
  target,
  review,
}: {
  url: string;
  filename: string;
  surface: MediaAnchor["surface"];
  target: MediaAnchor["target"];
  review: MediaReview;
}) {
  const [tool, setTool] = useState<AnnotationTool>("select");
  const [color, setColor] = useState<AnchorShape["color"]>("amber");
  const [message, setMessage] = useState("");
  const [natural, setNatural] = useState({ width: 0, height: 0 });
  const [width, setWidth] = useState(600);
  const [paused, setPaused] = useState(true);
  const [timeMs, setTimeMs] = useState<number>();
  const [navigating, setNavigating] = useState(false);
  const host = useRef<HTMLDivElement>(null);
  const fullscreen = useRef<HTMLDivElement>(null);
  const video = useRef<HTMLVideoElement>(null);
  const generation = useRef(0);
  const draft = mediaAnchor(review.anchor);
  const locked = Boolean(draft) || review.busy;
  const ready =
    surfaceType === "pdf" || (natural.width > 0 && natural.height > 0);
  const navigationAnchor = mediaAnchor(review.navigation?.anchor);
  const navigation =
    review.navigation &&
    navigationAnchor &&
    sameTarget(navigationAnchor.target, target)
      ? { ...review.navigation, anchor: navigationAnchor }
      : undefined;
  const surface: MediaAnchor = {
    type: "media",
    schemaVersion: 1,
    target,
    surface: surfaceType,
    shapes: [],
    ...(surfaceType === "video" ? { timeMs } : {}),
  };
  useEffect(() => {
    const player = video.current;
    return () => {
      if (player) {
        player.pause();
        player.removeAttribute("src");
        player.load();
      }
    };
  }, []);
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    const observer = new ResizeObserver(() =>
      setWidth(Math.max(160, element.clientWidth - 32)),
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    setTool("select");
  }, [Boolean(draft)]);
  useEffect(() => {
    if (
      surfaceType !== "video" ||
      !navigation ||
      !video.current ||
      !natural.width
    )
      return;
    const player = video.current,
      timestamp = navigation.anchor.timeMs;
    const current = ++generation.current;
    let completed = false;
    let frame = 0,
      timer: ReturnType<typeof setTimeout>;
    setTool("select");
    player.pause();
    setPaused(true);
    setTimeMs(undefined);
    if (
      timestamp === undefined ||
      !Number.isFinite(player.duration) ||
      timestamp / 1000 > player.duration
    ) {
      setMessage("This video location is unavailable.");
      return;
    }
    setMessage("");
    setNavigating(true);
    const show = () => {
      if (
        generation.current === current &&
        player.paused &&
        !player.seeking &&
        player.readyState >= 2 &&
        Math.abs(player.currentTime - timestamp / 1000) <= 0.1
      ) {
        completed = true;
        setTimeMs(timestamp);
        setNavigating(false);
      }
    };
    const settled = () => {
      if (Math.abs(player.currentTime - timestamp / 1000) > 0.1) return;
      if ("requestVideoFrameCallback" in player)
        frame = player.requestVideoFrameCallback(show);
      timer = setTimeout(show, 250);
    };
    player.addEventListener("seeked", settled);
    player.currentTime = timestamp / 1000;
    if (!player.seeking) settled();
    const timeout = setTimeout(() => {
      if (generation.current === current && !completed) {
        setNavigating(false);
        setMessage(
          "This video location could not be loaded. Select the comment to retry.",
        );
      }
    }, 10000);
    return () => {
      generation.current++;
      player.removeEventListener("seeked", settled);
      clearTimeout(timer);
      clearTimeout(timeout);
      if (frame) player.cancelVideoFrameCallback(frame);
    };
  }, [navigation?.id, natural.width, surfaceType]);
  const choose = (next: AnnotationTool) => {
    if (review.busy || !ready) return;
    if (surfaceType === "video") {
      const player = video.current;
      if (!player || player.seeking || navigating || player.readyState < 2)
        return;
      if (
        draft?.timeMs !== undefined &&
        Math.abs(player.currentTime * 1000 - draft.timeMs) > 100
      ) {
        setMessage("Discard these marks before annotating another time.");
        return;
      }
      player.pause();
      setPaused(true);
      setTimeMs(draft?.timeMs ?? Math.round(player.currentTime * 1000));
    }
    setTool(next);
    setMessage("");
  };
  const frameWidth = Math.min(natural.width || width, width);
  const frameHeight = natural.width
    ? (frameWidth * natural.height) / natural.width
    : 0;
  const showOverlay =
    ready &&
    (surfaceType !== "video" ||
      (paused && timeMs !== undefined && !navigating));
  return (
    <div
      className={`pc-media-review ${surfaceType === "image" ? "is-image" : ""}`}
      ref={fullscreen}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          setTool("select");
          if (!review.busy) review.onChange({ type: "version" });
        }
      }}
    >
      {(review.canEdit || surfaceType === "video") && (
        <div
          className="pc-markup-toolbar"
          role="group"
          aria-label="Annotation tools"
        >
          {review.canEdit && (
            <>
              {(
                [
                  ["select", surfaceType === "image" ? "Pan" : "Select"],
                  ["point", "Pin"],
                  ["rectangle", "Rectangle"],
                  ["ellipse", "Ellipse"],
                  ["freehand", "Freeform"],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={tool === value}
                  aria-label={label}
                  title={
                    label === "Pan" || label === "Select"
                      ? label
                      : `${label} comment`
                  }
                  disabled={!ready || review.busy || navigating}
                  onClick={() => choose(value)}
                >
                  {value === "select" && surfaceType === "image" ? (
                    <Hand size={15} />
                  ) : value === "select" ? (
                    <Cursor size={15} />
                  ) : value === "point" ? (
                    <Pin size={15} />
                  ) : value === "rectangle" ? (
                    <Square size={15} />
                  ) : value === "ellipse" ? (
                    <Circle size={15} />
                  ) : value === "freehand" ? (
                    <Pencil size={15} />
                  ) : null}
                  <span className="pc-markup-tool-label">{label}</span>
                </button>
              ))}
              <span className="pc-markup-divider" aria-hidden />
              <div
                className="flex items-center gap-[1px] flex-none"
                role="group"
                aria-label="Annotation color"
              >
                {(["amber", "rose", "blue"] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    className="pc-markup-swatch"
                    data-color={value}
                    aria-label={`${value} annotation`}
                    title={`${value} annotation`}
                    aria-pressed={color === value}
                    disabled={review.busy}
                    onClick={() => setColor(value)}
                  >
                    <span />
                  </button>
                ))}
              </div>
              <span className="pc-markup-divider" aria-hidden />
              <button
                disabled={!draft?.shapes.length || review.busy}
                aria-label="Undo last mark"
                title="Undo last mark"
                onClick={() => {
                  if (draft)
                    review.onChange(
                      draft.shapes.length > 1
                        ? { ...draft, shapes: draft.shapes.slice(0, -1) }
                        : { type: "version" },
                    );
                }}
              >
                <Undo2 size={16} />
              </button>
              {surfaceType === "video" && (
                <button
                  disabled={!ready || locked || navigating}
                  onClick={() => {
                    const player = video.current;
                    if (!player || player.seeking || player.readyState < 2)
                      return;
                    player.pause();
                    const time = Math.round(player.currentTime * 1000);
                    setTimeMs(time);
                    setPaused(true);
                    review.onChange({ ...surface, timeMs: time });
                  }}
                >
                  Comment at current time
                </button>
              )}
            </>
          )}
          {surfaceType === "video" && (
            <button
              disabled={!document.fullscreenEnabled}
              aria-label="Fullscreen video review"
              onClick={() => {
                if (document.fullscreenElement) void document.exitFullscreen();
                else
                  void fullscreen.current
                    ?.requestFullscreen()
                    .catch(() =>
                      setMessage("Fullscreen is unavailable in this browser."),
                    );
              }}
            >
              <Maximize2 size={16} />
            </button>
          )}
        </div>
      )}
      {message && (
        <p className="pc-annotation-notice" role="status">
          {message}
        </p>
      )}
      {navigating && (
        <p className="pc-annotation-notice" role="status">
          Opening video location…
        </p>
      )}
      {!draft && tool !== "select" && (
        <p className="pc-annotation-notice" role="status">
          {tool === "freehand"
            ? "Draw around a region. Release to close the selection and comment."
            : "Select a region to attach your comment."}
        </p>
      )}
      {draft && (
        <SelectionActions
          label={anchorLabel(draft)}
          busy={review.busy}
          onComment={review.onCompose}
          onCancel={() => {
            review.onChange({ type: "version" });
            setTool("select");
          }}
        />
      )}
      {surfaceType === "image" ? (
        <ImageReviewViewer
          url={url}
          filename={filename}
          natural={natural}
          onLoad={setNatural}
          surface={surface}
          review={review}
          tool={tool}
          color={color}
          onLimit={setMessage}
        />
      ) : (
        <div className="pc-media-stage" ref={host}>
          {surfaceType === "pdf" ? (
            <Suspense fallback={<p role="status">Opening PDF…</p>}>
              <PDFReader
                url={url}
                locked={locked}
                navigation={
                  navigation
                    ? {
                        id: navigation.id,
                        pageIndex: navigation.anchor.pageIndex ?? 0,
                      }
                    : undefined
                }
                onUnavailable={setMessage}
                overlay={(page) => (
                  <AnnotationOverlay
                    surface={{ ...surface, pageIndex: page.pageIndex }}
                    review={review}
                    tool={tool}
                    color={color}
                    width={page.width}
                    height={page.height}
                    toContent={page.toContent}
                    toView={page.toView}
                    onLimit={setMessage}
                  />
                )}
              />
            </Suspense>
          ) : (
            <div
              className="pc-media-frame relative m-[0_auto] shrink-0"
              style={{ width: frameWidth, height: frameHeight || undefined }}
            >
              <video
                ref={video}
                src={url}
                controls={!draft && tool === "select"}
                controlsList="nofullscreen"
                disablePictureInPicture
                preload="metadata"
                onLoadedMetadata={(e) =>
                  setNatural({
                    width: e.currentTarget.videoWidth,
                    height: e.currentTarget.videoHeight,
                  })
                }
                onError={() => {
                  setNavigating(false);
                  setTimeMs(undefined);
                  setMessage("This video could not be played.");
                }}
                onPlay={() => {
                  setPaused(false);
                  setTimeMs(undefined);
                  setTool("select");
                }}
                onPause={() => setPaused(true)}
                onSeeking={() => {
                  setTimeMs(undefined);
                  setTool("select");
                }}
              />
              {showOverlay && (
                <AnnotationOverlay
                  surface={surface}
                  review={review}
                  tool={tool}
                  color={color}
                  width={frameWidth}
                  height={frameHeight}
                  onLimit={setMessage}
                />
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
