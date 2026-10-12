import { mergeRuntimeActivities } from "@/features/chat-panel/model/chat-runtime-trace";
import { assistantTextFromBlocks } from "@/features/chat-panel/model/canonical-content-blocks";
import { mergeGeneratedFiles } from "@/features/chat-panel/model/chat-timeline";
import { generatedImagePreviewsFromActivities } from "@/features/chat-panel/model/generated-image-assets";
import type { ConversationCurrentRun } from "@/shared/api/types";
import type { ChatMessage, ChatMessageContentBlock } from "@/shared/chat/types";

export type ChatRunDisplayStatus =
  "finished" | "needs_input" | "stopped" | "working";

export type ChatTurnActivity = {
  currentRun?: ConversationCurrentRun | null;
  isContinuing?: boolean;
  pendingInputRunIds?: ReadonlySet<string>;
};

export type ChatConversationRenderItem =
  | {
      id: string;
      kind: "message";
      message: ChatMessage;
      messageIndex: number;
    }
  | {
      firstMessageIndex: number;
      hasTools: boolean;
      id: string;
      kind: "run";
      messages: ChatMessage[];
      runCreatedAt?: ChatMessage["runCreatedAt"];
      runId: string;
      status: ChatRunDisplayStatus;
      summaryMessage: ChatMessage;
    };

const activeRunStatuses = new Set<NonNullable<ChatMessage["runStatus"]>>([
  "queued",
  "deferred",
  "dispatched",
  "waiting_local_directory",
  "running",
]);

export function hidePreviousStopNotices(
  messages: ChatMessage[],
): ChatMessage[] {
  const latestPrompt = [...messages]
    .reverse()
    .find((message) => message.author === "user");
  if (!latestPrompt) return messages;
  const latestPromptIndex = messages.indexOf(latestPrompt);

  return messages.flatMap((message, index) => {
    if (
      message.author !== "team" ||
      !message.contentBlocks?.some(
        (block) => block.type === "text" && block.itemId === "terminal",
      )
    ) {
      return [message];
    }

    const promptIndex = messages.findIndex(
      (prompt) =>
        prompt.author === "user" &&
        ((message.runId && message.runId === prompt.runId) ||
          (message.clientMessageId &&
            message.clientMessageId === prompt.clientMessageId)),
    );
    if ((promptIndex >= 0 ? promptIndex : index) >= latestPromptIndex) {
      return [message];
    }

    // Only the client-added Stop notice expires. Agent text and tool/file
    // results remain available, even when history replay moves the notice.
    const contentBlocks = message.contentBlocks.filter(
      (block) => block.type !== "text" || block.itemId !== "terminal",
    );
    const text = assistantTextFromBlocks(contentBlocks);
    if (
      !contentBlocks.length &&
      !chatMessageHasTools(message) &&
      !message.generatedFiles?.length &&
      !message.richContent &&
      !message.runFailure
    ) {
      return [];
    }
    return [{ ...message, contentBlocks, text }];
  });
}

export function buildChatConversationRenderItems(
  messages: readonly ChatMessage[],
  { currentRun, isContinuing, pendingInputRunIds }: ChatTurnActivity = {},
): ChatConversationRenderItem[] {
  const items: ChatConversationRenderItem[] = [];
  const visibleRunIds = new Set(messages.map((message) => message.runId));
  const runSegments = new Map<string, number>();
  let pendingRun:
    | {
        firstMessageIndex: number;
        messages: ChatMessage[];
        runId: string;
      }
    | undefined;

  const flushRun = () => {
    if (!pendingRun) return;
    const { firstMessageIndex, messages: runMessages, runId } = pendingRun;
    const isLatestTurn =
      firstMessageIndex + runMessages.length === messages.length;
    const latestRunId = runMessages[runMessages.length - 1].runId;
    const latestRunMessages = runMessages.filter(
      (message) => message.runId === latestRunId,
    );
    const hasPendingInput = runMessages.some((message) =>
      pendingInputRunIds?.has(message.runId!),
    );
    const hasCurrentRun = Boolean(
      isLatestTurn &&
      currentRun &&
      (currentRun.id === latestRunId || !visibleRunIds.has(currentRun.id)),
    );
    const status =
      isLatestTurn && isContinuing
        ? "working"
        : hasPendingInput
          ? "needs_input"
          : hasCurrentRun
            ? "working"
            : chatRunDisplayStatus(latestRunMessages);
    // Only the final execution supplies the collapsed answer. Earlier tool
    // output and files keep their original Run provenance in the details.
    const summaryMessage = summarizeChatRun(latestRunMessages, status);
    const segment = runSegments.get(runId) ?? 0;
    runSegments.set(runId, segment + 1);
    items.push({
      firstMessageIndex,
      hasTools: hasPendingInput || runMessages.some(chatMessageHasTools),
      // Message acknowledgement changes temporary IDs, but not the chat turn.
      id: `run:${runId}:${runMessages[0].team}:${segment}`,
      kind: "run",
      messages: runMessages,
      runCreatedAt: runMessages.find((message) => message.runCreatedAt)
        ?.runCreatedAt,
      runId,
      status,
      summaryMessage,
    });
    pendingRun = undefined;
  };

  messages.forEach((message, messageIndex) => {
    if (message.author === "team" && message.runId) {
      // Backend continuations are still part of the same visible reply. A
      // human message or a different Agent/Conversation starts another group.
      if (
        pendingRun &&
        pendingRun.messages[0].team === message.team &&
        pendingRun.messages[0].conversationId === message.conversationId
      ) {
        pendingRun.messages.push(message);
        return;
      }
      flushRun();
      pendingRun = {
        firstMessageIndex: messageIndex,
        messages: [message],
        runId: message.runId,
      };
      return;
    }

    flushRun();
    items.push({
      id: `message:${message.team}:${message.id}`,
      kind: "message",
      message,
      messageIndex,
    });
  });
  flushRun();

  return items;
}

export function chatMessageHasTools(message: ChatMessage): boolean {
  return Boolean(
    message.activeTools?.length ||
    message.runtimeActivities?.length ||
    message.contentBlocks?.some((block) => block.type === "runtime_activity"),
  );
}

export function chatRunWaitingMessage(
  messages: readonly ChatMessage[],
  { currentRun, isContinuing, pendingInputRunIds }: ChatTurnActivity = {},
): string | null {
  if (!currentRun || isContinuing || pendingInputRunIds?.size) return null;
  const hasAssistantProgress = messages.some(
    (message) =>
      message.author === "team" &&
      (message.isThinking ||
        message.isStreaming ||
        (message.runId === currentRun.id &&
          (messageHasVisibleOutcome(message) || chatMessageHasTools(message)))),
  );
  if (hasAssistantProgress) return null;

  switch (currentRun.status) {
    case "queued":
      return "Queued; waiting for the run to start.";
    case "deferred":
      return "Queued for a later run attempt.";
    case "waiting_local_directory":
      return "Waiting for this machine's working directory to become free.";
    default:
      return null;
  }
}

function chatRunDisplayStatus(
  messages: readonly ChatMessage[],
): ChatRunDisplayStatus {
  const latestStatus = [...messages]
    .reverse()
    .find((message) => message.runStatus)?.runStatus;
  if (
    latestStatus === "failed" ||
    latestStatus === "cancelled" ||
    messages.some((message) => message.runFailure)
  ) {
    return "stopped";
  }
  if (messages.some((message) => message.pausedForUserInput)) {
    return "needs_input";
  }
  if (latestStatus === "completed") {
    return "finished";
  }
  if (
    (latestStatus && activeRunStatuses.has(latestStatus)) ||
    messages.some((message) => message.isStreaming || message.isThinking)
  ) {
    return "working";
  }
  return "finished";
}

function summarizeChatRun(
  messages: readonly ChatMessage[],
  status: ChatRunDisplayStatus,
): ChatMessage {
  if (status === "working" || status === "needs_input") {
    const latest = messages[messages.length - 1];
    // Collapsing live activity must not promote a progress update to a final
    // answer. Keep only the metadata needed by the Run header.
    return {
      id: latest.id,
      team: latest.team,
      author: latest.author,
      actor: latest.actor,
      conversationId: latest.conversationId,
      createdAt: latest.createdAt,
      runId: latest.runId,
      runStatus: latest.runStatus,
      timestamp: latest.timestamp,
      text: "",
    };
  }
  const baseMessage =
    [...messages].reverse().find(messageHasVisibleOutcome) ??
    messages[messages.length - 1];
  const finalText = findLastAssistantText(messages);
  const richContent = baseMessage.richContent;
  const runFailure = [...messages]
    .reverse()
    .find((message) => message.runFailure)?.runFailure;
  const runStatus = [...messages]
    .reverse()
    .find((message) => message.runStatus)?.runStatus;
  const activeTools = uniqueStrings(
    messages.flatMap((message) => message.activeTools ?? []),
  );
  const activeSkills = uniqueStrings(
    messages.flatMap((message) => message.activeSkills ?? []),
  );
  const runtimeActivities = messages.reduce(
    (current, message) =>
      mergeRuntimeActivities(current, message.runtimeActivities),
    undefined as ChatMessage["runtimeActivities"],
  );
  const generatedFiles = messages.reduce(
    (current, message) => mergeGeneratedFiles(current, message.generatedFiles),
    undefined as ChatMessage["generatedFiles"],
  );

  return {
    ...baseMessage,
    ...(activeSkills?.length ? { activeSkills } : { activeSkills: undefined }),
    ...(activeTools?.length ? { activeTools } : { activeTools: undefined }),
    contentBlocks: finalText?.block ? [finalText.block] : undefined,
    generatedFiles,
    isStreaming: messages.some((message) => message.isStreaming),
    isThinking: messages.some((message) => message.isThinking),
    pausedForUserInput: messages.some((message) => message.pausedForUserInput),
    richContent,
    runFailure,
    runStatus,
    runtimeActivities,
    text: finalText?.text ?? "",
  };
}

function messageHasVisibleOutcome(message: ChatMessage): boolean {
  return Boolean(
    message.text.trim() ||
    message.richContent ||
    message.runFailure ||
    message.generatedFiles?.length ||
    generatedImagePreviewsFromActivities(message.runtimeActivities).length ||
    message.runtimeActivities?.some(
      (activity) =>
        activity.toolName === "present_output" ||
        activity.toolName.endsWith(".present_output") ||
        activity.toolName.endsWith("/present_output") ||
        (activity.toolName.startsWith("mcp__") &&
          activity.toolName.endsWith("__present_output")),
    ),
  );
}

function findLastAssistantText(messages: readonly ChatMessage[]):
  | {
      block?: Extract<ChatMessageContentBlock, { type: "text" }>;
      text: string;
    }
  | undefined {
  for (
    let messageIndex = messages.length - 1;
    messageIndex >= 0;
    messageIndex -= 1
  ) {
    const message = messages[messageIndex];
    for (
      let blockIndex = (message.contentBlocks?.length ?? 0) - 1;
      blockIndex >= 0;
      blockIndex -= 1
    ) {
      const block = message.contentBlocks?.[blockIndex];
      if (block?.type === "text" && block.text.trim()) {
        return { block, text: block.text };
      }
    }
    if (message.text.trim()) {
      return { text: message.text };
    }
  }
  return undefined;
}

function uniqueStrings(values: readonly string[]): string[] | undefined {
  const unique = [...new Set(values)];
  return unique.length ? unique : undefined;
}
