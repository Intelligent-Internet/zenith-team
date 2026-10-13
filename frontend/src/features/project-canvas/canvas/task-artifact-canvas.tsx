import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  BackgroundVariant,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react";
import {
  ArrowUp,
  ArrowUpRight,
  ChevronLeft,
  FileText,
} from "@/shared/ui/icons";
import { canvasNodeTypes, type CanvasNode } from "./canvas-nodes";
import type { ArtifactCollectionProps } from "./artifact-collection";
import type { CanvasItem } from "../api/canvas-api";
import { GraphControls, type ViewCamera } from "../documents/graph-controls";

type TaskArtifactCanvasProps = ArtifactCollectionProps & {
  title: string;
  label: string;
  camera: ViewCamera;
  onBack: () => void;
  onTask?: () => void;
  onAdd?: (initial: "document" | "upload") => void;
  canEditDocuments: boolean;
  canManageFiles: boolean;
};
export function TaskArtifactCanvas(props: TaskArtifactCanvasProps) {
  return (
    <ReactFlowProvider>
      <TaskScene {...props} />
    </ReactFlowProvider>
  );
}
function TaskScene({
  artifacts,
  scope,
  title,
  label,
  camera,
  onOpen,
  onBack,
  onTask,
  onAdd,
  canEditDocuments,
  canManageFiles,
}: TaskArtifactCanvasProps) {
  const canCreate = !!onAdd && canEditDocuments;
  const canUpload = !!onAdd && canManageFiles;
  const flow = useReactFlow<CanvasNode>();
  const fitted = useRef(!!camera.current);
  const awaitingFirstArtifact = useRef(!artifacts.length);
  const [selected, setSelected] = useState<string>();
  const [comments, setComments] = useState<string | null>(null);
  // IDs reserve their own slots as live versions and comments change. New
  // artifacts append; an update never shuffles the rest of the task canvas.
  const [order, setOrder] = useState(() =>
    artifacts.map((row) => row.id).sort(),
  );
  const known = new Set(order);
  const additions = artifacts
    .filter((row) => !known.has(row.id))
    .map((row) => row.id)
    .sort();
  if (additions.length) setOrder([...order, ...additions]);
  const open = useCallback(
    (item: CanvasItem, threadId?: string) => {
      const artifact = artifacts.find((row) => row.id === item.artifactId);
      if (artifact) onOpen(artifact, undefined, threadId);
    },
    [artifacts, onOpen],
  );
  const onComments = useCallback(
    (id: string | null, opened: boolean) =>
      setComments((current) =>
        opened ? id : current === id || id === null ? null : current,
      ),
    [],
  );
  const nodes = useMemo<CanvasNode[]>(() => {
    const slots = new Map(order.map((id, index) => [id, index]));
    return [...artifacts]
      .sort(
        (a, b) =>
          (slots.get(a.id) ?? slots.size) - (slots.get(b.id) ?? slots.size),
      )
      .map((artifact) => {
        const slot = slots.get(artifact.id) ?? slots.size;
        const id = `task-artifact:${artifact.id}`;
        const item: CanvasItem = {
          id,
          type: "artifact",
          artifactId: artifact.id,
          version: artifact.latestVersion,
          pinnedVersionId: artifact.latestVersionId,
          versionPolicy: "pinned",
          parentFrameId: null,
          title: "",
          x: 40 + (slot % 4) * 364,
          y: 40 + Math.floor(slot / 4) * 324,
          width: 320,
          height: 280,
          stackOrder: slot,
          representation: "preview",
          viewConfig: {},
          geometryRevision: 0,
          contentRevision: 0,
        };
        return {
          id,
          type: "artifact",
          position: { x: item.x, y: item.y },
          width: item.width,
          height: item.height,
          selected: selected === id,
          data: {
            scope,
            item,
            artifact,
            canEdit: false,
            commentsOpen: comments === id,
            onCommentsOpenChange: onComments,
            onOpen: open,
            onInteractionStart: () => {},
            onResize: () => {},
          },
        };
      });
  }, [
    artifacts,
    order,
    scope.companyId,
    scope.projectId,
    selected,
    comments,
    onComments,
    open,
  ]);
  useEffect(() => {
    if (
      (fitted.current && !awaitingFirstArtifact.current) ||
      !nodes.length ||
      !flow.viewportInitialized
    )
      return;
    const frame = requestAnimationFrame(() => {
      fitted.current = true;
      awaitingFirstArtifact.current = false;
      // Open large collections at a readable scale without mounting every preview.
      void flow.fitView({
        nodes: nodes.slice(0, 8),
        padding: 0.18,
        maxZoom: 0.9,
      });
    });
    return () => cancelAnimationFrame(frame);
  }, [nodes, flow]);
  return (
    <div className="pc-task-canvas">
      <header className="pc-task-canvas-heading">
        <button className="pc-button" onClick={onBack}>
          <ChevronLeft size={16} />
          Project plan
        </button>
        <span className="pc-task-canvas-separator">/</span>
        <div>
          <span className="pc-eyebrow">{label}</span>
          <h3>{title}</h3>
          <p>
            {artifacts.length}{" "}
            {artifacts.length === 1 ? "artifact" : "artifacts"} · Auto arranged
            {artifacts.length ? " · Click a card for discussion" : ""}
          </p>
        </div>
        {onTask && (
          <button className="pc-button" onClick={onTask}>
            Open task
            <ArrowUpRight size={15} />
          </button>
        )}
      </header>
      <div
        className="pc-graph-surface"
        onKeyDownCapture={(event) => {
          if (
            event.key !== "Enter" ||
            (event.target as HTMLElement).closest(
              "button,input,textarea,select",
            )
          )
            return;
          const node = nodes.find((row) => row.id === selected);
          if (node) {
            event.preventDefault();
            open(node.data.item);
          }
        }}
      >
        <ReactFlow<CanvasNode>
          nodes={nodes}
          nodeTypes={canvasNodeTypes}
          nodesDraggable={false}
          nodesConnectable={false}
          deleteKeyCode={null}
          minZoom={0.08}
          maxZoom={2}
          onlyRenderVisibleElements
          panOnScroll
          zoomOnScroll={false}
          zoomOnDoubleClick={false}
          defaultViewport={camera.current}
          onMoveStart={(event) => {
            if (event) fitted.current = true;
          }}
          onMoveEnd={(_, viewport) => {
            camera.current = viewport;
          }}
          onNodeClick={(_, node) => setSelected(node.id)}
          onNodesChange={(changes) => {
            for (const change of changes)
              if (change.type === "select" && change.selected)
                setSelected(change.id);
          }}
          onNodeDoubleClick={(_, node) => open(node.data.item)}
          onPaneClick={() => {
            setComments(null);
            setSelected(undefined);
          }}
          proOptions={{ hideAttribution: true }}
        >
          <Background
            variant={BackgroundVariant.Dots}
            gap={24}
            size={1}
            color="var(--workspace-divider-strong)"
          />
        </ReactFlow>
        {onAdd && (canCreate || canUpload) && (
          <div
            className="pc-board-tools"
            role="toolbar"
            aria-label="Add task artifacts"
          >
            {canCreate && (
              <button className="pc-button" onClick={() => onAdd("document")}>
                <FileText size={17} />
                Create document
              </button>
            )}
            {canUpload && (
              <button className="pc-button" onClick={() => onAdd("upload")}>
                <ArrowUp size={17} />
                Upload artifact
              </button>
            )}
          </div>
        )}
        <GraphControls />
        <div className="pc-navigation-hint">
          {artifacts.length
            ? "Scroll to pan · Pinch to zoom · Double-click an artifact to review"
            : canCreate && canUpload
              ? "Create or upload an artifact to start · Scroll to pan · Pinch to zoom"
              : canCreate
                ? "Create an artifact to start · Scroll to pan · Pinch to zoom"
                : canUpload
                  ? "Upload an artifact to start · Scroll to pan · Pinch to zoom"
                  : "No artifacts yet · Scroll to pan · Pinch to zoom"}
        </div>
      </div>
    </div>
  );
}
