import { TaskAssignee } from "./task-assignee";
import { useTaskLabel } from "@/shared/lib/task-label";
import { useState, type ReactNode } from "react";
import {
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  Circle,
  CircleHelp,
  Clock3,
  GitBranch,
  Sparkles,
} from "@/shared/ui/icons";
import type {
  WorkNextUpDependentTask,
  WorkNextUpItem,
  WorkNextUpRelatedTask,
} from "@/features/company-plan/model/work-next-up-model";
import {
  taskStatusLabels,
  taskWorkStatusLabel,
} from "@/features/company-plan/model/task-work-state";
import "@/features/company-plan/styles/work-next-up-insight.css";

type WorkNextUpInsightProps = {
  item: WorkNextUpItem | undefined;
  compact?: boolean;
  isLoading: boolean;
  isError: boolean;
  onSelectTask: (taskId: string) => void;
  onRetry: () => void;
};

export function WorkNextUpInsight({
  compact = false,
  item,
  isLoading,
  isError,
  onSelectTask,
  onRetry,
}: WorkNextUpInsightProps) {
  if (isError) {
    return (
      <section className="work-next-up-insight" aria-label="Task readiness">
        <h3>
          <CircleHelp aria-hidden="true" size={15} />
          Task relationships unavailable
        </h3>
        <p className="work-next-up-insight__message" role="alert">
          Prerequisites could not be loaded. Readiness and following tasks
          cannot be verified yet.
        </p>
        <button
          className="work-next-up-insight__retry"
          type="button"
          onClick={onRetry}
        >
          Retry relationships
        </button>
      </section>
    );
  }

  if (isLoading || !item) {
    return (
      <section className="work-next-up-insight" aria-label="Task readiness">
        <p className="work-next-up-insight__message" role="status">
          Checking task relationships…
        </p>
      </section>
    );
  }

  return (
    <TaskReadinessInsight
      key={item.task.id}
      compact={compact}
      item={item}
      onSelectTask={onSelectTask}
    />
  );
}

function TaskReadinessInsight({
  compact,
  item,
  onSelectTask,
}: Pick<WorkNextUpInsightProps, "onSelectTask" | "compact"> & {
  item: WorkNextUpItem;
}) {
  const completedPrerequisites = item.prerequisites.filter(
    (task) => task.status === "done",
  ).length;
  const prerequisites = [...item.prerequisites].sort(
    (left, right) =>
      Number(left.status === "done") - Number(right.status === "done"),
  );

  return (
    <section
      className="work-next-up-insight"
      aria-label="Task readiness and next steps"
    >
      <div className="work-next-up-insight__reason">
        <h3>
          {compact ? (
            <Clock3 aria-hidden="true" size={15} />
          ) : (
            <Sparkles aria-hidden="true" size={15} />
          )}
          {compact
            ? item.category === "waiting"
              ? (item.readiness?.label ?? "Waiting for prerequisites")
              : "Task readiness"
            : "Why this task"}
        </h3>
        <p>{item.reason}</p>
        {item.recommendationReason ? (
          <p>
            <strong>{item.recommendationReason}</strong>
          </p>
        ) : null}
      </div>

      {!compact || !item.dependencyDataLoaded || prerequisites.length > 0 ? (
        <section className="work-next-up-insight__section">
          <h3>
            <CheckCircle2 aria-hidden="true" size={15} />
            {compact ? "Prerequisites" : "What needs to be completed first"}
            {item.dependencyDataLoaded && prerequisites.length > 0 ? (
              <span className="ml-auto text-workspace-muted text-workspace-micro leading-workspace-micro font-[400] [font-variant-numeric:tabular-nums]">
                {completedPrerequisites}/{prerequisites.length} done
              </span>
            ) : null}
          </h3>
          {!item.dependencyDataLoaded ? (
            <p className="work-next-up-insight__message">
              Some task relationships are unavailable. Prerequisites cannot be
              fully verified yet.
            </p>
          ) : null}
          {prerequisites.length > 0 ? (
            <RelationList
              items={prerequisites}
              label="prerequisites"
              renderItem={(relatedTask) => (
                <div className="overflow-hidden [border:1px_solid_var(--workspace-divider)] rounded-[8px] [background:var(--workspace-panel-raised-bg)]">
                  <RelatedTaskButton
                    relatedTask={relatedTask}
                    compact={compact}
                    onSelectTask={onSelectTask}
                  />
                </div>
              )}
            />
          ) : item.dependencyDataLoaded ? (
            <p className="work-next-up-insight__empty">
              No prerequisite tasks.
            </p>
          ) : null}
        </section>
      ) : null}

      {!compact || !item.dependencyDataLoaded || item.dependents.length > 0 ? (
        <section className="work-next-up-insight__section">
          <h3>
            <GitBranch aria-hidden="true" size={15} />
            {compact
              ? "Unlocks"
              : item.task.status === "done"
                ? "Following tasks"
                : "After this task is completed"}
            {item.dependents.length > 0 ? (
              <span className="ml-auto text-workspace-muted text-workspace-micro leading-workspace-micro font-[400] [font-variant-numeric:tabular-nums]">
                {item.dependents.length}
              </span>
            ) : null}
          </h3>
          {item.dependents.length > 0 ? (
            <RelationList
              items={item.dependents}
              label="following tasks"
              renderItem={(dependent) => (
                <DependentTask
                  dependent={dependent}
                  compact={compact}
                  selectedTaskId={item.task.id}
                  onSelectTask={onSelectTask}
                />
              )}
            />
          ) : (
            <p className="work-next-up-insight__empty">
              {item.dependencyDataLoaded
                ? "No tasks depend directly on this task."
                : "Following tasks could not be verified."}
            </p>
          )}
        </section>
      ) : null}
    </section>
  );
}

function DependentTask({
  compact,
  dependent,
  selectedTaskId,
  onSelectTask,
}: {
  compact?: boolean;
  dependent: WorkNextUpDependentTask;
  selectedTaskId: string;
  onSelectTask: (taskId: string) => void;
}) {
  const remainingPrerequisites = dependent.remainingPrerequisites.filter(
    (task) => task.id !== selectedTaskId,
  );

  return (
    <div className="overflow-hidden [border:1px_solid_var(--workspace-divider)] rounded-[8px] [background:var(--workspace-panel-raised-bg)]">
      <RelatedTaskButton
        relatedTask={dependent}
        compact={compact}
        description={
          compact && remainingPrerequisites.length > 0 ? "" : dependent.reason
        }
        futurePrerequisitesComplete={
          dependent.prerequisitesCompleteAfterCompletion
        }
        onSelectTask={onSelectTask}
      />
      {remainingPrerequisites.length > 0 ? (
        <details className="work-next-up-insight__remaining">
          <summary>
            Still needs {remainingPrerequisites.length}{" "}
            {remainingPrerequisites.length === 1 ? "other task" : "other tasks"}
            <ChevronDown aria-hidden="true" size={12} />
          </summary>
          <RelationList
            items={remainingPrerequisites}
            label="remaining prerequisites"
            renderItem={(relatedTask) => (
              <RelatedTaskButton
                relatedTask={relatedTask}
                onSelectTask={onSelectTask}
              />
            )}
          />
        </details>
      ) : null}
    </div>
  );
}

function RelatedTaskButton({
  compact = false,
  relatedTask,
  description,
  futurePrerequisitesComplete,
  onSelectTask,
}: {
  compact?: boolean;
  relatedTask: WorkNextUpRelatedTask;
  description?: string;
  futurePrerequisitesComplete?: boolean;
  onSelectTask: (taskId: string) => void;
}) {
  const labelTask = useTaskLabel(
    compact ? relatedTask.task?.companyId : undefined,
  );
  const status = relatedTask.status;
  const hasError = status === "failed" || status === "error";
  const showPrerequisitesCleared =
    futurePrerequisitesComplete &&
    status !== "cancelled" &&
    status !== null &&
    !hasError;
  const tone = hasError
    ? "error"
    : status === "done" || showPrerequisitesCleared
      ? "success"
      : status === "cancelled"
        ? "neutral"
        : "waiting";
  const StatusIcon =
    status === null
      ? CircleHelp
      : status === "done"
        ? CheckCircle2
        : showPrerequisitesCleared
          ? ArrowRight
          : status === "cancelled"
            ? Circle
            : Clock3;
  const statusLabel = relatedTask.task
    ? taskWorkStatusLabel(relatedTask.task)
    : status
      ? taskStatusLabels[status]
      : "Task details unavailable";

  return (
    <button
      className="work-next-up-insight__task"
      data-tone={tone}
      disabled={status === null}
      type="button"
      onClick={() => onSelectTask(relatedTask.id)}
    >
      <StatusIcon aria-hidden="true" size={16} />
      <span className="grid min-w-0 flex-1 gap-[4px]">
        {compact && relatedTask.task ? (
          <span className="text-workspace-micro text-workspace-muted">
            {labelTask(relatedTask.task)}
          </span>
        ) : null}
        <span className="work-next-up-insight__task-title">
          {relatedTask.title}
        </span>
        <span className="work-next-up-insight__status">
          {compact && relatedTask.task ? (
            <TaskAssignee
              assignee={relatedTask.task.assignee}
              executedBy={relatedTask.task.executedBy}
            />
          ) : null}
          {description ?? statusLabel}
        </span>
      </span>
    </button>
  );
}

function RelationList<T extends { id: string }>({
  items,
  label,
  renderItem,
}: {
  items: T[];
  label: string;
  renderItem: (item: T) => ReactNode;
}) {
  const [visibleCount, setVisibleCount] = useState(3);
  const remainingCount = Math.max(0, items.length - visibleCount);

  return (
    <div>
      <ul className="work-next-up-insight__list" aria-label={label}>
        {items.slice(0, visibleCount).map((item) => (
          <li key={item.id}>{renderItem(item)}</li>
        ))}
      </ul>
      {items.length > 3 ? (
        <div className="work-next-up-insight__list-actions">
          {remainingCount > 0 ? (
            <button
              aria-label={`Show ${Math.min(10, remainingCount)} more ${label}`}
              type="button"
              onClick={() => setVisibleCount((count) => count + 10)}
            >
              Show {Math.min(10, remainingCount)} more ({remainingCount}{" "}
              remaining)
            </button>
          ) : null}
          {visibleCount > 3 ? (
            <button
              aria-label={`Show fewer ${label}`}
              type="button"
              onClick={() => setVisibleCount(3)}
            >
              Show fewer
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
