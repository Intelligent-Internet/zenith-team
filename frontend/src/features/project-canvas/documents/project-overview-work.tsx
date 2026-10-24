import { useListWorkspaceTasksQuery } from "@/shared/api/task-api";
import { Link, useSearchParams } from "react-router";

import { ProjectTaskCollection } from "./work-blocks";
import { taskBlockSummary } from "./task-summary";
import type { OverviewTaskView } from "@/state/slice/work";
import {
  taskWorkStatusLabel,
  taskNeedsAttention,
} from "@/features/company-plan/model/task-work-state";
import { Icon } from "@/shared/ui/icon";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import { useProjectTaskReviews } from "@/features/company-plan/hooks/use-project-task-reviews";
import { useProjectAccessLoss } from "@/features/company-plan/hooks/use-project-access-loss";
import type { CanvasScope } from "../api/canvas-api";
import { TaskStatusIcon } from "@/features/company-plan/components/task-metadata-icons";

export function ProjectOverviewWork({
  companyId,
  projectId,
  display,
}: CanvasScope & { display: OverviewTaskView }) {
  const query = useListWorkspaceTasksQuery({
    companyId,
    options: { project_id: projectId },
  });
  const requests = useTaskAttention(companyId);
  const reviews = useProjectTaskReviews(companyId, projectId);
  // A 404 may mean the viewer just lost the Project: no red until the
  // Project is read again, and then they leave or see the error.
  const checkingAccess = useProjectAccessLoss(
    companyId,
    projectId,
    query.error,
  );
  const [params, setParams] = useSearchParams();
  const tasks = query.currentData?.items ?? [];
  const current = tasks.filter((task) => task.status !== "cancelled");
  const done = current.filter((task) => task.status === "done").length;
  const active = current.filter((task) => task.status === "in_progress");
  const review = current.filter((task) => task.status === "in_review");
  const attention = current.filter(
    (task) =>
      requests.taskIds.has(task.id) ||
      (!review.some((row) => row.id === task.id) && taskNeedsAttention(task)),
  );
  const visible = [
    ...new Map(
      [...attention, ...review, ...active, ...current].map((task) => [
        task.id,
        task,
      ]),
    ).values(),
  ];
  const taskStates = new Map(
    current.map((task) => [
      task.id,
      { label: taskWorkStatusLabel(task), status: task.status },
    ]),
  );
  const open = (taskId: string) => {
    const next = new URLSearchParams(params);
    next.set("taskId", taskId);
    setParams(next);
  };

  return (
    <section className="po-work-pulse" aria-label="Current project work">
      {(requests.isError || reviews.isError) && (
        <p role="alert">
          Some pending reviews or requests could not be loaded.{" "}
          <button
            onClick={() => {
              requests.retry();
              reviews.retry();
            }}
          >
            Retry
          </button>
        </p>
      )}
      {(requests.isLoading || reviews.isLoading) && (
        <p role="status">Checking pending reviews and responses…</p>
      )}
      {query.isLoading || checkingAccess ? (
        <p role="status">Loading project work…</p>
      ) : query.isError ? (
        <p role="alert">
          Current work is unavailable.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : !current.length ? (
        <div className="po-work-empty">
          <Icon name="task-square" className="size-4" />
          <p>
            {tasks.length
              ? "No active work. Add a Task when you are ready to continue."
              : "Your project starts here. Add a Task to put the plan in motion."}
          </p>
          <Link to={`/projects/${projectId}/tasks`}>
            Open Tasks <Icon name="arrow-right" className="size-4" />
          </Link>
        </div>
      ) : (
        <>
          <div className="po-work-summary">
            <div className="po-work-progress">
              <span>
                <strong>{done}</strong> / {current.length} Tasks complete
              </span>
              <progress
                value={done}
                max={current.length}
                aria-label="Completed Tasks, excluding cancelled work"
              />
            </div>
            <div className="po-work-counts">
              {active.length > 0 && <span>{active.length} in progress</span>}
              {review.length > 0 && (
                <span data-tone="review">{review.length} in review</span>
              )}
              {attention.length > 0 && (
                <span data-tone="attention">
                  {attention.length} need attention
                </span>
              )}
              <Link to={`/projects/${projectId}/tasks`}>
                All Tasks <Icon name="arrow-right" className="size-4" />
              </Link>
            </div>
          </div>
          {visible.length > 0 && (
            <ProjectTaskCollection
              tasks={visible.map(taskBlockSummary)}
              display={display}
              open={open}
              status={(task) => {
                const state = taskStates.get(task.id);
                return (
                  <span className={`pc-work-status is-${task.status}`}>
                    {state && <TaskStatusIcon status={state.status} />}
                    {requests.taskIds.has(task.id)
                      ? "Needs your response"
                      : reviews.taskIds.has(task.id)
                        ? "Needs your review"
                        : state?.label}
                  </span>
                );
              }}
              assignee={(task) => (
                <span className="pc-work-assignee">
                  {task.assigneeName}
                  {task.agentName && <small>With {task.agentName}</small>}
                </span>
              )}
            />
          )}
        </>
      )}
    </section>
  );
}
