import { AnalyticsSummaryCard } from "@/features/analytics/components/analytics-summary-card";
import { useId, useLayoutEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import { TaskDetailResults } from "@/features/company-plan/components/task-detail-results";
import { ArrowUpRight, ChevronDown } from "@/shared/ui/icons";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import { useTaskLabel } from "@/shared/lib/task-label";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { agentCurrentTask } from "@/features/agents/model/agent-current-task";
import { AgentSetupSummary } from "./agent-setup-summary";
import { AgentWorkHistory } from "./agent-work-history";
import type { AgentDto, TaskDto } from "@/shared/api/types";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";
import { TaskStatusIcon } from "@/features/company-plan/components/task-metadata-icons";
import "../styles/agent-overview.css";

export function AgentOverview({ agent }: { agent: AgentDto }) {
  const attention = useTaskAttention(agent.companyId);
  const query = useListWorkspaceTasksQuery({ companyId: agent.companyId });
  const tasks = (query.isError ? [] : (query.currentData?.items ?? [])).filter(
    (task) =>
      task.executedBy.kind === "agent" && task.executedBy.id === agent.id,
  );
  const activeRunsQuery = useListWorkActiveRunsQuery(agent.companyId);
  const focus =
    tasks.find(
      (task) =>
        task.status !== "done" &&
        task.status !== "cancelled" &&
        attention.taskIds.has(task.id),
    ) ??
    agentCurrentTask({
      agentId: agent.id,
      tasks,
      activeRuns: activeRunsQuery.isError
        ? []
        : (activeRunsQuery.currentData ?? []),
      attentionTaskIds: attention.taskIds,
    });
  const recentCompletedTask = tasks
    .filter((task) => task.status === "done")
    .sort((a, b) =>
      (b.completedAt ?? b.updatedAt).localeCompare(
        a.completedAt ?? a.updatedAt,
      ),
    )[0];
  const stats = [
    { label: "assigned", value: tasks.length },
    {
      label: "in review",
      value: tasks.filter((task) => task.status === "in_review").length,
    },
    {
      label: "completed",
      value: tasks.filter((task) => task.status === "done").length,
    },
  ].filter((stat) => stat.value > 0);
  const [usageExpanded, setUsageExpanded] = useState(false);

  return (
    <div className="agent-overview-layout agent-work-overview">
      <section className="agent-overview-main" aria-label="Agent work overview">
        {stats.length ? (
          <div className="agent-overview-work-heading">
            <div className="agent-overview-stats" aria-label="Task summary">
              {stats.map((stat) => (
                <span key={stat.label}>
                  {stat.value} {stat.label}
                </span>
              ))}
            </div>
            <Link to={`/agents/${agent.id}?tab=work`}>
              View all work <ArrowUpRight aria-hidden size={13} />
            </Link>
          </div>
        ) : null}
        {attention.isError ? (
          <p className="agent-overview-notice" role="alert">
            Requests needing your response could not be loaded.{" "}
            <button type="button" onClick={attention.retry}>
              Retry
            </button>
          </p>
        ) : null}
        {query.isError ? (
          <p className="agent-overview-notice" role="alert">
            Work could not be loaded.{" "}
            <button type="button" onClick={() => void query.refetch()}>
              Retry
            </button>
          </p>
        ) : !query.currentData ? (
          <p className="agent-overview-notice" role="status">
            Loading work…
          </p>
        ) : focus ? (
          <AgentCurrentWork
            key={focus.id}
            agent={agent}
            task={focus}
            needsResponse={attention.taskIds.has(focus.id)}
          />
        ) : tasks.length ? (
          <p className="agent-overview-notice">No active tasks right now.</p>
        ) : (
          <div className="agent-overview-empty">
            <h2>No tasks assigned yet</h2>
            <p>
              Start a conversation with {agent.name}, or assign work from a
              project. Task progress and saved results will appear here.
            </p>
            <Link
              className="dashboard-button"
              to={`/app?conversationKind=direct&agentId=${agent.id}&newSession=1`}
            >
              Start a conversation <ArrowUpRight aria-hidden size={14} />
            </Link>
          </div>
        )}
        {tasks.length ? (
          <AgentSavedWork
            key={agent.id}
            tasks={tasks}
            defaultTask={recentCompletedTask ?? focus ?? tasks[0]}
          />
        ) : null}
        <AgentWorkHistory
          agent={agent}
          tasks={tasks}
          title="Recent runs"
          compact
          historyHref={`/agents/${agent.id}?tab=work`}
        />
        <details
          className="agent-overview-usage"
          open={usageExpanded}
          onToggle={(event) => setUsageExpanded(event.currentTarget.open)}
        >
          <summary>
            <span>Usage</span>
            <span>Last 30 days</span>
            <ChevronDown aria-hidden size={14} />
          </summary>
          {usageExpanded ? (
            <AnalyticsSummaryCard
              companyId={agent.companyId}
              agentId={agent.id}
              className="agent-usage-summary"
            />
          ) : null}
        </details>
      </section>
      <AgentContextSidebar agent={agent} collapsibleSetup />
    </div>
  );
}

function AgentCurrentWork({
  agent,
  task,
  needsResponse,
}: {
  agent: AgentDto;
  task: TaskDto;
  needsResponse: boolean;
}) {
  const labelTask = useTaskLabel(agent.companyId);
  const descriptionId = useId();
  const descriptionRef = useRef<HTMLParagraphElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [canExpand, setCanExpand] = useState(false);

  useLayoutEffect(() => {
    const description = descriptionRef.current;
    if (!description || expanded) return;
    const measure = () =>
      setCanExpand(description.scrollHeight > description.clientHeight + 1);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(description);
    return () => observer.disconnect();
  }, [task.description, expanded]);

  return (
    <section
      aria-label={needsResponse ? "Needs your response" : "Current task"}
    >
      <div className="agent-section-heading">
        <h2>{needsResponse ? "Needs your response" : "Current task"}</h2>
      </div>
      <article
        className="agent-focus-card"
        data-attention={needsResponse || undefined}
      >
        <div className="agent-focus-meta">
          <span>{labelTask(task)}</span>
          <span>
            <TaskStatusIcon status={task.status} />
            {needsResponse ? "Needs your response" : taskWorkStatusLabel(task)}
          </span>
        </div>
        <h3>{task.title}</h3>
        {task.description ? (
          <p
            className="agent-focus-description"
            id={descriptionId}
            ref={descriptionRef}
            data-expanded={expanded || undefined}
          >
            {task.description}
          </p>
        ) : null}
        <footer>
          {canExpand ? (
            <button
              className="agent-brief-toggle"
              type="button"
              aria-expanded={expanded}
              aria-controls={descriptionId}
              onClick={() => setExpanded((value) => !value)}
            >
              {expanded ? "Show less" : "Read brief"}
            </button>
          ) : null}
          <Link
            className="agent-focus-task-link"
            to={
              needsResponse
                ? `/app?conversationKind=task&taskId=${task.id}&agentId=${agent.id}&conversationId=${task.conversationId}`
                : `/projects/${task.projectId}?taskId=${task.id}`
            }
          >
            {needsResponse ? "Respond in chat" : "View task"}{" "}
            <ArrowUpRight aria-hidden size={13} />
          </Link>
        </footer>
      </article>
    </section>
  );
}

export function AgentContextSidebar({
  agent,
  showSetup = true,
  collapsibleSetup = false,
}: {
  agent: AgentDto;
  showSetup?: boolean;
  collapsibleSetup?: boolean;
}) {
  return (
    <aside
      className="agent-overview-sidebar agent-context-sidebar"
      aria-label="Agent responsibility and execution"
    >
      {showSetup ? (
        <AgentSetupSummary agent={agent} collapsible={collapsibleSetup} />
      ) : null}
      <section className="agent-context-reporting">
        <h2>Reports to</h2>
        {agent.reportsTo ? (
          <Link
            className="agent-reporting-link"
            to={`/agents/${agent.reportsTo.id}`}
          >
            <FoldAvatar agentId={agent.reportsTo.id} size={28} paused />
            {agent.reportsTo.name}
          </Link>
        ) : (
          <p>Company owner</p>
        )}
      </section>
    </aside>
  );
}

function AgentSavedWork({
  tasks,
  defaultTask,
}: {
  tasks: TaskDto[];
  defaultTask: TaskDto;
}) {
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const task =
    tasks.find((candidate) => candidate.id === selectedTaskId) ?? defaultTask;
  const labelTask = useTaskLabel(task.companyId);
  const navigate = useNavigate();
  const results = useConversationArtifacts({
    companyId: task.companyId,
    conversationId: task.conversationId,
  });
  return (
    <section className="agent-saved-work">
      <div className="agent-section-heading">
        <h2>Saved artifacts</h2>
      </div>
      <label className="agent-artifact-task-picker">
        <span>Artifacts from task</span>
        <AgentRuntimeSelect
          value={task.id}
          onChange={(event) => setSelectedTaskId(event.target.value)}
        >
          {[...tasks]
            .sort((a, b) => a.number - b.number)
            .map((item) => (
              <option key={item.id} value={item.id}>
                {labelTask(item)} · {item.title}
              </option>
            ))}
        </AgentRuntimeSelect>
      </label>
      <TaskDetailResults
        key={task.id}
        companyId={task.companyId}
        sourceLabel={`${labelTask(task)} · ${task.title}`}
        results={results}
        latestOnly={false}
        emptyLabel="No saved artifacts for this task yet."
        onSelect={(result, fileId) => {
          const item = result.artifact.items.find((item) => item.id === fileId);
          navigate(
            `/companies/${task.companyId}/artifacts/${result.artifact.id}${item ? `?item=${item.ordinal}` : ""}`,
          );
        }}
      />
      {results.hasMore ? (
        <button
          type="button"
          className="dashboard-button"
          disabled={results.isLoadingMore}
          onClick={() => void results.loadMore()}
        >
          {results.isLoadingMore ? "Loading…" : "Load older artifacts"}
        </button>
      ) : null}
    </section>
  );
}
