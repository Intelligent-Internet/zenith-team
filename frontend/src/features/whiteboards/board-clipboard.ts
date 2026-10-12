import type { BoardCommand, BoardEdge, BoardItem } from "./board-api";
import { geometryOf } from "./board-scene-session";

const clipboardPrefix = "zenith-whiteboard:1:";
type ClipboardContent = { companyId: string; commands: BoardCommand[] };

export function selectionWithChildren(items: BoardItem[], selected: string[]) {
  const ids = new Set(selected);
  let expanded = true;
  while (expanded) {
    expanded = false;
    for (const item of items) {
      if (
        item.parentFrameId &&
        ids.has(item.parentFrameId) &&
        !ids.has(item.id)
      ) {
        ids.add(item.id);
        expanded = true;
      }
    }
  }
  return items.filter((item) => ids.has(item.id) && !item.deleted);
}

export async function copyCommands({
  items,
  edges,
  content,
}: {
  items: BoardItem[];
  edges: BoardEdge[];
  content: (item: BoardItem) => Promise<Pick<BoardCommand, "text" | "points">>;
}): Promise<BoardCommand[]> {
  const ids = new Set(items.map((item) => item.id));
  const links = edges.filter(
    (edge) => !edge.deleted && ids.has(edge.sourceId) && ids.has(edge.targetId),
  );
  if (items.length + links.length > 100)
    throw new Error("Copy up to 100 objects and connectors at a time.");
  const commands = await Promise.all(
    items.map(async (item): Promise<BoardCommand> => ({
      type: "create_item",
      draftId: item.id,
      itemType: item.type,
      geometry: { ...geometryOf(item), parentFrameId: null },
      properties: { ...item.properties, locked: false },
      parentRef:
        item.parentFrameId && ids.has(item.parentFrameId)
          ? item.parentFrameId
          : undefined,
      ...(await content(item)),
    })),
  );
  // Parents must exist before children in the atomic command batch.
  const ordered: BoardCommand[] = [];
  const visited = new Set<string>();
  const byId = new Map(commands.map((command) => [command.draftId!, command]));
  const visit = (command: BoardCommand) => {
    if (visited.has(command.draftId!)) return;
    visited.add(command.draftId!);
    if (command.parentRef) visit(byId.get(command.parentRef)!);
    ordered.push(command);
  };
  commands.forEach(visit);
  return [
    ...ordered,
    ...links.map((edge): BoardCommand => ({
      type: "create_edge",
      draftId: edge.id,
      sourceRef: edge.sourceId,
      targetRef: edge.targetId,
      label: edge.label,
      relation: edge.relation,
      ...connectorNotation(edge),
    })),
  ];
}

function readContent(text: string): ClipboardContent {
  const invalid = () =>
    new Error("This Whiteboard clipboard content is invalid.");
  if (new Blob([text]).size > 2 * 1024 * 1024) throw invalid();
  const value = JSON.parse(
    text.slice(clipboardPrefix.length),
  ) as ClipboardContent;
  if (
    !value ||
    typeof value.companyId !== "string" ||
    !Array.isArray(value.commands) ||
    !value.commands.length ||
    value.commands.length > 100
  )
    throw invalid();
  const items = new Map<string, string>();
  const ids = new Set<string>();
  for (const command of value.commands) {
    if (
      !command ||
      typeof command.draftId !== "string" ||
      ids.has(command.draftId)
    )
      throw invalid();
    ids.add(command.draftId);
    if (command.type === "create_edge") {
      if (
        !items.has(command.sourceRef ?? "") ||
        !items.has(command.targetRef ?? "") ||
        typeof command.label !== "string" ||
        typeof command.relation !== "string"
      )
        throw invalid();
      continue;
    }
    const geometry = command.geometry;
    const properties = command.properties;
    if (
      command.type !== "create_item" ||
      !command.itemType ||
      !["sticky", "text", "shape", "stroke", "image", "file", "frame"].includes(
        command.itemType,
      ) ||
      !geometry ||
      !properties ||
      typeof properties.locked !== "boolean" ||
      ![
        geometry.x,
        geometry.y,
        geometry.width,
        geometry.height,
        geometry.rotation,
        geometry.stackOrder,
        geometry.readingOrder,
      ].every(Number.isFinite) ||
      geometry.width <= 0 ||
      geometry.height <= 0 ||
      geometry.parentFrameId !== null ||
      (command.parentRef !== undefined &&
        items.get(command.parentRef) !== "frame") ||
      (command.text !== undefined && typeof command.text !== "string")
    )
      throw invalid();
    for (const [key, property] of Object.entries(properties)) {
      if (key === "locked") continue;
      // A class's sections and a fragment's dividers are the only lists.
      if (
        key === "strokeWidth"
          ? !Number.isFinite(property)
          : key === "sections"
            ? !Array.isArray(property) ||
              property.length > 4 ||
              !property.every((section) => typeof section === "string")
            : key === "dividers"
              ? !Array.isArray(property) ||
                !property.every((divider) => Number.isFinite(divider))
              : property !== null && typeof property !== "string"
      )
        throw invalid();
    }
    if (
      command.itemType === "stroke" &&
      (!Array.isArray(command.points) ||
        !command.points.length ||
        command.points.length > 4096 ||
        !command.points.every(
          (point) =>
            point &&
            Number.isFinite(point.x) &&
            Number.isFinite(point.y) &&
            (point.pressure === undefined || Number.isFinite(point.pressure)),
        ))
    )
      throw invalid();
    items.set(command.draftId, command.itemType);
  }
  if (!items.size) throw invalid();
  return value;
}

export class BoardClipboard {
  private token = "";
  private content?: Promise<ClipboardContent>;
  private lastPaste = "";
  private offset = 0;

  copy(event: ClipboardEvent, content: Promise<ClipboardContent>) {
    this.token = clipboardPrefix + crypto.randomUUID();
    this.content = content;
    event.preventDefault();
    // Native clipboard events work without a read-permission prompt. The token
    // also allows an immediate paste while offscreen ink/text is still loading.
    event.clipboardData?.setData("text/plain", this.token);
    if (navigator.clipboard?.write && typeof ClipboardItem !== "undefined") {
      const blob = content.then(
        (value) =>
          new Blob([clipboardPrefix + JSON.stringify(value)], {
            type: "text/plain",
          }),
      );
      void navigator.clipboard
        .write([new ClipboardItem({ "text/plain": blob })])
        .catch(() => {}); // The native token remains usable in this open Board.
    }
  }

  accepts(text: string) {
    return text.startsWith(clipboardPrefix);
  }

  async paste(
    text: string,
    companyId: string,
    stackOrder: number,
  ): Promise<BoardCommand[]> {
    const content =
      text === this.token && this.content
        ? await this.content
        : readContent(text);
    if (content.companyId !== companyId)
      throw new Error(
        "Paste these objects into a Whiteboard in the same company.",
      );
    const identity = JSON.stringify(content);
    this.offset = identity === this.lastPaste ? this.offset + 24 : 24;
    this.lastPaste = identity;
    const ids = new Map(
      content.commands.map((command) => [
        command.draftId!,
        crypto.randomUUID(),
      ]),
    );
    const layers = content.commands
      .filter((command) => command.geometry)
      .map((command) => command.geometry!.stackOrder)
      .sort((a, b) => a - b);
    return content.commands.map((command) =>
      command.type === "create_edge"
        ? {
            type: "create_edge",
            draftId: ids.get(command.draftId!),
            sourceRef: ids.get(command.sourceRef!),
            targetRef: ids.get(command.targetRef!),
            label: command.label,
            relation: command.relation,
            ...connectorNotation(command),
          }
        : {
            type: "create_item",
            draftId: ids.get(command.draftId!),
            itemType: command.itemType,
            geometry: {
              ...command.geometry!,
              x: command.geometry!.x + this.offset,
              y: command.geometry!.y + this.offset,
              stackOrder:
                stackOrder + layers.indexOf(command.geometry!.stackOrder) + 1,
              parentFrameId: null,
            },
            parentRef: command.parentRef
              ? ids.get(command.parentRef)
              : undefined,
            properties: { ...command.properties!, locked: false },
            text: command.text,
            points: command.points,
          },
    );
  }
}

// A connector's notation, copied as it is; a message's row is relative to
// its source, so it travels unchanged.
function connectorNotation(edge: {
  line?: BoardCommand["line"];
  startMarker?: BoardCommand["startMarker"];
  endMarker?: BoardCommand["endMarker"];
  route?: BoardCommand["route"];
  anchorY?: number | null;
  sourceLabel?: string;
  targetLabel?: string;
}): Partial<BoardCommand> {
  return {
    line: edge.line,
    startMarker: edge.startMarker,
    endMarker: edge.endMarker,
    route: edge.route,
    ...(edge.route === "message" && edge.anchorY != null
      ? { anchorY: edge.anchorY }
      : {}),
    sourceLabel: edge.sourceLabel,
    targetLabel: edge.targetLabel,
  };
}
