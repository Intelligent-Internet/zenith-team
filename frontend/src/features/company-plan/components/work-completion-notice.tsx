import { ArrowRight, CheckCircle2, X } from "@/shared/ui/icons";
import type { WorkNextUpModel } from "@/features/company-plan/model/work-next-up-model";
import type { WorkCompletionReceipt } from "@/state/slice/work";
import "@/features/company-plan/styles/work-completion.css";

export function WorkCompletionNotice({
  completion,
  model,
  onDismiss,
  onViewNext,
}: {
  completion: WorkCompletionReceipt;
  model: WorkNextUpModel;
  onDismiss: () => void;
  onViewNext: () => void;
}) {
  const completed = completion.completedTaskIds.flatMap(
    (id) => model.byTaskId.get(id)?.task ?? [],
  );
  const ready = completion.newlyReadyTaskIds.flatMap((id) => {
    const item = model.byTaskId.get(id);
    return item && ["start", "retry", "founder"].includes(item.action)
      ? [item.task]
      : [];
  });
  const remaining = new Map(
    completion.affectedWaitingTaskIds.flatMap((id) =>
      (model.byTaskId.get(id)?.prerequisites ?? [])
        .filter((task) => task.status !== "done")
        .map((task) => [task.id, task.title] as const),
    ),
  );
  const description = completion.isReconciling
    ? "Updating next steps…"
    : completion.isUnavailable
      ? "Next steps could not be verified. Check the latest tasks in Next up."
      : ready.length
        ? `${ready.length} task${ready.length === 1 ? " is" : "s are"} now actionable: ${ready
            .slice(0, 2)
            .map((task) => task.title)
            .join(", ")}${ready.length > 2 ? ", …" : ""}.`
        : remaining.size
          ? `No tasks newly ready. Following tasks still need: ${[...remaining.values()].slice(0, 2).join(", ")}${remaining.size > 2 ? ", …" : ""}.`
          : "No tasks newly ready. Next up shows the remaining work.";
  return (
    <aside
      className="work-chat-completion work-completion-notice"
      aria-label="Work completed: next steps"
    >
      <CheckCircle2 size={18} aria-hidden="true" />
      <div className="work-chat-completion__copy" role="status">
        <strong>
          {completed.length === 1
            ? completed[0].title
            : `${completed.length} tasks`}{" "}
          completed
        </strong>
        <span>{description}</span>
      </div>
      <button
        className="work-chat-completion__link"
        type="button"
        onClick={onViewNext}
      >
        See next tasks <ArrowRight size={14} aria-hidden="true" />
      </button>
      <button
        className="work-chat-completion__dismiss"
        type="button"
        aria-label="Dismiss task completion update"
        onClick={onDismiss}
      >
        <X size={16} aria-hidden="true" />
      </button>
    </aside>
  );
}
