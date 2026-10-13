import {
  useGetWorkspaceTaskDependencyGraphQuery,
  useListWorkspaceTasksQuery,
} from "@/shared/api/task-api";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  BackgroundVariant,
  Handle,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Node,
  type NodeProps,
} from "@xyflow/react";

import { TaskCanvasCard } from "@/features/company-plan/components/work-dependency-canvas";
import { ArrowUpRight, Layers, Search } from "@/shared/ui/icons";
import type { WorkDependencyCanvasTask } from "@/features/company-plan/model/work-dependency-canvas-model";
import { useTaskLabel } from "@/shared/lib/task-label";
import type { ArtifactCollectionProps } from "./artifact-collection";
import { GraphControls, type ViewCamera } from "../documents/graph-controls";
import { TaskArtifactCanvas } from "./task-artifact-canvas";
import { PLAN_LAYOUT_LIMIT } from "./plan-layout-limits";
import type {
  PlanLayoutRequest,
  PlanLayoutResponse,
} from "./plan-layout.types";

type PlanProps = ArtifactCollectionProps & {
  camera: ViewCamera;
  expanded: string;
  onExpand: (id: string) => void;
  onTask: (id: string) => void;
  onAdd?: (initial: "document" | "upload", taskId?: string) => void;
  canEditDocuments: boolean;
  canManageFiles: boolean;
};
type PlanNodeData = {
  task?: WorkDependencyCanvasTask;
  count: number;
  comments: number;
  dimmed: boolean;
  onExpand: (id: string) => void;
};
type PlanNode = Node<PlanNodeData, "planTask">;
const nodeTypes = { planTask: PlanTaskNode };
export function PlanArtifactCanvas(props: PlanProps) {
  return (
    <ReactFlowProvider>
      <PlanGraph {...props} />
    </ReactFlowProvider>
  );
}
function PlanGraph({
  scope,
  artifacts,
  onOpen,
  camera,
  expanded,
  onExpand,
  onTask,
  onAdd,
  canEditDocuments,
  canManageFiles,
}: PlanProps) {
  const labelTask = useTaskLabel(scope.companyId);
  const tasks = useListWorkspaceTasksQuery({
    companyId: scope.companyId,
    options: { project_id: scope.projectId },
  });
  const graph = useGetWorkspaceTaskDependencyGraphQuery({
    companyId: scope.companyId,
  });
  const [layout, setLayout] = useState<PlanLayoutResponse>();
  const [layoutError, setLayoutError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [search, setSearch] = useState("");
  const generation = useRef(0);
  const taskCameras = useRef(new Map<string, ViewCamera>());
  const flow = useReactFlow<PlanNode>();
  const fitted = useRef(!!camera.current);
  const orderedTasks = useMemo(
    () =>
      [...(tasks.currentData?.items ?? [])].sort(
        (a, b) => a.number - b.number || a.id.localeCompare(b.id),
      ),
    [tasks.currentData],
  );
  const scopedEdges = useMemo(() => {
    const ids = new Set(orderedTasks.map((task) => task.id));
    return (graph.currentData?.items ?? []).filter(
      (edge) =>
        ids.has(edge.prerequisiteTaskId) && ids.has(edge.dependentTaskId),
    );
  }, [orderedTasks, graph.currentData]);
  const tooLarge =
    orderedTasks.length > PLAN_LAYOUT_LIMIT.tasks ||
    scopedEdges.length > PLAN_LAYOUT_LIMIT.edges;
  useEffect(() => {
    if (
      !tasks.currentData ||
      tooLarge ||
      (!graph.currentData && !graph.isError)
    )
      return;
    const requestId = ++generation.current;
    let worker: Worker;
    try {
      worker = new Worker(new URL("./plan-layout.worker.ts", import.meta.url), {
        type: "module",
      });
    } catch {
      setLayoutError(true);
      return;
    }
    setLayoutError(false);
    const timeout = window.setTimeout(() => {
      setLayoutError(true);
      worker.terminate();
    }, 5000);
    worker.onmessage = ({ data }: MessageEvent<PlanLayoutResponse>) => {
      if (data.requestId !== generation.current) return;
      clearTimeout(timeout);
      setLayout(data);
      setLayoutError(!data.ok);
      worker.terminate();
    };
    worker.onerror = () => {
      clearTimeout(timeout);
      setLayoutError(true);
      worker.terminate();
    };
    const request: PlanLayoutRequest = {
      requestId,
      taskIds: orderedTasks.map((task) => task.id),
      edges: scopedEdges.map((edge) => ({
        id: `${edge.prerequisiteTaskId}->${edge.dependentTaskId}`,
        source: edge.prerequisiteTaskId,
        target: edge.dependentTaskId,
      })),
    };
    worker.postMessage(request);
    return () => {
      ++generation.current;
      clearTimeout(timeout);
      worker.terminate();
    };
  }, [
    orderedTasks,
    scopedEdges,
    attempt,
    tooLarge,
    tasks.currentData,
    graph.currentData,
    graph.isError,
  ]);
  const model = useMemo(() => {
    const valid = layout?.ok && !layoutError && !tooLarge;
    const positions = new Map(valid ? layout.positions : []);
    const cycleIds = new Set(valid ? layout.cycleTaskIds : []);
    return orderedTasks.map<WorkDependencyCanvasTask>((task, index) => ({
      id: task.id,
      label: labelTask(task),
      number: task.number,
      title: task.title,
      status: task.status,
      task,
      position: positions.get(task.id) ?? {
        x: 32 + (index % 4) * 360,
        y: 32 + Math.floor(index / 4) * 204,
      },
      isContext: false,
      isMatch: true,
      isHighlighted: false,
      isCycle: cycleIds.has(task.id),
    }));
  }, [orderedTasks, layout, layoutError, tooLarge, labelTask]);
  const grouped = useMemo(() => {
    const taskIds = new Set(orderedTasks.map((task) => task.id));
    const map = new Map<string, typeof artifacts>();
    for (const artifact of artifacts) {
      const key =
        artifact.taskId && taskIds.has(artifact.taskId)
          ? artifact.taskId
          : "resources";
      const list = map.get(key) ?? [];
      list.push(artifact);
      map.set(key, list);
    }
    return map;
  }, [artifacts, orderedTasks]);
  const nodes = useMemo<PlanNode[]>(() => {
    const rows = model.map((task) => {
      const contents = grouped.get(task.id) ?? [];
      return {
        id: task.id,
        type: "planTask" as const,
        position: {
          x: (task.position.x / 360) * 408,
          y: (task.position.y / 204) * 244,
        },
        width: 304,
        height: 200,
        draggable: false,
        selectable: false,
        data: {
          task,
          count: contents.length,
          comments: contents.reduce((n, a) => n + a.openComments, 0),
          dimmed:
            !!search.trim() &&
            !`${task.label} ${task.title} ${task.task?.assignee.name} ${contents.map((a) => a.title).join(" ")}`
              .toLowerCase()
              .includes(search.trim().toLowerCase()),
          onExpand,
        },
      };
    });
    const resources = grouped.get("resources") ?? [];
    return [
      ...rows,
      {
        id: "resources",
        type: "planTask",
        position: {
          x: 36,
          y:
            Math.max(0, ...rows.map((row) => row.position.y + row.height)) + 72,
        },
        width: 304,
        height: 132,
        draggable: false,
        selectable: false,
        data: {
          count: resources.length,
          comments: resources.reduce((n, a) => n + a.openComments, 0),
          dimmed: false,
          onExpand,
        },
      },
    ];
  }, [model, grouped, search, onExpand]);
  const edges = useMemo(() => {
    const states = new Map(orderedTasks.map((task) => [task.id, task.status]));
    return scopedEdges.map((edge) => ({
      id: `${edge.prerequisiteTaskId}->${edge.dependentTaskId}`,
      source: edge.prerequisiteTaskId,
      target: edge.dependentTaskId,
      sourceHandle: "out",
      targetHandle: "in",
      type: "smoothstep",
      style: {
        stroke:
          states.get(edge.prerequisiteTaskId) === "done"
            ? "var(--workspace-native-success, #658c72)"
            : "var(--workspace-native-text-faint)",
        strokeWidth: 1.4,
      },
      markerEnd: {
        type: "arrowclosed" as const,
        width: 16,
        height: 16,
        color: "var(--workspace-native-text-faint)",
      },
    }));
  }, [scopedEdges, orderedTasks]);
  useEffect(() => {
    if (
      fitted.current ||
      expanded ||
      !flow.viewportInitialized ||
      (!layout && !layoutError && !tooLarge)
    )
      return;
    const frame = requestAnimationFrame(() => {
      fitted.current = true;
      void flow.fitView({ maxZoom: 0.95, padding: 0.16 });
    });
    return () => cancelAnimationFrame(frame);
  }, [layout, layoutError, tooLarge, expanded, flow]);
  const retry = () => {
    void tasks.refetch();
    void graph.refetch();
    setAttempt((value) => value + 1);
  };
  if (tasks.isError && !tasks.currentData)
    return (
      <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
        <h3>Tasks couldn’t be loaded.</h3>
        <button className="pc-button" onClick={retry}>
          Try again
        </button>
      </div>
    );
  if (!tasks.currentData)
    return (
      <div
        className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]"
        role="status"
      >
        Loading project tasks…
      </div>
    );
  const task = model.find((row) => row.id === expanded);
  if (task || expanded === "resources") {
    let taskCamera = taskCameras.current.get(expanded);
    if (!taskCamera) {
      taskCamera = { current: undefined };
      taskCameras.current.set(expanded, taskCamera);
    }
    return (
      <TaskArtifactCanvas
        key={expanded}
        scope={scope}
        artifacts={grouped.get(expanded) ?? []}
        title={task?.title ?? "Project resources"}
        label={task?.label ?? "PROJECT"}
        camera={taskCamera}
        onOpen={onOpen}
        onBack={() => onExpand("")}
        onTask={task ? () => onTask(task.id) : undefined}
        onAdd={onAdd ? (initial) => onAdd(initial, task?.id) : undefined}
        canEditDocuments={canEditDocuments}
        canManageFiles={canManageFiles}
      />
    );
  }
  return (
    <div className="pc-plan-view">
      <div className="pc-view-toolbar">
        <div>
          <strong>From work to outcomes</strong>
          <span>
            {model.length} tasks · Open a task to explore its artifact canvas
          </span>
        </div>
        <label className="pc-search">
          <Search size={16} />
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Find a task or artifact…"
            aria-label="Find a task or artifact"
            onKeyDown={(event) => {
              if (event.key === "Enter")
                void flow.fitView({
                  nodes: nodes.filter((node) => !node.data.dimmed),
                  padding: 0.15,
                  maxZoom: 1,
                });
            }}
          />
        </label>
      </div>
      {(graph.isError || layoutError || tooLarge) && (
        <p className="m-0 p-[8px_24px] text-[length:11px] text-[var(--pc-muted)] [background:var(--workspace-control-bg)]">
          Tasks are available.{" "}
          {tooLarge
            ? "Showing a grid for this large plan."
            : "The dependency layout couldn’t be loaded."}{" "}
          {!tooLarge && (
            <button className="pc-text-button" onClick={retry}>
              Retry
            </button>
          )}
        </p>
      )}
      {layout?.ok && layout.cycleTaskIds.length > 0 && (
        <p className="m-0 p-[8px_24px] text-[length:11px] text-[var(--pc-muted)] [background:var(--workspace-control-bg)]">
          Some tasks form a dependency cycle. Their connections are preserved
          here.
        </p>
      )}
      <div className="pc-graph-surface relative flex-1 min-h-0 overflow-hidden [background:var(--workspace-content-bg)]">
        <ReactFlow<PlanNode>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          nodesDraggable={false}
          nodesConnectable={false}
          onNodeClick={(_, node) => onExpand(node.id)}
          deleteKeyCode={null}
          onlyRenderVisibleElements
          minZoom={0.12}
          maxZoom={1.8}
          panOnScroll
          zoomOnScroll={false}
          defaultViewport={camera.current}
          fitView={!camera.current}
          fitViewOptions={{ maxZoom: 0.95, padding: 0.16 }}
          onMoveStart={(event) => {
            if (event) fitted.current = true;
          }}
          onMoveEnd={(_, viewport) => {
            camera.current = viewport;
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
        <GraphControls />
        <div className="pc-navigation-hint">
          Arrows follow task dependencies · Scroll to pan · Pinch to zoom
        </div>
      </div>
    </div>
  );
}
function PlanTaskNode({ data, id }: NodeProps<PlanNode>) {
  return (
    <section className="pc-plan-node" data-dimmed={data.dimmed}>
      {data.task ? (
        <>
          <Handle
            type="target"
            position={Position.Left}
            id="in"
            isConnectable={false}
            style={{ top: 82 }}
          />
          <div className="pc-plan-task h-[162px]">
            <TaskCanvasCard
              item={data.task}
              onSelect={() => data.onExpand(id)}
              onFocusTask={() => {}}
            />
          </div>
          <Handle
            type="source"
            position={Position.Right}
            id="out"
            isConnectable={false}
            style={{ top: 82 }}
          />
        </>
      ) : (
        <button
          className="pc-resource-heading nodrag nopan"
          onClick={() => data.onExpand(id)}
        >
          <Layers size={25} />
          <span>
            <strong>Project resources</strong>
            <span>Artifacts outside tasks</span>
          </span>
        </button>
      )}
      <button
        className="pc-plan-expand nodrag nopan"
        onClick={() => data.onExpand(id)}
        aria-label={`Open ${data.task?.label ?? "project resources"} canvas`}
      >
        <span>
          {data.count} {data.count === 1 ? "artifact" : "artifacts"}
        </span>
        {data.comments > 0 && <small>{data.comments} comments</small>}
        <ArrowUpRight size={15} />
      </button>
    </section>
  );
}
