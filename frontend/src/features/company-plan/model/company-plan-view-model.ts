import { compareTaskReadiness } from "@/features/company-plan/model/task-work-state";
import { isRootAgent } from "@/shared/api/adapters";
import type { AgentDto, TaskDto } from "@/shared/api/types";
import {
  agentAccentColor,
  defaultAgentAccentColors,
} from "@/shared/ui/agent-avatar/agent-appearance";

/** Who a lane of the Company Plan belongs to: an Agent, or a person. */
export type CompanyPlanLaneAgent = {
  accentColor: string;
  avatarSrc?: string;
  id: string;
  isCeo: boolean;
  kind: "agent" | "user";
  name: string;
  title: string;
};

export type CompanyPlanTaskLane = {
  agent: CompanyPlanLaneAgent;
  tasks: TaskDto[];
};

export type CompanyPlanViewModel = {
  lanes: CompanyPlanTaskLane[];
};

/**
 * One lane per active Agent, root Agent first, plus a lane for anyone else
 * still doing Tasks: a member doing their own work, or an archived Agent
 * that has not been replaced. Lanes are keyed on who does the work
 * (executedBy), not on who owns the Task (ADR 0014).
 *
 * An Agent is called archived only when `agents` says so. One the list does
 * not name — because it has not loaded, or a caller passed none — is just
 * "Agent": a missing list must never turn the active CEO into an archived
 * one.
 */
export function createCompanyPlanViewModel(
  tasks: TaskDto[],
  agents: AgentDto[],
): CompanyPlanViewModel {
  const laneAgents = new Map<string, CompanyPlanLaneAgent>();
  const archivedAgentIds = new Set(
    agents.filter((agent) => agent.archivedAt).map((agent) => agent.id),
  );
  const activeAgents = agents
    .filter((agent) => !agent.archivedAt)
    .sort(compareAgents);
  for (const agent of activeAgents) {
    laneAgents.set(agent.id, {
      accentColor: agentAccentColor(agent),
      id: agent.id,
      isCeo: isRootAgent(agent),
      kind: "agent",
      name: agent.name,
      title: agent.title,
    });
  }

  const tasksByExecutorId = new Map<string, TaskDto[]>();
  for (const task of tasks) {
    const executorId = task.executedBy.id;
    const executorTasks = tasksByExecutorId.get(executorId) ?? [];
    executorTasks.push(task);
    tasksByExecutorId.set(executorId, executorTasks);
    if (!laneAgents.has(executorId)) {
      laneAgents.set(executorId, {
        accentColor:
          defaultAgentAccentColors[defaultAgentAccentColors.length - 1],
        avatarSrc: task.executedBy.avatarUrl ?? undefined,
        id: executorId,
        isCeo: false,
        kind: task.executedBy.kind,
        name: task.executedBy.name,
        title:
          task.executedBy.kind === "user"
            ? "Team member"
            : archivedAgentIds.has(executorId)
              ? "Archived Agent"
              : "Agent",
      });
    }
  }

  const lanes = [...laneAgents.values()].map((agent) => ({
    agent,
    tasks: [...(tasksByExecutorId.get(agent.id) ?? [])].sort(
      compareTaskReadiness,
    ),
  }));
  return { lanes };
}

function compareAgents(left: AgentDto, right: AgentDto) {
  const rootOrder = Number(isRootAgent(right)) - Number(isRootAgent(left));
  return rootOrder || left.name.localeCompare(right.name);
}
