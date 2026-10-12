import {
  Ban,
  Circle,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Eye,
  CircleX,
  TimerStart,
  SignalHigh,
  SignalLow,
  SignalMedium,
} from "@/shared/ui/icons";
import type { TaskDto } from "@/shared/api/types";

export function TaskStatusIcon({ status }: { status: TaskDto["status"] }) {
  const Icon = {
    backlog: CircleDashed,
    todo: Circle,
    in_progress: TimerStart,
    in_review: Eye,
    done: CircleCheck,
    blocked: Ban,
    failed: CircleAlert,
    error: CircleAlert,
    cancelled: CircleX,
  }[status];
  return (
    <Icon
      aria-hidden="true"
      size={15}
      className="task-status-icon"
      data-status={status}
    />
  );
}

export function TaskPriorityIcon({
  priority,
}: {
  priority: TaskDto["priority"];
}) {
  const Icon = {
    urgent: CircleAlert,
    high: SignalHigh,
    medium: SignalMedium,
    low: SignalLow,
  }[priority];
  return (
    <Icon
      aria-hidden="true"
      size={14}
      className="task-priority-icon"
      data-priority={priority}
    />
  );
}
