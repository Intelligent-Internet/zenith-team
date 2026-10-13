import { useTaskLabel } from "@/shared/lib/task-label";
import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import * as Avatar from "@radix-ui/react-avatar";
import { CircleAlert, Eye, EyeOff, GitBranch, Zap } from "@/shared/ui/icons";
import { useMemo, useState } from "react";

import type { CompanyPlanTaskLane } from "@/features/company-plan/model/company-plan-view-model";
import {
  compareTaskReadiness,
  taskWorkStatusLabel,
} from "@/features/company-plan/model/task-work-state";
import { taskExecutedByLabel } from "@/features/company-plan/model/task-execution";
import { TaskPriorityIcon, TaskStatusIcon } from "./task-metadata-icons";
import type { TaskDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { Tooltip } from "@/shared/ui/tooltip";
import "@/features/company-plan/styles/status-board.css";

type StatusColumnKey =
  | "backlog"
  | "ready"
  | "working"
  | "review"
  | "attention"
  | "completed"
  | "cancelled";

type StatusBoardTask = {
  agent: CompanyPlanTaskLane["agent"];
  task: TaskDto;
};

type StatusColumn = {
  key: StatusColumnKey;
  label: string;
  emptyText?: string;
  tasks: StatusBoardTask[];
};

const columnStatus: Record<StatusColumnKey, TaskDto["status"]> = {
  backlog: "backlog",
  ready: "todo",
  working: "in_progress",
  review: "in_review",
  attention: "blocked",
  completed: "done",
  cancelled: "cancelled",
};

const statusColumns: ReadonlyArray<Omit<StatusColumn, "tasks">> = [
  { key: "backlog", label: "Backlog" },
  {
    key: "ready",
    label: "To do",
    emptyText: "No tasks to do yet.",
  },
  {
    key: "working",
    label: "In progress",
    emptyText: "No tasks are currently in progress.",
  },
  {
    key: "review",
    label: "In review",
    emptyText: "Nothing is waiting for review.",
  },
  { key: "attention", label: "Needs attention" },
  {
    key: "completed",
    label: "Done",
    emptyText: "Completed tasks will appear here.",
  },
  { key: "cancelled", label: "Cancelled" },
];

export function StatusBoard({
  lanes,
  selectedTaskId,
  onTaskSelect,
  attentionTaskIds,
  reviewTaskIds,
  hideEmptyColumns = false,
}: {
  lanes: CompanyPlanTaskLane[];
  selectedTaskId?: string;
  onTaskSelect: (task: TaskDto) => void;
  attentionTaskIds: ReadonlySet<string>;
  /** Tasks that wait on this viewer's review (`useProjectTaskReviews`). */
  reviewTaskIds?: ReadonlySet<string>;
  hideEmptyColumns?: boolean;
}) {
  const companyId = lanes.flatMap((lane) => lane.tasks)[0]?.companyId ?? "";
  const labelTask = useTaskLabel(companyId || undefined);
  const runs = useListWorkActiveRunsQuery(companyId, { skip: !companyId });
  const runningTaskIds = new Set(
    (runs.currentData ?? [])
      .filter((run) => run.status === "running")
      .map((run) => run.taskId),
  );
  const columns = useMemo(
    () => groupTasksByStatus(lanes, hideEmptyColumns),
    [lanes, hideEmptyColumns],
  );

  return (
    <section
      aria-label="Tasks by status"
      className="status-board"
      data-filtered={hideEmptyColumns}
      tabIndex={0}
    >
      {columns.map((column) => (
        <section
          aria-label={`${column.label}: ${column.tasks.length} ${column.tasks.length === 1 ? "task" : "tasks"}`}
          className="flex min-w-0 min-h-0 flex-col overflow-hidden [scroll-snap-align:start] [background:transparent]"
          data-column={column.key}
          key={column.key}
        >
          <header className="status-board__column-header">
            <TaskStatusIcon status={columnStatus[column.key]} />
            <h3>{column.label}</h3>
            <span className="grid min-w-[19px] h-[19px] shrink-0 place-items-center rounded-[4px] px-[5px] [background:var(--workspace-control-bg)] text-workspace-muted text-workspace-micro leading-workspace-micro [font-variant-numeric:tabular-nums]">
              {column.tasks.length}
            </span>
          </header>
          <div className="status-board__column-body">
            {column.tasks.length ? (
              <ul className="grid grid-cols-[minmax(0,_1fr)] gap-[10px] m-0 p-0 [list-style:none]">
                {column.tasks.map((item) => (
                  <li key={item.task.id}>
                    <StatusTaskCard
                      item={item}
                      taskLabel={labelTask(item.task)}
                      running={runningTaskIds.has(item.task.id)}
                      awaitsReview={reviewTaskIds?.has(item.task.id) ?? false}
                      needsInput={
                        item.task.status !== "done" &&
                        item.task.status !== "cancelled" &&
                        attentionTaskIds.has(item.task.id)
                      }
                      onSelect={onTaskSelect}
                      selected={item.task.id === selectedTaskId}
                    />
                  </li>
                ))}
              </ul>
            ) : (
              <p className="m-0 [border:1px_dashed_var(--workspace-divider-strong)] rounded-[9px] p-[18px_12px] text-workspace-muted text-workspace-body leading-workspace-body">
                {column.emptyText}
              </p>
            )}
          </div>
        </section>
      ))}
    </section>
  );
}

function groupTasksByStatus(
  lanes: CompanyPlanTaskLane[],
  hideEmptyColumns: boolean,
): StatusColumn[] {
  const columns = statusColumns.map((column) => ({
    ...column,
    tasks: [] as StatusBoardTask[],
  }));
  const columnsByKey = new Map(columns.map((column) => [column.key, column]));
  const seenTaskIds = new Set<string>();

  for (const lane of lanes) {
    for (const task of lane.tasks) {
      if (seenTaskIds.has(task.id)) continue;
      seenTaskIds.add(task.id);
      columnsByKey.get(taskColumn(task))!.tasks.push({
        agent: lane.agent,
        task,
      });
    }
  }

  return columns
    .filter(
      (column) =>
        column.tasks.length || (!hideEmptyColumns && column.emptyText),
    )
    .map((column) => ({
      ...column,
      tasks: column.tasks.sort((left, right) =>
        compareTaskReadiness(left.task, right.task),
      ),
    }));
}

function taskColumn(task: TaskDto): StatusColumnKey {
  switch (task.status) {
    case "backlog":
      return "backlog";
    case "todo":
      return "ready";
    case "in_progress":
      return "working";
    case "in_review":
      return "review";
    case "done":
      return "completed";
    case "blocked":
    case "failed":
    case "error":
      return "attention";
    case "cancelled":
      return "cancelled";
  }
}

function StatusTaskCard({
  item: { agent, task },
  taskLabel,
  needsInput,
  awaitsReview,
  running,
  onSelect,
  selected,
}: {
  item: StatusBoardTask;
  taskLabel: string;
  needsInput: boolean;
  awaitsReview: boolean;
  running: boolean;
  onSelect: (task: TaskDto) => void;
  selected: boolean;
}) {
  const statusLabel = taskWorkStatusLabel(task, awaitsReview);
  const initials = personInitials(task.assignee.name);
  const [participantsTooltipOpen, setParticipantsTooltipOpen] = useState(false);
  const participantsLabel =
    agent.kind === "agent"
      ? `Executed by ${agent.name} · ${agent.isCeo ? "Leadership" : agent.title}. Assigned to ${task.assignee.name}.`
      : `Assigned to ${task.assignee.name}, who does the work themself.`;

  return (
    <button
      aria-label={`${taskLabel}: ${task.title}. ${statusLabel}. ${task.priority} priority. Assigned to ${task.assignee.name}.${task.assigneeOutsideProject ? ` ${task.assignee.name} can't see this project.` : ""} Executed by ${agent.kind === "agent" ? `${agent.name}, ${agent.title}` : taskExecutedByLabel(task)}.${needsInput ? " Pending question or approval." : ""}${awaitsReview ? " Needs your review." : ""}`}
      aria-pressed={selected}
      className={cx("status-board__task", selected && "is-selected")}
      onFocus={() => setParticipantsTooltipOpen(true)}
      onBlur={() => setParticipantsTooltipOpen(false)}
      onKeyDown={(event) => {
        if (event.key === "Escape") setParticipantsTooltipOpen(false);
      }}
      onClick={() => {
        setParticipantsTooltipOpen(false);
        onSelect(task);
      }}
      type="button"
    >
      <span className="status-board__task-key">
        <TaskPriorityIcon priority={task.priority} />
        {taskLabel}
      </span>
      <span className="status-board__task-title" title={task.title}>
        {task.title}
      </span>
      <span className="[display:-webkit-box] [-webkit-box-orient:vertical] [-webkit-line-clamp:2] overflow-hidden text-workspace-caption leading-workspace-caption text-workspace-muted">
        {task.description.replace(/[#*`]/g, "")}
      </span>
      {needsInput ? (
        <span
          className="inline-flex items-center gap-[5px] mt-[9px] [border:1px_solid_currentColor] rounded-[5px] p-[2px_5px] text-[var(--workspace-native-approval)] text-workspace-caption font-[550] leading-workspace-caption"
          title="Pending question or approval"
        >
          <CircleAlert aria-hidden="true" size={13} />
          Needs your input
        </span>
      ) : null}
      {awaitsReview ? (
        <span className="status-board__review-status inline-flex self-start items-center gap-[6px] text-workspace-muted text-workspace-caption leading-workspace-caption">
          <Eye aria-hidden="true" size={14} />
          <span>Needs your review</span>
        </span>
      ) : null}
      <span className="status-board__participants">
        <Tooltip
          label={participantsLabel}
          open={participantsTooltipOpen}
          onOpenChange={setParticipantsTooltipOpen}
        >
          <span
            aria-hidden="true"
            className="inline-flex flex-none items-center gap-[6px]"
          >
            {agent.kind === "agent" ? (
              <FoldAvatar agentId={agent.id} paused size={28} />
            ) : null}
            <Avatar.Root className="status-board__avatar inline-flex w-[28px] h-[28px] shrink-0 overflow-hidden rounded-[8px] [background:var(--workspace-accent-soft)] text-workspace-accent">
              {task.assignee.avatarUrl ? (
                <Avatar.Image
                  alt=""
                  draggable={false}
                  src={task.assignee.avatarUrl}
                />
              ) : null}
              <Avatar.Fallback>{initials}</Avatar.Fallback>
            </Avatar.Root>
          </span>
        </Tooltip>
        {task.assigneeOutsideProject ? (
          <span
            className="inline-grid flex-none place-items-center text-[var(--workspace-native-approval)]"
            title={`${task.assignee.name} can't see this project`}
          >
            <EyeOff size={14} aria-hidden="true" />
          </span>
        ) : null}
        <time
          className="flex-none ml-auto text-workspace-muted text-workspace-caption leading-workspace-caption whitespace-nowrap"
          dateTime={task.updatedAt}
        >
          {new Date(task.updatedAt).toLocaleDateString(undefined, {
            month: "short",
            day: "numeric",
          })}
        </time>
      </span>
      {running || task.blockedByCount > 0 ? (
        <span
          className="status-board__task-footer"
          data-running={running || undefined}
        >
          {running ? (
            <>
              <Zap size={13} />
              Running
            </>
          ) : (
            <>
              <GitBranch size={13} />
              {task.blockedByCount}{" "}
              {task.blockedByCount === 1 ? "prerequisite" : "prerequisites"} ·{" "}
              {task.dependenciesResolved ? "Resolved" : "Waiting"}
            </>
          )}
        </span>
      ) : null}
    </button>
  );
}
