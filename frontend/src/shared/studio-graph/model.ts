import type {
  StudioBootstrap,
  StudioEdgeResponse,
  StudioNodeResponse,
} from "@/shared/api/studio-direct-client";

export type {
  StudioBootstrap,
  StudioEdgeResponse,
  StudioNodeResponse,
} from "@/shared/api/studio-direct-client";
export type StudioPosition = { x: number; y: number };
export type StudioNodeKind =
  | "text"
  | "comment"
  | "node_group"
  | "image"
  | "infographic"
  | "video"
  | "audio"
  | "voice"
  | "unknown";

export function studioNodeKindLabel(kind: StudioNodeKind): string {
  if (kind === "comment") return "Note";
  if (kind === "node_group") return "Group";
  if (kind === "audio") return "Music";
  if (kind === "unknown") return "Node";
  return kind.charAt(0).toUpperCase() + kind.slice(1);
}

export type StudioPort = {
  id: string;
  type: "source" | "target";
  role?: string;
  label?: string;
};

export type StudioGraphNode = {
  id: string;
  projectId: string;
  kind: StudioNodeKind;
  label: string;
  position: StudioPosition;
  parentId?: string;
  width?: number;
  height?: number;
  ports: StudioPort[];
  prompt?: string;
  model?: string;
  outputText?: string;
  parameters?: Record<string, unknown>;
  mediaUrl?: string;
  mediaFormat?: string;
  posterUrl?: string;
  readOnly: boolean;
  raw: StudioNodeResponse;
};

export type StudioGraphEdge = {
  id: string;
  source: string;
  target: string;
  sourceHandle: string;
  targetHandle: string;
  raw: StudioEdgeResponse;
};

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function position(value: unknown): StudioPosition {
  const raw = record(value);
  const x = typeof raw.x === "number" && Number.isFinite(raw.x) ? raw.x : 0;
  const y = typeof raw.y === "number" && Number.isFinite(raw.y) ? raw.y : 0;
  return { x, y };
}

function ports(value: unknown): StudioPort[] {
  const metadata = record(value);
  const raw = Array.isArray(metadata.ports) ? metadata.ports : [];
  return raw.flatMap((item): StudioPort[] => {
    const port = record(item);
    if (
      typeof port.id !== "string" ||
      (port.type !== "source" && port.type !== "target")
    )
      return [];
    return [
      {
        id: port.id,
        type: port.type,
        role: typeof port.role === "string" ? port.role : undefined,
        label: typeof port.label === "string" ? port.label : undefined,
      },
    ];
  });
}

function metadata(node: StudioNodeResponse) {
  try {
    return record(JSON.parse(node.metadataJson));
  } catch {
    return {};
  }
}

function firstString(...values: unknown[]) {
  return values.find(
    (value): value is string =>
      typeof value === "string" && value.trim() !== "",
  );
}

function output(node: StudioNodeResponse, raw: Record<string, unknown>) {
  try {
    return node.outputJson ? JSON.parse(node.outputJson) : raw.output;
  } catch {
    return raw.output;
  }
}

function mediaUrl(
  kind: StudioNodeKind,
  result: Record<string, unknown>,
  raw: Record<string, unknown>,
) {
  if (kind === "audio" || kind === "voice") {
    return firstString(
      result.audio_url,
      result.url,
      result.file_url,
      record(result.audio).url,
      record(result.audio_file).url,
      raw.audio_url,
      raw.url,
      raw.file_url,
    );
  }
  if (kind === "video") {
    return (
      firstString(
        result.video_url,
        raw.video_url,
        result.file_url,
        raw.file_url,
        result.url,
        raw.url,
      ) ??
      [
        result.preview_url,
        raw.preview_url,
        result.display_url,
        raw.display_url,
      ].find(
        (value): value is string =>
          typeof value === "string" &&
          value.trim() !== "" &&
          !/\.(avif|gif|jpe?g|png|svg|webp)$/i.test(value.split(/[?#]/, 1)[0]),
      )
    );
  }
  return firstString(
    result.display_url,
    result.url,
    result.image_url,
    result.file_url,
    raw.display_url,
    raw.url,
    raw.image_url,
    raw.file_url,
  );
}

const knownKinds = new Set<StudioNodeKind>([
  "text",
  "comment",
  "node_group",
  "image",
  "infographic",
  "video",
  "audio",
  "voice",
]);

export function normalizeStudioNode(node: StudioNodeResponse): StudioGraphNode {
  const raw = metadata(node);
  const kind =
    node.nodeType === "group" || node.nodeType === "nodeGroup"
      ? "node_group"
      : node.nodeType === "video_tool"
        ? "video"
        : knownKinds.has(node.nodeType as StudioNodeKind)
          ? (node.nodeType as StudioNodeKind)
          : "unknown";
  const parentId =
    typeof raw.parentId === "string"
      ? raw.parentId
      : typeof raw.groupId === "string"
        ? raw.groupId
        : undefined;
  const parameters = record(raw.parameters);
  const result = output(node, raw);
  const resultFields = record(result);
  return {
    id: node.id,
    projectId: node.projectId,
    kind,
    label: node.label?.trim() || node.nodeType,
    position: position(raw.position),
    parentId,
    width: typeof raw.width === "number" ? raw.width : undefined,
    height: typeof raw.height === "number" ? raw.height : undefined,
    ports: ports(raw),
    prompt: typeof raw.prompt === "string" ? raw.prompt : undefined,
    model:
      raw.source === "upload" || resultFields.source === "upload"
        ? undefined
        : firstString(raw.model, raw.model_name),
    outputText: firstString(
      result,
      resultFields.text,
      resultFields.content,
      resultFields.result,
      raw.output_text,
      raw.result_text,
    ),
    parameters: Object.keys(parameters).length ? parameters : undefined,
    mediaUrl: mediaUrl(kind, resultFields, raw),
    mediaFormat: firstString(resultFields.output_format, raw.output_format),
    posterUrl:
      kind === "video"
        ? firstString(
            resultFields.thumbnail_url,
            resultFields.thumbnail_preview_url,
            raw.thumbnail_url,
            raw.thumbnail_preview_url,
            ...[
              resultFields.display_url,
              resultFields.preview_url,
              raw.display_url,
              raw.preview_url,
            ].filter(
              (value) =>
                typeof value === "string" &&
                /\.(avif|gif|jpe?g|png|webp)$/i.test(value.split(/[?#]/, 1)[0]),
            ),
          )
        : undefined,
    readOnly: kind === "unknown" || node.nodeType === "video_tool",
    raw: node,
  };
}

export function canGenerateStudioNode(node: StudioGraphNode) {
  if (
    node.readOnly ||
    !["text", "image", "infographic", "video", "audio", "voice"].includes(
      node.kind,
    )
  )
    return false;
  const raw = metadata(node.raw);
  return (
    raw.source !== "upload" && record(output(node.raw, raw)).source !== "upload"
  );
}

export function isStudioNodeRunning(node: StudioGraphNode) {
  return ["submitted", "executing", "cancelling"].includes(node.raw.status);
}

export function normalizeStudioGraph(bootstrap: StudioBootstrap) {
  const nodes = (bootstrap.nodes ?? []).map(normalizeStudioNode);
  const ids = new Set(nodes.map((node) => node.id));
  for (const node of nodes) {
    if (!node.parentId || node.parentId === node.id || !ids.has(node.parentId))
      node.parentId = undefined;
  }
  nodes.sort(
    (left, right) =>
      Number(Boolean(left.parentId)) - Number(Boolean(right.parentId)),
  );
  const edges = (bootstrap.edges ?? [])
    .filter((edge) => ids.has(edge.sourceId) && ids.has(edge.targetId))
    .map<StudioGraphEdge>((edge) => ({
      id: edge.id,
      source: edge.sourceId,
      target: edge.targetId,
      sourceHandle: edge.sourceHandle,
      targetHandle: edge.targetHandle,
      raw: edge,
    }));
  // Persisted connections can reference provider ports absent from metadata.
  // Render those exact IDs so React Flow can resolve both endpoints.
  for (const node of nodes) {
    const connectedPorts: StudioPort[] = edges.flatMap((edge) => [
      ...(edge.source === node.id
        ? [{ id: edge.sourceHandle, type: "source" as const }]
        : []),
      ...(edge.target === node.id
        ? [{ id: edge.targetHandle, type: "target" as const }]
        : []),
    ]);
    const declared = node.ports;
    node.ports = [...declared];
    for (const port of connectedPorts) {
      if (
        !node.ports.some(
          (item) => item.id === port.id && item.type === port.type,
        )
      )
        node.ports.push({ ...port, label: port.id.replaceAll("_", " ") });
    }
    for (const type of ["target", "source"] as const) {
      if (!node.ports.some((port) => port.type === type)) {
        const reference = node.kind === "image" || node.kind === "video";
        node.ports.push({
          id: type === "source" ? "out" : reference ? "reference_1" : "in",
          type,
          label:
            type === "source" ? "Output" : reference ? "Reference 1" : "Input",
        });
      }
    }
  }
  return {
    project: bootstrap.project,
    nodes,
    edges,
    members: bootstrap.members ?? [],
  };
}

export function moveGroup(
  nodes: readonly StudioGraphNode[],
  groupId: string,
  nextPosition: StudioPosition,
) {
  const group = nodes.find((node) => node.id === groupId);
  if (!group) return nodes;
  const dx = nextPosition.x - group.position.x;
  const dy = nextPosition.y - group.position.y;
  return nodes.map((node) =>
    node.id === groupId || node.parentId !== groupId
      ? node
      : {
          ...node,
          position: { x: node.position.x + dx, y: node.position.y + dy },
        },
  );
}

export function isEditableTextTarget(target: EventTarget | null) {
  const element = target instanceof HTMLElement ? target : null;
  return Boolean(
    element?.closest("input, textarea, select, [contenteditable='true']"),
  );
}
