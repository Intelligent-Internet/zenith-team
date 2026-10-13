import type { TaskDependencyPresentationItem } from "@/features/task-dependencies/model/use-task-dependency-presentation";
import "./task-dependency-summary.css";

type TaskDependencySummaryProps = {
  blockedBy: TaskDependencyPresentationItem[];
  isError: boolean;
  isLoading: boolean;
  onTaskSelect: (item: TaskDependencyPresentationItem) => void;
};

export function TaskDependencySummary({
  blockedBy,
  isError,
  isLoading,
  onTaskSelect,
}: TaskDependencySummaryProps) {
  if (isLoading) {
    return <DependencyState label="Loading dependency graph…" />;
  }
  if (isError) {
    return (
      <DependencyState label="Dependency graph is unavailable." tone="error" />
    );
  }
  if (blockedBy.length === 0) {
    return <DependencyState label="No task dependencies." />;
  }

  return (
    <div className="task-dependency-summary">
      <DependencyGroup items={blockedBy} onTaskSelect={onTaskSelect} />
    </div>
  );
}

function DependencyGroup({
  items,
  onTaskSelect,
}: {
  items: TaskDependencyPresentationItem[];
  onTaskSelect: (item: TaskDependencyPresentationItem) => void;
}) {
  if (items.length === 0) {
    return null;
  }
  return (
    <div>
      <div className="space-y-2">
        {items.map((item) => (
          <button
            className="task-dependency-summary-item"
            key={item.id}
            type="button"
            onClick={() => onTaskSelect(item)}
          >
            <div className="flex items-center justify-between gap-[10px] text-workspace-muted text-workspace-caption leading-workspace-caption">
              <span>{item.taskLabel}</span>
              <span
                className="task-dependency-summary-status"
                data-tone={
                  item.isUnresolved
                    ? "blocking"
                    : item.taskStatus === "done"
                      ? "done"
                      : "neutral"
                }
              >
                {item.isUnresolved
                  ? "Blocking"
                  : item.taskStatus === "done"
                    ? "Done"
                    : item.taskStatus.replaceAll("_", " ")}
              </span>
            </div>
            <p
              className="[display:-webkit-box] [-webkit-box-orient:vertical] [-webkit-line-clamp:2] overflow-hidden wrap-anywhere m-[8px_0_0] text-workspace-body leading-workspace-body font-[500]"
              title={item.taskTitle}
            >
              {item.taskTitle}
            </p>
            <span className="task-dependency-summary-assignee">
              {item.assigneeName}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

function DependencyState({
  label,
  tone = "default",
}: {
  label: string;
  tone?: "default" | "error";
}) {
  return (
    <p className="task-dependency-summary-state" data-tone={tone}>
      {label}
    </p>
  );
}
