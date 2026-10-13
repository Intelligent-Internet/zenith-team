import { useGetWorkspaceTaskDependencyGraphQuery } from "@/shared/api/task-api";
import { useSearchParams } from "react-router";
import { useEffect, useMemo, useState } from "react";
import { WorkDependencyCanvas } from "@/features/company-plan/components/work-dependency-canvas";
import { WorkTaskTable } from "@/features/company-plan/components/work-task-table";
import { StatusBoard } from "@/features/company-plan/components/status-board";
import {
  createCompanyPlanViewModel,
  type CompanyPlanTaskLane,
} from "@/features/company-plan/model/company-plan-view-model";
import type { Project } from "../api/project-api";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useTaskAttention } from "../hooks/use-task-attention";
import { useProjectTaskReviews } from "../hooks/use-project-task-reviews";
import type { AgentDto, TaskDto } from "@/shared/api/types";

import { Icon } from "@/shared/ui/icon";
import { useTaskLabel } from "@/shared/lib/task-label";
import {
  selectProjectTaskView,
  setProjectTaskView,
  useAppDispatch,
  useAppSelector,
  type ProjectTaskView,
} from "@/state";
import "@/features/company-plan/styles/company-plan.css";

const NO_AGENTS: AgentDto[] = [];

// Lanes name the Agent doing each Task, so they need the Company's Agents,
// archived ones included: an executor the list marks archived reads as
// "Archived Agent", and one the list does not name (not loaded yet) reads as
// plain "Agent".
export function projectTaskLanes(
  _project: Project,
  tasks: TaskDto[],
  agents: AgentDto[] = [],
): CompanyPlanTaskLane[] {
  return createCompanyPlanViewModel(tasks, agents).lanes.filter(
    (lane) => lane.tasks.length > 0,
  );
}

export function useProjectAgents(companyId: string): AgentDto[] {
  return useListCompanyAgentsQuery(companyId).currentData?.items ?? NO_AGENTS;
}

export function ProjectTasks({
  companyId,
  project,
  tasks,
  onSelectTask,
  selectedTaskId,
}: {
  companyId: string;
  project: Project;
  tasks: TaskDto[];
  onSelectTask: (id: string | undefined) => void;
  selectedTaskId?: string;
}) {
  const attention = useTaskAttention(companyId);
  const labelTask = useTaskLabel(companyId);
  const reviews = useProjectTaskReviews(companyId, project.id);
  const dispatch = useAppDispatch();
  const preferredView = useAppSelector((state) =>
    selectProjectTaskView(state, companyId, project.id),
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedView = searchParams.get("view");
  const reviewOnly = searchParams.get("review") === "mine";
  const view =
    requestedView === "board" ||
    requestedView === "table" ||
    requestedView === "dependencies"
      ? requestedView
      : preferredView;
  // An explicit link wins for this visit; only choosing a view changes the
  // preference. Opening a dependency from a Task does not reset it.
  useEffect(() => {
    if (view === requestedView) return;
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.set("view", view);
        return next;
      },
      { replace: true },
    );
  }, [requestedView, setSearchParams, view]);
  const setView = (nextView: ProjectTaskView) => {
    dispatch(
      setProjectTaskView({ companyId, projectId: project.id, view: nextView }),
    );
    const next = new URLSearchParams(searchParams);
    next.set("view", nextView);
    setSearchParams(next);
  };
  const [search, setSearch] = useState("");
  const graph = useGetWorkspaceTaskDependencyGraphQuery(
    { companyId },
    { skip: view !== "dependencies" },
  );
  const agents = useProjectAgents(companyId);
  const lanes = useMemo(
    () => projectTaskLanes(project, tasks, agents),
    [project, tasks, agents],
  );
  const matchingTasks = tasks.filter(
    (task) =>
      (!reviewOnly || reviews.taskIds.has(task.id)) &&
      `${labelTask(task)} ${task.key} ${task.title} ${task.assignee.name} ${task.executedBy.name}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  const filteredLanes = projectTaskLanes(project, matchingTasks, agents);
  const scopeTaskIds = useMemo(
    () => new Set(tasks.map((task) => task.id)),
    [tasks],
  );
  const matchingTaskIds = new Set(matchingTasks.map((task) => task.id));
  const select = (task: TaskDto) => onSelectTask(task.id);
  return (
    <section className="project-task-board-section" data-view={view}>
      <div className="project-section-heading">
        <div className="project-task-controls flex w-full items-center justify-between flex-wrap gap-[12px] min-w-0">
          <label className="project-task-search">
            <Icon name="search" className="size-4" />
            <input
              aria-label="Search project tasks"
              placeholder="Search tasks or assignees…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <div
            className="project-view-switch flex flex-wrap gap-[4px] p-[4px] [border:1px_solid_var(--workspace-divider)] rounded-[10px]"
            role="group"
            aria-label="Task view"
          >
            {(
              [
                ["board", "kanban", "Board"],
                ["table", "task-square", "Table"],
                ["dependencies", "hierarchy", "Dependencies"],
              ] as const
            ).map(([id, icon, label]) => (
              <button
                key={id}
                type="button"
                aria-pressed={view === id}
                onClick={() => setView(id)}
              >
                <Icon name={icon} className="size-4" />
                {label}
              </button>
            ))}
          </div>
        </div>
      </div>
      {reviews.isError ? (
        <p
          className="col-[1] m-0 text-workspace-muted text-workspace-caption"
          role="alert"
        >
          Reviews could not be loaded.{" "}
          <button type="button" onClick={reviews.retry}>
            Retry
          </button>
        </p>
      ) : null}
      <div className="project-task-view">
        {reviewOnly && (reviews.isLoading || reviews.isError) ? (
          <p role="status">
            {reviews.isLoading
              ? "Checking reviews…"
              : "Review status is unavailable."}
          </p>
        ) : tasks.length === 0 ? (
          <ProjectEmptyState />
        ) : view === "dependencies" ? (
          <WorkDependencyCanvas
            tasks={tasks}
            labelTask={labelTask}
            graph={graph.currentData}
            scopeTaskIds={scopeTaskIds}
            matchingTaskIds={matchingTaskIds}
            hasFilters={Boolean(search.trim()) || reviewOnly}
            selectedTaskId={selectedTaskId}
            lanes={lanes}
            isLoading={graph.isLoading}
            isError={graph.isError}
            onRetry={() => void graph.refetch()}
            onSelectTask={onSelectTask}
          />
        ) : matchingTasks.length === 0 ? (
          <ProjectEmptyState
            label={
              reviewOnly && !search.trim()
                ? "Nothing to review"
                : "No tasks match your filters."
            }
          />
        ) : view === "board" ? (
          <StatusBoard
            reviewTaskIds={reviews.taskIds}
            hideEmptyColumns={reviewOnly || Boolean(search.trim())}
            lanes={filteredLanes}
            selectedTaskId={selectedTaskId}
            onTaskSelect={select}
            attentionTaskIds={attention.taskIds}
          />
        ) : (
          <WorkTaskTable
            variant="project"
            reviewTaskIds={reviews.taskIds}
            attentionTaskIds={attention.taskIds}
            lanes={filteredLanes}
            selectedTaskId={selectedTaskId}
            onTaskSelect={select}
          />
        )}
      </div>
    </section>
  );
}

export function ProjectEmptyState({
  label = "No tasks in this project yet.",
}: {
  label?: string;
}) {
  return (
    <div className="project-workspace-empty">
      <Icon name="task-square" className="size-6" />
      <p>{label}</p>
    </div>
  );
}
