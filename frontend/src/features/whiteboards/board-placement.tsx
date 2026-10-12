import { useEffect, useRef, useState, type RefObject } from "react";
import { useReactFlow, useViewport } from "@xyflow/react";
import type { BoardItem } from "./board-api";
import { BoardShapeGraphic } from "./board-shape";

export type PlacementBounds = Pick<BoardItem, "x" | "y" | "width" | "height">;

export function BoardPlacement({
  surface,
  mode,
  shape,
  color,
  fill,
  strokeWidth,
  onComplete,
}: {
  surface: RefObject<HTMLDivElement | null>;
  mode: "frame" | "shape" | null;
  shape: string;
  color: string;
  fill: string;
  strokeWidth: number;
  onComplete: (bounds: PlacementBounds) => void;
}) {
  const flow = useReactFlow();
  const viewport = useViewport();
  const [bounds, setBounds] = useState<PlacementBounds | null>(null);
  const complete = useRef(onComplete);
  complete.current = onComplete;
  useEffect(() => {
    const element = surface.current;
    if (!mode || !element) return;
    let start: { x: number; y: number } | null = null;
    let pointerId: number | null = null;
    let frame: number | undefined;
    let preview: PlacementBounds | null = null;
    const reset = () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      frame = undefined;
      const captured = pointerId;
      start = null;
      pointerId = null;
      if (captured !== null && element.hasPointerCapture(captured))
        element.releasePointerCapture(captured);
      setBounds(null);
    };
    const rectangle = (event: PointerEvent): PlacementBounds => {
      const end = flow.screenToFlowPosition({
        x: event.clientX,
        y: event.clientY,
      });
      if (mode === "shape" && event.shiftKey) {
        const side = Math.max(
          Math.abs(end.x - start!.x),
          Math.abs(end.y - start!.y),
        );
        end.x = start!.x + (end.x < start!.x ? -side : side);
        end.y = start!.y + (end.y < start!.y ? -side : side);
      }
      return {
        x: Math.min(start!.x, end.x),
        y: Math.min(start!.y, end.y),
        width: Math.abs(end.x - start!.x),
        height: Math.abs(end.y - start!.y),
      };
    };
    const down = (event: PointerEvent) => {
      if (
        event.button !== 0 ||
        start ||
        (event.target as Element).closest(
          ".wb-tool-dock,.wb-zoom-controls,.wb-participants",
        )
      )
        return;
      event.preventDefault();
      event.stopPropagation();
      element.focus({ preventScroll: true });
      pointerId = event.pointerId;
      start = flow.screenToFlowPosition({ x: event.clientX, y: event.clientY });
      element.setPointerCapture(pointerId);
      setBounds(rectangle(event));
    };
    const move = (event: PointerEvent) => {
      if (pointerId !== event.pointerId) return;
      event.preventDefault();
      preview = rectangle(event);
      frame ??= requestAnimationFrame(() => {
        frame = undefined;
        setBounds(preview);
      });
    };
    const up = (event: PointerEvent) => {
      if (pointerId !== event.pointerId) return;
      event.preventDefault();
      event.stopPropagation();
      const result = rectangle(event);
      if (
        result.width * flow.getZoom() >= 8 &&
        result.height * flow.getZoom() >= 8
      )
        complete.current(result);
      reset();
    };
    const cancel = (event: PointerEvent) => {
      if (pointerId === event.pointerId) reset();
    };
    element.addEventListener("pointerdown", down, true);
    element.addEventListener("pointermove", move, true);
    element.addEventListener("pointerup", up, true);
    element.addEventListener("pointercancel", cancel, true);
    element.addEventListener("lostpointercapture", cancel, true);
    return () => {
      element.removeEventListener("pointerdown", down, true);
      element.removeEventListener("pointermove", move, true);
      element.removeEventListener("pointerup", up, true);
      element.removeEventListener("pointercancel", cancel, true);
      element.removeEventListener("lostpointercapture", cancel, true);
      reset();
    };
  }, [mode, flow, surface]);
  if (!bounds) return null;
  return (
    <div
      className={mode === "shape" ? "wb-shape-preview" : "wb-frame-preview"}
      aria-hidden
      style={{
        left: bounds.x * viewport.zoom + viewport.x,
        top: bounds.y * viewport.zoom + viewport.y,
        width: bounds.width * viewport.zoom,
        height: bounds.height * viewport.zoom,
        borderColor: mode === "frame" ? color : undefined,
        background: mode === "frame" ? fill : undefined,
      }}
    >
      {mode === "shape" && (
        <BoardShapeGraphic
          shape={shape}
          width={bounds.width}
          height={bounds.height}
          color={color}
          fill={fill}
          strokeWidth={strokeWidth}
        />
      )}
      <span>
        {Math.round(bounds.width)} × {Math.round(bounds.height)}
      </span>
    </div>
  );
}
