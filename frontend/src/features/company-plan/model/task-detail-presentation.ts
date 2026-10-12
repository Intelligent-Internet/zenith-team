import type { ActivityEventSummary, TaskStatusDto } from "@/shared/api/types";
import {
  activitySnapshotFieldLabel,
  changedActivityFields,
} from "@/shared/ui/activity-snapshot-details";
import { taskStatusLabels } from "./task-work-state";

export function taskActivityDescription(event: ActivityEventSummary) {
  if (event.action === "task.created") return "created this task";
  if (event.action !== "task.updated") {
    return event.action.replace(/^task\./, "").replaceAll("_", " ");
  }
  if (event.data.schemaVersion !== 1) return "updated this task";

  const changed = changedActivityFields(event.data.before, event.data.after);
  const status = event.data.after?.status;
  if (
    changed.includes("status") &&
    typeof status === "string" &&
    Object.hasOwn(taskStatusLabels, status)
  ) {
    return `changed status to ${taskStatusLabels[status as TaskStatusDto]}${
      changed.length > 1 ? " and updated other details" : ""
    }`;
  }
  const fields = [...new Set(changed.map(activitySnapshotFieldLabel))];
  return fields.length ? `updated ${fields.join(", ")}` : "updated this task";
}

export function formatTaskEventTime(value: string) {
  const date = new Date(value);
  const today = new Date();
  const day =
    date.toDateString() === today.toDateString()
      ? "Today"
      : date.toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
          ...(date.getFullYear() !== today.getFullYear()
            ? { year: "numeric" as const }
            : {}),
        });
  const time = date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
  return `${day}, ${time}`;
}
