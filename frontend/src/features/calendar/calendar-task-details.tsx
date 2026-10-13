import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";
import { useState, type CSSProperties } from "react";
import { Link } from "react-router";

import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { hasProjectPermission } from "@/shared/permissions";
import {
  CalendarDays,
  ChevronDown,
  Pencil,
  Repeat2,
  Users,
} from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import {
  CalendarEventDialog,
  type CalendarEventAnchor,
} from "./calendar-event-dialog";
import { useCalendarTheme } from "./calendar-theme";
import { taskScheduleStateLabel } from "./calendar-dates";
import { TaskRunTime, localTaskTime } from "./task-schedule-fields";
import { TaskScheduleEditor } from "./task-schedule-editor";
import { TaskAssignmentResponse } from "./task-assignment-response";
import {
  useTaskScheduleQuery,
  useChangeTaskOccurrenceMutation,
  type TaskCalendarOccurrence,
} from "./task-calendar-api";
import "./task-calendar.css";

export function CalendarTaskDetails({
  occurrence,
  anchor,
  onClose,
}: {
  occurrence: TaskCalendarOccurrence;
  anchor: CalendarEventAnchor;
  onClose: () => void;
}) {
  const theme = useCalendarTheme();
  const query = useGetWorkspaceTaskQuery(
    { companyId: occurrence.task.companyId, taskId: occurrence.task.id },
    { refetchOnMountOrArgChange: true },
  );
  const schedule = useTaskScheduleQuery({
    companyId: occurrence.task.companyId,
    taskId: occurrence.task.id,
  });
  const task = query.currentData;
  const [mode, setMode] = useState<"details" | "edit" | "series" | "cancel">(
    "details",
  );
  const [more, setMore] = useState(false);
  const [runAt, setRunAt] = useState(localTaskTime(occurrence.runAt));
  const [title, setTitle] = useState(occurrence.task.title);
  const [description, setDescription] = useState(occurrence.task.description);
  const [baseline, setBaseline] = useState(occurrence);
  const [change, mutation] = useChangeTaskOccurrenceMutation();
  const [error, setError] = useState("");
  const canEdit =
    task && hasProjectPermission(task.access, "project.tasks.write");
  const canChangeOccurrence =
    canEdit &&
    (!occurrence.materialized || task.scheduleDisposition === "scheduled");
  const repeating = schedule.currentData?.trigger.type === "cron";
  const shownTask = occurrence.materialized
    ? (task ?? occurrence.task)
    : occurrence.task;
  const shownRunAt = occurrence.materialized
    ? (task?.scheduledAt ?? occurrence.runAt)
    : occurrence.runAt;
  function editOccurrence() {
    if (!task) return;
    // Virtual occurrences carry the series brief, which can differ from the
    // template Task. Freeze its content, revision and target together.
    setBaseline({
      ...occurrence,
      task: shownTask,
      runAt: shownRunAt,
      occurrenceAt: occurrence.materialized
        ? (shownTask.scheduleOccurrenceAt ?? occurrence.occurrenceAt)
        : occurrence.occurrenceAt,
    });
    setTitle(shownTask.title);
    setDescription(shownTask.description);
    setRunAt(localTaskTime(shownRunAt));
    setError("");
    setMode("edit");
  }
  async function save(cancel: boolean) {
    if (!task) return;
    setError("");
    try {
      await change({
        companyId: baseline.task.companyId,
        taskId: baseline.task.id,
        input: {
          expectedRevision: baseline.task.revision,
          ...(!cancel
            ? {
                ...(title !== baseline.task.title ? { title } : {}),
                ...(description !== baseline.task.description
                  ? { description }
                  : {}),
                runAt: new Date(runAt).toISOString(),
              }
            : {}),
          occurrenceAt: baseline.occurrenceAt,
          cancel,
        },
      }).unwrap();
      toast.success(cancel ? "Occurrence cancelled" : "Task updated");
      onClose();
    } catch (caught) {
      setError(getBoardlyQueryErrorMessage(caught));
    }
  }
  return (
    <CalendarEventDialog
      anchor={anchor}
      busy={mutation.isLoading}
      style={
        { ...theme, "--event-color": "var(--calendar-sky)" } as CSSProperties
      }
      className={
        mode === "details"
          ? "calendar-detail-dialog"
          : mode === "cancel"
            ? "calendar-editor calendar-editor--deleting"
            : "calendar-editor"
      }
      title={
        mode === "cancel"
          ? "Cancel this occurrence?"
          : mode === "series"
            ? "Edit task schedule"
            : shownTask.title
      }
      description="Scheduled task"
      onClose={onClose}
      headingActions={
        mode === "details" && canChangeOccurrence ? (
          <button
            type="button"
            className="personal-icon-button"
            aria-label="Edit task occurrence"
            disabled={query.isFetching}
            onClick={editOccurrence}
          >
            <Pencil size={18} />
          </button>
        ) : undefined
      }
    >
      {query.isError ? (
        <p
          role="alert"
          className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
        >
          {getBoardlyQueryErrorMessage(query.error)}
        </p>
      ) : !task ? (
        <p role="status">Loading task…</p>
      ) : mode === "series" ? (
        <div className="personal-form calendar-task-series">
          <h3>Edit {repeating ? "repeating schedule" : "schedule"}</h3>
          <p className="personal-note">
            Applies to future occurrences. Work already running continues.
          </p>
          {schedule.isError ? (
            <p
              className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
              role="alert"
            >
              {getBoardlyQueryErrorMessage(schedule.error)}
            </p>
          ) : schedule.currentData ? (
            <TaskScheduleEditor
              appearance="calendar"
              task={task}
              schedule={schedule.currentData}
              onClose={onClose}
            />
          ) : (
            <p role="status">Loading schedule…</p>
          )}
        </div>
      ) : mode === "cancel" ? (
        <>
          <p className="personal-note">
            “{shownTask.title}” won’t run at this time.{" "}
            {repeating
              ? "The rest of the series stays scheduled."
              : "Its task and chat will remain available."}
          </p>
          {error && (
            <p
              role="alert"
              className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
            >
              {error}
            </p>
          )}
          <div className="personal-dialog-actions">
            <button
              className="personal-button"
              disabled={mutation.isLoading}
              onClick={() => setMode("edit")}
            >
              Keep task
            </button>
            <button
              className="personal-button personal-button--danger"
              disabled={mutation.isLoading}
              onClick={() => void save(true)}
            >
              {mutation.isLoading ? "Cancelling…" : "Cancel occurrence"}
            </button>
          </div>
        </>
      ) : mode === "edit" ? (
        <form
          className="personal-form"
          onSubmit={(event) => {
            event.preventDefault();
            void save(false);
          }}
        >
          <fieldset
            className="calendar-task-fields"
            disabled={mutation.isLoading}
          >
            <div className="personal-tabs">
              <button type="button" aria-pressed="true">
                Task
              </button>
            </div>
            <input
              className="personal-title-input"
              aria-label="Occurrence title"
              autoFocus
              required
              maxLength={200}
              placeholder="Task title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
            />
            <TaskRunTime value={runAt} onChange={setRunAt} />
            <p className="calendar-timezone-summary">
              {Intl.DateTimeFormat()
                .resolvedOptions()
                .timeZone.replaceAll("_", " ")}
            </p>
            <label>
              Notes
              <textarea
                aria-label="Occurrence description"
                placeholder="Add a little context…"
                maxLength={50000}
                rows={3}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            </label>
            {repeating && (
              <p className="personal-note">
                Only this occurrence changes. Use Edit series to update future
                run times.
              </p>
            )}
            {error && (
              <p
                role="alert"
                className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
              >
                {error}
              </p>
            )}
            <footer className="personal-dialog-actions">
              <button
                type="button"
                className="calendar-text-button calendar-task-cancel"
                onClick={() => setMode("cancel")}
              >
                Cancel occurrence
              </button>
              <button
                type="button"
                className="personal-button"
                onClick={() => setMode("details")}
              >
                Back
              </button>
              <button className="personal-button personal-button--primary">
                {mutation.isLoading ? "Saving…" : "Save"}
              </button>
            </footer>
          </fieldset>
        </form>
      ) : (
        <div className="calendar-event-details">
          <p>
            <CalendarDays size={18} />
            <span>
              {new Date(shownRunAt).toLocaleDateString([], {
                dateStyle: "medium",
              })}
              <small>
                {new Date(shownRunAt).toLocaleTimeString([], {
                  hour: "numeric",
                  minute: "2-digit",
                })}
                {occurrence.materialized &&
                task.scheduleDisposition &&
                task.scheduleDisposition !== "scheduled"
                  ? ` · ${taskScheduleStateLabel(task)}`
                  : ""}
                {schedule.currentData?.paused ? " · Schedule paused" : ""}
              </small>
            </span>
          </p>
          <p>
            <Users size={18} />
            <span>
              {task.assignee.name}
              <small>
                {task.executedBy.kind === "agent"
                  ? `Run with ${task.executedBy.name}`
                  : "Human work"}
              </small>
            </span>
          </p>
          {canEdit && (
            <button
              type="button"
              className="calendar-task-series-link"
              onClick={() => setMode("series")}
            >
              <Repeat2 size={18} />
              <span>{repeating ? "Edit series" : "Edit schedule"}</span>
            </button>
          )}
          <button
            className="calendar-more-toggle"
            type="button"
            aria-expanded={more}
            onClick={() => setMore(!more)}
          >
            {more ? "Fewer details" : "More details"}
            <ChevronDown size={14} />
          </button>
          {more && (
            <div className="calendar-detail-meta">
              <p>
                {task.key} · {task.status.replaceAll("_", " ")}
              </p>
              {!occurrence.materialized && (
                <p>This occurrence becomes a new task when its time arrives.</p>
              )}
              {shownTask.description && (
                <p className="calendar-event-description">
                  {shownTask.description}
                </p>
              )}
            </div>
          )}
          <TaskAssignmentResponse task={task} />
          <div className="personal-dialog-actions">
            <Link
              className="personal-button"
              onClick={onClose}
              to={`/projects/${task.projectId}/tasks?taskId=${task.id}`}
            >
              {occurrence.materialized ? "Open task" : "Open series task"}
            </Link>
            <button className="personal-button" onClick={onClose}>
              Close
            </button>
          </div>
        </div>
      )}
    </CalendarEventDialog>
  );
}
