import {
  hasSameStableMessageId,
  resolveStableRunEnvelopeTarget,
  type StableChatMessage,
  type StableChatMessageIndex,
} from "@/features/chat-panel/model/chat-message-convergence";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type {
  ChatGeneratedFile,
  ChatMessage,
  ChatMessageContentBlock,
  ChatRuntimeActivity,
} from "@/shared/chat/types";
import type { ConversationRunStreamEvent } from "@/shared/realtime/conversation-socket.service";

// What a Run stream event says about the row it belongs to.
export type StreamingIdentity = Partial<
  Pick<
    StableChatMessage,
    | "afterUserInputRequestId"
    | "clientMessageId"
    | "conversationId"
    | "itemId"
    | "messageId"
    | "messageSequence"
    | "runId"
    | "turnId"
    | "turnKey"
  >
>;

export type StreamingAssistantState = {
  activeSkills?: string[];
  activeTools?: string[];
  afterUserInputRequestId?: string;
  actor?: ChatMessage["actor"];
  clientMessageId?: string;
  contentBlocks?: ChatMessageContentBlock[];
  conversationId?: string;
  generatedFiles?: ChatGeneratedFile[];
  id: number;
  isThinking: boolean;
  itemId?: string;
  messageId?: string;
  messageSequence?: number;
  runtimeActivities?: ChatRuntimeActivity[];
  runId?: string;
  text: string;
  team: ChatTeamId;
  turnId?: number;
  turnKey?: string;
};

export function clientStreamingKey(team: ChatTeamId, clientMessageId: string) {
  return `${team}:${clientMessageId}`;
}

export function conversationStreamingAliasKey(
  team: ChatTeamId,
  conversationId: string,
) {
  return `${team}:${conversationId}:latest`;
}

export function sameStreamingAssistant(
  left: StreamingAssistantState,
  right: StreamingAssistantState,
) {
  if (left.team !== right.team) return false;
  if (
    left.conversationId &&
    right.conversationId &&
    left.conversationId !== right.conversationId
  ) {
    return false;
  }
  for (const field of ["clientMessageId", "messageId"] as const) {
    if (left[field] && right[field] && left[field] !== right[field]) {
      return false;
    }
  }
  if (
    left.runId !== undefined &&
    right.runId !== undefined &&
    left.runId !== right.runId
  ) {
    return false;
  }
  return Boolean(
    left.id === right.id ||
    (left.clientMessageId && left.clientMessageId === right.clientMessageId) ||
    (left.messageId && left.messageId === right.messageId) ||
    (left.runId !== undefined && left.runId === right.runId),
  );
}

export function conversationRunStreamingKey(
  team: ChatTeamId,
  event: ConversationRunStreamEvent,
  streamingByKey: Record<string, StreamingAssistantState>,
): string {
  if (event.clientRequestId) {
    return clientStreamingKey(team, event.clientRequestId);
  }
  const matches = Object.entries(streamingByKey).filter(
    ([, state]) =>
      state.team === team &&
      state.conversationId === event.conversationId &&
      state.runId === event.runId,
  );
  return matches.length === 1
    ? matches[0][0]
    : clientStreamingKey(team, `run:${event.runId}`);
}

export function retireCompletedStreamingAliases(
  streamingByKey: Record<string, StreamingAssistantState>,
  completedByKey: Record<string, StreamingAssistantState>,
  streamingKeyByConversation: Record<string, string>,
  canonicalKey: string,
  completed: StreamingAssistantState,
): void {
  const retiredKeys = new Set([canonicalKey]);
  for (const [candidateKey, candidate] of Object.entries(streamingByKey)) {
    if (sameStreamingAssistant(candidate, completed)) {
      retiredKeys.add(candidateKey);
    }
  }
  for (const retiredKey of retiredKeys) {
    const active = streamingByKey[retiredKey];
    if (!active || sameStreamingAssistant(active, completed)) {
      delete streamingByKey[retiredKey];
      completedByKey[retiredKey] = completed;
    }
  }
  for (const [alias, targetKey] of Object.entries(streamingKeyByConversation)) {
    if (retiredKeys.has(targetKey)) {
      streamingKeyByConversation[alias] = canonicalKey;
    }
  }
}

// A durable row committed for a Run that is still live becomes the Run's
// bubble: the streaming state takes the row's identity so every later event
// patches that row instead of opening a second one beside it. Returns the
// rebound state, or undefined when the key holds nothing or holds another
// Run's state.
export function rebindStreamingStateToDurableRow(
  streamingByKey: Record<string, StreamingAssistantState>,
  key: string,
  durable: StreamingAssistantState,
): StreamingAssistantState | undefined {
  const streaming = streamingByKey[key];
  if (!streaming || !sameStreamingAssistant(streaming, durable)) {
    return undefined;
  }
  const rebound: StreamingAssistantState = {
    ...streaming,
    id: durable.id,
    ...(durable.itemId ? { itemId: durable.itemId } : {}),
    ...(durable.messageId ? { messageId: durable.messageId } : {}),
    ...(durable.messageSequence === undefined
      ? {}
      : { messageSequence: durable.messageSequence }),
  };
  streamingByKey[key] = rebound;
  return rebound;
}

// The stored row a fresh streaming state should continue in, if any: the
// row committed under the same message id, or the Run's latest committed
// agent row, provided it is this team's, this conversation's, carries no
// conflicting identity, and no other live key already streams into it.
export function adoptableStoredEnvelope(
  index: StableChatMessageIndex,
  streamingByKey: Readonly<Record<string, StreamingAssistantState>>,
  team: ChatTeamId,
  identity: StreamingIdentity | undefined,
): StableChatMessage | undefined {
  if (!identity?.messageId && identity?.runId === undefined) {
    return undefined;
  }
  const target = resolveStableRunEnvelopeTarget(index, {
    author: "team",
    team,
    ...identity,
  });
  if (target.kind !== "patch") {
    return undefined;
  }
  const stored = target.message;
  if (
    identity.clientMessageId &&
    stored.clientMessageId &&
    stored.clientMessageId !== identity.clientMessageId
  ) {
    return undefined;
  }
  const boundElsewhere = Object.values(streamingByKey).some(
    (streaming) => streaming.team === team && streaming.id === stored.id,
  );
  return boundElsewhere ? undefined : stored;
}

// The conversation sequences of the Run's committed agent rows, which bound
// the segment a bubble that adopted one of them renders.
export function committedAgentSequencesForRun(
  index: StableChatMessageIndex,
  team: ChatTeamId,
  runId: string,
): number[] {
  return index
    .listTeam(team)
    .filter(
      (message) =>
        message.author === "team" &&
        message.runId === runId &&
        message.messageId !== undefined,
    )
    .map((message) => message.messageSequence ?? message.id);
}

// Identity only. A user's optimistic bubble learns the appended item's id and
// run from its own send receipt, so the two rows are matched on those keys —
// there is no longer a fallback that compares rendered text, which could not
// tell two identical prompts apart.
export function isSameVisibleMessage(left: ChatMessage, right: ChatMessage) {
  return (
    hasSameStableMessageId(
      left as StableChatMessage,
      right as StableChatMessage,
    ) ||
    isSameAssistantTurn(left, right) ||
    isSameClientMessage(left, right) ||
    isSameUserTurn(left, right)
  );
}

// The user's words of a run: exactly one item, so the run id identifies it.
function isSameUserTurn(left: ChatMessage, right: ChatMessage) {
  return Boolean(
    left.author === "user" &&
    right.author === "user" &&
    left.team === right.team &&
    left.runId !== undefined &&
    left.runId === right.runId,
  );
}

function isSameClientMessage(left: ChatMessage, right: ChatMessage) {
  return Boolean(
    left.author === right.author &&
    hasSameClientMessageId(left, right) &&
    hasSameAssistantSegment(left, right),
  );
}

export function hasSameClientMessageId(left: ChatMessage, right: ChatMessage) {
  return Boolean(
    left.team === right.team &&
    left.clientMessageId &&
    right.clientMessageId &&
    left.clientMessageId === right.clientMessageId,
  );
}

function hasSameTurnKey(left: ChatMessage, right: ChatMessage) {
  return Boolean(
    left.team === right.team &&
    left.turnKey &&
    right.turnKey &&
    left.turnKey === right.turnKey &&
    hasSameAssistantSegment(left, right),
  );
}

function hasSameRunId(left: ChatMessage, right: ChatMessage) {
  return Boolean(
    left.team === right.team &&
    left.runId !== undefined &&
    left.runId === right.runId &&
    hasSameAssistantSegment(left, right),
  );
}

function hasSameAssistantSegment(left: ChatMessage, right: ChatMessage) {
  return (
    left.author !== "team" ||
    right.author !== "team" ||
    (left.afterUserInputRequestId === right.afterUserInputRequestId &&
      Boolean(left.pausedForUserInput) === Boolean(right.pausedForUserInput))
  );
}

export function isSameAssistantTurn(left: ChatMessage, right: ChatMessage) {
  if (
    left.author !== "team" ||
    right.author !== "team" ||
    left.team !== right.team ||
    hasConflictingAssistantIdentity(left, right)
  ) {
    return false;
  }

  if (
    hasSameStableMessageId(
      left as StableChatMessage,
      right as StableChatMessage,
    )
  ) {
    return true;
  }

  return (
    isSameClientMessage(left, right) ||
    hasSameTurnKey(left, right) ||
    hasSameRunId(left, right)
  );
}

export function hasConflictingAssistantIdentity(
  left: ChatMessage,
  right: ChatMessage,
) {
  const stableLeft = left as StableChatMessage;
  const stableRight = right as StableChatMessage;
  return (
    hasConflictingNonEmptyIdentity(
      stableLeft.messageId,
      stableRight.messageId,
    ) ||
    hasConflictingRunId(left.runId, right.runId) ||
    hasConflictingNonEmptyIdentity(
      left.clientMessageId,
      right.clientMessageId,
    ) ||
    hasConflictingNonEmptyIdentity(left.conversationId, right.conversationId)
  );
}

function hasConflictingNonEmptyIdentity(
  left: string | undefined,
  right: string | undefined,
) {
  return Boolean(left && right && left !== right);
}

function hasConflictingRunId(
  left: string | undefined,
  right: string | undefined,
) {
  return left !== undefined && right !== undefined && left !== right;
}
