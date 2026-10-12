import { useTaskLabel } from "@/shared/lib/task-label";
import { Eye, GitBranch } from "@/shared/ui/icons";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { TaskPriorityIcon, TaskStatusIcon } from "./task-metadata-icons";
import type { CompanyPlanTaskLane } from "@/features/company-plan/model/company-plan-view-model";
import { milestoneLabel } from "@/features/company-plan/model/milestone-catalog";
import { OutsideProjectBadge } from "./outside-project-badge";
import { TaskAssignee, TaskMemberAvatar } from "./task-assignee";
import {
  taskWaitsForPrerequisites,
  taskWorkStatusLabel,
} from "@/features/company-plan/model/task-work-state";
import type { TaskDto } from "@/shared/api/types";
import "@/shared/ui/collection-list.css";

export function WorkTaskTable({
  lanes,
  onTaskSelect,
  selectedTaskId,
  variant = "default",
  attentionTaskIds,
  reviewTaskIds,
}: {
  lanes: CompanyPlanTaskLane[];
  attentionTaskIds?: ReadonlySet<string>;
  /** Tasks that wait on this viewer's review (`useProjectTaskReviews`). */
  reviewTaskIds?: ReadonlySet<string>;
  onTaskSelect: (task: TaskDto) => void;
  selectedTaskId?: string;
  variant?: "default" | "overview" | "project";
}) {
  const tasks = [
    ...new Map(
      lanes.flatMap((lane) =>
        lane.tasks.map((task) => [task.id, task] as const),
      ),
    ).values(),
  ];
  tasks.sort((left, right) => left.number - right.number);
  const companyId = tasks[0]?.companyId ?? "";
  const labelTask = useTaskLabel(companyId || undefined);
  return (
    <div
      className={`work-task-table-scroll${variant === "project" ? " collection-list" : ""}`}
      data-variant={variant}
    >
      <table className="work-task-table" data-variant={variant}>
        <caption className="sr-only">
          {variant === "default" ? "Company tasks" : "Project tasks"}
        </caption>
        {variant === "default" ? (
          <colgroup>
            <col style={{ width: "5%" }} />
            <col style={{ width: "30%" }} />
            <col style={{ width: "15%" }} />
            <col style={{ width: "17%" }} />
            <col style={{ width: "16%" }} />
            <col style={{ width: "12%" }} />
            <col style={{ width: "5%" }} />
          </colgroup>
        ) : variant === "project" ? (
          <colgroup>
            <col />
            <col className="work-table-time-column" />
            <col className="work-table-status-column" />
            <col className="work-table-agent-column" />
          </colgroup>
        ) : null}
        <thead>
          <tr
            className={
              variant === "project" ? "collection-list__header" : undefined
            }
          >
            {variant === "default" ? <th scope="col">Priority</th> : null}
            <th scope="col">Task</th>
            <th scope="col">Run time</th>
            <th scope="col">Status</th>
            <th scope="col">{variant === "project" ? "Agent" : "Assignee"}</th>
            {variant !== "project" ? (
              <th scope="col" className="work-table-milestone">
                Milestone
              </th>
            ) : null}
            {variant === "default" ? <th scope="col">Files</th> : null}
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr
              key={task.id}
              className={
                variant === "project" ? "collection-list__row" : undefined
              }
              data-selected={task.id === selectedTaskId || undefined}
              onClick={
                variant === "project" ? () => onTaskSelect(task) : undefined
              }
            >
              {variant === "default" ? (
                <td>
                  <span
                    title={`${task.priority} priority`}
                    aria-label={`${task.priority} priority`}
                  >
                    <TaskPriorityIcon priority={task.priority} />
                  </span>
                </td>
              ) : null}
              <td className="work-table-task">
                <button
                  type="button"
                  data-task-id={task.id}
                  title={variant === "project" ? task.title : undefined}
                  aria-pressed={task.id === selectedTaskId}
                  onClick={(event) => {
                    event.stopPropagation();
                    onTaskSelect(task);
                  }}
                >
                  <small>{labelTask(task)}</small>
                  <strong>{task.title}</strong>
                  {reviewTaskIds?.has(task.id) ? (
                    <span className="work-table-dependency">
                      <Eye size={12} />
                      Needs your review
                    </span>
                  ) : null}
                  {variant !== "project" && task.blockedByCount > 0 ? (
                    <span className="work-table-dependency">
                      <GitBranch size={12} />
                      {task.blockedByCount}{" "}
                      {task.blockedByCount === 1
                        ? "prerequisite"
                        : "prerequisites"}
                    </span>
                  ) : null}
                </button>
              </td>
              <td>
                <span className="task-calendar-time">
                  {task.scheduledAt
                    ? new Date(task.scheduledAt).toLocaleString([], {
                        month: "short",
                        day: "numeric",
                        hour: "2-digit",
                        minute: "2-digit",
                      })
                    : "Not scheduled"}
                </span>
                {task.scheduleDisposition &&
                task.scheduleDisposition !== "scheduled" ? (
                  <small className="task-calendar-time">
                    Run {task.scheduleDisposition}
                  </small>
                ) : null}
                {task.assignmentStatus !== "accepted" ? (
                  <small>
                    {task.assignmentStatus === "pending"
                      ? "Awaiting acceptance"
                      : "Declined"}
                  </small>
                ) : null}
              </td>
              <td>
                <span
                  className="work-table-status"
                  title={
                    attentionTaskIds?.has(task.id)
                      ? "Needs your response"
                      : taskWorkStatusLabel(
                          task,
                          reviewTaskIds?.has(task.id) ?? false,
                        )
                  }
                >
                  <TaskStatusIcon status={task.status} />
                  {attentionTaskIds?.has(task.id)
                    ? "Needs your response"
                    : variant !== "default" && taskWaitsForPrerequisites(task)
                      ? "Waiting"
                      : taskWorkStatusLabel(
                          task,
                          reviewTaskIds?.has(task.id) ?? false,
                        )}
                </span>
              </td>
              <td>
                <span className="work-table-assignee">
                  {variant === "project" ? (
                    <span
                      className="work-table-agent"
                      title={`Assigned to ${task.assignee.name}`}
                    >
                      {task.executedBy.kind === "agent" ? (
                        <>
                          <FoldAvatar
                            paused
                            agentId={task.executedBy.id}
                            size={32}
                          />
                          <span>{task.executedBy.name}</span>
                        </>
                      ) : (
                        <>
                          <TaskMemberAvatar member={task.assignee} />
                          <span>
                            {task.assignee.name} <small>Manual</small>
                          </span>
                        </>
                      )}
                    </span>
                  ) : (
                    <TaskAssignee
                      assignee={task.assignee}
                      executedBy={task.executedBy}
                      variant="avatars"
                    />
                  )}
                  <OutsideProjectBadge compact task={task} />
                </span>
              </td>
              {variant !== "project" ? (
                <td className="work-table-milestone">
                  {milestoneLabel(task.milestoneType)}
                </td>
              ) : null}
              {variant === "default" ? (
                <td>
                  <span title="Open the task to view its saved files">—</span>
                </td>
              ) : null}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
