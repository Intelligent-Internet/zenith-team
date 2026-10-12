import {
  notificationKindLabels,
  type NotificationKind,
} from "@/features/notifications/model/notification";
import { cx } from "@/shared/lib/cx";

const kindColors: Record<NotificationKind, string> = {
  calendar_update: "text-[var(--workspace-native-info)]",
  calendar_reminder: "text-[var(--workspace-native-info)]",
  approval_request: "text-[var(--workspace-native-approval)]",
  blocker: "text-[var(--workspace-native-danger)]",
  question_request: "text-[var(--workspace-native-accent)]",
  run_completed: "text-[var(--workspace-native-success)]",
  run_failed: "text-[var(--workspace-native-danger)]",
  runtime_offline: "text-[var(--workspace-native-danger)]",
  invitation_renewal: "text-[var(--workspace-native-info)]",
  huddle_missed: "text-[var(--workspace-native-info)]",
  task_assigned: "text-[var(--workspace-native-info)]",
  discussion_mention: "text-[var(--workspace-native-accent)]",
  task_run_requested: "text-[var(--workspace-native-accent)]",
  task_handoff: "text-[var(--workspace-native-info)]",
  analytics_budget: "text-[var(--workspace-native-warning)]",
  project_access_granted: "text-[var(--workspace-native-info)]",
};

export function NotificationKindLabel({ kind }: { kind: NotificationKind }) {
  return (
    <span
      className={cx(
        "text-[12px] font-bold uppercase tracking-[0.12em]",
        kindColors[kind],
      )}
    >
      {notificationKindLabels[kind]}
    </span>
  );
}
