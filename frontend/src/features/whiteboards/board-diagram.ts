import type { components } from "@/shared/api/generated/api.generated";

// Diagram geometry, the same rules the server's previews and layouts follow
// (server/internal/canvas/board_diagram.go): the path each connector route
// takes, in Board units.

export type BoardRouteType = components["schemas"]["BoardRouteType"];
export type BoardMarkerType = components["schemas"]["BoardMarkerType"];
export type BoardLineType = components["schemas"]["BoardLineType"];
type ShapeKind = components["schemas"]["BoardShapeType"] | undefined;

export type BoardRect = { x: number; y: number; width: number; height: number };
export type BoardPoint = [number, number];

// A self message loops this far right and down from its lifeline.
export const selfMessageWidth = 40;
export const selfMessageHeight = 24;

function center(r: BoardRect): BoardPoint {
  return [r.x + r.width / 2, r.y + r.height / 2];
}

// How far along (dx, dy), as a fraction of it, a box's outline lies from its
// center: the ellipse for round kinds, the diamond's edges, else the box.
function outlineReach(r: BoardRect, kind: ShapeKind, dx: number, dy: number) {
  const a = r.width / 2,
    b = r.height / 2;
  if (kind === "ellipse" || kind === "cloud")
    return 1 / Math.sqrt((dx * dx) / (a * a) + (dy * dy) / (b * b));
  if (kind === "diamond") return 1 / (Math.abs(dx) / a + Math.abs(dy) / b);
  let t = Infinity;
  if (dx !== 0) t = a / Math.abs(dx);
  if (dy !== 0) t = Math.min(t, b / Math.abs(dy));
  return t;
}

// Repeated points and the middle of straight runs drop out, so an elbow
// between aligned boxes is one segment.
function distinct(points: BoardPoint[]): BoardPoint[] {
  const out: BoardPoint[] = [points[0]];
  for (const p of points.slice(1)) {
    const last = out[out.length - 1];
    if (Math.abs(last[0] - p[0]) < 1e-9 && Math.abs(last[1] - p[1]) < 1e-9)
      continue;
    if (out.length >= 2) {
      const prev = out[out.length - 2];
      const cross =
        (last[0] - prev[0]) * (p[1] - last[1]) -
        (last[1] - prev[1]) * (p[0] - last[0]);
      if (Math.abs(cross) < 1e-9) {
        out[out.length - 1] = p;
        continue;
      }
    }
    out.push(p);
  }
  return out;
}

/**
 * The path a connector takes from a to b: flow from a's right middle to b's
 * left middle; straight between the centers from outline to outline; elbow
 * out of the facing sides, turning once halfway across the larger gap; a
 * message straight across at its row, or a loop when it returns to a.
 */
export function boardEdgePoints(
  a: BoardRect,
  b: BoardRect,
  aKind: ShapeKind,
  bKind: ShapeKind,
  route: BoardRouteType,
  anchorY: number | null | undefined,
  self: boolean,
): BoardPoint[] {
  const [ax, ay] = center(a);
  const [bx, by] = center(b);
  if (route === "straight") {
    const dx = bx - ax,
      dy = by - ay;
    if (dx === 0 && dy === 0)
      return [
        [ax, ay],
        [bx, by],
      ];
    const ta = outlineReach(a, aKind, dx, dy),
      tb = outlineReach(b, bKind, dx, dy);
    if (ta + tb >= 1)
      return [
        [ax, ay],
        [bx, by],
      ];
    return [
      [ax + ta * dx, ay + ta * dy],
      [bx - tb * dx, by - tb * dy],
    ];
  }
  if (route === "elbow") {
    const gapX = Math.max(b.x - (a.x + a.width), a.x - (b.x + b.width));
    const gapY = Math.max(b.y - (a.y + a.height), a.y - (b.y + b.height));
    if (gapY >= gapX && gapY > 0) {
      const from: BoardPoint = b.y < a.y ? [ax, a.y] : [ax, a.y + a.height];
      const to: BoardPoint = b.y < a.y ? [bx, b.y + b.height] : [bx, b.y];
      const middle = (from[1] + to[1]) / 2;
      return distinct([from, [from[0], middle], [to[0], middle], to]);
    }
    if (gapX > 0) {
      const from: BoardPoint = b.x < a.x ? [a.x, ay] : [a.x + a.width, ay];
      const to: BoardPoint = b.x < a.x ? [b.x + b.width, by] : [b.x, by];
      const middle = (from[0] + to[0]) / 2;
      return distinct([from, [middle, from[1]], [middle, to[1]], to]);
    }
    return boardEdgePoints(a, b, aKind, bKind, "straight", anchorY, self);
  }
  if (route === "message") {
    const y = a.y + (anchorY ?? a.height / 2);
    if (self)
      return [
        [ax, y],
        [ax + selfMessageWidth, y],
        [ax + selfMessageWidth, y + selfMessageHeight],
        [ax, y + selfMessageHeight],
      ];
    return [
      [ax, y],
      [bx, y],
    ];
  }
  return [
    [a.x + a.width, a.y + a.height / 2],
    [b.x, b.y + b.height / 2],
  ];
}

/** The point halfway along a path, where its label goes. */
export function boardPathMiddle(points: BoardPoint[]): BoardPoint {
  let total = 0;
  for (let i = 1; i < points.length; i++)
    total += Math.hypot(
      points[i][0] - points[i - 1][0],
      points[i][1] - points[i - 1][1],
    );
  let half = total / 2;
  for (let i = 1; i < points.length; i++) {
    const length = Math.hypot(
      points[i][0] - points[i - 1][0],
      points[i][1] - points[i - 1][1],
    );
    if (length >= half && length > 0) {
      const t = half / length;
      return [
        points[i - 1][0] + t * (points[i][0] - points[i - 1][0]),
        points[i - 1][1] + t * (points[i][1] - points[i - 1][1]),
      ];
    }
    half -= length;
  }
  return points[0];
}

/** An SVG path through the points, with gently rounded corners. */
export function boardPolylinePath(points: BoardPoint[], radius = 5): string {
  if (points.length < 3)
    return `M${points.map(([x, y]) => `${x},${y}`).join(" L")}`;
  let d = `M${points[0][0]},${points[0][1]}`;
  for (let i = 1; i < points.length - 1; i++) {
    const [px, py] = points[i - 1],
      [x, y] = points[i],
      [nx, ny] = points[i + 1];
    const inLength = Math.hypot(x - px, y - py),
      outLength = Math.hypot(nx - x, ny - y);
    const r = Math.min(radius, inLength / 2, outLength / 2);
    const sx = x - ((x - px) / inLength) * r,
      sy = y - ((y - py) / inLength) * r;
    const ex = x + ((nx - x) / outLength) * r,
      ey = y + ((ny - y) / outLength) * r;
    d += ` L${sx},${sy} Q${x},${y} ${ex},${ey}`;
  }
  const last = points[points.length - 1];
  return `${d} L${last[0]},${last[1]}`;
}

/**
 * Where a connector's end label goes: beside the line just off its tip,
 * wholly on one side of it, so it never covers the line or its marker. The
 * alignments are the fractions of the label's own size to shift it by.
 */
export function boardEndLabelPlace(tip: BoardPoint, from: BoardPoint) {
  const length = Math.hypot(from[0] - tip[0], from[1] - tip[1]) || 1;
  const ux = (from[0] - tip[0]) / length,
    uy = (from[1] - tip[1]) / length;
  const nx = -uy,
    ny = ux;
  const align = (n: number) => (n > 0.3 ? 0 : n < -0.3 ? -1 : -0.5);
  return {
    x: tip[0] + ux * 18 + nx * 10,
    y: tip[1] + uy * 18 + ny * 10,
    alignX: align(nx),
    alignY: align(ny),
  };
}
