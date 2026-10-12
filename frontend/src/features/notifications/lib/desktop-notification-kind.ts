import type { DesktopNotificationKind } from "@/app/desktop/desktop-environment.service";
import type { NotificationKind } from "@/features/notifications/model/notification";

export function toDesktopNotificationKind(
  kind: NotificationKind,
): DesktopNotificationKind {
  if (kind === "discussion_mention") return "chat_mention";
  if (
    kind === "invitation_renewal" ||
    kind === "huddle_missed" ||
    kind === "project_access_granted"
  )
    return "question_request";
  if (kind === "run_completed") return "work_completed";
  // Offline machines use the category for failed work and unavailable services.
  if (kind === "run_failed" || kind === "runtime_offline") return "work_failed";
  if (kind === "task_run_requested" || kind === "task_handoff") {
    return "task_assigned";
  }
  if (kind === "analytics_budget") return "blocker";
  return kind;
}
