import { Streamdown } from "streamdown";
import { ChevronRight } from "@/shared/ui/icons";
import { milestoneLabel } from "@/features/company-plan/model/milestone-catalog";
import { chatMarkdownLinkComponents } from "@/features/chat-panel/components/chat-markdown-link";
import type {
  CompanyPlanChangeLine,
  CompanyPlanPresentation,
  CompanyPlanReview,
  CompanyPlanReviewTask,
} from "@/features/chat-panel/model/company-plan-approval-preview";

export function CompanyPlanTasks({
  review,
  presentation,
  agentNames,
  agentsLoading,
  pending,
}: {
  review: CompanyPlanReview;
  presentation: CompanyPlanPresentation;
  agentNames: ReadonlyMap<string, string>;
  agentsLoading: boolean;
  pending: boolean;
}) {
  const taskChanges = new Map(
    presentation.changes
      .filter((change) => change.entityType === "task")
      .map((change) => [change.key, change.operation]),
  );
  const taskTitles = new Map(
    presentation.plan.tasks.map((task) => [task.key, task.title]),
  );
  const multipleProjects = review.projects.length > 1;

  return (
    <div className="company-plan-approval__work">
      {review.projects.map((project) => {
        const tasks = review.tasks.filter(
          (task) => task.projectKey === project.key,
        );
        return (
          <section key={project.key} className="company-plan-approval__project">
            <h3 className="company-plan-approval__section-title">
              {multipleProjects
                ? project.title
                : tasks.length
                  ? "Tasks"
                  : "Project changes"}
            </h3>
            {multipleProjects && project.description ? (
              <p className="company-plan-approval__description">
                {project.description}
              </p>
            ) : null}
            {tasks.length ? (
              <ol className="company-plan-approval__tasks">
                {tasks.map((task, index) => {
                  const operation = taskChanges.get(task.key);
                  const forYou =
                    !task.agentId && operation === "create" && pending;
                  const owner = task.agentId
                    ? (agentNames.get(task.agentId) ??
                      (agentsLoading ? "Loading agent…" : "Agent unavailable"))
                    : forYou
                      ? "You"
                      : "Human work";
                  return (
                    <li key={task.key}>
                      <details className="company-plan-approval__task">
                        <summary>
                          <span
                            aria-hidden
                            className="company-plan-approval__number"
                          >
                            {index + 1}
                          </span>
                          <span className="company-plan-approval__task-heading">
                            <span className="company-plan-approval__task-title">
                              {task.title}
                            </span>
                            <span
                              className="company-plan-approval__owner"
                              data-you={forYou}
                            >
                              {owner}
                            </span>
                          </span>
                          <ChevronRight
                            aria-hidden
                            size={18}
                            className="company-plan-approval__chevron"
                          />
                        </summary>
                        <div className="company-plan-approval__task-body">
                          <p className="company-plan-approval__task-context">
                            {operation === "create"
                              ? "New task"
                              : operation === "update"
                                ? "Task updated"
                                : "Existing task"}
                          </p>
                          <CompanyPlanMarkdown>
                            {task.description}
                          </CompanyPlanMarkdown>
                          <TaskMetadata task={task} taskTitles={taskTitles} />
                        </div>
                      </details>
                    </li>
                  );
                })}
              </ol>
            ) : (
              <p className="company-plan-approval__muted">
                No task changes in this project.
              </p>
            )}
          </section>
        );
      })}
    </div>
  );
}

function TaskMetadata({
  task,
  taskTitles,
}: {
  task: CompanyPlanReviewTask;
  taskTitles: ReadonlyMap<string, string>;
}) {
  return (
    <dl className="company-plan-approval__metadata">
      <dt>Milestone</dt>
      <dd>{milestoneLabel(task.milestoneType)}</dd>
      <dt>Priority</dt>
      <dd className="capitalize">{task.priority}</dd>
      <dt>Status</dt>
      <dd className="capitalize">{task.status.replaceAll("_", " ")}</dd>
      {task.dependsOn.length ? (
        <>
          <dt>Depends on</dt>
          <dd>
            <ul>
              {task.dependsOn.map(({ taskKey }) => (
                <li key={taskKey}>{taskTitles.get(taskKey) ?? taskKey}</li>
              ))}
            </ul>
          </dd>
        </>
      ) : null}
    </dl>
  );
}

export function CompanyPlanBriefs({ review }: { review: CompanyPlanReview }) {
  return review.projects.map((project) => (
    <section key={project.key} className="company-plan-approval__brief">
      <h3 className="company-plan-approval__section-title">{project.title}</h3>
      <CompanyPlanMarkdown>{project.description}</CompanyPlanMarkdown>
    </section>
  ));
}

export function CompanyPlanChangeList({
  lines,
  destructive = false,
}: {
  lines: CompanyPlanChangeLine[];
  destructive?: boolean;
}) {
  if (!lines.length) return null;
  return (
    <ul
      aria-label={
        destructive ? "Projects and tasks to remove or archive" : "Plan changes"
      }
      className="company-plan-approval__change-lines"
      data-destructive={destructive}
    >
      {lines.map((line) => (
        <li key={line.key}>
          <p>{line.summary}</p>
          {!destructive &&
            line.details.map((detail, index) => (
              <p className="company-plan-approval__muted" key={index}>
                {detail}
              </p>
            ))}
        </li>
      ))}
    </ul>
  );
}

export function CompanyPlanMarkdown({ children }: { children: string }) {
  return (
    <div className="company-plan-approval__markdown">
      <Streamdown components={chatMarkdownLinkComponents} controls={false}>
        {children}
      </Streamdown>
    </div>
  );
}
