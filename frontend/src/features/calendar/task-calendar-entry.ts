import type { CalendarOccurrence } from "./calendar-api";
import type { TaskCalendarOccurrence } from "./task-calendar-api";
import { hasProjectPermission } from "@/shared/permissions";
import { dateKey } from "./calendar-dates";
import { localTaskTime } from "./task-schedule-fields";

// Calendar layout projection only. Task writes always use the Task API; this
// object is never sent to the Event/Reminder endpoints.
export function taskCalendarEntry(
  occurrence: TaskCalendarOccurrence,
): CalendarOccurrence {
  const { task } = occurrence;
  const start = new Date(occurrence.runAt);
  const end = new Date(start.getTime() + 30 * 60000);
  return {
    id: taskCalendarEntryId(occurrence),
    type: "event",
    taskOccurrence: occurrence,
    title: task.title,
    description: task.description,
    location: occurrence.schedule.paused ? "Task · paused" : "Task",
    color: "sky",
    allDay: false,
    startLocal: localTaskTime(start),
    endLocal: localTaskTime(end),
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    recurrence: "none",
    recurrenceInterval: 1,
    recurrenceWeekdays: [],
    recurrenceMonthlyMode: "day_of_month",
    recurrenceCount: null,
    recurrenceUntil: null,
    reminderMinutes: null,
    revision: task.revision,
    seriesStartLocal: localTaskTime(start),
    seriesEndLocal: localTaskTime(end),
    occurrenceDate: dateKey(start),
    startAt: start.toISOString(),
    endAt: end.toISOString(),
    completed:
      task.status === "done" ||
      task.scheduleDisposition === "cancelled" ||
      task.scheduleDisposition === "skipped" ||
      task.scheduleDisposition === "missed",
    calendarId: task.projectId,
    calendarType: "company",
    calendarName: "Tasks",
    companyId: task.companyId,
    organizerUserId: task.assignee.id,
    canEdit:
      hasProjectPermission(task.access, "project.tasks.write") &&
      (!occurrence.materialized || task.scheduleDisposition === "scheduled"),
    responseStatus: "",
    attendees: [],
    organizerName: task.assignee.name,
  };
}

export function taskCalendarEntryId(occurrence: TaskCalendarOccurrence) {
  return occurrence.materialized
    ? `task:${occurrence.task.id}`
    : `preview:${occurrence.schedule.id}:${occurrence.occurrenceAt}`;
}
