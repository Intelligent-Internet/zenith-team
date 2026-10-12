import type { AgentDto, DirectConversationListItem } from "@/shared/api/types";

// A desktop build can arrive before the matching API promotion. A response
// from the pre-binding API carries no `binding` key at all, and the safe
// reading of silence is that the viewer has not set this agent up on a
// computer of their own: the run button then offers to set it up instead of
// starting work the server would refuse (ADR 0014). An explicit verdict from
// the current API is never replaced, and the server remains the execution
// authority either way.
export function withAgentBindingDefaults(agent: AgentDto): AgentDto {
  if (agent.binding !== undefined) {
    return agent;
  }
  return {
    ...agent,
    binding: null,
    runtimeAvailable: false,
    unavailableReason: "not_bound",
    access: { ...agent.access, execute: false },
  };
}

export function withConversationAgentBindingDefaults(
  conversation: DirectConversationListItem,
): DirectConversationListItem {
  const agent = conversation.agent;
  if (!agent || agent.runtimeAccess !== undefined) {
    return conversation;
  }
  return {
    ...conversation,
    agent: {
      ...agent,
      runtimeAccess: { execute: false },
      runtimeAvailable: false,
    },
  };
}
