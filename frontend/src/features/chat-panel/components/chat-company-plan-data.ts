import type { AgentDto } from "@/shared/api/types";
import type {
  CeoCompanyPlanMessagePayload,
  CeoCompanyPlanTask,
} from "@/shared/chat/types";
import {
  agentAccentColor,
  defaultAgentAccentColors,
} from "@/shared/ui/agent-avatar/agent-appearance";

export type AgentPlanTask = {
  id: string;
  outcome: string;
  title: string;
};

/** The slice of a Company Plan that one Agent will carry. */
export type AgentPlan = {
  accentColor: string;
  agentId: string;
  agentName: string;
  agentRole: string;
  avatarSrc?: string;
  tasks: AgentPlanTask[];
  id: string;
  objective: string;
  responsibilities: string[];
  title: string;
};

// A plan task with no Agent is the founder's own work: the server assigns it
// to the founder who applies the plan, so it groups under its own slice and
// the overview can name the founder.
const founderPlanId = "founder";

/** The slice a plan task belongs to: its Agent, or the founder. */
function planSliceId(task: Pick<CeoCompanyPlanTask, "agentId">) {
  return task.agentId || founderPlanId;
}

export function agentPlanTasks(plan: AgentPlan): AgentPlanTask[] {
  return plan.tasks;
}

export function agentPlansFromCompanyPlan(
  plan: CeoCompanyPlanMessagePayload,
  agents: readonly AgentDto[],
): AgentPlan[] {
  const agentById = new Map(agents.map((agent) => [agent.id, agent] as const));
  const tasksByAgentId = new Map<string, AgentPlanTask[]>();
  for (const task of plan.tasks) {
    const agentId = planSliceId(task);
    const tasks = tasksByAgentId.get(agentId) ?? [];
    tasks.push(task);
    tasksByAgentId.set(agentId, tasks);
  }

  return [...tasksByAgentId.entries()].map(([agentId, tasks]) => {
    const agent = agentById.get(agentId);
    const founder = agentId === founderPlanId;
    return {
      accentColor: agent
        ? agentAccentColor(agent)
        : defaultAgentAccentColors[defaultAgentAccentColors.length - 1],
      agentId: founder ? "" : agentId,
      agentName: agent?.name ?? (founder ? "You" : "Unknown Agent"),
      agentRole: agent?.title ?? (founder ? "Founder work" : "Agent work"),
      id: agentId,
      objective: tasks
        .map((task) => task.outcome)
        .filter(Boolean)
        .join(" "),
      responsibilities: [
        ...new Set(tasks.map((task) => task.outcome || task.title)),
      ],
      tasks,
      title: agent?.name ?? (founder ? "You" : "Agent"),
    };
  });
}
