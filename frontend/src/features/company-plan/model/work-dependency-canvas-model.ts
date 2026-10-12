import type {
  TaskDependencyGraphDto,
  TaskDto,
  TaskStatusDto,
} from "@/shared/api/types";
import {
  formatTaskLabel,
  type LabelTask,
} from "@/shared/lib/task-label-format";

export const WORK_DEPENDENCY_CANVAS_NODE_WIDTH = 264;
export const WORK_DEPENDENCY_CANVAS_NODE_HEIGHT = 164;

export type WorkDependencyCanvasTask = {
  id: string;
  /** The one label every surface shows for the task, {prefix}-{number}. */
  label: string;
  number: number | null;
  title: string;
  status: TaskStatusDto | null;
  task?: TaskDto;
  position: { x: number; y: number };
  isContext: boolean;
  isMatch: boolean;
  isHighlighted: boolean;
  isCycle: boolean;
};

export type WorkDependencyCanvasEdge = {
  id: string;
  source: string;
  target: string;
  resolved: boolean;
  highlighted: boolean;
};

export type WorkDependencyCanvasModel = {
  nodes: WorkDependencyCanvasTask[];
  edges: WorkDependencyCanvasEdge[];
  matchingCount: number;
  contextCount: number;
  hasCycle: boolean;
};

type WorkDependencyCanvasInput = {
  tasks: readonly TaskDto[];
  graph: TaskDependencyGraphDto;
  /** Defaults to the prefix-less formatter until Company data is known. */
  labelTask?: LabelTask;
  scopeTaskIds: ReadonlySet<string>;
  matchingTaskIds: ReadonlySet<string>;
  hasFilters: boolean;
  selectedTaskId?: string;
  focusSelected: boolean;
};

type CanvasTaskSource = Pick<
  WorkDependencyCanvasTask,
  "id" | "label" | "number" | "title" | "status" | "task"
>;

type CanvasEdgeSource = Pick<
  WorkDependencyCanvasEdge,
  "id" | "source" | "target"
>;

type TaskConnections = {
  prerequisites: Map<string, string[]>;
  dependents: Map<string, string[]>;
};

export function createWorkDependencyCanvasModel({
  tasks,
  graph,
  labelTask = formatTaskLabel,
  scopeTaskIds,
  matchingTaskIds,
  hasFilters,
  selectedTaskId,
  focusSelected,
}: WorkDependencyCanvasInput): WorkDependencyCanvasModel {
  const sources = createTaskSources(tasks, graph, labelTask);
  const edgesById = new Map<string, CanvasEdgeSource>();
  for (const edge of graph.items) {
    const source = edge.prerequisiteTaskId;
    const target = edge.dependentTaskId;
    const id = `${source}->${target}`;
    edgesById.set(id, { id, source, target });
    for (const taskId of [source, target]) {
      if (!sources.has(taskId)) sources.set(taskId, unavailableTask(taskId));
    }
  }
  for (const taskId of scopeTaskIds) {
    if (!sources.has(taskId)) sources.set(taskId, unavailableTask(taskId));
  }

  const orderedSources = [...sources.values()].sort(compareTasks);
  const orderById = new Map(
    orderedSources.map((task, index) => [task.id, index]),
  );
  const edges = [...edgesById.values()].sort(
    (left, right) =>
      orderById.get(left.source)! - orderById.get(right.source)! ||
      orderById.get(left.target)! - orderById.get(right.target)!,
  );
  const connections = createConnections(edges);
  const matches = new Set(
    [...scopeTaskIds].filter((id) => !hasFilters || matchingTaskIds.has(id)),
  );
  const visible = hasFilters
    ? collectTaskChain(matches, connections)
    : collectDirectContext(matches, connections);
  const highlighted = selectedTaskId
    ? collectTaskChain(new Set([selectedTaskId]), connections)
    : new Set<string>();

  // Focus narrows the current filter scope; selection alone never moves cards.
  if (focusSelected && selectedTaskId && visible.has(selectedTaskId)) {
    for (const id of visible) {
      if (!highlighted.has(id)) visible.delete(id);
    }
  }

  const visibleSources = orderedSources.filter((task) => visible.has(task.id));
  const visibleEdges = edges.filter(
    (edge) => visible.has(edge.source) && visible.has(edge.target),
  );
  const { positions, cycleTaskIds } = createLayeredLayout(
    visibleSources.map((task) => task.id),
    visibleEdges,
  );
  const nodes = visibleSources.map<WorkDependencyCanvasTask>((source) => ({
    ...source,
    position: positions.get(source.id)!,
    isContext: !matches.has(source.id),
    isMatch: matches.has(source.id),
    isHighlighted: highlighted.has(source.id),
    isCycle: cycleTaskIds.has(source.id),
  }));
  const matchingCount = nodes.filter((node) => node.isMatch).length;

  return {
    nodes,
    edges: visibleEdges.map((edge) => ({
      ...edge,
      // An absent prerequisite remains unresolved. Task readiness stays server-owned.
      resolved: sources.get(edge.source)?.status === "done",
      highlighted: highlighted.has(edge.source) && highlighted.has(edge.target),
    })),
    matchingCount,
    contextCount: nodes.length - matchingCount,
    hasCycle: cycleTaskIds.size > 0,
  };
}

function createTaskSources(
  tasks: readonly TaskDto[],
  graph: TaskDependencyGraphDto,
  labelTask: LabelTask,
) {
  const sources = new Map<string, CanvasTaskSource>();
  for (const task of graph.tasks) {
    sources.set(task.id, {
      id: task.id,
      label: labelTask(task),
      number: task.number,
      title: task.title,
      status: task.status,
    });
  }
  for (const task of tasks) {
    sources.set(task.id, {
      id: task.id,
      label: labelTask(task),
      number: task.number,
      title: task.title,
      status: task.status,
      task,
    });
  }
  return sources;
}

function unavailableTask(id: string): CanvasTaskSource {
  return {
    id,
    label: "Task",
    number: null,
    title: "Task details unavailable",
    status: null,
  };
}

function compareTasks(left: CanvasTaskSource, right: CanvasTaskSource) {
  const numberOrder =
    (left.number ?? Number.MAX_SAFE_INTEGER) -
    (right.number ?? Number.MAX_SAFE_INTEGER);
  return numberOrder || left.id.localeCompare(right.id);
}

function createConnections(
  edges: readonly CanvasEdgeSource[],
): TaskConnections {
  const prerequisites = new Map<string, string[]>();
  const dependents = new Map<string, string[]>();
  for (const edge of edges) {
    const previous = prerequisites.get(edge.target) ?? [];
    previous.push(edge.source);
    prerequisites.set(edge.target, previous);
    const next = dependents.get(edge.source) ?? [];
    next.push(edge.target);
    dependents.set(edge.source, next);
  }
  return { prerequisites, dependents };
}

function collectDirectContext(
  seeds: ReadonlySet<string>,
  connections: TaskConnections,
) {
  const result = new Set(seeds);
  for (const id of seeds) {
    for (const related of connections.prerequisites.get(id) ?? []) {
      result.add(related);
    }
    for (const related of connections.dependents.get(id) ?? []) {
      result.add(related);
    }
  }
  return result;
}

function collectTaskChain(
  seeds: ReadonlySet<string>,
  connections: TaskConnections,
) {
  // Traverse each direction independently so a shared prerequisite adds no siblings.
  const result = new Set(seeds);
  for (const neighbors of [connections.prerequisites, connections.dependents]) {
    const visited = new Set(seeds);
    const pending = [...seeds];
    for (let index = 0; index < pending.length; index += 1) {
      for (const related of neighbors.get(pending[index]) ?? []) {
        if (visited.has(related)) continue;
        visited.add(related);
        result.add(related);
        pending.push(related);
      }
    }
  }
  return result;
}

export function createLayeredLayout(
  orderedIds: readonly string[],
  edges: readonly CanvasEdgeSource[],
) {
  const connections = createConnections(edges);
  const components = findStronglyConnectedComponents(orderedIds, connections);
  const componentByTaskId = new Map<string, number>();
  const cycleTaskIds = new Set<string>();
  components.forEach((ids, component) => {
    for (const id of ids) {
      componentByTaskId.set(id, component);
      if (ids.length > 1) cycleTaskIds.add(id);
    }
  });

  const dependents = components.map(() => new Set<number>());
  const incomingCounts = components.map(() => 0);
  for (const edge of edges) {
    const source = componentByTaskId.get(edge.source)!;
    const target = componentByTaskId.get(edge.target)!;
    if (source === target) {
      if (edge.source === edge.target) cycleTaskIds.add(edge.source);
      continue;
    }
    if (dependents[source].has(target)) continue;
    dependents[source].add(target);
    incomingCounts[target] += 1;
  }

  // Condensing cycles produces a DAG without incorrectly marking their descendants.
  const columns = components.map(() => 0);
  const pending = incomingCounts.flatMap((count, index) =>
    count === 0 ? [index] : [],
  );
  for (let index = 0; index < pending.length; index += 1) {
    const component = pending[index];
    for (const dependent of dependents[component]) {
      columns[dependent] = Math.max(columns[dependent], columns[component] + 1);
      incomingCounts[dependent] -= 1;
      if (incomingCounts[dependent] === 0) pending.push(dependent);
    }
  }

  const rowCounts = new Map<number, number>();
  const positions = new Map<string, { x: number; y: number }>();
  for (const id of orderedIds) {
    const column = columns[componentByTaskId.get(id)!];
    const row = rowCounts.get(column) ?? 0;
    rowCounts.set(column, row + 1);
    positions.set(id, {
      x: 32 + column * (WORK_DEPENDENCY_CANVAS_NODE_WIDTH + 96),
      y: 32 + row * (WORK_DEPENDENCY_CANVAS_NODE_HEIGHT + 40),
    });
  }
  return { positions, cycleTaskIds };
}

function findStronglyConnectedComponents(
  orderedIds: readonly string[],
  connections: TaskConnections,
) {
  // Iterative Kosaraju traversal avoids call-stack limits on long dependency chains.
  const visited = new Set<string>();
  const finished: string[] = [];
  for (const id of orderedIds) {
    if (visited.has(id)) continue;
    visited.add(id);
    const stack = [{ id, nextNeighbor: 0 }];
    while (stack.length) {
      const frame = stack[stack.length - 1];
      const neighbors = connections.dependents.get(frame.id) ?? [];
      if (frame.nextNeighbor === neighbors.length) {
        finished.push(frame.id);
        stack.pop();
        continue;
      }
      const neighbor = neighbors[frame.nextNeighbor];
      frame.nextNeighbor += 1;
      if (visited.has(neighbor)) continue;
      visited.add(neighbor);
      stack.push({ id: neighbor, nextNeighbor: 0 });
    }
  }

  const assigned = new Set<string>();
  const components: string[][] = [];
  for (let index = finished.length - 1; index >= 0; index -= 1) {
    const id = finished[index];
    if (assigned.has(id)) continue;
    assigned.add(id);
    const pending = [id];
    for (let cursor = 0; cursor < pending.length; cursor += 1) {
      for (const prerequisite of connections.prerequisites.get(
        pending[cursor],
      ) ?? []) {
        if (assigned.has(prerequisite)) continue;
        assigned.add(prerequisite);
        pending.push(prerequisite);
      }
    }
    components.push(pending);
  }
  return components;
}
