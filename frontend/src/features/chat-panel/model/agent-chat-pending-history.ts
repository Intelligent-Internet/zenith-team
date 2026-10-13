import { formatChatTimestamp } from "@/features/chat-panel/utils";
import { mergeRuntimeActivities } from "@/features/chat-panel/model/chat-runtime-trace";
import {
  hasSameStableMessageId,
  type StableChatMessage,
} from "@/features/chat-panel/model/chat-message-convergence";
import {
  hasSameClientMessageId,
  isSameAssistantTurn,
  isSameVisibleMessage,
  type StreamingAssistantState,
} from "@/features/chat-panel/model/chat-streaming-identity";
import {
  mergeGeneratedFiles,
  mergeTraceList,
} from "@/features/chat-panel/model/chat-timeline";
import { type ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";
import { contentBlocksForCanonicalText } from "@/features/chat-panel/model/agent-chat-transcript";
import { findLastIndex } from "@/features/chat-panel/model/agent-chat-transcript";

export type RuntimeTraceSnapshot = {
  activeSkills?: string[];
  activeTools?: string[];
  assistantMessage?: StableChatMessage;
  expectedRichContentType?: ExpectedRichContentType;
  finalText?: string;
};

type ExpectedRichContentType = NonNullable<ChatMessage["richContent"]>["type"];

type PendingHistoryMergeResult = {
  messages: ChatMessage[];
  traceResolved: boolean;
  userMessagesResolved: boolean;
};

export function rememberPendingHistoryTrace(
  pendingTraces: Record<string, RuntimeTraceSnapshot>,
  conversationId: string,
  completed: StreamingAssistantState | undefined,
) {
  if (!completed) {
    return;
  }

  const trace = {
    activeSkills: completed.activeSkills,
    activeTools: completed.activeTools,
    assistantMessage: {
      activeSkills: completed.activeSkills,
      activeTools: completed.activeTools,
      author: "team" as const,
      ...(completed.clientMessageId
        ? { clientMessageId: completed.clientMessageId }
        : {}),
      ...(completed.conversationId
        ? { conversationId: completed.conversationId }
        : {}),
      ...(completed.contentBlocks
        ? { contentBlocks: completed.contentBlocks }
        : {}),
      ...(completed.generatedFiles?.length
        ? { generatedFiles: completed.generatedFiles }
        : {}),
      id: completed.id,
      isThinking: false,
      isStreaming: false,
      ...(completed.afterUserInputRequestId
        ? { afterUserInputRequestId: completed.afterUserInputRequestId }
        : {}),
      ...(completed.itemId ? { itemId: completed.itemId } : {}),
      ...(completed.messageId ? { messageId: completed.messageId } : {}),
      ...(completed.messageSequence === undefined
        ? {}
        : { messageSequence: completed.messageSequence }),
      ...(completed.runId === undefined ? {} : { runId: completed.runId }),
      runtimeActivities: completed.runtimeActivities,
      team: completed.team,
      text: completed.text,
      timestamp: formatChatTimestamp(),
      ...(completed.turnId !== undefined ? { turnId: completed.turnId } : {}),
      ...(completed.turnKey ? { turnKey: completed.turnKey } : {}),
    },
    expectedRichContentType: expectedRichContentTypeFromTools(
      completed.activeTools,
    ),
    finalText: completed.text,
  };

  if (!hasPendingHistorySnapshot(trace)) {
    return;
  }

  pendingTraces[conversationId] = trace;
}

export function rememberPendingUserMessage(
  pendingUserMessages: Record<string, StableChatMessage[]>,
  conversationId: string,
  message: StableChatMessage,
) {
  const existing = pendingUserMessages[conversationId] ?? [];

  if (existing.some((item) => isSameVisibleMessage(item, message))) {
    return;
  }

  pendingUserMessages[conversationId] = [...existing, message];
}

export function removePendingUserMessage(
  pendingUserMessages: Record<string, StableChatMessage[]>,
  conversationId: string,
  clientMessageId: string,
) {
  const remaining = (pendingUserMessages[conversationId] ?? []).filter(
    (message) => message.clientMessageId !== clientMessageId,
  );
  if (remaining.length) {
    pendingUserMessages[conversationId] = remaining;
    return;
  }
  delete pendingUserMessages[conversationId];
}

export function stampPendingUserMessage(
  pendingUserMessages: Record<string, StableChatMessage[]>,
  conversationId: string,
  clientMessageId: string,
  identity: { messageId: string; messageSequence: number; runId: string },
) {
  const existing = pendingUserMessages[conversationId];
  if (!existing?.length) {
    return;
  }
  pendingUserMessages[conversationId] = existing.map((pending) =>
    pending.author === "user" && pending.clientMessageId === clientMessageId
      ? { ...pending, ...identity }
      : pending,
  );
}

export function mergePendingHistoryReplay(
  messages: ChatMessage[],
  pendingUserMessages: ChatMessage[] | undefined,
  trace: RuntimeTraceSnapshot | undefined,
): PendingHistoryMergeResult {
  let nextMessages = messages;
  let userMessagesResolved = true;

  for (const userMessage of pendingUserMessages ?? []) {
    if (
      nextMessages.some((message) => isSameVisibleMessage(message, userMessage))
    ) {
      continue;
    }

    if (nextMessages === messages) {
      nextMessages = [...messages];
    }
    nextMessages.splice(
      findPendingUserInsertIndex(nextMessages, trace, userMessage),
      0,
      userMessage,
    );
    userMessagesResolved = false;
  }

  const traceResult = mergePendingHistoryTrace(
    nextMessages,
    trace,
    pendingUserMessages,
  );
  nextMessages = traceResult.messages;

  return {
    messages: nextMessages,
    traceResolved: traceResult.resolved,
    userMessagesResolved,
  };
}

function findPendingUserInsertIndex(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot | undefined,
  userMessage: ChatMessage,
) {
  if (
    !trace?.assistantMessage ||
    !hasSameClientMessageId(userMessage, trace.assistantMessage)
  ) {
    return messages.length;
  }

  const targetAssistantIndex = findTraceTargetMessageIndex(
    messages,
    trace?.finalText,
  );

  return targetAssistantIndex >= 0 ? targetAssistantIndex : messages.length;
}

function mergePendingHistoryTrace(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot | undefined,
  pendingUserMessages?: ChatMessage[],
): { messages: ChatMessage[]; resolved: boolean } {
  if (!trace || !hasPendingHistorySnapshot(trace)) {
    return { messages, resolved: true };
  }

  const targetIndex = findPendingTraceTargetMessageIndex(
    messages,
    trace,
    pendingUserMessages,
  );
  if (targetIndex < 0) {
    if (!trace.assistantMessage || !hasPendingAssistantSnapshot(trace)) {
      return { messages, resolved: false };
    }
    if (
      isPendingAssistantSnapshotAlreadyPresent(
        messages,
        trace.assistantMessage,
        pendingUserMessages,
      )
    ) {
      return { messages, resolved: false };
    }

    const nextMessages = [...messages];
    nextMessages.splice(
      findActiveStreamingInsertIndex(
        nextMessages,
        trace.assistantMessage,
        pendingUserMessages,
      ),
      0,
      trace.assistantMessage,
    );
    return {
      messages: nextMessages,
      resolved: false,
    };
  }

  const targetMessage = messages[targetIndex];
  const pendingAssistantText = trace.assistantMessage?.text.trim()
    ? trace.assistantMessage.text
    : undefined;
  const shouldApplyPendingAssistantText = Boolean(
    pendingAssistantText && !targetMessage.text.trim(),
  );
  const activeSkills = mergeTraceList(
    targetMessage.activeSkills,
    trace.activeSkills,
  );
  const activeTools = mergeTraceList(
    targetMessage.activeTools,
    trace.activeTools,
  );
  const runtimeActivities = mergeRuntimeActivities(
    targetMessage.runtimeActivities,
    trace.assistantMessage?.runtimeActivities,
  );
  const generatedFiles = mergeGeneratedFiles(
    targetMessage.generatedFiles,
    trace.assistantMessage?.generatedFiles,
  );
  const contentBlocks = contentBlocksForCanonicalText(
    trace.assistantMessage?.contentBlocks,
    shouldApplyPendingAssistantText
      ? (pendingAssistantText ?? targetMessage.text)
      : targetMessage.text,
  );
  const hasExpectedRichContent = targetHasExpectedRichContent(
    targetMessage,
    trace.expectedRichContentType,
  );

  if (
    !activeSkills?.length &&
    !activeTools?.length &&
    !contentBlocks?.length &&
    !generatedFiles?.length &&
    !runtimeActivities?.length &&
    !shouldApplyPendingAssistantText
  ) {
    return {
      messages,
      resolved: hasExpectedRichContent || !trace.expectedRichContentType,
    };
  }

  const nextMessages = [...messages];
  nextMessages[targetIndex] = {
    ...targetMessage,
    ...(shouldApplyPendingAssistantText
      ? {
          isStreaming: false,
          isThinking: false,
          text: pendingAssistantText,
          timestamp:
            trace.assistantMessage?.timestamp ?? targetMessage.timestamp,
        }
      : {}),
    activeSkills,
    activeTools,
    contentBlocks,
    generatedFiles,
    runtimeActivities,
  };
  return { messages: nextMessages, resolved: hasExpectedRichContent };
}

function findPendingTraceTargetMessageIndex(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot,
  pendingUserMessages?: ChatMessage[],
) {
  const identityTargetIndex = findTraceIdentityTargetMessageIndex(
    messages,
    trace,
  );
  if (identityTargetIndex >= 0) {
    return identityTargetIndex;
  }

  // A known Run cannot borrow the next Run's answer while its own durable
  // row is absent (for example, when Stop interrupted it before any text).
  if (trace.assistantMessage?.runId || trace.assistantMessage?.messageId) {
    return -1;
  }

  if (trace.expectedRichContentType) {
    const richContentIndex = findExpectedRichContentTargetMessageIndex(
      messages,
      trace.expectedRichContentType,
      pendingUserMessages,
    );
    if (richContentIndex >= 0) {
      return richContentIndex;
    }
  }

  const assistantAfterUserIndex = findAssistantAfterPendingUserMessageIndex(
    messages,
    trace,
    pendingUserMessages,
  );
  if (assistantAfterUserIndex >= 0) {
    return assistantAfterUserIndex;
  }

  const pendingUserIndex = findPendingUserMessageIndexForAssistant(
    messages,
    trace.assistantMessage,
    pendingUserMessages,
  );
  if (trace.finalText?.trim()) {
    const targetIndex =
      pendingUserIndex >= 0
        ? findTraceTargetMessageIndexAfterUser(
            messages,
            trace.finalText,
            pendingUserIndex,
          )
        : -1;
    if (targetIndex >= 0) {
      return targetIndex;
    }
  }

  if (trace.finalText?.trim() && pendingUserIndex < 0) {
    const targetIndex = findTraceTargetMessageIndex(messages, trace.finalText);
    if (targetIndex >= 0) {
      return targetIndex;
    }
  }

  return findTraceOnlyTargetMessageIndex(messages, trace, pendingUserMessages);
}

function findTraceIdentityTargetMessageIndex(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot,
) {
  const assistant = trace.assistantMessage;
  if (!assistant) {
    return -1;
  }

  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (
      hasSameStableMessageId(messages[index] as StableChatMessage, assistant) ||
      isSameAssistantTurn(messages[index], assistant)
    ) {
      return index;
    }
  }

  return -1;
}

function findAssistantAfterPendingUserMessageIndex(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot,
  pendingUserMessages?: ChatMessage[],
) {
  const userIndex = findPendingUserMessageIndexForAssistant(
    messages,
    trace.assistantMessage,
    pendingUserMessages,
  );

  if (userIndex < 0) {
    return -1;
  }

  for (let index = userIndex + 1; index < messages.length; index += 1) {
    const message = messages[index];
    if (message.author === "team" && !isTraceOnlyMessage(message)) {
      return index;
    }
  }

  return -1;
}

function expectedRichContentTypeFromTools(
  activeTools: string[] | undefined,
): ExpectedRichContentType | undefined {
  if (activeTools?.includes("emit_company_plan_payload")) {
    return "ceo_company_plan_message";
  }
  if (activeTools?.includes("emit_discovery_payload")) {
    return "ceo_discovery_message";
  }
  return undefined;
}

function targetHasExpectedRichContent(
  message: ChatMessage,
  expectedRichContentType: ExpectedRichContentType | undefined,
) {
  return (
    !expectedRichContentType ||
    message.richContent?.type === expectedRichContentType
  );
}

function findLastPendingUserMessageIndex(
  messages: ChatMessage[],
  pendingUserMessages?: ChatMessage[],
) {
  if (!pendingUserMessages?.length) {
    return -1;
  }

  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (
      message.author === "user" &&
      pendingUserMessages.some((pending) =>
        isSameVisibleMessage(message, pending),
      )
    ) {
      return index;
    }
  }

  return -1;
}

function findExpectedRichContentTargetMessageIndex(
  messages: ChatMessage[],
  expectedRichContentType: ExpectedRichContentType,
  pendingUserMessages?: ChatMessage[],
) {
  const userIndex = findLastPendingUserMessageIndex(
    messages,
    pendingUserMessages,
  );

  for (let index = messages.length - 1; index > userIndex; index -= 1) {
    const message = messages[index];

    if (
      message.author === "team" &&
      message.richContent?.type === expectedRichContentType
    ) {
      return index;
    }
  }

  return -1;
}

function findTraceOnlyTargetMessageIndex(
  messages: ChatMessage[],
  trace: RuntimeTraceSnapshot,
  pendingUserMessages?: ChatMessage[],
) {
  const userIndex = findPendingUserMessageIndexForAssistant(
    messages,
    trace.assistantMessage,
    pendingUserMessages,
  );

  if (userIndex < 0) {
    return -1;
  }

  for (let index = messages.length - 1; index > userIndex; index -= 1) {
    const message = messages[index];

    if (isTraceOnlyMessage(message)) {
      return index;
    }
  }

  return -1;
}

export function activeStreamingStatesForConversation(
  streamingByKey: Record<string, StreamingAssistantState>,
  team: ChatTeamId,
  conversationId: string,
) {
  return Object.values(streamingByKey).filter(
    (streaming) =>
      streaming.team === team && streaming.conversationId === conversationId,
  );
}

export function mergeActiveStreamingMessages(
  messages: ChatMessage[],
  activeStreaming: StreamingAssistantState[],
  pendingUserMessages?: ChatMessage[],
) {
  if (!activeStreaming.length) {
    return messages;
  }

  let nextMessages = messages;
  for (const streaming of activeStreaming) {
    const streamingMessage = chatMessageFromStreamingState(streaming);
    const targetIndex = findTraceIdentityTargetMessageIndex(nextMessages, {
      assistantMessage: streamingMessage,
    });

    if (targetIndex >= 0) {
      if (nextMessages === messages) {
        nextMessages = [...messages];
      }
      continue;
    }

    if (
      isPendingAssistantSnapshotAlreadyPresent(
        nextMessages,
        streamingMessage,
        pendingUserMessages,
      )
    ) {
      continue;
    }

    if (nextMessages === messages) {
      nextMessages = [...messages];
    }
    nextMessages.splice(
      findActiveStreamingInsertIndex(
        nextMessages,
        streamingMessage,
        pendingUserMessages,
      ),
      0,
      streamingMessage,
    );
  }

  return nextMessages;
}

function chatMessageFromStreamingState(
  streaming: StreamingAssistantState,
): StableChatMessage {
  return {
    activeSkills: streaming.activeSkills,
    activeTools: streaming.activeTools,
    ...(streaming.actor ? { actor: streaming.actor } : {}),
    author: "team",
    ...(streaming.clientMessageId
      ? { clientMessageId: streaming.clientMessageId }
      : {}),
    ...(streaming.conversationId
      ? { conversationId: streaming.conversationId }
      : {}),
    ...(streaming.contentBlocks
      ? { contentBlocks: streaming.contentBlocks }
      : {}),
    ...(streaming.generatedFiles?.length
      ? { generatedFiles: streaming.generatedFiles }
      : {}),
    id: streaming.id,
    isStreaming: true,
    isThinking: streaming.isThinking,
    ...(streaming.afterUserInputRequestId
      ? { afterUserInputRequestId: streaming.afterUserInputRequestId }
      : {}),
    ...(streaming.itemId ? { itemId: streaming.itemId } : {}),
    ...(streaming.messageId ? { messageId: streaming.messageId } : {}),
    ...(streaming.messageSequence === undefined
      ? {}
      : { messageSequence: streaming.messageSequence }),
    ...(streaming.runId === undefined ? {} : { runId: streaming.runId }),
    runtimeActivities: streaming.runtimeActivities,
    team: streaming.team,
    text: streaming.text,
    timestamp: formatChatTimestamp(),
    ...(streaming.turnId !== undefined ? { turnId: streaming.turnId } : {}),
    ...(streaming.turnKey ? { turnKey: streaming.turnKey } : {}),
  };
}

function findActiveStreamingInsertIndex(
  messages: ChatMessage[],
  streamingMessage: ChatMessage,
  pendingUserMessages?: ChatMessage[],
) {
  const userIndex = findPendingUserMessageIndexForAssistant(
    messages,
    streamingMessage,
    pendingUserMessages,
  );

  return userIndex >= 0 ? userIndex + 1 : messages.length;
}

function findPendingUserMessageIndexForAssistant(
  messages: ChatMessage[],
  assistantMessage: ChatMessage | undefined,
  pendingUserMessages?: ChatMessage[],
) {
  // The prompt may already be acknowledged and removed from the pending
  // list. Its durable Run/client identity still anchors the reply.
  if (assistantMessage) {
    const anchorIndex = findLastIndex(
      messages,
      (message) =>
        message.author === "user" &&
        message.team === assistantMessage.team &&
        (!message.conversationId ||
          !assistantMessage.conversationId ||
          message.conversationId === assistantMessage.conversationId) &&
        (hasSameClientMessageId(message, assistantMessage) ||
          Boolean(
            assistantMessage.runId && message.runId === assistantMessage.runId,
          )),
    );
    if (anchorIndex >= 0) return anchorIndex;
    if (assistantMessage.runId) return -1;
  }

  if (!pendingUserMessages?.length) {
    return -1;
  }

  const matchingPendingUsers = assistantMessage?.clientMessageId
    ? pendingUserMessages.filter((pending) =>
        hasSameClientMessageId(pending, assistantMessage),
      )
    : pendingUserMessages;

  if (!matchingPendingUsers.length) {
    return -1;
  }

  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];

    if (
      message.author === "user" &&
      matchingPendingUsers.some((pending) =>
        isSameVisibleMessage(message, pending),
      )
    ) {
      return index;
    }
  }

  return -1;
}

function isTraceOnlyMessage(message: ChatMessage) {
  return Boolean(
    message.author === "team" &&
    !message.text.trim() &&
    (message.runtimeActivities?.length ||
      message.generatedFiles?.length ||
      message.activeTools?.length ||
      message.activeSkills?.length),
  );
}

function findTraceTargetMessageIndex(
  messages: ChatMessage[],
  finalText?: string,
) {
  const normalizedFinalText = finalText?.trim();

  if (!normalizedFinalText) {
    return -1;
  }

  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];

    if (message.author !== "team") {
      continue;
    }
    if (message.text.trim() === normalizedFinalText) {
      return index;
    }
  }

  return -1;
}

function findTraceTargetMessageIndexAfterUser(
  messages: ChatMessage[],
  finalText: string,
  userIndex: number,
) {
  const normalizedFinalText = finalText.trim();
  if (!normalizedFinalText) {
    return -1;
  }

  for (let index = userIndex + 1; index < messages.length; index += 1) {
    const message = messages[index];
    if (message.author === "user") {
      return -1;
    }
    if (
      message.author === "team" &&
      message.text.trim() === normalizedFinalText
    ) {
      return index;
    }
  }

  return -1;
}

function hasRuntimeTrace(trace: RuntimeTraceSnapshot) {
  return Boolean(trace.activeSkills?.length || trace.activeTools?.length);
}

function hasPendingHistorySnapshot(trace: RuntimeTraceSnapshot) {
  return Boolean(hasPendingAssistantSnapshot(trace) || hasRuntimeTrace(trace));
}

function hasPendingAssistantSnapshot(trace: RuntimeTraceSnapshot) {
  const assistant = trace.assistantMessage;
  return Boolean(
    assistant?.text.trim() ||
    assistant?.generatedFiles?.length ||
    assistant?.runtimeActivities?.length ||
    assistant?.activeSkills?.length ||
    assistant?.activeTools?.length,
  );
}

function isPendingAssistantSnapshotAlreadyPresent(
  messages: ChatMessage[],
  assistantMessage: ChatMessage,
  pendingUserMessages?: ChatMessage[],
) {
  const pendingUserIndex = findPendingUserMessageIndexForAssistant(
    messages,
    assistantMessage,
    pendingUserMessages,
  );

  return messages.some((message, index) => {
    if (pendingUserIndex >= 0 && index <= pendingUserIndex) {
      return false;
    }

    if (
      !isSameVisibleMessage(message, assistantMessage) &&
      !isSameAssistantTurn(message, assistantMessage)
    ) {
      return false;
    }

    if (!assistantMessage.text.trim()) {
      return Boolean(
        message.generatedFiles?.length ||
        message.runtimeActivities?.length ||
        message.activeSkills?.length ||
        message.activeTools?.length,
      );
    }

    return true;
  });
}
