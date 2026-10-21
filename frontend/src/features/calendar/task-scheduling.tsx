import * as Popover from "@radix-ui/react-popover";
import { useState } from "react";
import { Link } from "react-router";
import type { TaskDto } from "@/shared/api/types";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import { hasProjectPermission } from "@/shared/permissions";
import { ArrowUpRight, CalendarDays, Repeat2 } from "@/shared/ui/icons";
import { dateKey, taskScheduleStateLabel } from "./calendar-dates";
import { useTaskScheduleQuery } from "./task-calendar-api";
import { TaskScheduleEditor } from "./task-schedule-editor";
import { TaskAssignmentResponse } from "./task-assignment-response";
import "./task-calendar.css";

export function TaskScheduling({
  task,
  canEdit = true,
}: {
  task: TaskDto;
  canEdit?: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const editable =
    canEdit && hasProjectPermission(task.access, "project.tasks.write");
  const schedule = useTaskScheduleQuery(
    { companyId: task.companyId, taskId: task.id },
    { skip: !task.scheduleId },
  );
  const label = task.scheduledAt
    ? new Date(task.scheduledAt).toLocaleString([], {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      })
    : "Set run time";
  const repeating = schedule.currentData?.trigger.type === "cron";
  return (
    <>
      <div className="task-property">
        <dt>
          <span
            aria-hidden="true"
            className="grid size-[15px] shrink-0 place-items-center"
          >
            <CalendarDays />
          </span>
          Run schedule
        </dt>
        <dd className="task-schedule-property flex-wrap gap-[4px]">
          <Popover.Root open={editing} onOpenChange={setEditing}>
            <Popover.Trigger
              type="button"
              className="task-property-date-trigger"
              disabled={!editable}
              aria-label={`Run schedule, ${label}`}
            >
              {repeating ? <Repeat2 size={15} /> : <CalendarDays size={15} />}
              {label}
            </Popover.Trigger>
            <Popover.Portal
              container={document.querySelector(".zenith-team-workspace-theme")}
            >
              <Popover.Content
                className="task-schedule-popover"
                sideOffset={6}
                collisionPadding={12}
                align="end"
                aria-label="Task schedule"
              >
                <h3>{task.scheduleId ? "Edit schedule" : "Schedule task"}</h3>
                <p className="task-schedule-note">
                  {task.scheduleId
                    ? "Applies to future occurrences. Running work continues."
                    : "Runs on the assignee’s computer after they accept."}
                </p>
                {schedule.isError ? (
                  <p
                    className="text-workspace-danger text-[length:12px] leading-[1.5]"
                    role="alert"
                  >
                    {getZenithTeamQueryErrorMessage(schedule.error)}
                  </p>
                ) : !task.scheduleId || schedule.currentData ? (
                  <TaskScheduleEditor
                    key={task.id}
                    task={task}
                    schedule={schedule.currentData}
                    onClose={() => setEditing(false)}
                  />
                ) : (
                  <p role="status">Loading schedule…</p>
                )}
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
          {task.scheduledAt && (
            <Link
              className="task-schedule-calendar-link"
              to={`/calendar?date=${dateKey(new Date(task.scheduledAt))}&taskId=${task.id}`}
              aria-label="View task in Calendar"
              title="View in Calendar"
            >
              <ArrowUpRight size={14} />
            </Link>
          )}
          {task.scheduleDisposition && (
            <small>
              {schedule.currentData?.paused &&
              task.scheduleDisposition === "scheduled"
                ? "Paused"
                : taskScheduleStateLabel(task)}
            </small>
          )}
        </dd>
      </div>
      {task.assignmentStatus !== "accepted" && (
        <div className="task-property">
          <dt>Assignment</dt>
          <dd>
            <TaskAssignmentResponse task={task} />
          </dd>
        </div>
      )}
    </>
  );
}
