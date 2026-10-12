import {
  taskWaitsForPrerequisites,
  taskWorkStatusLabel,
} from "@/features/company-plan/model/task-work-state";
import type {
  CompanyPlanMilestone,
  CompanyPlanMilestoneTask,
} from "@/features/company-plan/model/company-plan-milestone-model";
import type { TaskStatusDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Icon } from "@/shared/ui/icon";

const taskStatusAppearance: Record<
  TaskStatusDto,
  { className: string; icon: string; label: string }
> = {
  backlog: {
    className: "bg-white/14 text-white/78",
    icon: "hourglass",
    label: "Available",
  },
  blocked: {
    className: "bg-[#ffb454]/22 text-[#ffd08a]",
    icon: "alert",
    label: "Blocked",
  },
  error: {
    className: "bg-[#ff8a57]/22 text-[#ffc0a3]",
    icon: "alert",
    label: "Technical error",
  },
  failed: {
    className: "bg-[#ff6b75]/20 text-[#ffadb3]",
    icon: "close-circle",
    label: "Failed",
  },
  cancelled: {
    className: "bg-[#ff6b75]/20 text-[#ffadb3]",
    icon: "close-circle",
    label: "Cancelled",
  },
  done: {
    className: "bg-[#69d267]/20 text-[#9bea9a]",
    icon: "checkmark",
    label: "Completed",
  },
  in_progress: {
    className: "bg-[#6aa9ff]/22 text-[#a4cdff]",
    icon: "arrow-right",
    label: "Working",
  },
  in_review: {
    className: "bg-[#b388ff]/22 text-[#d2bcff]",
    icon: "eye",
    label: "In review",
  },
  todo: {
    className: "bg-yellow/20 text-[#ffe17a]",
    icon: "play",
    label: "Available",
  },
};

export function MilestoneBoard({
  milestones,
  onTaskSelect,
  selectedTaskId,
}: {
  milestones: CompanyPlanMilestone[];
  onTaskSelect: (item: CompanyPlanMilestoneTask) => void;
  selectedTaskId: string | undefined;
}) {
  return (
    <section
      aria-label="Tasks by milestone"
      className="workspace-work-board flex h-full min-h-0 overflow-x-auto overflow-y-hidden border"
    >
      {milestones.map((milestone, index) => (
        <article
          key={milestone.type}
          className={cx(
            "flex h-full min-h-0 w-[324px] shrink-0 flex-col",
            index < milestones.length - 1 && "border-r",
          )}
        >
          <header className="workspace-work-board__column-header flex h-[62px] shrink-0 items-center justify-between border-b px-4">
            <span className="workspace-work-board__column-label border px-3 py-1 font-mono text-[11px] uppercase tracking-[0.12em]">
              {milestone.name}
            </span>
            <span className="font-mono text-xs text-[var(--workspace-native-text-muted)]">
              {milestone.completedTaskCount}/{milestone.tasks.length}
            </span>
          </header>

          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            <div className="grid gap-3">
              {milestone.tasks.map((item) => (
                <MilestoneTaskCard
                  key={item.task.id}
                  item={item}
                  selected={item.task.id === selectedTaskId}
                  onSelect={onTaskSelect}
                />
              ))}
            </div>
          </div>
        </article>
      ))}
    </section>
  );
}

function MilestoneTaskCard({
  item,
  onSelect,
  selected,
}: {
  item: CompanyPlanMilestoneTask;
  onSelect: (item: CompanyPlanMilestoneTask) => void;
  selected: boolean;
}) {
  const { agent, task } = item;
  const isDependencyLocked = taskWaitsForPrerequisites(task);
  const appearance = {
    ...taskStatusAppearance[task.status],
    ...(isDependencyLocked
      ? { className: "bg-white/14 text-white/72", icon: "lock" }
      : {}),
    label: taskWorkStatusLabel(task),
  };

  return (
    <button
      aria-label={`View task ${task.title}`}
      aria-pressed={selected}
      className={cx(
        "workspace-work-task-card group relative min-h-[112px] w-full cursor-pointer overflow-hidden border p-3 text-left transition",
        selected && "is-selected",
      )}
      type="button"
      onClick={() => onSelect(item)}
    >
      <div className="flex items-start gap-3">
        <span
          aria-hidden="true"
          className="workspace-work-task-card__avatar grid size-10 shrink-0 place-items-center overflow-hidden border text-white"
          style={{ backgroundColor: `${agent.accentColor}42` }}
        >
          {agent.kind === "agent" ? (
            <FoldAvatar agentId={agent.id} paused size={40} />
          ) : (
            <Icon name="user-circle" className="size-6" />
          )}
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="m-0 line-clamp-2 text-sm font-bold leading-[18px] text-[var(--workspace-native-text)]">
            {task.title}
          </h3>
          <p className="m-0 mt-1 line-clamp-1 text-[11px] leading-4 text-[var(--workspace-native-text-muted)]">
            {agent.name}
          </p>
        </div>
      </div>

      <div className="mt-3 flex items-center justify-between gap-3">
        <span
          className={cx(
            "workspace-task-status inline-flex min-w-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-bold",
            appearance.className,
          )}
          title={appearance.label}
          data-status={isDependencyLocked ? "locked" : task.status}
        >
          <Icon name={appearance.icon} className="size-3.5 shrink-0" />
          <span className="truncate">{appearance.label}</span>
        </span>
      </div>
    </button>
  );
}
