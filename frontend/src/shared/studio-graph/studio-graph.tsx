import {
  findStudioModel,
  studioDisplayName,
  type StudioModel,
  type StudioModelCatalog,
} from "@/shared/api/studio-model-catalog";
import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import {
  Background,
  ControlButton,
  Controls,
  ConnectionLineType,
  useUpdateNodeInternals,
  Handle,
  MiniMap,
  NodeResizer,
  Position,
  ReactFlow,
  useReactFlow,
  useNodesInitialized,
  useStore,
  useViewport,
  applyNodeChanges,
  type Edge,
  type Connection,
  type Node,
  type NodeChange,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import type {
  StudioCollaboratorCursor,
  StudioCursorUpdate,
} from "@/shared/api/studio-collaboration";
import {
  ImageIcon,
  PencilLine,
  TriangleAlert,
  Video,
  Maximize,
  ZoomIn,
  ZoomOut,
  ArrowRight,
  Copy,
  Trash2,
  Music,
  Text,
  Workflow,
  StickyNote,
} from "@/shared/ui/icons";
import {
  isEditableTextTarget,
  canGenerateStudioNode,
  isStudioNodeRunning,
  normalizeStudioGraph,
  studioNodeKindLabel,
  type StudioBootstrap,
  type StudioGraphNode,
  type StudioPosition,
} from "./model";
import "@xyflow/react/dist/style.css";
import "./studio-graph.css";
import { StudioMedia, studioMediaIdentity } from "./studio-media";
import { StudioCollaborators } from "./studio-collaborators";
import { StudioNodeModelPicker } from "./studio-node-model-picker";
import { StudioGenerateButton } from "./studio-generate-button";

type NodeSize = { width: number; height: number };
type ResizeGeometry = NodeSize & { x: number; y: number };
type LocalGeometry<T> = {
  value: T;
  saved: boolean;
  acknowledgedVersion?: number;
};

type CoreNodeData = {
  graphNode: StudioGraphNode;
  canResize: boolean;
  onRefreshMedia?: (nodeId: string) => Promise<string | undefined>;
  onError?: (error: unknown) => void;
  onInspect: (node: StudioGraphNode) => void;
  models: StudioModel[];
  onModelChange?: (nodeId: string, model: StudioModel) => Promise<void>;
  onGenerate?: (nodeId: string) => Promise<void>;
  generationPending: boolean;
  generationDisabledReason?: string;
  onContext: (node: StudioGraphNode, event: MouseEvent) => void;
  onResize: (node: StudioGraphNode, geometry: ResizeGeometry) => void;
  onResizeCommit: (node: StudioGraphNode, geometry: ResizeGeometry) => void;
  onMediaDimensions: (
    node: StudioGraphNode,
    dimensions: NodeSize,
    source: string,
  ) => void;
  groupMemberCount: number;
};
type CoreNode = Node<CoreNodeData, "studio">;

function nodeSize(node: StudioGraphNode): NodeSize {
  return {
    width: node.width ?? 300,
    height: node.height ?? (node.kind === "node_group" ? 180 : 230),
  };
}

function samePosition(left: StudioPosition, right: StudioPosition) {
  return Math.abs(left.x - right.x) < 0.01 && Math.abs(left.y - right.y) < 0.01;
}

function sameSize(left: NodeSize, right: NodeSize) {
  return (
    Math.abs(left.width - right.width) < 0.01 &&
    Math.abs(left.height - right.height) < 0.01
  );
}

function iconFor(kind: StudioGraphNode["kind"]) {
  if (["image", "infographic"].includes(kind)) return ImageIcon;
  if (kind === "video") return Video;
  if (kind === "audio" || kind === "voice") return Music;
  if (kind === "node_group") return Workflow;
  if (kind === "comment") return StickyNote;
  if (kind === "unknown") return TriangleAlert;
  return Text;
}

function nodeExecutionLabel(node: StudioGraphNode) {
  const labels: Record<string, string> = {
    submitted: "Generating",
    executing: "Generating",
    failed: "Failed",
    cancelled: "Cancelled",
    cancelling: "Cancelling",
  };
  return labels[node.raw.status];
}

function parseNodeMetadata(value: string) {
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

function formatPortLabel(label: string | undefined, fallback: string) {
  const text = label?.trim() || fallback;
  return text.charAt(0).toLocaleUpperCase() + text.slice(1);
}

const NodeMedia = memo(StudioMedia);

const GraphNode = memo(function GraphNode({
  data,
  selected,
}: NodeProps<CoreNode>) {
  const { graphNode } = data;
  const Icon = iconFor(graphNode.kind);
  const outputLabel = studioNodeKindLabel(graphNode.kind);
  const loadedVisual =
    Boolean(graphNode.mediaUrl) &&
    ["image", "infographic", "video"].includes(graphNode.kind);
  const noteText = graphNode.kind === "comment" ? graphNode.prompt : undefined;
  const structural =
    graphNode.kind === "comment" || graphNode.kind === "node_group";
  const generative = Boolean(
    data.onGenerate && canGenerateStudioNode(graphNode),
  );
  const generating =
    !structural && (data.generationPending || isStudioNodeRunning(graphNode));
  const executionLabel = structural ? undefined : nodeExecutionLabel(graphNode);
  const customLabel =
    graphNode.label && graphNode.label !== outputLabel
      ? graphNode.label
      : undefined;
  const hasContent = Boolean(
    graphNode.mediaUrl ||
    graphNode.outputText ||
    noteText ||
    (graphNode.kind === "node_group" && data.groupMemberCount),
  );
  const ports = graphNode.ports;
  const portSignature = ports
    .map((port) => `${port.type}:${port.id}`)
    .join("|");
  const graphNodeRef = useRef(graphNode);
  graphNodeRef.current = graphNode;
  const resize = useCallback(
    (_: unknown, geometry: ResizeGeometry) => {
      data.onResize(graphNodeRef.current, geometry);
    },
    [data.onResize],
  );
  const finishResize = useCallback(
    (_: unknown, geometry: ResizeGeometry) => {
      data.onResizeCommit(graphNodeRef.current, geometry);
    },
    [data.onResizeCommit],
  );
  const refresh = useCallback(
    () => data.onRefreshMedia?.(graphNode.id) ?? Promise.resolve(undefined),
    [data.onRefreshMedia, graphNode.id],
  );
  const dimensions = useCallback(
    (size: NodeSize, source: string) =>
      data.onMediaDimensions(graphNodeRef.current, size, source),
    [data.onMediaDimensions],
  );
  const updateNodeInternals = useUpdateNodeInternals();
  useEffect(() => {
    updateNodeInternals(graphNode.id);
  }, [graphNode.id, portSignature, updateNodeInternals]);
  return (
    <div
      className={`studio-graph-node${graphNode.kind === "node_group" ? " is-group" : ""}${selected ? " is-selected" : ""}${graphNode.readOnly ? " is-read-only" : ""}${loadedVisual ? " has-visual-media" : ""}${!hasContent ? " is-empty" : ""}${generating ? " is-generating" : ""}`}
      data-kind={graphNode.kind}
      data-status={graphNode.raw.status}
      onContextMenu={(event) => data.onContext(graphNode, event)}
    >
      {data.canResize ? (
        <NodeResizer
          isVisible
          keepAspectRatio={
            graphNode.kind === "image" ||
            graphNode.kind === "infographic" ||
            graphNode.kind === "video"
          }
          minWidth={graphNode.kind === "node_group" ? 220 : 180}
          minHeight={
            loadedVisual
              ? 40
              : graphNode.kind === "node_group"
                ? 140
                : !hasContent
                  ? 180
                  : 150
          }
          onResize={resize}
          onResizeEnd={finishResize}
        />
      ) : null}
      {ports
        .filter((port) => port.type === "target")
        .map((port, index) => (
          <Handle
            key={`target-${port.id}`}
            id={port.id}
            type="target"
            position={Position.Left}
            style={{
              top: `${((index + 1) * 100) / (ports.filter((item) => item.type === "target").length + 1)}%`,
            }}
          >
            <span className="studio-graph-node__port-label is-input">
              {formatPortLabel(port.label, "Input")}
            </span>
          </Handle>
        ))}
      <div className="studio-graph-node__header">
        <span className="studio-graph-node__kind-icon">
          <Icon size={17} aria-hidden />
        </span>
        <div className="studio-graph-node__identity">
          <strong title={graphNode.label}>
            {structural ? graphNode.label || outputLabel : outputLabel}
          </strong>
          {graphNode.kind === "node_group" && data.groupMemberCount ? (
            <span className="studio-graph-node__model">
              {data.groupMemberCount}{" "}
              {data.groupMemberCount === 1 ? "node" : "nodes"}
            </span>
          ) : null}
        </div>
        {graphNode.model && !structural ? (
          <StudioNodeModelPicker
            node={graphNode}
            models={data.models}
            onChange={data.onModelChange}
            onError={data.onError}
          />
        ) : null}
      </div>
      <div
        className="studio-graph-node__preview"
        title={graphNode.label}
        aria-busy={generating}
      >
        {customLabel &&
        !structural &&
        !generating &&
        (hasContent || graphNode.prompt) ? (
          <span className="studio-graph-node__label">{customLabel}</span>
        ) : null}
        {generating ? (
          <div className="studio-graph-node__generating" role="status">
            <span className="studio-graph-node__sheen" aria-hidden />
            <span>Generating…</span>
          </div>
        ) : graphNode.mediaUrl ? (
          <NodeMedia
            source={graphNode.mediaUrl}
            poster={graphNode.posterUrl}
            label={graphNode.label}
            format={graphNode.mediaFormat}
            kind={
              graphNode.kind === "video"
                ? "video"
                : graphNode.kind === "audio" || graphNode.kind === "voice"
                  ? "audio"
                  : "image"
            }
            refresh={refresh}
            onError={data.onError}
            onDimensions={dimensions}
          />
        ) : graphNode.outputText || noteText ? (
          <p className="studio-graph-node__text">
            {graphNode.outputText || noteText}
          </p>
        ) : graphNode.kind === "node_group" && data.groupMemberCount ? null : (
          <div className="studio-graph-node__empty">
            <p data-has-prompt={Boolean(graphNode.prompt)}>
              {graphNode.prompt ||
                (graphNode.kind === "node_group"
                  ? "Keep related ideas and media together."
                  : graphNode.kind === "comment"
                    ? "Write a note…"
                    : graphNode.readOnly
                      ? "No output yet."
                      : graphNode.kind === "text"
                        ? "Describe what you want to write…"
                        : graphNode.kind === "voice"
                          ? "Write the words you want spoken…"
                          : `Describe the ${outputLabel.toLowerCase()} you want to create…`)}
            </p>
            {!generative && (
              <button
                type="button"
                className="studio-graph-node__edit nodrag nopan"
                onClick={(event) => {
                  event.stopPropagation();
                  data.onInspect(graphNode);
                }}
              >
                {graphNode.readOnly
                  ? "View details"
                  : graphNode.prompt
                    ? "Edit prompt"
                    : graphNode.kind === "node_group"
                      ? "Edit group"
                      : graphNode.kind === "comment"
                        ? "Write note"
                        : "Add prompt"}
                <ArrowRight size={14} aria-hidden />
              </button>
            )}
          </div>
        )}
      </div>
      {generative &&
      data.onGenerate &&
      !generating &&
      !hasContent &&
      graphNode.raw.status !== "success" ? (
        <div className="studio-graph-node__actions nodrag nopan">
          <StudioGenerateButton
            node={graphNode}
            pending={data.generationPending}
            disabledReason={data.generationDisabledReason}
            onGenerate={data.onGenerate}
            onError={data.onError}
          />
        </div>
      ) : null}
      {executionLabel && !generative ? (
        <span className="studio-graph-node__execution" role="status">
          {executionLabel}
        </span>
      ) : null}
      {ports
        .filter((port) => port.type === "source")
        .map((port, index) => (
          <Handle
            key={`source-${port.id}`}
            id={port.id}
            type="source"
            position={Position.Right}
            style={{
              top: `${((index + 1) * 100) / (ports.filter((item) => item.type === "source").length + 1)}%`,
            }}
          >
            <span className="studio-graph-node__port-label is-output">
              {outputLabel}
            </span>
          </Handle>
        ))}
    </div>
  );
});

function StudioGraphViewport({
  onReady,
}: {
  onReady?: (instance: ReactFlowInstance) => void;
}) {
  const instance = useReactFlow();
  const { fitView, getViewport, setViewport } = instance;
  const initialized = useNodesInitialized();
  const width = useStore((state) => state.width);
  const height = useStore((state) => state.height);
  const fitted = useRef(false);
  const bounds = useRef<{ width: number; height: number } | null>(null);

  useEffect(() => {
    if (instance.viewportInitialized) onReady?.(instance);
  }, [instance, onReady]);

  useEffect(() => {
    if (!width || !height) return;
    const previous = bounds.current;
    bounds.current = { width, height };
    if (!previous || !fitted.current) return;
    const viewport = getViewport();
    void setViewport({
      ...viewport,
      x: viewport.x + (width - previous.width) / 2,
      y: viewport.y + (height - previous.height) / 2,
    });
  }, [width, height, getViewport, setViewport]);

  useEffect(() => {
    if (fitted.current || !initialized || !width || !height) return;
    const frame = requestAnimationFrame(() => {
      fitted.current = true;
      void fitView({
        padding: { top: "80px", bottom: "60px", left: "48px", right: "48px" },
        maxZoom: 1.35,
      });
    });
    return () => cancelAnimationFrame(frame);
  }, [fitView, initialized, width, height]);

  return null;
}

function StudioGraphControls() {
  const { fitView, zoomIn, zoomOut } = useReactFlow();
  const { zoom } = useViewport();
  const zoomLabel = `${Math.round(zoom * 100)}%`;

  return (
    <Controls
      className="studio-graph__controls"
      position="bottom-right"
      orientation="horizontal"
      showZoom={false}
      showFitView={false}
      showInteractive={false}
    >
      <ControlButton
        aria-label="Zoom out"
        title="Zoom out"
        onClick={() => void zoomOut({ duration: 140 })}
      >
        <ZoomOut size={16} aria-hidden />
      </ControlButton>
      <span className="studio-graph__zoom-value" aria-live="polite">
        {zoomLabel}
      </span>
      <ControlButton
        aria-label="Zoom in"
        title="Zoom in"
        onClick={() => void zoomIn({ duration: 140 })}
      >
        <ZoomIn size={16} aria-hidden />
      </ControlButton>
      <ControlButton
        aria-label="Fit canvas"
        title="Fit canvas"
        onClick={() =>
          void fitView({
            padding: {
              top: "80px",
              bottom: "60px",
              left: "48px",
              right: "48px",
            },
            maxZoom: 1.35,
          })
        }
      >
        <Maximize size={16} aria-hidden />
      </ControlButton>
    </Controls>
  );
}

const nodeTypes = { studio: GraphNode };

export type StudioGraphCapabilities = {
  updateNodePosition: boolean;
  resizeNode?: boolean;
  createNode?: boolean;
  renameNode?: boolean;
  deleteNode?: boolean;
  connectEdge?: boolean;
  disconnectEdge?: boolean;
  layout?: boolean;
};

export type StudioGraphMutation =
  | { operation: "createNode"; payload: Record<string, unknown> }
  | { operation: "updateNode"; payload: Record<string, unknown> }
  | { operation: "deleteNode"; payload: Record<string, unknown> }
  | { operation: "createEdge"; payload: Record<string, unknown> }
  | { operation: "deleteEdge"; payload: Record<string, unknown> }
  | { operation: "applyLayout"; payload: Record<string, unknown> };

export function StudioGraph({
  bootstrap,
  catalog,
  capabilities,
  onPositionCommit,
  onMutation,
  onInspect,
  onModelChange,
  onGenerate,
  generatingIds,
  restoringIds,
  generationDisabledReason,
  onUnsupportedAction,
  onRefreshMedia,
  onError,
  onReady,
  interactionMode = "select",
  collaboration,
  children,
}: {
  onError?: (error: unknown) => void;
  onRefreshMedia?: (nodeId: string) => Promise<string | undefined>;
  bootstrap: StudioBootstrap;
  catalog?: StudioModelCatalog | null;
  capabilities: StudioGraphCapabilities;
  onPositionCommit: (
    nodeId: string,
    position: StudioPosition,
    immediate?: boolean,
  ) => Promise<void>;
  onMutation?: (mutation: StudioGraphMutation) => Promise<void | number>;
  onInspect?: (node: StudioGraphNode) => void;
  onModelChange?: (nodeId: string, model: StudioModel) => Promise<void>;
  onGenerate?: (nodeId: string) => Promise<void>;
  generatingIds?: ReadonlySet<string>;
  restoringIds?: ReadonlySet<string>;
  generationDisabledReason?: string;
  onUnsupportedAction?: (action: string) => void;
  onReady?: (instance: ReactFlowInstance) => void;
  interactionMode?: "select" | "pan";
  collaboration?: {
    connected: boolean;
    cursors: StudioCollaboratorCursor[];
    emitCursor: (update: StudioCursorUpdate) => boolean;
  };
  children?: ReactNode;
}) {
  const graph = useMemo(() => {
    const graph = normalizeStudioGraph(bootstrap);
    for (const node of graph.nodes) {
      if (node.model)
        node.model =
          findStudioModel(catalog?.models[node.raw.nodeType] ?? [], node.model)
            ?.label ?? studioDisplayName(node.model);
    }
    return graph;
  }, [bootstrap, catalog]);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [selectedEdgeIds, setSelectedEdgeIds] = useState<Set<string>>(
    new Set(),
  );
  const [nodes, setNodes] = useState<CoreNode[]>([]);
  const [geometryRevision, setGeometryRevision] = useState(0);
  const [contextNode, setContextNode] = useState<StudioGraphNode | null>(null);
  const [contextPosition, setContextPosition] = useState<StudioPosition>({
    x: 12,
    y: 12,
  });
  const canvas = useRef<HTMLDivElement>(null);
  const cloneInFlight = useRef(false);
  const pasteCount = useRef(0);
  const copiedNodes = useRef<Record<string, unknown>[]>([]);
  const nodesRef = useRef<CoreNode[]>([]);
  const graphRef = useRef(graph);
  graphRef.current = graph;
  const actions = useRef({
    onPositionCommit,
    onMutation,
    onInspect,
    onModelChange,
    onGenerate,
    onRefreshMedia,
    onError,
  });
  actions.current = {
    onPositionCommit,
    onMutation,
    onInspect,
    onModelChange,
    onGenerate,
    onRefreshMedia,
    onError,
  };
  const localPositions = useRef(
    new Map<string, LocalGeometry<StudioPosition>>(),
  );
  const localSizes = useRef(new Map<string, LocalGeometry<NodeSize>>());
  const mediaRatios = useRef(
    new Map<string, { identity: string; ratio: number }>(),
  );
  const geometryWrites = useRef(new Map<string, Promise<void | number>>());
  const activeDrags = useRef(new Set<string>());
  const activeResizes = useRef(new Set<string>());
  const resizeAvailable = Boolean(capabilities.resizeNode && onMutation);
  const modelChangeAvailable = Boolean(onModelChange);
  const generationAvailable = Boolean(onGenerate);
  const reportError = useCallback(
    (error: unknown) => actions.current.onError?.(error),
    [],
  );
  const refreshMedia = useCallback(
    (nodeId: string) =>
      actions.current.onRefreshMedia?.(nodeId) ?? Promise.resolve(undefined),
    [],
  );
  const inspectNode = useCallback(
    (node: StudioGraphNode) => actions.current.onInspect?.(node),
    [],
  );
  const changeModel = useCallback(
    async (nodeId: string, model: StudioModel) => {
      await actions.current.onModelChange?.(nodeId, model);
    },
    [],
  );
  const generateNode = useCallback(async (nodeId: string) => {
    await actions.current.onGenerate?.(nodeId);
  }, []);
  const openContext = useCallback(
    (node: StudioGraphNode, event: MouseEvent) => {
      event.preventDefault();
      const bounds = canvas.current?.getBoundingClientRect();
      if (bounds)
        setContextPosition({
          x: Math.max(
            8,
            Math.min(event.clientX - bounds.left, bounds.width - 216),
          ),
          y: Math.max(
            8,
            Math.min(event.clientY - bounds.top, bounds.height - 184),
          ),
        });
      setContextNode(node);
      setSelectedIds(new Set([node.id]));
      setSelectedEdgeIds(new Set());
      nodesRef.current = nodesRef.current.map((item) => ({
        ...item,
        selected: item.id === node.id,
      }));
      setNodes(nodesRef.current);
    },
    [],
  );

  const resolvedNodeSize = useCallback((node: StudioGraphNode) => {
    const size = nodeSize(node);
    const media = mediaRatios.current.get(node.id);
    return media &&
      node.mediaUrl &&
      media.identity === studioMediaIdentity(node.mediaUrl)
      ? { width: size.width, height: size.width / media.ratio }
      : size;
  }, []);

  const loadedMediaDimensions = useCallback(
    (node: StudioGraphNode, dimensions: NodeSize, source: string) => {
      if (
        !node.mediaUrl ||
        !["image", "infographic", "video"].includes(node.kind)
      )
        return;
      const identity = studioMediaIdentity(source);
      if (identity !== studioMediaIdentity(node.mediaUrl)) return;
      const ratio = dimensions.width / dimensions.height;
      const previous = mediaRatios.current.get(node.id);
      if (
        previous?.identity === identity &&
        Math.abs(previous.ratio - ratio) < 0.0001
      )
        return;
      mediaRatios.current.set(node.id, { identity, ratio });
      setGeometryRevision((revision) => revision + 1);
    },
    [],
  );

  const queueGeometryWrite = useCallback(
    (nodeId: string, work: () => Promise<void | number>) => {
      const previous = geometryWrites.current.get(nodeId) ?? Promise.resolve();
      // Resize metadata contains position too, so both gestures share one queue.
      const write = previous.catch(() => undefined).then(work);
      geometryWrites.current.set(nodeId, write);
      const clear = () => {
        if (geometryWrites.current.get(nodeId) === write)
          geometryWrites.current.delete(nodeId);
      };
      void write.then(clear, clear);
      return write;
    },
    [],
  );

  const rollbackGeometry = useCallback(
    (
      nodeId: string,
      position?: LocalGeometry<StudioPosition>,
      size?: LocalGeometry<NodeSize>,
    ) => {
      const rollbackPosition =
        position && localPositions.current.get(nodeId) === position;
      const rollbackSize = size && localSizes.current.get(nodeId) === size;
      if (!rollbackPosition && !rollbackSize) return;
      if (rollbackPosition) localPositions.current.delete(nodeId);
      if (rollbackSize) localSizes.current.delete(nodeId);
      const serverNode = graphRef.current.nodes.find(
        (node) => node.id === nodeId,
      );
      if (!serverNode) return;
      nodesRef.current = nodesRef.current.map((node) =>
        node.id === nodeId
          ? {
              ...node,
              ...(rollbackPosition ? { position: serverNode.position } : {}),
              ...(rollbackSize
                ? {
                    ...resolvedNodeSize(serverNode),
                    style: { ...node.style, ...resolvedNodeSize(serverNode) },
                  }
                : {}),
            }
          : node,
      );
      setNodes(nodesRef.current);
    },
    [resolvedNodeSize],
  );

  const resizeNode = useCallback(
    (node: StudioGraphNode, geometry: ResizeGeometry) => {
      activeResizes.current.add(node.id);
      localPositions.current.set(node.id, {
        value: { x: geometry.x, y: geometry.y },
        saved: false,
      });
      localSizes.current.set(node.id, {
        value: { width: geometry.width, height: geometry.height },
        saved: false,
      });
      // React Flow applies the same geometry once through onNodesChange.
    },
    [],
  );

  const commitResize = useCallback(
    (node: StudioGraphNode, geometry: ResizeGeometry) => {
      activeResizes.current.delete(node.id);
      const position = localPositions.current.get(node.id);
      const size = localSizes.current.get(node.id);
      const write = queueGeometryWrite(node.id, () => {
        const latest =
          graphRef.current.nodes.find((item) => item.id === node.id) ?? node;
        const mutate = actions.current.onMutation;
        if (!mutate)
          return Promise.reject(new Error("Resizing is unavailable."));
        return mutate({
          operation: "updateNode",
          payload: {
            nodeId: node.id,
            version: latest.raw.version,
            metadata: {
              width: geometry.width,
              height: geometry.height,
              position: { x: geometry.x, y: geometry.y },
            },
          },
        });
      });
      void write
        .then((acknowledgedVersion) => {
          for (const local of [position, size]) {
            if (!local) continue;
            local.saved = true;
            if (typeof acknowledgedVersion === "number")
              local.acknowledgedVersion = acknowledgedVersion;
          }
          setGeometryRevision((revision) => revision + 1);
        })
        .catch((error: unknown) => {
          rollbackGeometry(node.id, position, size);
          reportError(error);
        });
    },
    [queueGeometryWrite, reportError, rollbackGeometry],
  );

  useEffect(() => {
    const previous = new Map(nodesRef.current.map((node) => [node.id, node]));
    const liveIds = new Set(graph.nodes.map((node) => node.id));
    for (const local of [localPositions.current, localSizes.current]) {
      for (const nodeId of local.keys())
        if (!liveIds.has(nodeId)) local.delete(nodeId);
    }
    for (const active of [activeDrags.current, activeResizes.current]) {
      for (const nodeId of active)
        if (!liveIds.has(nodeId)) active.delete(nodeId);
    }
    for (const nodeId of mediaRatios.current.keys())
      if (!liveIds.has(nodeId)) mediaRatios.current.delete(nodeId);
    nodesRef.current = graph.nodes.map((incoming) => {
      const existing = previous.get(incoming.id);
      // Position writes advance updatedAt without changing the metadata version.
      const previousRaw = existing?.data.graphNode.raw;
      const stale =
        previousRaw &&
        (previousRaw.version > incoming.raw.version ||
          (previousRaw.version === incoming.raw.version &&
            Date.parse(previousRaw.updatedAt) >
              Date.parse(incoming.raw.updatedAt)));
      const graphNode = stale && existing ? existing.data.graphNode : incoming;
      const localPosition = localPositions.current.get(graphNode.id);
      const localSize = localSizes.current.get(graphNode.id);
      const active =
        activeDrags.current.has(graphNode.id) ||
        activeResizes.current.has(graphNode.id);
      if (
        !active &&
        localPosition?.saved &&
        (samePosition(localPosition.value, graphNode.position) ||
          (localPosition.acknowledgedVersion !== undefined &&
            graphNode.raw.version >= localPosition.acknowledgedVersion))
      )
        localPositions.current.delete(graphNode.id);
      if (
        !active &&
        localSize?.saved &&
        (sameSize(localSize.value, nodeSize(graphNode)) ||
          (localSize.acknowledgedVersion !== undefined &&
            graphNode.raw.version >= localSize.acknowledgedVersion))
      )
        localSizes.current.delete(graphNode.id);
      const size =
        localSizes.current.get(graphNode.id)?.value ??
        resolvedNodeSize(graphNode);
      const visualMedia =
        Boolean(graphNode.mediaUrl) &&
        ["image", "infographic", "video"].includes(graphNode.kind);
      const media = mediaRatios.current.get(graphNode.id);
      const ratioReady =
        media &&
        graphNode.mediaUrl &&
        media.identity === studioMediaIdentity(graphNode.mediaUrl);
      return {
        ...existing,
        id: graphNode.id,
        type: "studio",
        position:
          localPositions.current.get(graphNode.id)?.value ?? graphNode.position,
        // Studio positions are absolute even for group members.
        parentId: undefined,
        draggable:
          capabilities.updateNodePosition &&
          !graphNode.readOnly &&
          interactionMode === "select",
        selectable: interactionMode === "select",
        connectable:
          interactionMode === "select" &&
          capabilities.connectEdge &&
          !graphNode.readOnly,
        ...size,
        style: { ...existing?.style, ...size },
        data: {
          graphNode,
          onError: reportError,
          onRefreshMedia: refreshMedia,
          canResize:
            resizeAvailable &&
            interactionMode === "select" &&
            !graphNode.readOnly &&
            (!visualMedia || Boolean(ratioReady)),
          onInspect: inspectNode,
          models: catalog?.models[graphNode.raw.nodeType] ?? [],
          onModelChange:
            modelChangeAvailable &&
            !generatingIds?.has(graphNode.id) &&
            !restoringIds?.has(graphNode.id)
              ? changeModel
              : undefined,
          onGenerate: generationAvailable ? generateNode : undefined,
          generationPending: generatingIds?.has(graphNode.id) ?? false,
          generationDisabledReason: restoringIds?.has(graphNode.id)
            ? "Restoring a version…"
            : generationDisabledReason,
          onContext: openContext,
          onResize: resizeNode,
          onResizeCommit: commitResize,
          onMediaDimensions: loadedMediaDimensions,
          groupMemberCount: graph.nodes.filter(
            (node) => node.parentId === graphNode.id,
          ).length,
        },
      };
    });
    setNodes(nodesRef.current);
    setSelectedIds(
      (current) =>
        new Set(
          [...current].filter((id) =>
            graph.nodes.some((node) => node.id === id),
          ),
        ),
    );
  }, [
    resizeAvailable,
    capabilities.updateNodePosition,
    capabilities.connectEdge,
    graph.nodes,
    geometryRevision,
    interactionMode,
    commitResize,
    inspectNode,
    catalog,
    modelChangeAvailable,
    changeModel,
    generateNode,
    generationAvailable,
    generatingIds,
    restoringIds,
    generationDisabledReason,
    openContext,
    refreshMedia,
    reportError,
    resizeNode,
    resolvedNodeSize,
    loadedMediaDimensions,
  ]);

  const edges = useMemo<Edge[]>(
    () =>
      graph.edges.map((edge) => ({
        id: edge.id,
        source: edge.source,
        target: edge.target,
        sourceHandle: edge.sourceHandle,
        targetHandle: edge.targetHandle,
        selected: selectedEdgeIds.has(edge.id),
        type: "default",
        style: { stroke: "var(--studio-edge)", strokeWidth: 1.6 },
        interactionWidth: 24,
      })),
    [graph.edges, selectedEdgeIds],
  );

  const commitPosition = useCallback(
    (nodeId: string) => {
      const position = localPositions.current.get(nodeId);
      if (!position || activeResizes.current.has(nodeId)) return;
      void queueGeometryWrite(nodeId, () =>
        actions.current.onPositionCommit(nodeId, position.value, true),
      )
        .then(() => {
          // The caller applies the position acknowledgement before resolving,
          // including any newer collaborator position it has already received.
          if (
            localPositions.current.get(nodeId) === position &&
            !activeDrags.current.has(nodeId) &&
            !activeResizes.current.has(nodeId)
          )
            localPositions.current.delete(nodeId);
          setGeometryRevision((revision) => revision + 1);
        })
        .catch((error: unknown) => {
          rollbackGeometry(nodeId, position);
          reportError(error);
        });
    },
    [queueGeometryWrite, reportError, rollbackGeometry],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      const previous = new Map(nodesRef.current.map((node) => [node.id, node]));
      const graphNodes = new Map(
        graphRef.current.nodes.map((node) => [node.id, node]),
      );
      const positionChanges = new Map(
        changes.flatMap((change) =>
          change.type === "position" && change.position
            ? [[change.id, change] as const]
            : [],
        ),
      );
      const changedGroups = new Map(
        [...positionChanges].filter(
          ([nodeId]) =>
            graphNodes.get(nodeId)?.kind === "node_group" &&
            !activeResizes.current.has(nodeId),
        ),
      );
      const groupFor = (nodeId: string) => {
        let parentId = graphNodes.get(nodeId)?.parentId;
        const visited = new Set<string>();
        while (parentId && !visited.has(parentId)) {
          visited.add(parentId);
          const change = changedGroups.get(parentId);
          if (change) return change;
          parentId = graphNodes.get(parentId)?.parentId;
        }
      };
      const next = applyNodeChanges(changes, nodesRef.current) as CoreNode[];
      const finished = new Set<string>();
      nodesRef.current = next.map((node) => {
        const explicit = positionChanges.get(node.id);
        const group = groupFor(node.id);
        const change = explicit ?? group;
        if (!change?.position) return node;
        let position = node.position;
        if (!explicit && group?.position) {
          const previousGroup = previous.get(group.id);
          if (previousGroup)
            position = {
              x: node.position.x + group.position.x - previousGroup.position.x,
              y: node.position.y + group.position.y - previousGroup.position.y,
            };
        }
        const prior = localPositions.current.get(node.id);
        if (!prior || !samePosition(prior.value, position))
          localPositions.current.set(node.id, {
            value: position,
            saved: false,
          });
        if (change.dragging) activeDrags.current.add(node.id);
        else if (!activeResizes.current.has(node.id)) {
          activeDrags.current.delete(node.id);
          finished.add(node.id);
        }
        return position === node.position ? node : { ...node, position };
      });
      setNodes(nodesRef.current);
      for (const change of changes) {
        // Resize positions are persisted together with dimensions at release.
        if (change.type === "dimensions" && change.resizing !== undefined)
          finished.delete(change.id);
      }
      const selection = changes.filter((change) => change.type === "select");
      if (selection.length)
        setSelectedIds((current) => {
          const next = new Set(current);
          for (const change of selection) {
            if (change.type === "select") {
              if (change.selected) next.add(change.id);
              else next.delete(change.id);
            }
          }
          return next;
        });
      if (capabilities.updateNodePosition)
        for (const nodeId of finished) commitPosition(nodeId);
    },
    [capabilities.updateNodePosition, commitPosition],
  );

  const clearSelection = useCallback(() => {
    setContextNode(null);
    setSelectedIds(new Set());
    setSelectedEdgeIds(new Set());
    nodesRef.current = nodesRef.current.map((node) =>
      node.selected ? { ...node, selected: false } : node,
    );
    setNodes(nodesRef.current);
  }, []);

  const clipboardNode = useCallback((node: StudioGraphNode) => {
    const rendered = nodesRef.current.find((item) => item.id === node.id);
    return {
      nodeType: node.raw.nodeType,
      label: node.label,
      metadata: {
        ...parseNodeMetadata(node.raw.metadataJson),
        position: rendered?.position ?? node.position,
        width: rendered?.width ?? node.width ?? 300,
        height: rendered?.height ?? node.height ?? 230,
      },
    };
  }, []);

  const cloneNodes = useCallback(
    async (copies: Array<Record<string, unknown>>) => {
      if (
        !capabilities.createNode ||
        !actions.current.onMutation ||
        cloneInFlight.current
      )
        return;
      const supported = [
        "text",
        "comment",
        "node_group",
        "group",
        "nodeGroup",
        "image",
        "infographic",
        "video",
        "audio",
        "voice",
      ];
      if (
        !copies.length ||
        copies.some(
          (node) =>
            typeof node.nodeType !== "string" ||
            !supported.includes(node.nodeType),
        )
      )
        throw new Error(
          "The clipboard does not contain editable Studio nodes.",
        );
      cloneInFlight.current = true;
      const offset = 40 + pasteCount.current++ * 20;
      try {
        for (const node of copies) {
          const metadata =
            typeof node.metadataJson === "string"
              ? parseNodeMetadata(node.metadataJson)
              : node.metadata &&
                  typeof node.metadata === "object" &&
                  !Array.isArray(node.metadata)
                ? (node.metadata as Record<string, unknown>)
                : {};
          const position =
            metadata.position && typeof metadata.position === "object"
              ? (metadata.position as Record<string, unknown>)
              : {};
          await actions.current.onMutation({
            operation: "createNode",
            payload: {
              nodeType: node.nodeType,
              label:
                typeof node.label === "string" ? `${node.label} copy` : "Copy",
              metadata: {
                ...metadata,
                position: {
                  x:
                    (typeof position.x === "number" &&
                    Number.isFinite(position.x)
                      ? position.x
                      : 0) + offset,
                  y:
                    (typeof position.y === "number" &&
                    Number.isFinite(position.y)
                      ? position.y
                      : 0) + offset,
                },
              },
            },
          });
        }
      } finally {
        cloneInFlight.current = false;
      }
    },
    [capabilities.createNode],
  );

  useEffect(() => {
    const onKeyDown = async (event: KeyboardEvent) => {
      const root = canvas.current;
      if (
        !root?.contains(document.activeElement) ||
        isEditableTextTarget(event.target)
      )
        return;
      if (
        event.target instanceof Element &&
        event.target.closest(".studio-player.is-expanded")
      )
        return;
      if (
        event.key === "Enter" &&
        event.target instanceof HTMLElement &&
        event.target.matches(".react-flow__node")
      ) {
        const nodeId = event.target.dataset.id;
        const node = graphRef.current.nodes.find((item) => item.id === nodeId);
        if (node) {
          event.preventDefault();
          inspectNode(node);
          return;
        }
      }
      if (event.key === "Escape") {
        event.preventDefault();
        clearSelection();
        return;
      }
      if (event.key === "Delete" || event.key === "Backspace") {
        if (!selectedIds.size && !selectedEdgeIds.size) return;
        event.preventDefault();
        if (selectedIds.size) {
          if (!capabilities.deleteNode) onUnsupportedAction?.("deleteNode");
          else
            void Promise.all(
              graphRef.current.nodes
                .filter((node) => selectedIds.has(node.id) && !node.readOnly)
                .map((node) =>
                  onMutation?.({
                    operation: "deleteNode",
                    payload: { nodeId: node.id },
                  }),
                ),
            ).catch((error: unknown) => onError?.(error));
        }
        if (selectedEdgeIds.size) {
          if (!capabilities.disconnectEdge) onUnsupportedAction?.("deleteEdge");
          else
            void Promise.all(
              [...selectedEdgeIds].map((edgeId) =>
                onMutation?.({ operation: "deleteEdge", payload: { edgeId } }),
              ),
            ).catch((error: unknown) => onError?.(error));
          setSelectedEdgeIds(new Set());
        }
      }
      if (
        (event.metaKey || event.ctrlKey) &&
        event.key.toLowerCase() === "c" &&
        selectedIds.size
      ) {
        const selected = graphRef.current.nodes.filter(
          (node) => selectedIds.has(node.id) && !node.readOnly,
        );
        if (!selected.length) return;
        event.preventDefault();
        pasteCount.current = 0;
        copiedNodes.current = selected.map(clipboardNode);
        void navigator.clipboard
          .writeText(
            JSON.stringify({
              type: "zenithStudioNodes",
              nodes: copiedNodes.current,
            }),
          )
          .catch(() => {
            // Desktop may deny system clipboard access; keep canvas copies usable.
          });
      }
      if (
        (event.metaKey || event.ctrlKey) &&
        event.key.toLowerCase() === "v" &&
        capabilities.createNode &&
        onMutation
      ) {
        event.preventDefault();
        try {
          let text: string;
          try {
            text = await navigator.clipboard.readText();
          } catch (error) {
            if (!copiedNodes.current.length) throw error;
            await cloneNodes(copiedNodes.current);
            return;
          }
          const raw: unknown = JSON.parse(text);
          const values = Array.isArray(raw)
            ? raw
            : raw &&
                typeof raw === "object" &&
                "type" in raw &&
                raw.type === "zenithStudioNodes" &&
                "nodes" in raw &&
                Array.isArray(raw.nodes)
              ? raw.nodes
              : [];
          const copies = values.filter(
            (value): value is Record<string, unknown> =>
              value && typeof value === "object" && !Array.isArray(value),
          );
          await cloneNodes(copies);
        } catch (error: unknown) {
          reportError(error);
        }
      }
    };
    const root = canvas.current;
    root?.addEventListener("keydown", onKeyDown);
    return () => root?.removeEventListener("keydown", onKeyDown);
  }, [
    capabilities.createNode,
    capabilities.deleteNode,
    onMutation,
    onError,
    onUnsupportedAction,
    selectedIds,
    selectedEdgeIds,
    clearSelection,
    clipboardNode,
    cloneNodes,
    inspectNode,
    reportError,
  ]);

  const onConnect = useCallback(
    (connection: Connection) => {
      if (
        !capabilities.connectEdge ||
        !onMutation ||
        !connection.source ||
        !connection.target
      ) {
        onUnsupportedAction?.("connectEdge");
        return;
      }
      void onMutation({
        operation: "createEdge",
        payload: {
          sourceId: connection.source,
          targetId: connection.target,
          sourceHandle: connection.sourceHandle ?? "out",
          targetHandle: connection.targetHandle ?? "in",
        },
      }).catch((error: unknown) => onError?.(error));
    },
    [capabilities.connectEdge, onMutation, onUnsupportedAction, onError],
  );

  return (
    <div
      ref={canvas}
      className="studio-graph"
      tabIndex={0}
      role="region"
      aria-label="Studio canvas"
      onClick={() => setContextNode(null)}
      onPointerDownCapture={(event) => {
        const target = event.target;
        if (
          !(target instanceof HTMLElement) ||
          target.closest(
            "button,input,textarea,select,[contenteditable='true'],[role='menu'],.studio-player.is-expanded",
          )
        )
          return;
        const focus =
          target.closest<HTMLElement>(".react-flow__node") ?? canvas.current;
        focus?.focus({ preventScroll: true });
      }}
    >
      <ReactFlow<Node>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onConnect={onConnect}
        onNodeClick={(event, node) => {
          if (event.button !== 0 || interactionMode !== "select") return;
          const selected = graphRef.current.nodes.find(
            (item) => item.id === node.id,
          );
          if (selected) inspectNode(selected);
        }}
        deleteKeyCode={null}
        onEdgesDelete={(deleted) => {
          if (!capabilities.disconnectEdge || !onMutation) {
            onUnsupportedAction?.("deleteEdge");
            return;
          }
          void Promise.all(
            deleted.map((edge) =>
              onMutation({
                operation: "deleteEdge",
                payload: { edgeId: edge.id },
              }),
            ),
          ).catch((error: unknown) => onError?.(error));
        }}
        onEdgeClick={(_, edge) => {
          setSelectedEdgeIds(new Set([edge.id]));
        }}
        onPaneClick={clearSelection}
        connectionLineType={ConnectionLineType.Bezier}
        connectionLineStyle={{
          stroke: "var(--studio-edge-active)",
          strokeWidth: 2.5,
        }}
        defaultEdgeOptions={{
          type: "default",
          style: { stroke: "var(--studio-edge)", strokeWidth: 1.6 },
        }}
        nodeDragThreshold={0}
        selectionOnDrag={interactionMode === "select"}
        panOnDrag={interactionMode === "pan" ? true : [1, 2]}
        panOnScroll
        panOnScrollSpeed={0.8}
        zoomOnScroll={false}
        zoomOnPinch
        zoomOnDoubleClick={false}
        preventScrolling={false}
        minZoom={0.2}
        maxZoom={2.5}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="var(--studio-grid)" gap={28} size={1.5} />
        <MiniMap
          pannable
          zoomable
          style={{ width: 144, height: 96 }}
          nodeColor="var(--workspace-divider-strong)"
          maskColor="color-mix(in srgb, var(--workspace-hover-bg) 35%, transparent)"
          maskStrokeColor="var(--workspace-native-text-muted)"
          maskStrokeWidth={1.5}
        />
        <StudioGraphControls />
        <StudioGraphViewport onReady={onReady} />
        {collaboration ? <StudioCollaborators {...collaboration} /> : null}
        {children}
      </ReactFlow>
      {contextNode ? (
        <div
          className="studio-graph__context"
          role="menu"
          aria-label={`Actions for ${contextNode.label}`}
          style={{ left: contextPosition.x, top: contextPosition.y }}
          onClick={(event) => event.stopPropagation()}
        >
          <strong>{contextNode.label}</strong>
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              inspectNode(contextNode);
              setContextNode(null);
            }}
          >
            <PencilLine size={15} aria-hidden />{" "}
            {contextNode.readOnly ? "View details" : "Edit details"}
          </button>
          <button
            type="button"
            role="menuitem"
            disabled={!capabilities.createNode || contextNode.readOnly}
            onClick={() => {
              const latest =
                graphRef.current.nodes.find(
                  (node) => node.id === contextNode.id,
                ) ?? contextNode;
              void cloneNodes([clipboardNode(latest)]).catch(reportError);
              setContextNode(null);
            }}
          >
            <Copy size={15} aria-hidden /> Duplicate
          </button>
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              if (capabilities.deleteNode && onMutation)
                void onMutation({
                  operation: "deleteNode",
                  payload: { nodeId: contextNode.id },
                }).catch(reportError);
              else onUnsupportedAction?.("deleteNode");
              setContextNode(null);
            }}
            disabled={!capabilities.deleteNode || contextNode.readOnly}
          >
            <Trash2 size={15} aria-hidden /> Delete
          </button>
        </div>
      ) : null}
    </div>
  );
}
