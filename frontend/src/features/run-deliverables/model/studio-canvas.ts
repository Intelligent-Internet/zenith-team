import type { components } from "@/shared/api/generated/api.generated";
import type { RunOutputItem } from "./run-deliverable";

export type StudioCanvas = components["schemas"]["StudioCanvas"];
export type StudioCanvasNode = components["schemas"]["StudioCanvasNode"];

export function isStudioCanvasItem(item: RunOutputItem) {
  return item.contentType === "application/vnd.zenith-studio.canvas+json";
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Invalid canvas object");
  return value as Record<string, unknown>;
}

function text(value: unknown, limit: number): string {
  if (
    typeof value !== "string" ||
    !value.length ||
    value.trim() !== value ||
    value.includes("\u0000") ||
    [...value].length > limit
  )
    throw new Error("Invalid canvas text");
  return value;
}

function coordinate(value: unknown): number {
  if (
    typeof value !== "number" ||
    !Number.isFinite(value) ||
    Math.abs(value) > 1_000_000
  )
    throw new Error("Invalid canvas position");
  return value;
}

export function parseStudioCanvas(
  value: unknown,
  mediaOrdinals: number[],
): StudioCanvas {
  const canvas = record(value);
  if (
    canvas.version !== 1 ||
    !Array.isArray(canvas.nodes) ||
    canvas.nodes.length < 1 ||
    canvas.nodes.length > 200 ||
    !Array.isArray(canvas.edges) ||
    canvas.edges.length > 400
  )
    throw new Error("Unsupported canvas snapshot");
  const nodes: StudioCanvasNode[] = canvas.nodes.map((value) => {
    const node = record(value);
    const position = record(node.position);
    const result: StudioCanvasNode = {
      id: text(node.id, 128),
      type: text(node.type, 80),
      label: text(node.label, 160),
      position: { x: coordinate(position.x), y: coordinate(position.y) },
    };
    if (node.prompt !== undefined && node.prompt !== "")
      result.prompt = text(node.prompt, 16000);
    if (node.model !== undefined && node.model !== "")
      result.model = text(node.model, 16000);
    if (node.status !== undefined && node.status !== "")
      result.status = text(node.status, 16000);
    if (node.outputOrdinal !== undefined) {
      if (
        typeof node.outputOrdinal !== "number" ||
        !Number.isInteger(node.outputOrdinal) ||
        !mediaOrdinals.includes(node.outputOrdinal)
      )
        throw new Error("Canvas media is not in this result");
      result.outputOrdinal = node.outputOrdinal;
    }
    return result;
  });
  const ids = new Set(nodes.map((node) => node.id));
  if (ids.size !== nodes.length) throw new Error("Duplicate canvas node");
  const seenEdges = new Set<string>();
  const edges = canvas.edges.map((value) => {
    const edge = record(value);
    const source = text(edge.source, 128);
    const target = text(edge.target, 128);
    const key = JSON.stringify([source, target]);
    if (
      !ids.has(source) ||
      !ids.has(target) ||
      source === target ||
      seenEdges.has(key)
    )
      throw new Error("Invalid canvas dependency");
    seenEdges.add(key);
    return { source, target };
  });
  return { version: 1, projectId: text(canvas.projectId, 128), nodes, edges };
}

export type SavedStudioCanvas = components["schemas"]["SavedStudioCanvas"];
