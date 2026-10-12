import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { CircleCheck, CircleX } from "@/shared/ui/icons";
import "../styles/task-report-message.css";

type TaskReportItem = {
  taskId: string;
  taskKey: string;
  title: string;
  outcome: "done" | "failed";
  completedAt: string;
  summary: string;
};

export function parseTaskReportMessage(text: string): TaskReportItem[] | null {
  const content = text.trim();
  if (
    !content.startsWith("<task-updates ") ||
    typeof DOMParser === "undefined"
  ) {
    return null;
  }
  const document = new DOMParser().parseFromString(content, "application/xml");
  const root = document.documentElement;
  if (
    document.querySelector("parsererror") ||
    document.doctype ||
    root.tagName !== "task-updates" ||
    root.getAttribute("version") !== "2"
  ) {
    return null;
  }
  const items: TaskReportItem[] = [];
  for (const child of root.children) {
    if (child.tagName !== "task-update") return null;
    const field = (name: string) =>
      Array.from(child.children)
        .find((element) => element.tagName === name)
        ?.textContent?.trim() ?? "";
    const outcome = field("outcome");
    const taskId = field("task-id");
    const title = field("title");
    if (!taskId || !title || (outcome !== "done" && outcome !== "failed")) {
      return null;
    }
    items.push({
      taskId,
      taskKey: field("key"),
      title,
      outcome,
      completedAt: field("completed-at"),
      summary: field("result-summary"),
    });
  }
  return items.length > 0 && root.getAttribute("count") === String(items.length)
    ? items
    : null;
}

export function TaskReportMessage({ items }: { items: TaskReportItem[] }) {
  return (
    <section
      aria-label="Task reports sent to CEO"
      className="task-report-message"
    >
      <p className="task-report-message__label">Sent to CEO</p>
      <ul className="task-report-message__list">
        {items.map((item, index) => (
          <li
            key={`${item.taskId}:${index}`}
            className="task-report-message__item"
          >
            {item.summary ? (
              <details>
                <DisclosureSummary
                  className="task-report-message__toggle"
                  title="View result summary"
                >
                  <TaskReportHeading item={item} />
                </DisclosureSummary>
                <div className="task-report-message__result">
                  <p className="task-report-message__summary">{item.summary}</p>
                  {item.taskKey ? (
                    <p className="task-report-message__key">{item.taskKey}</p>
                  ) : null}
                </div>
              </details>
            ) : (
              <div className="task-report-message__static">
                <TaskReportHeading item={item} />
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function TaskReportHeading({ item }: { item: TaskReportItem }) {
  const completedAt = new Date(item.completedAt);
  const hasCompletedAt = !Number.isNaN(completedAt.getTime());
  const StatusIcon = item.outcome === "done" ? CircleCheck : CircleX;

  return (
    <span className="task-report-message__heading">
      <StatusIcon
        aria-hidden="true"
        size={18}
        className="task-report-message__icon"
        data-outcome={item.outcome}
      />
      <span className="task-report-message__copy">
        <span
          className="task-report-message__title"
          title={item.taskKey || undefined}
        >
          {item.title}
        </span>
        <span className="task-report-message__meta">
          <span>{item.outcome === "done" ? "Completed" : "Failed"}</span>
          {hasCompletedAt ? (
            <>
              <span aria-hidden="true">·</span>
              <time
                dateTime={item.completedAt}
                title={completedAt.toLocaleString()}
              >
                {completedAt.toLocaleString(undefined, {
                  month: "short",
                  day: "numeric",
                  year:
                    completedAt.getFullYear() !== new Date().getFullYear()
                      ? "numeric"
                      : undefined,
                  hour: "numeric",
                  minute: "2-digit",
                })}
              </time>
            </>
          ) : null}
        </span>
      </span>
    </span>
  );
}
