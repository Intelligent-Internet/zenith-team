import { useUpdateWorkspaceTaskMutation } from "@/shared/api/task-api";
import { TaskScheduling } from "@/features/calendar/task-scheduling";
import { TaskAssigneeField, TaskExecutorField } from "./task-assignment-fields";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { TaskProjectLabel } from "./project-controls";
import {
  Bot,
  CircleDot,
  CircleUserRound,
  Clock3,
  Flag,
  SignalHigh,
  Tag,
  Users,
} from "@/shared/ui/icons";
import { TaskPriorityIcon, TaskStatusIcon } from "./task-metadata-icons";
import type { ReactNode } from "react";
import { milestoneLabel } from "@/features/company-plan/model/milestone-catalog";
import { useTaskWorkStatusLabel } from "@/features/company-plan/hooks/use-project-task-reviews";
import type { TaskDto, TaskUpdateRequest } from "@/shared/api/types";

import { toast } from "@/shared/ui/sonner";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { hasProjectPermission } from "@/shared/permissions";
import { TaskPropertyDate } from "./task-property-date";

export function TaskProperties({
  task,
  variant = "default",
  canEdit: canEditRequested = true,
}: {
  task: TaskDto;
  variant?: "default" | "compact" | "detail";
  canEdit?: boolean;
}) {
  // Every field is a Task edit, which needs "Create and edit tasks" on its
  // Project (ADR 0017); without it the properties read only.
  const canEdit =
    canEditRequested &&
    hasProjectPermission(task.access, "project.tasks.write");
  const [update, mutation] = useUpdateWorkspaceTaskMutation();
  const statusLabel = useTaskWorkStatusLabel(task);
  async function save(changes: Omit<TaskUpdateRequest, "expectedRevision">) {
    if (!canEdit) return false;
    try {
      await update({
        companyId: task.companyId,
        taskId: task.id,
        request: { expectedRevision: task.revision, ...changes },
      }).unwrap();
      toast.success("Task updated");
      return true;
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(
          error,
          "Task could not be updated. Refresh and try again.",
        ),
      );
      return false;
    }
  }

  const labels = (
    <input
      key={`${task.id}-labels-${task.revision}`}
      className="task-property-input"
      aria-label="Labels, separated by commas"
      placeholder="Add labels"
      defaultValue={task.labels.join(", ")}
      disabled={!canEdit || mutation.isLoading}
      onBlur={(event) => {
        const next = event.currentTarget.value
          .split(",")
          .map((label) => label.trim())
          .filter(Boolean);
        if (next.join(", ") !== task.labels.join(", "))
          void save({ labels: next });
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter") event.currentTarget.blur();
      }}
    />
  );
  // One date picker in every layout, never the browser's own date field.
  const dateInput = (field: "startDate" | "dueDate", label: string) =>
    canEdit ? (
      <TaskPropertyDate
        label={label}
        value={task[field]}
        disabled={mutation.isLoading}
        onSave={(value) => save({ [field]: value })}
      />
    ) : (
      <span>{task[field] ?? "Not set"}</span>
    );
  // The assignee is a member and is drawn as one; the Agent doing the work
  // has its own row, so a person never carries an Agent's avatar (ADR 0014).
  // Whoever may hand the Task over changes either row in place.
  const assignee = (
    <>
      <Property icon={<Users />} label="Assignee">
        <TaskAssigneeField task={task} />
      </Property>
      <Property
        icon={<Bot />}
        label={
          variant !== "default" && task.executedBy.kind === "agent"
            ? "Agent"
            : "Executed by"
        }
      >
        <TaskExecutorField task={task} />
      </Property>
    </>
  );
  const priority = (
    <Property icon={<SignalHigh />} label="Priority">
      <TaskPriorityIcon priority={task.priority} />
      <span className="task-execution-label">{task.priority}</span>
    </Property>
  );
  return (
    <section className="task-properties" aria-label="Task properties">
      <h3>{variant === "detail" ? "Details" : "Properties"}</h3>
      {variant === "detail" ? (
        <>
          <dl>
            <TaskScheduling task={task} canEdit={canEdit} />
            {assignee}
            {priority}
            <Property label="Due date">
              {dateInput("dueDate", "Due date")}
            </Property>
            <Property icon={<Tag />} label="Labels">
              {canEdit ? labels : task.labels.join(", ") || "None"}
            </Property>
          </dl>
          <details className="task-property-more">
            <DisclosureSummary>More details</DisclosureSummary>
            <dl>
              <Property label="Start date">
                {dateInput("startDate", "Start date")}
              </Property>
              <Property icon={<CircleUserRound />} label="Created by">
                {task.creator.kind === "agent" ? (
                  <FoldAvatar paused agentId={task.creator.id} size={22} />
                ) : null}
                {task.creator.name}
              </Property>
              <Property icon={<Clock3 />} label="Created on">
                <time dateTime={task.createdAt}>
                  {new Date(task.createdAt).toLocaleDateString(undefined, {
                    day: "numeric",
                    month: "short",
                    year: "numeric",
                  })}
                </time>
              </Property>
              <Property icon={<Flag />} label="Milestone">
                {milestoneLabel(task.milestoneType)}
              </Property>
            </dl>
          </details>
        </>
      ) : variant === "compact" ? (
        <>
          <dl>
            <TaskScheduling task={task} canEdit={canEdit} />
            {assignee}
            {priority}
            <Property icon={<Clock3 />} label="Due date">
              {dateInput("dueDate", "Due date")}
            </Property>
          </dl>
          <details className="task-property-more">
            <DisclosureSummary count={task.labels.length}>
              Labels
            </DisclosureSummary>
            <dl>
              <Property icon={<Tag />} label="Labels">
                {canEdit ? labels : task.labels.join(", ") || "None"}
              </Property>
            </dl>
          </details>
        </>
      ) : (
        <>
          <dl>
            <Property icon={<Tag />} label="Project">
              <TaskProjectLabel
                companyId={task.companyId}
                projectId={task.projectId}
                withLabel={false}
              />
            </Property>
            <Property icon={<CircleDot />} label="Status">
              <TaskStatusIcon status={task.status} />
              <span data-status={task.status}>{statusLabel}</span>
            </Property>
            {priority}
            <Property icon={<Tag />} label="Labels">
              {canEdit ? labels : task.labels.join(", ") || "None"}
            </Property>
          </dl>
          <dl>
            {assignee}
            <Property icon={<CircleUserRound />} label="Created by">
              {task.creator.kind === "agent" ? (
                <FoldAvatar paused agentId={task.creator.id} size={22} />
              ) : null}
              {task.creator.name}
            </Property>
          </dl>
          <dl>
            <TaskScheduling task={task} canEdit={canEdit} />
            <Property label="Start date">
              {dateInput("startDate", "Start date")}
            </Property>
            <Property label="Due date">
              {dateInput("dueDate", "Due date")}
            </Property>
            <Property icon={<Clock3 />} label="Created on">
              <time dateTime={task.createdAt}>
                {new Date(task.createdAt).toLocaleDateString(undefined, {
                  day: "numeric",
                  month: "short",
                  year: "numeric",
                })}
              </time>
            </Property>
          </dl>
          <dl>
            <Property icon={<Flag />} label="Milestone">
              {milestoneLabel(task.milestoneType)}
            </Property>
          </dl>
        </>
      )}
    </section>
  );
}

function Property({
  icon,
  label,
  children,
}: {
  icon?: ReactNode;
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="task-property">
      <dt>
        <span
          aria-hidden="true"
          className="grid size-[15px] shrink-0 place-items-center"
        >
          {icon}
        </span>
        {label}
      </dt>
      <dd>{children}</dd>
    </div>
  );
}
