import { useEffect, useRef, useState } from "react";
import { Maximize2, ZoomIn, ZoomOut } from "@/shared/ui/icons";
import {
  AnnotationOverlay,
  mediaAnchor,
  sameTarget,
  type AnnotationTool,
  type AnchorShape,
  type MediaAnchor,
  type MediaReview,
} from "./annotation-overlay";

type Camera = { x: number; y: number; scale: number };
type Point = { x: number; y: number };
type Gesture =
  | { type: "pan"; id: number; start: Point; camera: Camera; moved: boolean }
  | { type: "pinch"; distance: number; center: Point; camera: Camera };

export function ImageReviewViewer({
  url,
  filename,
  natural,
  onLoad,
  surface,
  review,
  tool,
  color,
  onLimit,
}: {
  url: string;
  filename: string;
  natural: { width: number; height: number };
  onLoad: (size: { width: number; height: number }) => void;
  surface: MediaAnchor;
  review: MediaReview;
  tool: AnnotationTool;
  color: AnchorShape["color"];
  onLimit: (message: string) => void;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const scene = useRef<HTMLDivElement>(null);
  const zoomLabel = useRef<HTMLSpanElement>(null);
  const camera = useRef<Camera>({ x: 0, y: 0, scale: 1 });
  const interaction = useRef({ drawing: false });
  interaction.current.drawing =
    tool !== "select" && review.canEdit && !review.busy;
  const navigation = useRef(review.navigation);
  navigation.current = review.navigation;
  const commands = useRef<{
    fit: () => void;
    zoom: (factor: number) => void;
    actual: () => void;
    navigate: () => void;
  }>(undefined);
  const [zoom, setZoom] = useState(1);
  const [failed, setFailed] = useState(false);
  const ready = natural.width > 0 && natural.height > 0;
  const verticalInset = review.canEdit ? 160 : 96;
  const getDisplayScale = () => camera.current.scale;

  useEffect(() => {
    const element = viewport.current;
    const layer = scene.current;
    if (!element || !layer || !natural.width || !natural.height) return;
    let size = { width: element.clientWidth, height: element.clientHeight };
    let initialized = false;
    let frame = 0;
    let settleTimer: ReturnType<typeof setTimeout> | undefined;
    let clickTimer: ReturnType<typeof setTimeout> | undefined;
    let gesture: Gesture | undefined;
    let marking: number | undefined;
    let space = false;
    let suppressClick = false;
    let jumpedTo: number | undefined;
    const touches = new Map<number, Point>();
    const fitScale = () =>
      Math.min(
        1,
        Math.max(1, size.width - 64) / natural.width,
        Math.max(1, size.height - verticalInset) / natural.height,
      );
    const limitScale = (scale: number) =>
      Math.max(Math.min(0.05, fitScale() / 4), Math.min(16, scale));
    const settle = () => {
      clearTimeout(settleTimer);
      setZoom(camera.current.scale);
    };
    const apply = (next: Camera) => {
      const gutter = Math.min(64, size.width / 4, size.height / 4);
      const scale = limitScale(next.scale);
      camera.current = {
        scale,
        x: Math.max(
          gutter - natural.width * scale,
          Math.min(size.width - gutter, next.x),
        ),
        y: Math.max(
          gutter - natural.height * scale,
          Math.min(size.height - gutter, next.y),
        ),
      };
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        const value = camera.current;
        layer.style.transform = `translate3d(${value.x}px, ${value.y}px, 0) scale(${value.scale})`;
        layer.style.setProperty("--pc-image-scale", String(value.scale));
        layer.classList.add("is-ready");
        if (zoomLabel.current)
          zoomLabel.current.textContent = `${Math.round(value.scale * 100)}%`;
      });
    };
    const center = () => ({ x: size.width / 2, y: size.height / 2 });
    const local = (event: { clientX: number; clientY: number }): Point => {
      const bounds = element.getBoundingClientRect();
      return { x: event.clientX - bounds.left, y: event.clientY - bounds.top };
    };
    const zoomAt = (scale: number, point: Point) => {
      const before = camera.current;
      const nextScale = limitScale(scale);
      apply({
        scale: nextScale,
        x: point.x - ((point.x - before.x) / before.scale) * nextScale,
        y: point.y - ((point.y - before.y) / before.scale) * nextScale,
      });
    };
    const fit = () => {
      if (marking !== undefined || gesture) return;
      const scale = fitScale();
      apply({
        scale,
        x: (size.width - natural.width * scale) / 2,
        y: (size.height - natural.height * scale) / 2,
      });
      settle();
    };
    const navigate = () => {
      const request = navigation.current;
      const anchor = mediaAnchor(request?.anchor);
      if (marking !== undefined || gesture) return;
      if (
        initialized &&
        request?.anchor.type === "version" &&
        jumpedTo !== request.id
      ) {
        fit();
        jumpedTo = request.id;
        return;
      }
      if (
        !initialized ||
        !request ||
        jumpedTo === request.id ||
        !anchor ||
        anchor.surface !== "image" ||
        !sameTarget(anchor.target, surface.target)
      )
        return;
      const points = anchor.shapes.flatMap((shape) =>
        shape.type === "freehand"
          ? shape.points
          : shape.type === "point"
            ? [[shape.x, shape.y]]
            : [
                [shape.x, shape.y],
                [shape.x + shape.width, shape.y + shape.height],
              ],
      );
      if (!points.length) {
        fit();
        jumpedTo = request.id;
        return;
      }
      const xs = points.map((point) => point[0] * natural.width);
      const ys = points.map((point) => point[1] * natural.height);
      const left = Math.min(...xs),
        right = Math.max(...xs),
        top = Math.min(...ys),
        bottom = Math.max(...ys);
      const scale = limitScale(
        Math.min(
          camera.current.scale,
          Math.max(1, size.width - 96) / Math.max(1, right - left),
          Math.max(1, size.height - verticalInset) / Math.max(1, bottom - top),
        ),
      );
      apply({
        scale,
        x: size.width / 2 - ((left + right) / 2) * scale,
        y: size.height / 2 - ((top + bottom) / 2) * scale,
      });
      jumpedTo = request.id;
      settle();
    };
    const resize = () => {
      const before = size;
      const value = camera.current;
      const fitted =
        Math.abs(value.scale - fitScale()) < 0.0001 &&
        Math.abs(value.x - (before.width - natural.width * value.scale) / 2) <
          1 &&
        Math.abs(value.y - (before.height - natural.height * value.scale) / 2) <
          1;
      size = { width: element.clientWidth, height: element.clientHeight };
      if (!size.width || !size.height) return;
      if (
        initialized &&
        (size.width !== before.width || size.height !== before.height)
      )
        cancelGesture();
      if (!initialized) {
        initialized = true;
        fit();
        navigate();
      } else if (fitted) {
        fit();
      } else {
        apply({
          ...value,
          x: value.x + (size.width - before.width) / 2,
          y: value.y + (size.height - before.height) / 2,
        });
        settle();
      }
    };
    const wheel = (event: WheelEvent) => {
      event.preventDefault();
      event.stopPropagation();
      if (marking !== undefined || gesture) return;
      const unit =
        event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? size.height : 1;
      if (event.ctrlKey || event.metaKey)
        zoomAt(
          camera.current.scale *
            Math.exp(
              -Math.max(-200, Math.min(200, event.deltaY * unit)) * 0.01,
            ),
          local(event),
        );
      else
        apply({
          ...camera.current,
          x: camera.current.x - event.deltaX * unit,
          y: camera.current.y - event.deltaY * unit,
        });
      clearTimeout(settleTimer);
      settleTimer = setTimeout(settle, 150);
    };
    const stopClick = () => {
      suppressClick = true;
      clearTimeout(clickTimer);
      clickTimer = setTimeout(() => {
        suppressClick = false;
      }, 300);
    };
    const controls = (target: EventTarget | null) =>
      (target as Element | null)?.closest(".pc-image-controls");
    const capture = (id: number) => {
      if (!element.hasPointerCapture(id)) element.setPointerCapture(id);
    };
    const pointerDown = (event: PointerEvent) => {
      if (controls(event.target) || (event.button !== 0 && event.button !== 1))
        return;
      const point = local(event);
      if (
        event.pointerType === "touch" &&
        touches.size === 2 &&
        !touches.has(event.pointerId)
      ) {
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      if (event.pointerType === "touch") touches.set(event.pointerId, point);
      if (touches.size >= 2) {
        const entries = [...touches.entries()].slice(0, 2);
        const [a, b] = entries.map((entry) => entry[1]);
        entries.forEach(([id]) => capture(id));
        marking = undefined;
        gesture = {
          type: "pinch",
          distance: Math.max(1, Math.hypot(b.x - a.x, b.y - a.y)),
          center: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 },
          camera: { ...camera.current },
        };
        element.classList.add("is-panning");
        event.preventDefault();
        event.stopPropagation();
        stopClick();
        return;
      }
      if (gesture || marking !== undefined) return;
      element.focus({ preventScroll: true });
      if (
        event.button === 0 &&
        !space &&
        interaction.current.drawing &&
        (event.target as Element).closest(".pc-media-overlay")
      ) {
        marking = event.pointerId;
        return;
      }
      gesture = {
        type: "pan",
        id: event.pointerId,
        start: point,
        camera: { ...camera.current },
        moved: false,
      };
      // Leave a stationary annotation click available to open its thread.
      if (
        !(event.target as Element).closest(".pc-annotation-mark") ||
        space ||
        event.button === 1
      ) {
        capture(event.pointerId);
        event.preventDefault();
        event.stopPropagation();
      }
    };
    const pointerMove = (event: PointerEvent) => {
      if (!gesture && touches.size === 0) return;
      const point = local(event);
      if (touches.has(event.pointerId)) touches.set(event.pointerId, point);
      if (!gesture) return;
      if (gesture.type === "pinch") {
        const [a, b] = [...touches.values()];
        if (!a || !b) return;
        const midpoint = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
        const scale = limitScale(
          (gesture.camera.scale * Math.hypot(b.x - a.x, b.y - a.y)) /
            gesture.distance,
        );
        apply({
          scale,
          x:
            midpoint.x -
            ((gesture.center.x - gesture.camera.x) / gesture.camera.scale) *
              scale,
          y:
            midpoint.y -
            ((gesture.center.y - gesture.camera.y) / gesture.camera.scale) *
              scale,
        });
      } else {
        if (event.pointerId !== gesture.id) return;
        const dx = point.x - gesture.start.x,
          dy = point.y - gesture.start.y;
        if (!gesture.moved && Math.hypot(dx, dy) < 4) return;
        gesture.moved = true;
        capture(event.pointerId);
        element.classList.add("is-panning");
        apply({
          ...gesture.camera,
          x: gesture.camera.x + dx,
          y: gesture.camera.y + dy,
        });
      }
      event.preventDefault();
      event.stopPropagation();
      stopClick();
    };
    const pointerEnd = (event: PointerEvent) => {
      if (gesture?.type === "pinch" && !touches.has(event.pointerId)) return;
      touches.delete(event.pointerId);
      if (marking === event.pointerId) marking = undefined;
      if (!gesture) return;
      if (gesture.type === "pan" && event.pointerId !== gesture.id) return;
      if (gesture.type === "pinch" && touches.size === 1) {
        const [id, point] = [...touches.entries()][0];
        gesture = {
          type: "pan",
          id,
          start: point,
          camera: { ...camera.current },
          moved: true,
        };
        return;
      }
      if (gesture.type === "pinch" || gesture.moved) stopClick();
      gesture = undefined;
      element.classList.remove("is-panning");
      settle();
      navigate();
    };
    const lostCapture = (event: PointerEvent) => {
      // SVG capture transfers to the viewport when a second finger starts a pinch.
      if (event.target === element) pointerEnd(event);
    };
    const cancelGesture = () => {
      for (const id of touches.keys())
        if (element.hasPointerCapture(id)) element.releasePointerCapture(id);
      if (gesture?.type === "pan" && element.hasPointerCapture(gesture.id))
        element.releasePointerCapture(gesture.id);
      if (marking !== undefined) {
        const overlay = layer.querySelector("svg");
        if (overlay?.hasPointerCapture(marking))
          overlay.releasePointerCapture(marking);
      }
      gesture = undefined;
      marking = undefined;
      space = false;
      touches.clear();
      element.classList.remove("is-panning", "is-space-pan");
    };
    const reset = () => {
      cancelGesture();
      settle();
    };
    const keyDown = (event: KeyboardEvent) => {
      if (
        (event.target as Element).closest(
          "button, a, input, textarea, select, [contenteditable], [role=button]",
        )
      )
        return;
      if (event.code === "Space") {
        if (marking !== undefined) return;
        space = true;
        element.classList.add("is-space-pan");
      } else if (marking === undefined && !gesture) {
        if (event.key === "+" || event.key === "=")
          zoomAt(camera.current.scale * 1.25, center());
        else if (event.key === "-")
          zoomAt(camera.current.scale / 1.25, center());
        else if (event.key === "0") fit();
        else if (event.key === "1") zoomAt(1, center());
        else if (event.key.startsWith("Arrow")) {
          const distance = event.shiftKey ? 100 : 40;
          apply({
            ...camera.current,
            x:
              camera.current.x +
              (event.key === "ArrowLeft"
                ? distance
                : event.key === "ArrowRight"
                  ? -distance
                  : 0),
            y:
              camera.current.y +
              (event.key === "ArrowUp"
                ? distance
                : event.key === "ArrowDown"
                  ? -distance
                  : 0),
          });
        } else return;
        settle();
      } else return;
      event.preventDefault();
      event.stopPropagation();
    };
    const keyUp = (event: KeyboardEvent) => {
      if (event.code !== "Space") return;
      space = false;
      element.classList.remove("is-space-pan");
    };
    const click = (event: MouseEvent) => {
      if (!suppressClick || controls(event.target)) return;
      event.preventDefault();
      event.stopPropagation();
      suppressClick = false;
    };
    const doubleClick = (event: MouseEvent) => {
      if (
        controls(event.target) ||
        marking !== undefined ||
        interaction.current.drawing ||
        (event.target as Element).closest(".pc-annotation-mark")
      )
        return;
      if (camera.current.scale > fitScale() * 1.1) fit();
      else {
        zoomAt(Math.max(1, camera.current.scale * 2), local(event));
        settle();
      }
      event.preventDefault();
      event.stopPropagation();
    };
    commands.current = {
      fit,
      zoom: (factor) => {
        if (marking === undefined && !gesture) {
          zoomAt(camera.current.scale * factor, center());
          settle();
        }
      },
      actual: () => {
        if (marking === undefined && !gesture) {
          zoomAt(1, center());
          settle();
        }
      },
      navigate,
    };
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    resize();
    element.addEventListener("wheel", wheel, { passive: false });
    element.addEventListener("pointerdown", pointerDown, true);
    element.addEventListener("pointermove", pointerMove, true);
    element.addEventListener("pointerup", pointerEnd, true);
    element.addEventListener("pointercancel", pointerEnd, true);
    element.addEventListener("lostpointercapture", lostCapture);
    element.addEventListener("keydown", keyDown);
    window.addEventListener("keyup", keyUp);
    element.addEventListener("click", click, true);
    element.addEventListener("dblclick", doubleClick);
    window.addEventListener("blur", reset);
    return () => {
      commands.current = undefined;
      observer.disconnect();
      cancelAnimationFrame(frame);
      clearTimeout(settleTimer);
      clearTimeout(clickTimer);
      element.removeEventListener("wheel", wheel);
      element.removeEventListener("pointerdown", pointerDown, true);
      element.removeEventListener("pointermove", pointerMove, true);
      element.removeEventListener("pointerup", pointerEnd, true);
      element.removeEventListener("pointercancel", pointerEnd, true);
      element.removeEventListener("lostpointercapture", lostCapture);
      element.removeEventListener("keydown", keyDown);
      window.removeEventListener("keyup", keyUp);
      element.removeEventListener("click", click, true);
      element.removeEventListener("dblclick", doubleClick);
      window.removeEventListener("blur", reset);
      cancelGesture();
    };
  }, [
    natural.width,
    natural.height,
    verticalInset,
    surface.target.assetVersionId,
    surface.target.outputItemId,
  ]);
  useEffect(() => commands.current?.navigate(), [review.navigation?.id]);

  return (
    <div
      ref={viewport}
      className="pc-image-viewport"
      data-drawing={interaction.current.drawing}
      tabIndex={0}
      role="region"
      aria-label={`Image canvas: ${filename}. Pinch or Ctrl/Cmd-scroll to zoom, scroll or drag to pan. Hold Space to pan while marking. Plus and minus zoom; zero fits the image; one shows actual size.`}
    >
      <div
        ref={scene}
        className="pc-image-scene"
        style={{
          width: natural.width || undefined,
          height: natural.height || undefined,
        }}
      >
        <img
          src={url}
          alt={filename}
          draggable={false}
          onLoad={(event) =>
            onLoad({
              width: event.currentTarget.naturalWidth,
              height: event.currentTarget.naturalHeight,
            })
          }
          onError={() => {
            setFailed(true);
            onLimit("This image could not be decoded.");
          }}
        />
        {ready && (
          <AnnotationOverlay
            surface={surface}
            review={review}
            tool={tool}
            color={color}
            width={natural.width}
            height={natural.height}
            getDisplayScale={getDisplayScale}
            autoNavigate={false}
            onLimit={onLimit}
          />
        )}
      </div>
      {!ready && !failed && (
        <p
          className="grid [place-content:center] h-full m-0 text-[var(--pc-muted)] text-[length:12px]"
          role="status"
        >
          Opening image…
        </p>
      )}
      <span className="pc-image-hint" aria-hidden>
        Pinch / Ctrl/Cmd-scroll to zoom · Drag to pan · Space while marking
      </span>
      <div className="pc-image-controls" role="toolbar" aria-label="Image view">
        <button
          type="button"
          disabled={!ready}
          onClick={() => commands.current?.fit()}
          title="Fit image (0)"
        >
          <Maximize2 size={16} /> Fit
        </button>
        <button
          type="button"
          disabled={!ready}
          onClick={() => commands.current?.actual()}
          title="Actual size (1)"
          aria-label="Show image at actual size"
        >
          100%
        </button>
        <span className="w-[1px] h-[16px] m-[0_4px] [background:var(--pc-line)]" />
        <button
          type="button"
          disabled={!ready}
          onClick={() => commands.current?.zoom(1 / 1.25)}
          aria-label="Zoom out image"
        >
          <ZoomOut size={16} />
        </button>
        <span
          ref={zoomLabel}
          className="min-w-[44px] text-center text-[length:11px] [font-variant-numeric:tabular-nums] text-[var(--pc-muted)]"
        >
          {Math.round(zoom * 100)}%
        </span>
        <button
          type="button"
          disabled={!ready}
          onClick={() => commands.current?.zoom(1.25)}
          aria-label="Zoom in image"
        >
          <ZoomIn size={16} />
        </button>
      </div>
    </div>
  );
}
