import type {
  ChatConversationTarget,
  DiscussionConversationTarget,
} from "@/shared/chat/types";

export function chatConversationTargetKey(
  target: ChatConversationTarget | DiscussionConversationTarget,
) {
  if (target.kind === "discussion")
    return `discussion:${target.conversationId}`;
  return target.kind === "task"
    ? `task:${target.taskId}`
    : target.conversationId
      ? `direct-conversation:${target.conversationId}`
      : `direct-agent:${target.agentId}`;
}

export function isChatConversationTarget(
  value: unknown,
): value is ChatConversationTarget {
  if (!isRecord(value)) {
    return false;
  }
  if (value.kind !== "direct" && value.kind !== "task") return false;
  if (value.kind === "direct" && !filledString(value.agentId)) {
    return false;
  }
  if (value.kind === "task" && !filledString(value.taskId)) {
    return false;
  }

  return (
    optionalFilledString(value.agentId) &&
    optionalFilledString(value.conversationId)
  );
}

function optionalFilledString(value: unknown) {
  return value === undefined || filledString(value);
}

function filledString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
