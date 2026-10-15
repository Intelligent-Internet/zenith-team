import type { PromptInputMessage } from "@/shared/ui/core/ai-elements/prompt-input";
import { CEO_CHAT_TEAM_ID, type ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";

export function clampValue(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

export function formatChatTimestamp(timestamp = Date.now()) {
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    hour12: false,
    minute: "2-digit",
  }).format(timestamp);
}

export function getPromptInputPayload(message: PromptInputMessage) {
  const text = message.text?.trim() ? message.text : "";
  const files = message.files?.length ? message.files : undefined;

  if (!text && !files?.length && !message.artifactIds?.length) {
    return null;
  }

  return { files, text };
}

export function createUserChatMessage({
  message,
  sentAt = Date.now(),
  team,
}: {
  message: PromptInputMessage;
  sentAt?: number;
  team: ChatTeamId;
}): ChatMessage | null {
  const payload = getPromptInputPayload(message);

  if (!payload) {
    return null;
  }

  return {
    id: sentAt,
    team,
    author: "user",
    createdAt: new Date(sentAt).toISOString(),
    text:
      payload.text ||
      (message.artifactIds?.length ? "Attached artifacts." : ""),
    files: payload.files,
    artifactIds: message.artifactIds,
    skillReferences: message.skillReferences,
    connectorReferences: message.connectorReferences,
    timestamp: formatChatTimestamp(sentAt),
  };
}

export function createTeamChatMessage({
  afterUserInputRequestId,
  clientMessageId,
  conversationId,
  id = Date.now(),
  isThinking = false,
  isStreaming = false,
  itemId,
  runId,
  team,
  text,
  turnId,
  turnKey,
}: {
  afterUserInputRequestId?: string;
  clientMessageId?: string;
  conversationId?: string;
  id?: number;
  isThinking?: boolean;
  isStreaming?: boolean;
  itemId?: string;
  runId?: string;
  team: ChatTeamId;
  text: string;
  turnId?: number;
  turnKey?: string;
}): ChatMessage {
  return {
    ...(afterUserInputRequestId ? { afterUserInputRequestId } : {}),
    ...(clientMessageId ? { clientMessageId } : {}),
    ...(conversationId ? { conversationId } : {}),
    id,
    createdAt: new Date(id).toISOString(),
    ...(isThinking ? { isThinking } : {}),
    isStreaming,
    ...(itemId ? { itemId } : {}),
    ...(runId === undefined ? {} : { runId }),
    team,
    author: "team",
    text,
    timestamp: formatChatTimestamp(id),
    ...(turnId !== undefined ? { turnId } : {}),
    ...(turnKey ? { turnKey } : {}),
  };
}

export type AgentGreetingSource = {
  agentName?: string;
  agentRole?: string;
  id?: ChatTeamId;
};

const AGENT_GREETING_MESSAGE_ID = -1;

export function createAgentGreetingText({
  agentName,
  agentRole,
}: AgentGreetingSource) {
  const displayName = agentName?.trim() || "your Agent";
  const displayRole = agentRole?.trim();
  const intro = displayRole
    ? `Hi, I'm ${displayName}, your ${displayRole}.`
    : `Hi, I'm ${displayName}.`;

  return `${intro} Tell me what you want to move forward, and I'll help turn it into clear next steps.`;
}

/**
 * An empty lane with a non-root Agent opens with that Agent's greeting. The
 * CEO lane starts from the onboarding flow instead.
 */
export function withAgentGreetingMessage(
  messages: ChatMessage[],
  agent?: AgentGreetingSource,
): ChatMessage[] {
  if (messages.length > 0 || !agent?.id || agent.id === CEO_CHAT_TEAM_ID) {
    return messages;
  }

  return [
    {
      author: "team",
      id: AGENT_GREETING_MESSAGE_ID,
      team: agent.id,
      text: createAgentGreetingText(agent),
    },
  ];
}

export function createTeamWelcomeText({
  agentName,
  agentRole,
}: {
  agentName?: string;
  agentRole?: string;
}) {
  const displayName = agentName?.trim();
  const displayRole = agentRole?.trim() || "Agent";
  const identityLine = displayName
    ? `I’m ${displayName}, your ${displayRole}.`
    : `I’m your ${displayRole}.`;

  return `Hello, Welcome to Zenith Team! 👋\n${identityLine}\n\nPlease provide your business context.\nThen we gonna build something great together!\n\nDon’t worry! I’m always here ...`;
}

export function syncTeamWelcomeMessages(
  messages: ChatMessage[],
  agent?: {
    agentName?: string;
    agentRole?: string;
    id?: ChatTeamId;
  },
): ChatMessage[] {
  if (!agent?.id) {
    return messages;
  }

  return messages.map((message) =>
    message.id === 1 && message.team === agent.id && message.author === "team"
      ? { ...message, text: createTeamWelcomeText(agent) }
      : message,
  );
}
