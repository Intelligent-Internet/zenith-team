export type DrawingPoint = { x: number; y: number; pressure?: number };

export const maxDrawingPoints = 4096;

/**
 * Keep pointer work bounded and drop subpixel noise. Appends in place so a long
 * gesture stays linear; returns whether the point was kept. A gesture that
 * reaches the point limit is simplified in place, coarser each time it must
 * be, so a long stroke keeps its whole length instead of being cut off.
 */
export function appendDrawingPoint(
  points: DrawingPoint[],
  point: DrawingPoint,
  zoom: number,
) {
  const previous = points.at(-1);
  if (
    previous &&
    Math.hypot(point.x - previous.x, point.y - previous.y) * zoom < 1.5
  )
    return false;
  for (
    let tolerance = 0.5 / zoom;
    points.length >= maxDrawingPoints;
    tolerance *= 2
  ) {
    const kept = simplifyDrawing(points, tolerance);
    if (kept.length < (maxDrawingPoints * 3) / 4)
      points.splice(0, points.length, ...kept);
  }
  points.push(point);
  return true;
}

/**
 * Ramer–Douglas–Peucker: drop points that lie within `tolerance` of the line
 * through their neighbours, keeping corners. Iterative, so long strokes cannot
 * exhaust the call stack.
 */
export function simplifyDrawing(points: DrawingPoint[], tolerance: number) {
  if (points.length < 3) return points;
  const keep = new Uint8Array(points.length);
  keep[0] = keep[points.length - 1] = 1;
  const ranges: [number, number][] = [[0, points.length - 1]];
  while (ranges.length) {
    const [start, end] = ranges.pop()!;
    const a = points[start],
      b = points[end];
    const dx = b.x - a.x,
      dy = b.y - a.y;
    const length = Math.hypot(dx, dy);
    let farthest = -1,
      distance = tolerance;
    for (let i = start + 1; i < end; i++) {
      const p = points[i];
      const d =
        length === 0
          ? Math.hypot(p.x - a.x, p.y - a.y)
          : Math.abs(dy * (p.x - a.x) - dx * (p.y - a.y)) / length;
      if (d > distance) {
        farthest = i;
        distance = d;
      }
    }
    if (farthest < 0) continue;
    keep[farthest] = 1;
    ranges.push([start, farthest], [farthest, end]);
  }
  return points.filter((_, i) => keep[i]);
}

// The server stores ink at a tenth of a Board unit and pressure at a hundredth.
const quantize = (value: number, scale: number) =>
  Math.round(value * scale) / scale + 0;

export function normalizeDrawing(
  points: DrawingPoint[],
  padding = 14,
  minimum = { width: 84, height: 54 },
) {
  let minX = Infinity,
    maxX = -Infinity,
    minY = Infinity,
    maxY = -Infinity;
  for (const point of points) {
    minX = Math.min(minX, point.x);
    maxX = Math.max(maxX, point.x);
    minY = Math.min(minY, point.y);
    maxY = Math.max(maxY, point.y);
  }
  return {
    position: { x: minX - padding, y: minY - padding },
    width: Math.max(minimum.width, maxX - minX + padding * 2),
    height: Math.max(minimum.height, maxY - minY + padding * 2),
    points: points.map((point) => ({
      x: quantize(point.x - minX + padding, 10),
      y: quantize(point.y - minY + padding, 10),
      ...(point.pressure === undefined
        ? {}
        : { pressure: quantize(point.pressure, 100) }),
    })),
  };
}
