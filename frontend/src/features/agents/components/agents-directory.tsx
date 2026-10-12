import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import {
  ArrowUpRight,
  Bot,
  ChevronDown,
  ChevronRight,
  Plus,
  Search,
} from "@/shared/ui/icons";
import { useState } from "react";
import { Link } from "react-router";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import { runtimeMachineName } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  agentAvailabilityHelp,
  agentAvailabilityLabel,
} from "@/features/agents/model/agent-binding";
import { agentCurrentTask } from "@/features/agents/model/agent-current-task";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import type { AgentDto } from "@/shared/api/types";
import { useTaskLabel } from "@/shared/lib/task-label";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { TaskStatusIcon } from "@/features/company-plan/components/task-metadata-icons";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";

export function AgentsDirectory({
  agents,
  companyId,
  onCreateAgent,
}: {
  agents: AgentDto[];
  companyId: string;
  onCreateAgent?: () => void;
}) {
  const [search, setSearch] = useState("");
  const [provider, setProvider] = useState("");
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  const attention = useTaskAttention(companyId);
  const tasksQuery = useListWorkspaceTasksQuery({ companyId });
  const activeRunsQuery = useListWorkActiveRunsQuery(companyId);
  const labelTask = useTaskLabel(companyId);
  const tasks = tasksQuery.currentData?.items ?? [];
  const activeRuns = activeRunsQuery.currentData ?? [];
  const visible = agents.filter(
    (agent) =>
      (!provider || agent.provider === provider) &&
      `${agent.name} ${agent.title} ${agent.provider}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  return (
    <>
      <div className="agents-directory-toolbar">
        <label className="agents-directory-search">
          <Search size={16} />
          <input
            aria-label="Search agents"
            placeholder="Search agents…"
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        <span className="agents-provider-filter">
          <select
            className="collection-toolbar-control"
            aria-label="Filter agents by provider"
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
          >
            <option value="">All providers</option>
            <option value="claude">Claude</option>
            <option value="codex">Codex</option>
          </select>
          <ChevronDown aria-hidden="true" size={16} />
        </span>
        <span className="agents-directory-count" role="status">
          {search.trim() || provider ? `${visible.length} of ` : ""}
          {agents.length} {agents.length === 1 ? "agent" : "agents"}
        </span>
        <Link className="agents-directory-work-link" to="/tasks">
          View company work <ArrowUpRight size={14} />
        </Link>
      </div>
      {tasksQuery.isError ? (
        <p role="status">
          Task activity is unavailable.{" "}
          <button type="button" onClick={() => void tasksQuery.refetch()}>
            Retry
          </button>
        </p>
      ) : null}
      <div className="agents-directory-table collection-list">
        <div
          className="agents-directory-columns collection-list__header"
          aria-hidden="true"
        >
          <span>Agent</span>
          <span>Current task</span>
          <span>On your computer</span>
          <span />
        </div>
        {visible.map((agent) => {
          const runtime = agent.binding?.runtime;
          const computerName = runtime ? runtimeMachineName(runtime) : null;
          const providerName =
            agent.provider === "claude" ? "Claude Code" : "Codex";
          const focus = agentCurrentTask({
            agentId: agent.id,
            tasks,
            activeRuns,
            attentionTaskIds: attention.taskIds,
          });
          return (
            // The link covers the whole row, so the row still opens the
            // Agent; "Set up" sits above it, because a button cannot live
            // inside a link.
            <div
              key={agent.id}
              className="agents-directory-row collection-list__row"
            >
              <Link
                className="agents-directory-row-link"
                to={`/agents/${agent.id}`}
                aria-label={`View ${agent.name}, ${agent.title}. ${focus ? `${focus.title}: ${attention.taskIds.has(focus.id) ? "Needs your response" : taskWorkStatusLabel(focus)}. ` : ""}${agent.activeRunCount ? `${agent.activeRunCount} active task runs` : "No active task runs"}. ${agentAvailabilityLabel(agent)}`}
              />
              <div className="agents-directory-identity collection-list__identity">
                <FoldAvatar agentId={agent.id} size={36} paused />
                <div>
                  <div className="agents-directory-name">
                    <strong title={agent.name}>{agent.name}</strong>
                    {agent.activeRunCount > 0 ? (
                      <span
                        className="agents-directory-activity"
                        data-active="true"
                        title={`${agent.activeRunCount} / ${agent.maxConcurrentRuns} task slots occupied on your computer`}
                      >
                        Running · {agent.activeRunCount}
                      </span>
                    ) : null}
                  </div>
                  <small title={agent.title}>{agent.title}</small>
                </div>
              </div>
              <div className="agents-directory-focus" data-empty={!focus}>
                <strong title={focus?.title}>
                  {focus?.title ??
                    (tasksQuery.isError
                      ? "Work unavailable"
                      : tasksQuery.isFetching && !tasksQuery.currentData
                        ? "Loading work…"
                        : "No current task")}
                </strong>
                {focus ? (
                  <small>
                    <TaskStatusIcon status={focus.status} />
                    <span>
                      {labelTask(focus)} ·{" "}
                      {attention.taskIds.has(focus.id)
                        ? "Needs your response"
                        : taskWorkStatusLabel(focus)}
                    </span>
                  </small>
                ) : null}
              </div>
              <div className="agents-directory-runtime">
                {/* Setup and availability describe only the viewer's own
                    computer (ADR 0014). */}
                {agent.binding === null ? (
                  <>
                    <button
                      className="agents-directory-setup"
                      type="button"
                      aria-label={`Set up ${agent.name}`}
                      title={agentAvailabilityHelp(agent)}
                      onClick={() => openBindDialog(agent)}
                    >
                      Set up <ChevronRight size={12} aria-hidden="true" />
                    </button>
                    <small>{providerName}</small>
                  </>
                ) : (
                  <>
                    <span
                      className="agent-availability agents-directory-availability"
                      data-online={agent.runtimeAvailable}
                      title={agentAvailabilityHelp(agent)}
                    >
                      <span>{agentAvailabilityLabel(agent)}</span>
                    </span>
                    <small
                      className="agents-directory-computer"
                      title={`${providerName} · ${computerName ?? ""}`}
                    >
                      {providerName} · {computerName}
                    </small>
                  </>
                )}
              </div>
              <ChevronRight size={14} />
            </div>
          );
        })}
        {!visible.length ? (
          <div className="dashboard-empty">
            <Bot size={24} />
            <h2>{agents.length ? "No matching agents" : "No agents yet"}</h2>
            <p>
              {agents.length
                ? "Try a different name or provider."
                : "Create your first agent. It needs an online runtime to run on."}
            </p>
            {!agents.length && onCreateAgent ? (
              <button
                className="dashboard-button dashboard-button-primary"
                type="button"
                onClick={onCreateAgent}
              >
                <Plus size={16} />
                New agent
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      {bindDialog}
    </>
  );
}
