import { useEffect, useRef, useState, type RefObject } from "react";
import { useReactFlow, useViewport } from "@xyflow/react";
import {
  appendDrawingPoint,
  normalizeDrawing,
  simplifyDrawing,
  type DrawingPoint,
} from "@/shared/canvas/freehand";

export function BoardDrawing({
  surface,
  active,
  color,
  width,
  opacity,
  onComplete,
}: {
  surface: RefObject<HTMLDivElement | null>;
  active: boolean;
  color: string;
  width: number;
  opacity: number;
  onComplete: (drawing: ReturnType<typeof normalizeDrawing>) => void;
}) {
  const flow = useReactFlow();
  const viewport = useViewport();
  // The preview polyline grows by appending to one string, never by copying
  // the gesture, so long strokes stay linear.
  const [preview, setPreview] = useState("");
  const complete = useRef(onComplete);
  complete.current = onComplete;

  useEffect(() => {
    const element = surface.current;
    if (!active || !element) return;
    let pointerId: number | null = null;
    let stroke: DrawingPoint[] = [];
    let path = "";
    let frame: number | undefined;
    let finish = complete.current;
    // Only a pen reports real pressure; mouse and touch would store a constant.
    const point = (event: PointerEvent): DrawingPoint => ({
      ...flow.screenToFlowPosition({ x: event.clientX, y: event.clientY }),
      ...(event.pointerType === "pen" && event.pressure > 0
        ? { pressure: event.pressure }
        : {}),
    });
    const add = (event: PointerEvent) => {
      const next = point(event);
      const length = stroke.length;
      if (!appendDrawingPoint(stroke, next, flow.getZoom())) return;
      // A long gesture was simplified to stay within the point limit.
      path =
        stroke.length > length
          ? `${path} ${next.x},${next.y}`
          : stroke.map((kept) => `${kept.x},${kept.y}`).join(" ");
    };
    const reset = () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      frame = undefined;
      const captured = pointerId;
      pointerId = null;
      stroke = [];
      path = "";
      if (captured !== null && element.hasPointerCapture(captured))
        element.releasePointerCapture(captured);
      setPreview("");
    };
    const down = (event: PointerEvent) => {
      if (
        event.button !== 0 ||
        pointerId !== null ||
        (event.target as Element).closest(
          ".wb-tool-dock,.wb-zoom-controls,.wb-participants",
        )
      )
        return;
      event.preventDefault();
      event.stopPropagation();
      pointerId = event.pointerId;
      element.setPointerCapture(pointerId);
      finish = complete.current;
      const first = point(event);
      stroke = [first];
      path = `${first.x},${first.y}`;
      setPreview(path);
    };
    const move = (event: PointerEvent) => {
      if (pointerId !== event.pointerId) return;
      event.preventDefault();
      const samples = event.getCoalescedEvents?.();
      for (const sample of samples?.length ? samples : [event]) add(sample);
      // Pointer samples update only this ink layer, at most once per frame.
      frame ??= requestAnimationFrame(() => {
        frame = undefined;
        setPreview(path);
      });
    };
    const finishStroke = () => {
      const completed = stroke;
      // Clear the gesture before releasing capture or notifying the scene so
      // a subsequent lostpointercapture cannot complete the same ink twice.
      reset();
      // Drop points within half a screen pixel of the drawn line.
      if (completed.length > 1)
        finish(
          normalizeDrawing(
            simplifyDrawing(completed, 0.5 / flow.getZoom()),
            6,
            { width: 12, height: 12 },
          ),
        );
    };
    const up = (event: PointerEvent) => {
      if (pointerId !== event.pointerId) return;
      event.preventDefault();
      event.stopPropagation();
      add(event);
      finishStroke();
    };
    const interrupted = (event: PointerEvent) => {
      // Capture can be lost outside the window without a pointerup. Preserve
      // collected ink; cancellation events need not carry a final position.
      if (pointerId === event.pointerId) finishStroke();
    };
    element.addEventListener("pointerdown", down, true);
    element.addEventListener("pointermove", move, true);
    element.addEventListener("pointerup", up, true);
    element.addEventListener("pointercancel", interrupted, true);
    element.addEventListener("lostpointercapture", interrupted, true);
    return () => {
      element.removeEventListener("pointerdown", down, true);
      element.removeEventListener("pointermove", move, true);
      element.removeEventListener("pointerup", up, true);
      element.removeEventListener("pointercancel", interrupted, true);
      element.removeEventListener("lostpointercapture", interrupted, true);
      reset();
    };
  }, [active, flow, surface]);

  if (!preview) return null;
  return (
    <svg
      className="absolute inset-0 w-full h-full pointer-events-none z-[4]"
      aria-hidden="true"
    >
      <g
        transform={`translate(${viewport.x} ${viewport.y}) scale(${viewport.zoom})`}
      >
        <polyline
          points={preview}
          fill="none"
          stroke={color}
          strokeWidth={width}
          opacity={opacity}
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </g>
    </svg>
  );
}
