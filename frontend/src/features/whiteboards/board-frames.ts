import type { BoardCommand, BoardItem } from "./board-api";

type Bounds = { x: number; y: number; width: number; height: number };

/**
 * The innermost frame that wholly holds `bounds`: what sits in a frame moves
 * with it. Frames still being saved, and the ones `skip` names, never hold
 * anything.
 */
export function boardFrameAround(
  items: readonly BoardItem[],
  bounds: Bounds,
  skip: (frame: BoardItem) => boolean = () => false,
): string | null {
  let holder: BoardItem | null = null;
  for (const frame of items) {
    if (
      frame.type !== "frame" ||
      frame.deleted ||
      frame.rotation !== 0 ||
      frame.id.startsWith("local:") ||
      skip(frame) ||
      bounds.x < frame.x ||
      bounds.y < frame.y ||
      bounds.x + bounds.width > frame.x + frame.width ||
      bounds.y + bounds.height > frame.y + frame.height
    )
      continue;
    if (!holder || frame.width * frame.height < holder.width * holder.height)
      holder = frame;
  }
  return holder?.id ?? null;
}

/** New objects join the frame they are placed in, as if drawn there. */
export function boardIntoFrames(
  commands: BoardCommand[],
  items: readonly BoardItem[],
): BoardCommand[] {
  return commands.map((command) => {
    const geometry = command.geometry;
    if (
      command.type !== "create_item" ||
      command.itemType === "frame" ||
      command.parentRef ||
      !geometry ||
      geometry.parentFrameId
    )
      return command;
    const parentFrameId = boardFrameAround(items, geometry);
    return parentFrameId
      ? { ...command, geometry: { ...geometry, parentFrameId } }
      : command;
  });
}
