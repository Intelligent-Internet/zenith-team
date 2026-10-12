import { Position, type NodeHandle } from "@xyflow/react";
import type { BoardItem } from "./board-api";
import { boardLifelineHead } from "./board-shape";

/**
 * The connection handles a Board object offers, precomputed: React Flow
 * otherwise mounts every unmeasured node to discover them, eagerly hydrating
 * offscreen documents and media. Frames take no connectors. Their id is null,
 * as the rendered handles' is, so a connector dropped near a handle rather
 * than on it still finds it.
 */
export function boardHandles(
  item: BoardItem,
  width: number,
  height: number,
): NodeHandle[] {
  if (item.type === "frame") return [];
  // A lifeline connects at the middle of its head, where its name is.
  const y =
    item.type === "shape" && item.properties.shape === "lifeline"
      ? boardLifelineHead(height, item.properties.head) / 2
      : height / 2;
  return [
    {
      id: null,
      type: "source",
      position: Position.Right,
      x: width - 4.5,
      y: y - 4.5,
      width: 9,
      height: 9,
    },
    {
      id: null,
      type: "target",
      position: Position.Left,
      x: -4.5,
      y: y - 4.5,
      width: 9,
      height: 9,
    },
  ];
}
