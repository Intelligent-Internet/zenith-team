import { formatChatTimestamp } from "@/features/chat-panel/utils";
import type { ChatMessage } from "@/shared/chat/types";

const CEO_TEAM_ID = "ceo";

export const CEO_CONVERSATION_UNAVAILABLE_MESSAGE =
  "CEO conversation could not be loaded. Check your connection and try again.";

export function createCeoConversationUnavailableMessage(
  sentAt = Date.now(),
): ChatMessage {
  return {
    author: "team",
    id: sentAt,
    team: CEO_TEAM_ID,
    text: CEO_CONVERSATION_UNAVAILABLE_MESSAGE,
    timestamp: formatChatTimestamp(sentAt),
  };
}

export function hasCeoConversationUnavailableMessage(messages: ChatMessage[]) {
  return messages.some(
    (message) =>
      message.author === "team" &&
      message.team === CEO_TEAM_ID &&
      message.text.trim() === CEO_CONVERSATION_UNAVAILABLE_MESSAGE,
  );
}
