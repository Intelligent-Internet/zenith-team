/**
 * A chat lane is the Agent the founder is talking to. The chat store keys
 * messages and drafts by lane. Every lane is the Agent's id, except the root
 * (CEO) Agent's lane, which is a fixed key so the home dock and the chat
 * workspace agree on it before the Agent list has loaded.
 */
export type ChatTeamId = string;

export const CEO_CHAT_TEAM_ID: ChatTeamId = "ceo";

export function agentChatTeamId(agentId: string, isCeo: boolean): ChatTeamId {
  return isCeo ? CEO_CHAT_TEAM_ID : agentId;
}
