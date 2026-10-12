import type {
  SchedulePreviewResponse,
  ScheduleRunRecord,
  ScheduledTaskStatus,
  ScheduleTrigger,
} from "@/shared/api/schedule.service";
import type { StatusPillTone } from "@/shared/ui/status-pill";

const cronLabels = new Map<string, string>([
  ["0 * * * *", "Every hour"],
  ["0 8 * * *", "Every day at 8:00 AM"],
  ["0 9 * * *", "Every day at 9:00 AM"],
  ["0 9 * * 1-5", "Weekdays at 9:00 AM"],
  ["0 9 * * 1", "Mondays at 9:00 AM"],
  ["0 9 1 * *", "The first day of each month at 9:00 AM"],
]);

export function formatScheduleTrigger(trigger: ScheduleTrigger) {
  if (trigger.type === "once") {
    return `Once · ${formatScheduleDateTime(trigger.runAt)}`;
  }

  const schedule = cronLabels.get(trigger.expression) ?? trigger.expression;
  return `${schedule} · ${trigger.timeZone}`;
}

// The review shows the server's plain words for a cron trigger. A once
// trigger keeps the local date and time, since the server words it in UTC.
export function formatPreviewTrigger(preview: SchedulePreviewResponse) {
  if (preview.normalizedTrigger.type === "cron" && preview.humanSchedule) {
    return preview.humanSchedule;
  }
  return formatScheduleTrigger(preview.normalizedTrigger);
}

export function formatScheduleDateTime(
  value: string | null | undefined,
  timeZone?: string,
) {
  if (!value) return "Not scheduled";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unknown time";

  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      ...(timeZone ? { timeZone } : {}),
    }).format(date);
  } catch {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(date);
  }
}

export function scheduleStatusLabel(status: ScheduledTaskStatus) {
  if (status === "cancelled") return "Cancelled";
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function scheduleStatusTone(
  status: ScheduledTaskStatus,
): StatusPillTone {
  if (status === "active") return "success";
  if (status === "completed") return "success";
  if (status === "paused") return "paused";
  if (status === "draft") return "draft";
  return "muted";
}

export function scheduleRunLabel(run: ScheduleRunRecord) {
  if (run.runStatus) {
    return run.runStatus.replace(/_/g, " ");
  }
  return run.disposition.replace(/_/g, " ");
}

export function scheduleRunTone(run: ScheduleRunRecord): StatusPillTone {
  const status = run.runStatus?.toLowerCase();
  if (status === "succeeded" || status === "completed") return "success";
  if (status === "failed" || status === "cancelled" || status === "canceled") {
    return "danger";
  }
  if (status === "queued" || status === "running" || status === "claimed") {
    return "active";
  }
  if (run.disposition === "needs_attention") return "warning";
  return "muted";
}
