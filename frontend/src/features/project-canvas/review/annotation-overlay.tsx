import { memo, useEffect, useRef, useState } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import { centerReviewRegion } from "./review-navigation";

export type MediaAnchor = components["schemas"]["CanvasMediaAnchor"];
export type AnchorShape = components["schemas"]["CanvasAnchorShape"];
export type ReviewAnchor = components["schemas"]["CanvasAnchor"];
export type ReviewThread = components["schemas"]["CanvasThread"];
export type AnnotationTool = "select" | AnchorShape["type"];
export type Point = [number, number];
export type MediaReview = {
  anchor: ReviewAnchor;
  onChange: (anchor: ReviewAnchor) => void;
  threads: ReviewThread[];
  activeThread: string;
  hoveredThread: string;
  onHover: (id: string) => void;
  onCompose: () => void;
  onSelect: (id: string) => void;
  navigation?: { id: number; threadId: string; anchor: ReviewAnchor };
  canEdit: boolean;
  busy: boolean;
};
export const mediaAnchor = (
  value: ReviewAnchor | undefined,
): MediaAnchor | undefined => (value?.type === "media" ? value : undefined);
export const sameTarget = (
  a: MediaAnchor["target"],
  b: MediaAnchor["target"],
) => a.assetVersionId === b.assetVersionId && a.outputItemId === b.outputItemId;
export const sameSurface = (a: MediaAnchor, b: MediaAnchor) =>
  sameTarget(a.target, b.target) &&
  a.surface === b.surface &&
  a.pageIndex === b.pageIndex &&
  a.timeMs === b.timeMs;
export function anchorLabel(anchor: MediaAnchor) {
  if (anchor.surface === "pdf") return `Page ${(anchor.pageIndex ?? 0) + 1}`;
  if (anchor.surface === "video") {
    const ms = anchor.timeMs ?? 0;
    return `${Math.floor(ms / 60000)}:${String(Math.floor(ms / 1000) % 60).padStart(2, "0")}.${String(ms % 1000).padStart(3, "0")}`;
  }
  return "Image annotation";
}
const colors = { amber: "#b87712", rose: "#da4868", blue: "#377cda" };
const identity = (p: Point): Point => p;
const round = (n: number) =>
  Math.round(Math.max(0, Math.min(1, n)) * 10000) / 10000;

// A bounded linear pass removes nearly straight intermediate samples.
function simplify(
  points: Point[],
  width: number,
  height: number,
  toView: (p: Point) => Point,
): Point[] {
  if (points.length < 3) return points;
  const result = [points[0]];
  for (let i = 1; i < points.length - 1; i++) {
    const a = toView(result[result.length - 1]),
      b = toView(points[i]),
      c = toView(points[i + 1]);
    const dx = (c[0] - a[0]) * width,
      dy = (c[1] - a[1]) * height;
    const bx = (b[0] - a[0]) * width,
      by = (b[1] - a[1]) * height;
    const length = dx * dx + dy * dy;
    const t = length ? (bx * dx + by * dy) / length : -1;
    if (t < 0 || t > 1 || Math.hypot(bx - t * dx, by - t * dy) > 0.6)
      result.push(points[i]);
  }
  result.push(points[points.length - 1]);
  return result;
}

export function AnnotationOverlay({
  surface,
  review,
  tool,
  color,
  width,
  height,
  toContent = identity,
  toView = identity,
  getDisplayScale,
  autoNavigate = true,
  onLimit,
}: {
  surface: MediaAnchor;
  review: MediaReview;
  tool: AnnotationTool;
  color: AnchorShape["color"];
  width: number;
  height: number;
  toContent?: (p: Point) => Point;
  toView?: (p: Point) => Point;
  getDisplayScale?: () => number;
  autoNavigate?: boolean;
  onLimit: (message: string) => void;
}) {
  const svg = useRef<SVGSVGElement>(null);
  const jumpedTo = useRef<number>(undefined);
  const gesture = useRef<{ points: Point[]; pointerId: number } | undefined>(
    undefined,
  );
  const raf = useRef(0);
  const [preview, setPreview] = useState<AnchorShape>();
  const draft = mediaAnchor(review.anchor);
  const draftShapes = draft && sameSurface(draft, surface) ? draft.shapes : [];
  const drawing = tool !== "select" && review.canEdit && !review.busy;
  useEffect(() => {
    const navigation = review.navigation;
    const target = mediaAnchor(navigation?.anchor);
    if (
      !autoNavigate ||
      !navigation ||
      !target?.shapes.length ||
      jumpedTo.current === navigation.id ||
      !sameSurface(target, surface) ||
      !width ||
      !height
    )
      return;
    const frame = requestAnimationFrame(() => {
      const region = svg.current?.querySelector(
        `[data-annotation-thread="${navigation.threadId}"]`,
      );
      const container = svg.current?.closest<HTMLElement>(".pc-media-stage");
      if (!region || !container) return;
      centerReviewRegion(container, region.getBoundingClientRect());
      jumpedTo.current = navigation.id;
    });
    return () => cancelAnimationFrame(frame);
  }, [
    review.navigation?.id,
    autoNavigate,
    review.threads,
    surface.pageIndex,
    surface.timeMs,
    width,
    height,
  ]);
  useEffect(() => () => cancelAnimationFrame(raf.current), []);
  useEffect(() => {
    gesture.current = undefined;
    setPreview(undefined);
    cancelAnimationFrame(raf.current);
    raf.current = 0;
  }, [
    width,
    height,
    tool,
    surface.pageIndex,
    surface.timeMs,
    review.busy,
    color,
  ]);
  const shape = (points: Point[]): AnchorShape => {
    const scale = getDisplayScale?.() ?? 1;
    const first = points[0],
      last = points[points.length - 1];
    if (tool === "freehand" && points.length > 2)
      return { type: "freehand", color, points };
    if (
      (tool === "rectangle" || tool === "ellipse") &&
      Math.abs(first[0] - last[0]) * width * scale > 3 &&
      Math.abs(first[1] - last[1]) * height * scale > 3
    )
      return {
        type: tool,
        color,
        x: Math.min(first[0], last[0]),
        y: Math.min(first[1], last[1]),
        width: round(Math.abs(first[0] - last[0])),
        height: round(Math.abs(first[1] - last[1])),
      };
    return { type: "point", color, x: first[0], y: first[1] };
  };
  const sample = (event: React.PointerEvent<SVGSVGElement>): Point => {
    const bounds = event.currentTarget.getBoundingClientRect();
    return toContent([
      (event.clientX - bounds.left) / bounds.width,
      (event.clientY - bounds.top) / bounds.height,
    ]).map(round) as Point;
  };
  return (
    <svg
      ref={svg}
      className={`pc-media-overlay ${drawing ? "is-drawing" : ""}`}
      viewBox={`0 0 ${width} ${height}`}
      aria-label="Review annotations"
      onPointerDown={(event) => {
        if (!drawing || event.button !== 0 || gesture.current) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        gesture.current = {
          points: [sample(event)],
          pointerId: event.pointerId,
        };
        setPreview(shape(gesture.current.points));
      }}
      onPointerMove={(event) => {
        const current = gesture.current;
        if (!current || current.pointerId !== event.pointerId) return;
        const next = sample(event),
          last = current.points[current.points.length - 1];
        const scale = getDisplayScale?.() ?? 1;
        if (
          Math.hypot(
            (next[0] - last[0]) * width * scale,
            (next[1] - last[1]) * height * scale,
          ) < 2
        )
          return;
        const remaining =
          512 -
          draftShapes.reduce(
            (n, s) => n + (s.type === "freehand" ? s.points.length : 0),
            0,
          );
        if (tool === "freehand" && current.points.length >= remaining) {
          onLimit(
            "Stroke limit reached. Post this comment before adding more marks.",
          );
          return;
        }
        if (tool === "freehand") current.points.push(next);
        else current.points = [current.points[0], next];
        if (!raf.current)
          raf.current = requestAnimationFrame(() => {
            raf.current = 0;
            if (gesture.current) setPreview(shape([...gesture.current.points]));
          });
      }}
      onPointerUp={(event) => {
        const current = gesture.current;
        if (!current || current.pointerId !== event.pointerId) return;
        const endpoint = sample(event);
        const last = current.points[current.points.length - 1];
        if (endpoint[0] !== last[0] || endpoint[1] !== last[1]) {
          if (tool !== "freehand")
            current.points = [current.points[0], endpoint];
          else if (
            current.points.length <
            512 -
              draftShapes.reduce(
                (n, s) => n + (s.type === "freehand" ? s.points.length : 0),
                0,
              )
          )
            current.points.push(endpoint);
        }
        gesture.current = undefined;
        cancelAnimationFrame(raf.current);
        raf.current = 0;
        setPreview(undefined);
        event.currentTarget.releasePointerCapture(event.pointerId);
        const next = {
          ...surface,
          shapes: [
            ...draftShapes,
            shape(
              tool === "freehand"
                ? simplify(
                    current.points,
                    width * (getDisplayScale?.() ?? 1),
                    height * (getDisplayScale?.() ?? 1),
                    toView,
                  )
                : current.points,
            ),
          ],
        };
        const points = next.shapes.reduce(
          (n, s) => n + (s.type === "freehand" ? s.points.length : 0),
          0,
        );
        if (
          next.shapes.length > 16 ||
          points > 512 ||
          new TextEncoder().encode(JSON.stringify(next)).length > 16384
        ) {
          onLimit("This annotation is full. Post it before adding more marks.");
          return;
        }
        review.onChange(next);
        review.onCompose();
        onLimit("");
      }}
      onPointerCancel={() => {
        gesture.current = undefined;
        setPreview(undefined);
      }}
      onLostPointerCapture={() => {
        gesture.current = undefined;
        setPreview(undefined);
      }}
    >
      {review.threads.map((thread, index) => {
        const anchor = mediaAnchor(thread.anchor);
        if (
          !anchor ||
          !sameSurface(anchor, surface) ||
          (thread.status !== "open" &&
            review.activeThread !== thread.id &&
            review.hoveredThread !== thread.id)
        )
          return null;
        return (
          <g
            key={thread.id}
            data-annotation-thread={thread.id}
            className={`pc-annotation-mark ${review.activeThread === thread.id || review.hoveredThread === thread.id ? "is-active" : ""}`}
            role="button"
            tabIndex={drawing ? -1 : 0}
            aria-label={`Open annotation ${index + 1}`}
            onPointerEnter={() => !drawing && review.onHover(thread.id)}
            onPointerLeave={() => review.onHover("")}
            onFocus={() => review.onHover(thread.id)}
            onBlur={() => review.onHover("")}
            onClick={() => !drawing && review.onSelect(thread.id)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                review.onSelect(thread.id);
              }
            }}
          >
            <title>{`Comment ${index + 1}`}</title>
            <Shapes
              shapes={anchor.shapes}
              width={width}
              height={height}
              toView={toView}
            />
          </g>
        );
      })}
      <g className="pc-annotation-draft">
        <Shapes
          shapes={draftShapes}
          width={width}
          height={height}
          toView={toView}
        />
        {preview && (
          <Shapes
            shapes={[preview]}
            width={width}
            height={height}
            toView={toView}
          />
        )}
      </g>
    </svg>
  );
}
const Shapes = memo(function Shapes({
  shapes,
  width,
  height,
  toView,
}: {
  shapes: AnchorShape[];
  width: number;
  height: number;
  toView: (p: Point) => Point;
}) {
  const xy = (p: Point) => {
    const q = toView(p);
    return [q[0] * width, q[1] * height] as Point;
  };
  return (
    <>
      {shapes.map((s, i) => {
        const style = {
          stroke: colors[s.color],
          strokeWidth: 2.5,
          fill: colors[s.color],
          fillOpacity: "var(--pc-region-opacity, 0.05)",
          vectorEffect: "non-scaling-stroke" as const,
          strokeLinejoin: "round" as const,
          strokeLinecap: "round" as const,
        };
        if (s.type === "point") {
          const p = xy([s.x, s.y]);
          return (
            <circle
              key={i}
              cx={p[0]}
              cy={p[1]}
              r={7}
              style={{ ...style, fillOpacity: 1 }}
            />
          );
        }
        if (s.type === "freehand")
          return (
            <path
              key={i}
              d={`${s.points
                .map((p, n) => `${n ? "L" : "M"}${xy(p as Point).join(" ")}`)
                .join(" ")} Z`}
              style={style}
            />
          );
        const p = xy([s.x, s.y]),
          q = xy([s.x + s.width, s.y + s.height]);
        const x = Math.min(p[0], q[0]),
          y = Math.min(p[1], q[1]),
          w = Math.abs(q[0] - p[0]),
          h = Math.abs(q[1] - p[1]);
        return s.type === "rectangle" ? (
          <rect key={i} x={x} y={y} width={w} height={h} style={style} />
        ) : (
          <ellipse
            key={i}
            cx={x + w / 2}
            cy={y + h / 2}
            rx={w / 2}
            ry={h / 2}
            style={style}
          />
        );
      })}
    </>
  );
});
