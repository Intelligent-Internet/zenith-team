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
  if (status === "completed") return "Time recorded";
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function scheduleStatusTone(
  status: ScheduledTaskStatus,
): StatusPillTone {
  if (status === "active") return "success";
  if (status === "completed") return "muted";
  if (status === "paused") return "paused";
  if (status === "draft") return "draft";
  return "muted";
}

export function scheduleRunLabel(run: ScheduleRunRecord) {
  if (run.runStatus === "completed" || run.runStatus === "failed") {
    const outcome =
      run.runStatus === "completed"
        ? "Completed"
        : run.runFailureReason === "queued_expired"
          ? "Expired while waiting"
          : "Failed";
    if (run.disposition === "cancelled")
      return `${outcome} · occurrence cancelled`;
    if (run.disposition === "needs_attention")
      return `${outcome} · needs review`;
    return outcome;
  }
  if (run.disposition === "superseded") return "Replaced by a newer occurrence";
  if (run.disposition === "skipped") return "Skipped";
  const running = ["running", "dispatched", "waiting_local_directory"].includes(
    run.runStatus ?? "",
  );
  if (run.disposition === "cancelled")
    return running ? "Running · future retries cancelled" : "Cancelled";
  if (run.disposition === "needs_attention")
    return running ? "Running · needs review" : "Needs review";
  if (run.runStatus === "queued" || run.runStatus === "deferred") {
    if (run.runtimeUnusableReason === "below_minimum_version")
      return "Update your computer app";
    if (run.runtimeUnusableReason === "not_executable")
      return "Computer cannot run this Agent";
    return run.runtimeStatus === "offline"
      ? "Waiting for your computer"
      : "Waiting to start";
  }
  return (run.runStatus ?? run.disposition).replace(/_/g, " ");
}

export function scheduleRunTone(run: ScheduleRunRecord): StatusPillTone {
  if (run.runStatus === "completed") return "success";
  if (run.runStatus === "failed") return "danger";
  if (
    run.disposition === "superseded" ||
    run.disposition === "skipped" ||
    run.disposition === "cancelled"
  )
    return "muted";
  if (run.disposition === "needs_attention") return "warning";
  if (
    [
      "queued",
      "deferred",
      "running",
      "dispatched",
      "waiting_local_directory",
    ].includes(run.runStatus ?? "")
  )
    return "active";
  return "muted";
}

export function scheduleRunEffectNotice(
  effect:
    "none" | "waiting_run_cancelled" | "running_run_continues" | undefined,
) {
  if (effect === "waiting_run_cancelled")
    return "A run waiting for your computer was cancelled.";
  if (effect === "running_run_continues")
    return "The current run continues. It will not retry automatically.";
  return "";
}

export function scheduleRunReason(run: ScheduleRunRecord) {
  const reason = run.reason ?? run.runCancelReason;
  const descriptions: Record<string, string> = {
    newer_occurrence: "A newer occurrence replaced this waiting run.",
    run_active: "The previous run was still active when this time arrived.",
    schedule_paused:
      "The schedule was paused. No further attempts will run for this occurrence.",
    schedule_cancelled: "The schedule was cancelled.",
    schedule_edited:
      "The schedule was edited. Future runs use the reviewed definition.",
    definition_updated:
      "This due time was skipped when the definition changed.",
    source_changed: "Review the selected connections before resuming.",
    agent_unavailable: "Set up an available Agent, then review the schedule.",
    trigger_invalid: "Edit the timing and review the next occurrences.",
    firing_failed:
      "This occurrence could not be prepared. Review the schedule to resume.",
    queued_expired: "This one-time run waited seven days without starting.",
  };
  const details: string[] = [];
  if (reason) details.push(descriptions[reason] ?? reason.replace(/_/g, " "));
  if (run.runFailureReason && run.runFailureReason !== reason) {
    details.push(
      `Run failure: ${descriptions[run.runFailureReason] ?? run.runFailureReason.replace(/_/g, " ")}`,
    );
  }
  return details.length > 0 ? details.join(" ") : null;
}
