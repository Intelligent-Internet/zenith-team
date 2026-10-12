import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  BackgroundVariant,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import {
  AlertCircle,
  Focus,
  Map as MapIcon,
  Maximize,
  Minus,
  Plus,
} from "@/shared/ui/icons";
import type { TaskDependencyGraphDto, TaskDto } from "@/shared/api/types";
import type { LabelTask } from "@/shared/lib/task-label";
import type { CompanyPlanTaskLane } from "@/features/company-plan/model/company-plan-view-model";
import {
  createWorkDependencyCanvasModel,
  WORK_DEPENDENCY_CANVAS_NODE_HEIGHT,
  WORK_DEPENDENCY_CANVAS_NODE_WIDTH,
  type WorkDependencyCanvasTask,
} from "@/features/company-plan/model/work-dependency-canvas-model";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";
import { TaskMemberAvatar } from "@/features/company-plan/components/task-assignee";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import "@xyflow/react/dist/style.css";
import "@/features/company-plan/styles/work-dependency-canvas.css";

export type WorkDependencyCanvasProps = {
  tasks: readonly TaskDto[];
  graph?: TaskDependencyGraphDto;
  labelTask?: LabelTask;
  scopeTaskIds: ReadonlySet<string>;
  matchingTaskIds: ReadonlySet<string>;
  hasFilters: boolean;
  selectedTaskId?: string;
  lanes: CompanyPlanTaskLane[];
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  onSelectTask: (taskId: string | undefined) => void;
};

type DependencyNodeData = {
  item: WorkDependencyCanvasTask;
  agent?: CompanyPlanTaskLane["agent"];
  contextLabel?: string;
  dimmed: boolean;
  onSelect: (taskId: string | undefined) => void;
  onFocusTask: (taskId: string) => void;
};

type DependencyNode = Node<DependencyNodeData, "workTask">;
type DependencyFlow = ReactFlowInstance<DependencyNode, Edge>;

const dependencyNodeTypes = { workTask: DependencyTaskNode };
const fitOptions = { padding: 0.18, maxZoom: 1, minZoom: 0.08 };
const readableZoom = 0.75;

export function WorkDependencyCanvas({
  tasks,
  graph,
  labelTask,
  scopeTaskIds,
  matchingTaskIds,
  hasFilters,
  selectedTaskId,
  lanes,
  isLoading,
  isError,
  onRetry,
  onSelectTask,
}: WorkDependencyCanvasProps) {
  const [focusSelected, setFocusSelected] = useState(false);
  const [showMap, setShowMap] = useState<boolean | null>(null);
  const [flow, setFlow] = useState<DependencyFlow | null>(null);
  const [zoom, setZoom] = useState(1);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const selectedTaskRef = useRef(selectedTaskId);
  selectedTaskRef.current = selectedTaskId;

  const model = useMemo(
    () =>
      graph && !isError && !isLoading
        ? createWorkDependencyCanvasModel({
            tasks,
            graph,
            labelTask,
            scopeTaskIds,
            matchingTaskIds,
            hasFilters,
            selectedTaskId,
            focusSelected,
          })
        : null,
    [
      tasks,
      graph,
      labelTask,
      scopeTaskIds,
      matchingTaskIds,
      hasFilters,
      selectedTaskId,
      focusSelected,
      isError,
      isLoading,
    ],
  );
  const laneAgentByExecutorId = useMemo(() => {
    const result = new Map<string, CompanyPlanTaskLane["agent"]>();
    for (const lane of lanes) {
      result.set(lane.agent.id, lane.agent);
    }
    return result;
  }, [lanes]);
  const selectionIsVisible = Boolean(
    model?.nodes.some((node) => node.id === selectedTaskId),
  );

  const keepTaskVisible = useCallback(
    (taskId: string) => {
      if (!flow || !surfaceRef.current) return;
      const node = flow.getNode(taskId);
      if (!node) return;
      const { x, y, zoom } = flow.getViewport();
      const { clientWidth, clientHeight } = surfaceRef.current;
      const left = x + node.position.x * zoom;
      const top = y + node.position.y * zoom;
      const right = left + WORK_DEPENDENCY_CANVAS_NODE_WIDTH * zoom;
      const bottom = top + WORK_DEPENDENCY_CANVAS_NODE_HEIGHT * zoom;
      if (
        zoom < readableZoom ||
        left < 24 ||
        right > clientWidth - 24 ||
        top < 24 ||
        bottom > clientHeight - 24
      ) {
        void flow.setCenter(
          node.position.x + WORK_DEPENDENCY_CANVAS_NODE_WIDTH / 2,
          node.position.y + WORK_DEPENDENCY_CANVAS_NODE_HEIGHT / 2,
          { zoom: Math.max(zoom, readableZoom) },
        );
      }
    },
    [flow],
  );

  const nodes = useMemo<DependencyNode[]>(
    () =>
      (model?.nodes ?? []).map((item) => ({
        id: item.id,
        type: "workTask",
        position: item.position,
        width: WORK_DEPENDENCY_CANVAS_NODE_WIDTH,
        height: WORK_DEPENDENCY_CANVAS_NODE_HEIGHT,
        selected: item.id === selectedTaskId,
        draggable: false,
        selectable: false,
        connectable: false,
        deletable: false,
        focusable: false,
        data: {
          item,
          agent: item.task
            ? laneAgentByExecutorId.get(item.task.executedBy.id)
            : undefined,
          contextLabel: item.isContext ? "Related task" : undefined,
          dimmed: selectionIsVisible && !item.isHighlighted,
          onSelect: onSelectTask,
          onFocusTask: keepTaskVisible,
        },
      })),
    [
      model,
      selectedTaskId,
      selectionIsVisible,
      laneAgentByExecutorId,
      scopeTaskIds,
      onSelectTask,
      keepTaskVisible,
    ],
  );
  const edges = useMemo<Edge[]>(() => {
    const items = model?.nodes ?? [];
    const positions = new Map(items.map((item) => [item.id, item.position]));
    return (model?.edges ?? []).map((edge) => {
      const source = positions.get(edge.source);
      const target = positions.get(edge.target);
      // Route past intervening cards without covering their own dependency links.
      const passesCard =
        source &&
        target &&
        source.y === target.y &&
        items.some(
          (item) =>
            item.position.y === source.y &&
            item.position.x > source.x &&
            item.position.x < target.x,
        );
      const color = edge.resolved
        ? "var(--workspace-native-success, #38765a)"
        : "var(--workspace-native-text-muted, #82758e)";
      return {
        ...edge,
        type: "smoothstep",
        sourceHandle: passesCard ? "out-bottom" : "out",
        targetHandle: passesCard ? "in-bottom" : "in",
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color,
          width: 18,
          height: 18,
        },
        style: {
          stroke: color,
          strokeWidth: edge.highlighted ? 2.5 : 1.5,
          strokeDasharray: edge.resolved ? undefined : "5 4",
          opacity: selectionIsVisible && !edge.highlighted ? 0.18 : 0.85,
        },
        selectable: false,
        deletable: false,
        focusable: false,
        reconnectable: false,
        ariaLabel: edge.resolved
          ? "Prerequisite completed"
          : "Prerequisite not completed",
      };
    });
  }, [model, selectionIsVisible]);

  // Data refreshes and selection preserve the viewport. Only a scope change refits it.
  const scopeKey = JSON.stringify([
    [...scopeTaskIds].sort(),
    hasFilters ? [...matchingTaskIds].sort() : null,
    focusSelected ? selectedTaskId : null,
  ]);
  useEffect(() => {
    if (!flow) return;
    const frame = window.requestAnimationFrame(() => {
      void flow.fitView(fitOptions);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [flow, scopeKey]);

  useEffect(() => {
    if (!selectedTaskId) setFocusSelected(false);
    else keepTaskVisible(selectedTaskId);
  }, [selectedTaskId, keepTaskVisible]);

  useEffect(() => {
    const surface = surfaceRef.current;
    if (!surface || !flow) return;
    let width = surface.clientWidth;
    let height = surface.clientHeight;
    const observer = new ResizeObserver(() => {
      const nextWidth = surface.clientWidth;
      const nextHeight = surface.clientHeight;
      // The initial notification must not replace the full graph fit with a task focus.
      if (nextWidth === width && nextHeight === height) return;
      width = nextWidth;
      height = nextHeight;
      if (selectedTaskRef.current) keepTaskVisible(selectedTaskRef.current);
    });
    observer.observe(surface);
    return () => observer.disconnect();
  }, [flow, keepTaskVisible]);

  if (isError || (!isLoading && !graph)) {
    return (
      <div className="work-dependency-canvas__empty" role="alert">
        <p>Task dependencies could not be loaded.</p>
        <button type="button" onClick={onRetry}>
          Retry
        </button>
      </div>
    );
  }
  if (isLoading || !model) {
    return (
      <div className="work-dependency-canvas__empty" role="status">
        Loading task dependencies…
      </div>
    );
  }
  if (model.nodes.length === 0) {
    return (
      <div className="work-dependency-canvas__empty">
        {hasFilters
          ? "No tasks match these filters. Clear the filters to see their dependencies."
          : "No tasks yet. Plan tasks with the CEO Agent to see their dependencies here."}
      </div>
    );
  }

  const mapVisible = showMap ?? nodes.length > 20;
  return (
    <section
      aria-label="Task dependency canvas"
      className="work-dependency-canvas"
    >
      <div
        className="work-dependency-canvas__toolbar"
        role="group"
        aria-label="Dependency canvas controls"
      >
        <select
          aria-label="Jump to task"
          value={selectionIsVisible ? selectedTaskId : ""}
          onChange={(event) => {
            if (event.target.value) {
              onSelectTask(event.target.value);
              keepTaskVisible(event.target.value);
            }
          }}
        >
          <option value="" disabled>
            Jump to task…
          </option>
          {model.nodes
            .filter((node) => node.task)
            .map((node) => (
              <option key={node.id} value={node.id}>
                {node.label} · {node.title}
              </option>
            ))}
        </select>
        <button
          aria-pressed={focusSelected}
          disabled={!selectionIsVisible && !focusSelected}
          title={
            selectionIsVisible
              ? "Show this task and its dependency chain"
              : "Select a task to focus its chain"
          }
          type="button"
          onClick={() => setFocusSelected((value) => !value)}
        >
          <Focus aria-hidden="true" size={15} /> Focus chain
        </button>
        <button type="button" onClick={() => void flow?.fitView(fitOptions)}>
          <Maximize aria-hidden="true" size={14} /> Fit all
        </button>
        <span className="work-dependency-canvas__toolbar-spacer" />
        <button
          aria-label="Zoom out"
          title="Zoom out"
          type="button"
          onClick={() => void flow?.zoomOut()}
        >
          <Minus aria-hidden="true" size={16} />
        </button>
        <button
          aria-label={`Zoom ${Math.round(zoom * 100)} percent. Reset to 100 percent`}
          title="Reset zoom to 100%"
          type="button"
          onClick={() => void flow?.zoomTo(1)}
        >
          {Math.round(zoom * 100)}%
        </button>
        <button
          aria-label="Zoom in"
          title="Zoom in"
          type="button"
          onClick={() => void flow?.zoomIn()}
        >
          <Plus aria-hidden="true" size={16} />
        </button>
        <button
          aria-label="Show minimap"
          aria-pressed={mapVisible}
          title="Show minimap"
          type="button"
          onClick={() => setShowMap(!mapVisible)}
        >
          <MapIcon aria-hidden="true" size={16} />
        </button>
      </div>
      {model.hasCycle ? (
        <p className="work-dependency-canvas__notice" role="status">
          <AlertCircle aria-hidden="true" size={15} />
          Some tasks depend on each other in a loop. Review the marked tasks to
          resolve the cycle.
        </p>
      ) : edges.length === 0 ? (
        <p className="work-dependency-canvas__notice">
          No dependency links are shown in this view. Open a task to check its
          prerequisites and next action.
        </p>
      ) : null}
      <div className="work-dependency-canvas__surface" ref={surfaceRef}>
        <ReactFlow<DependencyNode, Edge>
          aria-label="Dependencies from prerequisite tasks on the left to dependent tasks on the right"
          nodes={nodes}
          edges={edges}
          nodeTypes={dependencyNodeTypes}
          onInit={setFlow}
          onMove={(_event, viewport) => setZoom(viewport.zoom)}
          onPaneClick={() => onSelectTask(undefined)}
          onNodeClick={(_event, node) => {
            if (node.data.item.task) onSelectTask(node.id);
          }}
          nodesDraggable={false}
          nodesConnectable={false}
          nodesFocusable={false}
          edgesFocusable={false}
          edgesReconnectable={false}
          elementsSelectable={false}
          deleteKeyCode={null}
          selectionKeyCode={null}
          multiSelectionKeyCode={null}
          connectOnClick={false}
          selectionOnDrag={false}
          onlyRenderVisibleElements
          panOnDrag
          panOnScroll
          zoomOnScroll={false}
          zoomOnDoubleClick={false}
          zoomOnPinch
          minZoom={0.08}
          maxZoom={1.6}
          proOptions={{ hideAttribution: true }}
        >
          <Background
            variant={BackgroundVariant.Dots}
            gap={24}
            size={1.5}
            color="color-mix(in srgb, var(--workspace-divider-strong, #c9bdce) 75%, var(--workspace-native-text-muted, #706875))"
          />
          {mapVisible ? (
            <MiniMap
              ariaLabel="Dependency overview"
              className="work-dependency-canvas__minimap"
              nodeColor={(node) =>
                node.selected
                  ? "var(--workspace-native-accent, #7556b9)"
                  : "var(--workspace-divider-strong, #d4cbdc)"
              }
              maskColor="var(--workspace-canvas-minimap-mask, rgb(247 244 249 / 72%))"
              pannable
              zoomable
            />
          ) : null}
        </ReactFlow>
      </div>
      <footer className="work-dependency-canvas__legend">
        <span>Arrows point to tasks that depend on the earlier task.</span>
        <span>
          <i className="work-dependency-canvas__line" data-resolved="true" />{" "}
          Prerequisite completed
        </span>
        <span>
          <i className="work-dependency-canvas__line" /> Prerequisite pending
        </span>
      </footer>
    </section>
  );
}

function DependencyTaskNode({ data, selected }: NodeProps<DependencyNode>) {
  const { item, agent, contextLabel, dimmed, onSelect, onFocusTask } = data;
  return (
    <div className="work-dependency-canvas__node" data-dimmed={dimmed}>
      <Handle
        type="target"
        position={Position.Left}
        id="in"
        isConnectable={false}
      />
      <Handle
        type="target"
        position={Position.Bottom}
        id="in-bottom"
        isConnectable={false}
      />
      <TaskCanvasCard
        item={item}
        agent={agent}
        contextLabel={contextLabel}
        selected={selected}
        onSelect={onSelect}
        onFocusTask={onFocusTask}
      />
      <Handle
        type="source"
        position={Position.Right}
        id="out"
        isConnectable={false}
      />
      <Handle
        type="source"
        position={Position.Bottom}
        id="out-bottom"
        isConnectable={false}
      />
    </div>
  );
}

// Shared by the task dependency canvas and the artifact Plan view.
export function TaskCanvasCard({
  item,
  agent,
  contextLabel,
  selected,
  onSelect,
  onFocusTask,
}: Omit<DependencyNodeData, "dimmed"> & { selected?: boolean }) {
  const statusLabel = item.task
    ? taskWorkStatusLabel(item.task)
    : "Details unavailable";
  return (
    <button
      aria-label={`${item.label}: ${item.title}. ${statusLabel}${item.task ? `. Assigned to ${item.task.assignee.name}` : ""}${contextLabel ? `. ${contextLabel}` : ""}`}
      aria-pressed={selected}
      className="work-dependency-canvas__task nodrag nopan"
      data-context={item.isContext}
      data-cycle={item.isCycle}
      data-selected={selected}
      disabled={!item.task}
      title={item.title}
      type="button"
      onClick={(event) => {
        event.stopPropagation();
        onSelect(item.id);
      }}
      onFocus={(event) => {
        // Pointer focus must not move the card before its click completes.
        if (event.currentTarget.matches(":focus-visible")) onFocusTask(item.id);
      }}
    >
      <span className="work-dependency-canvas__task-top">
        <span>{item.label}</span>
        {contextLabel ? (
          <span className="work-dependency-canvas__context">
            {contextLabel}
          </span>
        ) : null}
        {item.isCycle ? (
          <AlertCircle aria-label="Dependency cycle" size={14} />
        ) : null}
      </span>
      <strong>{item.title}</strong>
      <span className="work-dependency-canvas__assignee">
        {/* Who does the work: the Agent, or the assignee themself. Lanes
            are keyed on it (ADR 0014). */}
        {item.task?.executedBy.kind === "agent" ? (
          <FoldAvatar agentId={item.task.executedBy.id} paused size={28} />
        ) : item.task?.executedBy.avatarUrl ? (
          <img alt="" src={item.task.executedBy.avatarUrl} />
        ) : null}
        <span className="work-dependency-canvas__assignee-copy">
          <span>{item.task?.executedBy.name ?? "Details unavailable"}</span>
          <span>
            {agent?.title ?? (item.task ? "Company" : "Related task")}
          </span>
        </span>
      </span>
      <span
        className="work-dependency-canvas__status"
        data-status={item.status}
      >
        <i aria-hidden="true" />
        <span className="work-dependency-canvas__status-label">
          {statusLabel}
        </span>
        {/* Whose task it is: the assignee, always a member. Only when an
            Agent does the work; otherwise the line above names them. */}
        {item.task?.executedBy.kind === "agent" ? (
          <span
            className="work-dependency-canvas__owner"
            title={`Assigned to ${item.task.assignee.name}`}
          >
            <TaskMemberAvatar member={item.task.assignee} />
            <span>{item.task.assignee.name}</span>
          </span>
        ) : null}
      </span>
    </button>
  );
}
