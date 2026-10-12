import { useState } from "react";
import {
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  Play,
  Sparkles,
} from "@/shared/ui/icons";
import type { WorkNextUpItem } from "@/features/company-plan/model/work-next-up-model";
import type { CompanyPlanTaskLane } from "@/features/company-plan/model/company-plan-view-model";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";
import { TaskAssignee } from "@/features/company-plan/components/task-assignee";
import type { LabelTask } from "@/shared/lib/task-label";
import "@/features/company-plan/styles/work-next-up.css";

type NextUpFilter = "next" | "running" | "waiting" | "all";

export type WorkNextUpProps = {
  contextKey: string;
  verificationError: boolean;
  onRetryVerification: () => void;
  items: WorkNextUpItem[];
  lanes: CompanyPlanTaskLane[];
  selectedTaskId?: string;
  newlyReadyTaskIds: ReadonlySet<string>;
  pendingTaskId?: string;
  actionsUnavailable: boolean;
  labelTask: LabelTask;
  onSelectTask: (taskId: string) => void;
  onAction: (item: WorkNextUpItem) => void;
};

export function WorkNextUp(props: WorkNextUpProps) {
  const [filter, setFilter] = useState<NextUpFilter>("next");
  const { items } = props;
  const ready = items.filter((item) => item.category === "ready");
  const attention = items.filter((item) => item.category === "attention");
  const waiting = items.filter((item) => item.category === "waiting");
  const running = items.filter((item) => item.category === "running");
  const filters: { value: NextUpFilter; label: string; count: number }[] = [
    { value: "next", label: "Next up", count: ready.length + attention.length },
    { value: "running", label: "Running / queued", count: running.length },
    { value: "waiting", label: "Waiting", count: waiting.length },
    { value: "all", label: "All tasks", count: items.length },
  ];
  return (
    <div className="work-next-up">
      <div
        className="work-next-up__filters"
        role="group"
        aria-label="Filter next actions"
      >
        {filters.map((option) => (
          <button
            key={option.value}
            type="button"
            aria-pressed={filter === option.value}
            onClick={() => setFilter(option.value)}
          >
            {option.label}
            <span>{option.count}</span>
          </button>
        ))}
      </div>
      <div className="work-next-up__scroll">
        {props.actionsUnavailable ? (
          <div className="work-next-up__notice" role="status">
            {props.verificationError
              ? "Run or response status could not be verified."
              : "Checking run and response status. Start actions are unavailable until this is verified."}
            {props.verificationError ? (
              <button
                className="work-next-up__link"
                type="button"
                onClick={props.onRetryVerification}
              >
                Retry verification
              </button>
            ) : null}
          </div>
        ) : null}
        {filter === "next" ? (
          <>
            <NextUpSection
              {...props}
              items={ready}
              title="Ready to start"
              description="These tasks can begin independently. Ordered by priority, then the work they make possible."
              empty="No tasks are ready to start. Check your input or waiting tasks below."
              recommend
            />
            {attention.length > 0 ? (
              <NextUpSection
                {...props}
                items={attention}
                title="Needs your input"
                description="Review results, respond to requests, or resolve what is holding work up."
              />
            ) : null}
            {waiting.length > 0 ? (
              <details className="work-next-up__waiting">
                <summary>
                  <ChevronDown size={16} aria-hidden="true" />
                  Waiting to start <span>{waiting.length}</span>
                </summary>
                <NextUpSection
                  {...props}
                  items={waiting}
                  title="Waiting"
                  description="Each one waits for a prerequisite, an answer, or its assignee's computer. Open a task to see what it still needs."
                />
              </details>
            ) : null}
            {ready.length === 0 &&
            attention.length === 0 &&
            running.length > 0 ? (
              <button
                className="work-next-up__link"
                type="button"
                onClick={() => setFilter("running")}
              >
                View {running.length} running or queued tasks{" "}
                <ArrowRight size={14} />
              </button>
            ) : null}
            {ready.length === 0 &&
            attention.length === 0 &&
            waiting.length === 0 &&
            running.length === 0 ? (
              <button
                className="work-next-up__link"
                type="button"
                onClick={() => setFilter("all")}
              >
                View completed and cancelled tasks <ArrowRight size={14} />
              </button>
            ) : null}
          </>
        ) : (
          <NextUpSection
            {...props}
            key={filter}
            items={
              filter === "running"
                ? running
                : filter === "waiting"
                  ? waiting
                  : items
            }
            title={
              filter === "running"
                ? "Running and queued"
                : filter === "waiting"
                  ? "Waiting to start"
                  : "All tasks"
            }
            empty="No tasks in this group match the current filters."
          />
        )}
      </div>
    </div>
  );
}

function NextUpSection({
  title,
  description,
  empty,
  recommend,
  ...props
}: WorkNextUpProps & {
  title: string;
  description?: string;
  empty?: string;
  recommend?: boolean;
}) {
  const [page, setPage] = useState(0);
  const lastPage = Math.max(0, Math.ceil(props.items.length / 20) - 1);
  const currentPage = Math.min(page, lastPage);
  const start = currentPage * 20;
  const laneAgents = new Map(
    props.lanes.flatMap((lane) =>
      lane.tasks.map((task) => [task.id, lane.agent] as const),
    ),
  );
  return (
    <section className="work-next-up__section" aria-label={title}>
      <header>
        <h2>
          {title}
          <span>{props.items.length}</span>
        </h2>
        {description ? <p>{description}</p> : null}
      </header>
      {props.items.length === 0 ? (
        <div className="work-next-up__empty">
          <CheckCircle2 size={20} aria-hidden="true" />
          <p>{empty}</p>
        </div>
      ) : (
        <div className="work-next-up__rows">
          {props.items.slice(start, start + 20).map((item, index) => {
            const laneAgent = laneAgents.get(item.task.id);
            const isNew =
              props.newlyReadyTaskIds.has(item.task.id) &&
              ["start", "retry", "founder"].includes(item.action);
            const recommended = recommend && currentPage === 0 && index === 0;
            const canStart = item.action === "start" || item.action === "retry";
            const pending = props.pendingTaskId === item.task.id;
            return (
              <article
                key={item.task.id}
                className="work-next-up__row"
                data-selected={props.selectedTaskId === item.task.id}
              >
                <button
                  className="work-next-up__task"
                  type="button"
                  aria-label={`View ${item.task.title}`}
                  aria-pressed={props.selectedTaskId === item.task.id}
                  onClick={() => props.onSelectTask(item.task.id)}
                >
                  <span className="work-next-up__task-top">
                    <span>{props.labelTask(item.task)}</span>
                    {recommended ? (
                      <span className="work-next-up__recommended">
                        <Sparkles size={12} aria-hidden="true" />
                        Suggested first
                      </span>
                    ) : null}
                    {isNew ? (
                      <span className="work-next-up__new">Newly ready</span>
                    ) : null}
                  </span>
                  <strong>{item.task.title}</strong>
                  {/* Why to start it first, for a viewer who can start
                      it. Anyone else reads who can, and where it runs. */}
                  <span className="work-next-up__reason">
                    {recommended &&
                    canStart &&
                    item.readyAfterCompletionCount > 0
                      ? item.recommendationReason
                      : item.reason}
                  </span>
                  <span className="work-next-up__metadata">
                    {/* Whose task it is, then the Agent doing the work
                        with its role. The queue is company-wide and only
                        the assignee can start a task (ADR 0014). */}
                    <TaskAssignee
                      assignee={item.task.assignee}
                      executedBy={item.task.executedBy}
                    />
                    {item.task.executedBy.kind === "agent" &&
                    laneAgent?.title ? (
                      <span>· {laneAgent.title}</span>
                    ) : null}
                    <span
                      className="work-next-up__priority"
                      data-priority={item.task.priority}
                    >
                      {item.task.priority}
                    </span>
                    {item.category !== "ready" ? (
                      <span>
                        {item.runStatus
                          ? item.runStatus.replaceAll("_", " ")
                          : taskWorkStatusLabel(item.task)}
                      </span>
                    ) : null}
                  </span>
                </button>
                <button
                  className="work-next-up__action"
                  data-primary={recommended || undefined}
                  type="button"
                  disabled={
                    pending ||
                    (canStart &&
                      (props.actionsUnavailable ||
                        Boolean(props.pendingTaskId)))
                  }
                  aria-label={`${item.actionLabel}: ${item.task.title}`}
                  onClick={() => props.onAction(item)}
                >
                  {canStart ? <Play size={13} aria-hidden="true" /> : null}
                  {pending ? "Starting…" : item.actionLabel}
                  {!canStart ? (
                    <ArrowRight size={13} aria-hidden="true" />
                  ) : null}
                </button>
              </article>
            );
          })}
        </div>
      )}
      {lastPage > 0 ? (
        <nav className="work-next-up__pagination" aria-label={`${title} pages`}>
          <span>
            {start + 1}–{Math.min(start + 20, props.items.length)} of{" "}
            {props.items.length}
          </span>
          <button
            type="button"
            disabled={currentPage === 0}
            onClick={() => setPage(currentPage - 1)}
          >
            Previous
          </button>
          <button
            type="button"
            disabled={currentPage === lastPage}
            onClick={() => setPage(currentPage + 1)}
          >
            Next
          </button>
        </nav>
      ) : null}
    </section>
  );
}
