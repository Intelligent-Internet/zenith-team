import type { TeamNodeData } from "@/features/home/types";
import type {
  CompanyConversationSearchResult,
  CompanyTaskSearchResult,
} from "@/shared/api/types";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ConversationDockTab } from "@/state";

export function createDirectConversationDockTab({
  conversationId,
  team,
}: {
  conversationId?: string | null;
  team: TeamNodeData;
}): ConversationDockTab {
  return {
    agentId: team.agentId,
    avatarSrc: team.iconSrc,
    ...(conversationId ? { conversationId } : {}),
    id: directConversationDockTabId(team.teamId),
    kind: "direct",
    subtitle: team.agentRole,
    teamId: team.teamId,
    title: team.label,
  };
}

export function createTaskConversationDockTab({
  conversationId,
  team,
  taskId,
  taskTitle,
}: {
  conversationId?: string | null;
  team: TeamNodeData;
  taskId: string;
  taskTitle: string;
}): ConversationDockTab {
  return {
    agentId: team.agentId,
    avatarSrc: team.iconSrc,
    ...(conversationId ? { conversationId } : {}),
    id: taskConversationDockTabId(taskId),
    kind: "task",
    subtitle: createTaskConversationSubtitle(team.label),
    taskId,
    teamId: team.teamId,
    title: taskTitle,
  };
}

export function createSearchTaskConversationDockTab(
  result: CompanyTaskSearchResult,
  teams: TeamNodeData[],
): ConversationDockTab {
  const team = findTeamForAgent(teams, result.agent.id);

  return {
    agentId: result.agent.id,
    avatarSrc: team?.iconSrc ?? "",
    conversationId: result.conversationId,
    id: taskConversationDockTabId(result.id),
    kind: "task",
    subtitle: createTaskConversationSubtitle(team?.label ?? result.agent.name),
    taskId: result.id,
    teamId: team?.teamId ?? result.agent.id,
    title: result.title,
  };
}

export function createSearchDirectConversationDockTab(
  result: CompanyConversationSearchResult,
  teams: TeamNodeData[],
): ConversationDockTab {
  const team = findTeamForAgent(teams, result.agent.id);
  const teamId = team?.teamId ?? result.agent.id;

  return {
    agentId: result.agent.id,
    avatarSrc: team?.iconSrc ?? "",
    conversationId: result.id,
    id: directConversationDockTabId(teamId),
    kind: "direct",
    subtitle: team?.agentRole ?? "Direct message",
    teamId,
    title: team?.label ?? result.agent.name,
  };
}

export function createTaskConversationSubtitle(agentLabel: string) {
  return `Task · ${agentLabel}`;
}

function findTeamForAgent(teams: TeamNodeData[], agentId: string) {
  return teams.find((team) => team.agentId === agentId);
}

function directConversationDockTabId(teamId: ChatTeamId) {
  return `direct:${teamId}`;
}

function taskConversationDockTabId(taskId: string) {
  return `task:${taskId}`;
}
