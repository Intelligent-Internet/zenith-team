import { ProjectEmptyState } from "./project-tasks";
import { AnalyticsSummaryCard } from "@/features/analytics/components/analytics-summary-card";
import { useTaskAttention } from "../hooks/use-task-attention";
import { useProjectTaskReviews } from "../hooks/use-project-task-reviews";
import { taskNeedsAttention } from "../model/task-work-state";
import { Link } from "react-router";
import {
  Bar,
  BarChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { Project } from "../api/project-api";
import type { TaskDto } from "@/shared/api/types";
import { Icon } from "@/shared/ui/icon";
const workloadGroups: {
  label: string;
  statuses: TaskDto["status"][];
  tone: string;
}[] = [
  { label: "Planned", statuses: ["backlog", "todo"], tone: "planned" },
  { label: "In progress", statuses: ["in_progress"], tone: "active" },
  { label: "In review", statuses: ["in_review"], tone: "review" },
  {
    label: "Needs attention",
    statuses: ["blocked", "failed", "error"],
    tone: "attention",
  },
  { label: "Completed", statuses: ["done"], tone: "completed" },
  { label: "Cancelled", statuses: ["cancelled"], tone: "cancelled" },
];

export function ProjectPerformance({
  companyId,
  project,
  tasks,
}: {
  companyId: string;
  project: Project;
  tasks: TaskDto[];
}) {
  const requests = useTaskAttention(companyId);
  const reviews = useProjectTaskReviews(companyId, project.id);
  const completed = tasks.filter((task) => task.status === "done").length;
  const inProgress = tasks.filter(
    (task) => task.status === "in_progress",
  ).length;
  const issues = tasks.filter(
    (task) => task.status !== "in_review" && taskNeedsAttention(task),
  ).length;
  const total = tasks.filter((task) => task.status !== "cancelled").length;
  const completion = total ? Math.round((completed / total) * 100) : 0;
  const metrics = [
    {
      label: "In progress",
      value: inProgress,
      icon: "timer-start",
      hint: "Tasks currently in progress",
    },
    {
      label: "Tasks to review",
      value: reviews.count ?? "—",
      icon: "eye",
      hint: reviews.isError
        ? "Review status unavailable"
        : reviews.isLoading
          ? "Checking reviews…"
          : "Awaiting your review",
    },
    {
      label: "Blocked or failed",
      value: issues,
      icon: "info-circle",
      hint: "Excludes tasks waiting for prerequisites",
    },
    {
      label: "Completed",
      value: `${completed} / ${total}`,
      icon: "tick-circle",
      hint: "Excluding cancelled tasks",
    },
  ];
  return (
    <div className="project-overview" data-analysis="true">
      <section className="project-overview-intro col-[1_/_-1] min-w-0">
        <h2>Performance</h2>
      </section>
      <div className="project-metric-grid" aria-label="Project summary">
        {metrics.map((metric) => (
          <section
            className="project-metric"
            key={metric.label}
            data-metric={metric.label}
            data-attention={metric.label === "Blocked or failed" && issues > 0}
          >
            <Icon
              name={metric.icon}
              className="w-[22px] h-[22px] flex-none text-workspace-muted"
            />
            <div>
              <span>{metric.label}</span>
              <strong>{metric.value}</strong>
              {metric.label === "Completed" ? (
                <div className="project-completion" title={metric.hint}>
                  <progress
                    aria-label="Completed tasks, excluding cancelled work"
                    value={completed}
                    max={total || 1}
                  />
                  <span>{completion}%</span>
                </div>
              ) : (
                <p>
                  {metric.label === "Tasks to review" && reviews.count ? (
                    <Link to={`/projects/${project.id}/tasks?review=mine`}>
                      {metric.hint}
                    </Link>
                  ) : (
                    metric.hint
                  )}
                </p>
              )}
            </div>
          </section>
        ))}
      </div>
      <AnalyticsSummaryCard
        companyId={companyId}
        projectId={project.id}
        className="project-panel"
      />
      {reviews.isError ? (
        <p
          className="col-[1] m-0 text-workspace-muted text-workspace-caption"
          role="alert"
        >
          Reviews could not be loaded.{" "}
          <button type="button" onClick={reviews.retry}>
            Retry
          </button>
        </p>
      ) : null}
      {requests.isError ? (
        <p
          className="col-[1] m-0 text-workspace-muted text-workspace-caption"
          role="alert"
        >
          Pending responses could not be loaded.{" "}
          <button type="button" onClick={requests.retry}>
            Retry
          </button>
        </p>
      ) : null}
      {requests.isLoading ? (
        <p
          className="col-[1] m-0 text-workspace-muted text-workspace-caption"
          role="status"
        >
          Checking pending questions and approvals…
        </p>
      ) : null}
      <div className="project-overview-panels">
        <section className="project-panel">
          <div className="project-panel-heading">
            <h3>Tasks by status</h3>
          </div>
          <div className="project-workload-list">
            {workloadGroups.map((group) => {
              const count = tasks.filter((task) =>
                group.statuses.includes(task.status),
              ).length;
              if (!count && tasks.length) return null;
              return (
                <div className="project-workload-row" key={group.label}>
                  <span>{group.label}</span>
                  <div>
                    <i
                      className={`tone-${group.tone}`}
                      style={{
                        width: `${tasks.length ? (count / tasks.length) * 100 : 0}%`,
                      }}
                    />
                  </div>
                  <strong>{count}</strong>
                </div>
              );
            })}
          </div>
        </section>
        <ProjectProgress tasks={tasks} />
      </div>
      <ProjectMilestones tasks={tasks} />
    </div>
  );
}

function ProjectProgress({ tasks }: { tasks: TaskDto[] }) {
  const days = Array.from({ length: 14 }, (_, index) => {
    const end = new Date();
    end.setDate(end.getDate() - 13 + index);
    end.setHours(23, 59, 59, 999);
    const start = new Date(end);
    start.setHours(0, 0, 0, 0);
    return {
      day: start.toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
      }),
      completed: tasks.filter(
        (task) =>
          task.status === "done" &&
          task.completedAt &&
          Date.parse(task.completedAt) >= start.getTime() &&
          Date.parse(task.completedAt) <= end.getTime(),
      ).length,
    };
  });
  return (
    <section className="project-panel">
      <div className="project-panel-heading">
        <div>
          <h3>Daily completions</h3>
          <p>Last 14 days</p>
        </div>
      </div>
      {days.some((day) => day.completed > 0) ? (
        <div
          className="project-progress-chart"
          role="img"
          aria-label={days
            .map((day) => `${day.day}: ${day.completed} completed`)
            .join(", ")}
        >
          <ResponsiveContainer width="100%" height="100%">
            <BarChart
              data={days}
              margin={{ left: 0, right: 8, top: 8, bottom: 0 }}
            >
              <XAxis
                dataKey="day"
                tick={{
                  fontSize: 11,
                  fill: "var(--workspace-native-text-muted)",
                }}
                minTickGap={35}
                axisLine={false}
                tickLine={false}
              />
              <YAxis
                allowDecimals={false}
                width={24}
                tick={{
                  fontSize: 11,
                  fill: "var(--workspace-native-text-muted)",
                }}
                axisLine={false}
                tickLine={false}
              />
              <Tooltip
                contentStyle={{
                  background: "var(--workspace-panel-bg, #171b23)",
                  border: "1px solid var(--workspace-divider)",
                  borderRadius: 10,
                  color: "var(--workspace-native-text)",
                }}
              />
              <Bar
                name="Completed tasks"
                dataKey="completed"
                fill="var(--workspace-native-success)"
                radius={[3, 3, 0, 0]}
                maxBarSize={22}
                isAnimationActive={false}
              />
            </BarChart>
          </ResponsiveContainer>
        </div>
      ) : (
        <ProjectEmptyState label="No completed tasks in the last 14 days." />
      )}
    </section>
  );
}

function ProjectMilestones({ tasks }: { tasks: TaskDto[] }) {
  const milestones = [...new Set(tasks.map((task) => task.milestoneType))];
  return (
    <section className="project-panel col-[1_/_-1]">
      <div className="project-panel-heading">
        <div>
          <h3>Milestone progress</h3>
          <p>Completed tasks by milestone, excluding cancelled work</p>
        </div>
      </div>
      {milestones.length ? (
        <div className="project-milestone-list">
          {milestones.map((milestone) => {
            const items = tasks.filter(
              (task) =>
                task.milestoneType === milestone && task.status !== "cancelled",
            );
            const done = items.filter((task) => task.status === "done").length;
            return (
              <div key={milestone}>
                <span>{milestone.replaceAll("_", " ")}</span>
                <progress
                  aria-label={`${milestone} completion`}
                  max={items.length || 1}
                  value={done}
                />
                <strong>
                  {done} / {items.length}
                </strong>
              </div>
            );
          })}
        </div>
      ) : (
        <ProjectEmptyState />
      )}
    </section>
  );
}
