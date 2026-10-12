import { canonicalUuid } from "@/shared/lib/uuid";
import type { FileTransferHandler } from "@/shared/chat/attachment-preview";
import { useCallback, useEffect, useRef, useState } from "react";
import { useStore } from "react-redux";

import {
  createTeamChatMessage,
  formatChatTimestamp,
} from "@/features/chat-panel/utils";
import type { AgentRealtimeConnectionStatus } from "@/features/chat-panel/model/chat-connection-recovery";
import { parseCeoCompanyPlanMessagePayload } from "@/features/chat-panel/model/ceo-company-plan-message";
import { parseCeoDiscoveryMessagePayload } from "@/features/chat-panel/model/ceo-discovery-message";
import {
  appendRuntimeToolActivity,
  mergeRuntimeActivities,
  runtimeActivitiesFromTools,
} from "@/features/chat-panel/model/chat-runtime-trace";
import {
  appendAssistantTextBlock,
  assistantTextFromBlocks,
  completeAssistantTextBlock,
  completeReasoningTextBlock,
} from "@/features/chat-panel/model/canonical-content-blocks";
import {
  hasSameStableMessageId,
  StableChatMessageIndex,
  type StableChatMessage,
} from "@/features/chat-panel/model/chat-message-convergence";
import {
  adoptableStoredEnvelope,
  clientStreamingKey,
  committedAgentSequencesForRun,
  conversationRunStreamingKey,
  conversationStreamingAliasKey,
  hasSameClientMessageId,
  isSameAssistantTurn,
  isSameVisibleMessage,
  rebindStreamingStateToDurableRow,
  retireCompletedStreamingAliases,
  sameStreamingAssistant,
  type StreamingAssistantState,
} from "@/features/chat-panel/model/chat-streaming-identity";
import {
  appendNewerHistoryWindow,
  liveRunTimelineSegment,
  mergeDurableRunTimeline,
  mergeGeneratedFiles,
  mergeLatestHistoryWindow,
  mergeTraceList,
  prependOlderHistoryWindow,
  runTimelineFromRunMessages,
  runTimelineSegmentForMessage,
  runtimeActivitiesFromTrace,
  runtimeTraceFromPayload,
  runtimeTraceRecord,
  stringFromUnknown,
  stringifyDisplayPayloadValue,
  type DurableConversationHistory,
  type DurableRunTimeline,
} from "@/features/chat-panel/model/chat-timeline";
import {
  publishQueuedReceipt,
  requestConversationRunQueueRefresh,
} from "@/features/chat-panel/hooks/use-conversation-run-queue";
import { CEO_CHAT_TEAM_ID, type ChatTeamId } from "@/shared/chat/chat-team";
import type {
  ChatMessage,
  ChatMessageContentBlock,
  ChatRunFailure,
  ChatRuntimeActivity,
} from "@/shared/chat/types";
import {
  boardlyApi,
  cockpitService,
  conversationService,
  runService,
  workspaceApi,
} from "@/shared/api";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type { TaskWorkspaceResponse } from "@/shared/api/task.service";
import type {
  ActorRefDto,
  AgentProvider,
  ConversationDisplayItemSummary,
  ConversationTranscriptEntryDto,
  ResourceAttachmentSnapshot,
  RunDto,
  RunStatusDto,
} from "@/shared/api/types";
import {
  runCancellationCopy,
  runFailureCopy,
} from "@/shared/agent-execution/run-access-reasons";
import {
  createConversationSocketSession,
  ConversationMessageRejectedError,
  describeConversationSendFailure,
  type ConversationMessageReceipt,
  type ConversationRunStreamEvent,
} from "@/shared/realtime/conversation-socket.service";
import {
  appendChatMessage,
  setTeamChatMessages,
  updateChatMessage,
  useAppDispatch,
  type RootState,
} from "@/state";

const HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX = "ui-control-";
const HISTORY_LOAD_ERROR = "Earlier messages could not be loaded.";
const RUN_TRANSCRIPT_PAGE_SIZE = 200;

function agentIsThinkingAfterRunEvent(event: ConversationRunStreamEvent) {
  if (event.kind === "delta") {
    return event.channel === "thinking";
  }

  if (event.kind === "scratch_reset") {
    return event.reason !== "terminal";
  }

  if (event.kind === "lifecycle") {
    return (
      event.status !== "completed" &&
      event.status !== "failed" &&
      event.status !== "cancelled"
    );
  }

  if (event.scratch.text) {
    return false;
  }
  if (event.scratch.thinking) {
    return true;
  }

  const lastPart = event.message.parts[event.message.parts.length - 1];
  return lastPart?.type !== "toolCall";
}

type ConversationSocketSession = ReturnType<
  typeof createConversationSocketSession
>;

type RuntimeTraceSnapshot = {
  activeSkills?: string[];
  activeTools?: string[];
  assistantMessage?: StableChatMessage;
  expectedRichContentType?: ExpectedRichContentType;
  finalText?: string;
};
type ExpectedRichContentType = NonNullable<ChatMessage["richContent"]>["type"];
type DisplayToolTrace = {
  activeTools?: string[];
  lastSequence?: number;
  runtimeActivities?: ChatRuntimeActivity[];
  turnKey?: string;
};
type PendingHistoryMergeResult = {
  messages: ChatMessage[];
  traceResolved: boolean;
  userMessagesResolved: boolean;
};
type DurableHistoryReplay = (
  messageTeam: ChatTeamId,
  conversationId: string,
) => Promise<string>;
type ChatHistoryPaginationState = {
  conversationId: string | null;
  error: string | null;
  hasOlder: boolean;
  isLoading: boolean;
};

export type SendAgentMessageOptions = {
  onFileTransfer?: FileTransferHandler;
  clientMessageId?: string;
  requestKey?: string;
  displayUserMessage?: boolean;
  hiddenTranscript?: boolean;
};

type ActiveConversationSocket = {
  conversationId: string;
  socket: ConversationSocketSession;
  unsubscribe: () => void;
  unsubscribeRecovery: () => void;
  unsubscribeStatus: () => void;
};

type ResolveConversation = (team: ChatTeamId) => Promise<string | null>;
type ChatMessageChanges = Partial<Omit<StableChatMessage, "id" | "team">>;
type ChatMessageStore = {
  appendMessage?: (message: ChatMessage) => void;
  setMessages?: (team: ChatTeamId, messages: ChatMessage[]) => void;
  updateMessage?: (
    team: ChatTeamId,
    id: number,
    changes: ChatMessageChanges,
  ) => void;
};

type UseAgentRealtimeChatOptions = {
  assistantActor?: ActorRefDto;
  conversationKey?: string | null;
  messageStore?: ChatMessageStore;
  loadInitialConversation?: () => Promise<string | null>;
  loadInitialTaskWorkspace?: () => Promise<TaskWorkspaceResponse | null>;
  loadActiveRunIds?: (conversationId: string) => Promise<string[]>;
  resolveConversation: ResolveConversation;
  taskId?: string;
  team: ChatTeamId;
  unavailableMessage?: string;
};

type ChatMessageIdentity = Pick<
  ChatMessage,
  | "clientMessageId"
  | "conversationId"
  | "afterUserInputRequestId"
  | "itemId"
  | "runId"
  | "turnId"
  | "turnKey"
> & {
  messageId?: string;
  messageSequence?: number;
};

function filesFromPayloadAttachments(
  payload: ConversationDisplayItemSummary["payload"],
): ChatMessage["files"] | undefined {
  const attachments = payload.attachments;
  if (!Array.isArray(attachments)) {
    return undefined;
  }

  const files = attachments.flatMap((attachment) => {
    if (!runtimeTraceRecord(attachment)) {
      return [];
    }

    const filename =
      stringFromUnknown(attachment.file_name) ??
      stringFromUnknown(attachment.filename) ??
      "attachment";
    const mediaType =
      stringFromUnknown(attachment.contentType) ??
      stringFromUnknown(attachment.content_type) ??
      stringFromUnknown(attachment.mediaType) ??
      stringFromUnknown(attachment.media_type) ??
      "application/octet-stream";

    return [
      {
        filename,
        mediaType,
        type: "file" as const,
        url: stringFromUnknown(attachment.url) ?? "",
      },
    ];
  });

  return files.length ? files : undefined;
}

function numberFromUnknown(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized = value.trim();
  if (!/^\d+$/.test(normalized)) {
    return undefined;
  }
  return Number(normalized);
}

function actorRefFromUnknown(value: unknown): ActorRefDto | undefined {
  if (!runtimeTraceRecord(value)) {
    return undefined;
  }
  const id = stringFromUnknown(value.id);
  const kind = value.kind;
  const name = stringFromUnknown(value.name);
  const avatarUrl = value.avatarUrl;

  if (
    !id ||
    (kind !== "user" && kind !== "agent") ||
    !name ||
    (avatarUrl !== null && typeof avatarUrl !== "string")
  ) {
    return undefined;
  }

  return { avatarUrl, id, kind, name };
}

function turnIdFromItemId(itemId: string | null | undefined) {
  const match = itemId?.match(/^assistant:turn:(\d+)$/);
  return match ? Number(match[1]) : undefined;
}

function chatMessageIdentityFromDisplayItem(
  item: ConversationDisplayItemSummary,
  conversationId?: string,
): ChatMessageIdentity {
  const clientMessageId =
    stringFromUnknown(item.payload.client_message_id) ??
    stringFromUnknown(item.payload.client_id);
  const itemId = stringFromUnknown(item.item_id);
  const identityItemId =
    item.role === "assistant" && itemId?.startsWith("assistant:turn:")
      ? itemId
      : undefined;
  const turnId =
    numberFromUnknown(item.payload.turn_id) ?? turnIdFromItemId(item.item_id);
  const turnKey = stringFromUnknown(item.turn_id);
  const runId = runIdFromDisplayItem(item);

  return {
    ...(clientMessageId ? { clientMessageId } : {}),
    ...(conversationId ? { conversationId } : {}),
    ...(identityItemId ? { itemId: identityItemId } : {}),
    messageId: item.event_id,
    messageSequence: item.sequence,
    ...(runId === undefined ? {} : { runId }),
    ...(turnId !== undefined ? { turnId } : {}),
    ...(turnKey ? { turnKey } : {}),
  };
}

// Every conversation item names its run, on either author.
function runIdFromDisplayItem(item: ConversationDisplayItemSummary) {
  return canonicalUuid(item.payload.run_id);
}

function hiddenTranscriptContent(content: string) {
  try {
    const parsed = JSON.parse(content) as unknown;
    return runtimeTraceRecord(parsed) && parsed.hidden_from_transcript === true;
  } catch {
    return false;
  }
}

function hiddenTranscriptDisplayItem(item: ConversationDisplayItemSummary) {
  const clientMessageId = stringFromUnknown(item.payload.client_message_id);

  return (
    clientMessageId?.startsWith(HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX) ||
    item.payload.hidden_from_transcript === true ||
    hiddenTranscriptContent(item.content)
  );
}

// The last-resort sentence, used only when the server sent no failure copy
// (a frame from an older server, or a run that never got a classifier). Every
// classified failure carries its own provider-aware sentence.
const GENERIC_RUN_FAILURE_MESSAGE = "The agent could not finish this turn.";

function terminalFailureMessage(message?: string | null): string {
  return message?.trim() || GENERIC_RUN_FAILURE_MESSAGE;
}

// The slice of a Run the failure join needs, so a live lifecycle frame can
// stand in for a REST row until the next history load lists the real one.
// A frame may lack the attempt numbers and never names the runtime; a REST
// row always carries both, the runtime named to its registrant alone.
type RunFailureSource = Pick<
  RunDto,
  | "failureDetail"
  | "failureMessage"
  | "failureReason"
  | "id"
  | "parentRunId"
  | "status"
  | "updatedAt"
> &
  Partial<
    Pick<
      RunDto,
      "attempt" | "createdAt" | "error" | "maxAttempts" | "runtimeId"
    >
  > & {
    provider: AgentProvider | null;
  };

// The one adapter from a lifecycle frame to the Run slice: the frame's own
// clock when it carries one, its failure copy when it failed.
function runFailureSourceFromLifecycle(
  event: Extract<ConversationRunStreamEvent, { kind: "lifecycle" }>,
): RunFailureSource {
  return {
    ...(event.attempt === undefined ? {} : { attempt: event.attempt }),
    ...(event.createdAt === undefined ? {} : { createdAt: event.createdAt }),
    ...(event.maxAttempts === undefined
      ? {}
      : { maxAttempts: event.maxAttempts }),
    failureDetail: event.failure?.detail ?? null,
    failureMessage: event.failure?.message ?? null,
    failureReason: event.failure?.reason ?? null,
    id: event.runId,
    parentRunId: event.parentRunId ?? null,
    provider: event.failure?.provider ?? null,
    status: event.status,
    updatedAt: event.updatedAt ?? new Date().toISOString(),
  };
}

function runFailureFromRun(
  run: RunFailureSource,
  children: readonly RunFailureSource[],
): ChatRunFailure {
  return {
    ...(run.attempt === undefined ? {} : { attempt: run.attempt }),
    detail: run.failureDetail ?? null,
    ...(run.maxAttempts === undefined ? {} : { maxAttempts: run.maxAttempts }),
    // Project access ends Runs two ways (ADR 0017): a failure whose server
    // sentence may be missing, and a cancellation with only a marker.
    message: terminalFailureMessage(
      runFailureCopy(run.failureReason, run.failureMessage) ??
        runCancellationCopy(run),
    ),
    provider: run.provider ?? null,
    ranOnViewerComputer: Boolean(run.runtimeId),
    reason: runCancellationCopy(run)
      ? "project_access_revoked"
      : run.failureReason,
    retried: children.length > 0,
    retryActive: children.some(
      (child) => !TERMINAL_RUN_STATUSES.has(child.status),
    ),
    runId: run.id,
  };
}

// attachRunFailures makes every assistant row carry its Run's authoritative
// lifecycle state and makes failed Runs visible after a reload. The durable
// transcript only holds what people and agents said, so a Run
// that failed before saying anything leaves no row; this joins the Run list
// back in: a failed Run's copy lands on the assistant row it did write, or
// on a synthetic row right after the person's message that started it, or —
// for a Run with no words of its own, like a task assignment — after the
// last row older than the Run. Failures older than the loaded window are
// left for the page that will contain their anchor.
export function attachRunFailures(
  messages: readonly StableChatMessage[],
  runs: readonly RunFailureSource[],
  team: ChatTeamId,
  conversationId: string,
  window: { hasOlder: boolean } = { hasOlder: false },
): StableChatMessage[] {
  const runsById = new Map(runs.map((run) => [run.id, run]));
  const messagesWithRunStatus = messages.map((message) => {
    if (message.author !== "team" || !message.runId) return message;
    const run = runsById.get(message.runId);
    return run
      ? {
          ...message,
          ...(run.createdAt ? { runCreatedAt: run.createdAt } : {}),
          runStatus: run.status,
        }
      : message;
  });
  // A Run cancelled because its member lost the Project (ADR 0017) says so
  // in the chat like a failure, since nobody pressed Stop.
  const failed = runs
    .filter(
      (run) => run.status === "failed" || runCancellationCopy(run) !== null,
    )
    .sort(
      (left, right) => Date.parse(left.updatedAt) - Date.parse(right.updatedAt),
    );
  if (failed.length === 0) return messagesWithRunStatus;
  const childrenByParent = new Map<string, RunFailureSource[]>();
  for (const run of runs) {
    if (!run.parentRunId) continue;
    childrenByParent.set(run.parentRunId, [
      ...(childrenByParent.get(run.parentRunId) ?? []),
      run,
    ]);
  }
  const result: StableChatMessage[] = [...messagesWithRunStatus];
  const earliestCreatedAt = Math.min(
    ...messages
      .map((message) =>
        message.createdAt ? Date.parse(message.createdAt) : Number.NaN,
      )
      .filter(Number.isFinite),
  );
  // Synthetic rows are grouped behind the real row they follow, so several
  // failures between two durable items get distinct, ordered ids.
  const pendingByAnchor = new Map<
    number,
    { failure: ChatRunFailure; failedAt: number; status: RunStatusDto }[]
  >();
  for (const run of failed) {
    const children = childrenByParent.get(run.id) ?? [];
    const failure = runFailureFromRun(run, children);
    const assistantIndex = findLastIndex(
      result,
      (message) => message.author === "team" && message.runId === run.id,
    );
    if (assistantIndex >= 0) {
      const current = result[assistantIndex];
      result[assistantIndex] = { ...current, runFailure: failure };
      continue;
    }
    const failedAt = Date.parse(run.updatedAt);
    const anchorIndex = findLastIndex(
      messages,
      (message) => message.author === "user" && message.runId === run.id,
    );
    if (
      anchorIndex < 0 &&
      window.hasOlder &&
      Number.isFinite(earliestCreatedAt) &&
      failedAt < earliestCreatedAt
    ) {
      continue;
    }
    const fallbackIndex = findLastIndex(messages, (message) => {
      const createdAt = message.createdAt
        ? Date.parse(message.createdAt)
        : Number.NaN;
      return Number.isFinite(createdAt) && createdAt <= failedAt;
    });
    const anchorId =
      messages[anchorIndex >= 0 ? anchorIndex : fallbackIndex]?.id ?? 0;
    pendingByAnchor.set(anchorId, [
      ...(pendingByAnchor.get(anchorId) ?? []),
      { failure, failedAt, status: run.status },
    ]);
  }
  for (const [anchorId, group] of pendingByAnchor) {
    group.forEach(({ failure, failedAt, status }, index) => {
      result.push({
        author: "team",
        conversationId,
        createdAt: new Date(failedAt).toISOString(),
        // Fractions between the anchor and the next durable seq keep the
        // rows in failure order without colliding with a real item.
        id: anchorId + (index + 1) / (group.length + 1),
        isStreaming: false,
        runFailure: failure,
        runId: failure.runId,
        runStatus: status,
        team,
        text: "",
        timestamp: formatChatTimestamp(failedAt),
      });
    });
  }
  return result.sort((left, right) => left.id - right.id);
}

function findLastIndex<T>(
  items: readonly T[],
  predicate: (item: T) => boolean,
): number {
  for (let index = items.length - 1; index >= 0; index -= 1) {
    if (predicate(items[index])) return index;
  }
  return -1;
}

// mergeConversationRuns keeps whichever record of a Run knows more: a terminal
// status over a live one, else the newer one. A frame never names the
// runtime, so it keeps the one a REST row already named: a Run stays on its
// authorizer's own computers.
function mergeConversationRuns(
  known: readonly RunFailureSource[],
  incoming: readonly RunFailureSource[],
): RunFailureSource[] {
  const byId = new Map(known.map((run) => [run.id, run]));
  for (const run of incoming) {
    const current = byId.get(run.id);
    if (
      !current ||
      TERMINAL_RUN_STATUSES.has(run.status) ||
      (!TERMINAL_RUN_STATUSES.has(current.status) &&
        Date.parse(run.updatedAt) >= Date.parse(current.updatedAt))
    ) {
      // A frame has no `error` either; keep the REST row's cancellation
      // marker, which is the only place a Project access stop is named.
      byId.set(run.id, {
        ...run,
        ...(run.createdAt === undefined && current?.createdAt
          ? { createdAt: current.createdAt }
          : {}),
        ...(run.runtimeId === undefined && current?.runtimeId
          ? { runtimeId: current.runtimeId }
          : {}),
        ...(run.error === undefined && current?.error
          ? { error: current.error }
          : {}),
      });
    }
  }
  return [...byId.values()];
}

const TERMINAL_RUN_STATUSES = new Set<RunDto["status"]>([
  "completed",
  "failed",
  "cancelled",
]);

function stoppedMessage(): string {
  return "Stopped.";
}

// A failed Run must never keep the transcript from loading: when the Run list
// cannot be read the history still renders, with whatever failures the
// lifecycle frames already reported.
async function loadConversationRuns(
  conversationId: string,
): Promise<RunDto[] | null> {
  try {
    return await runService.listAllRuns(requireActiveCompanyId(), {
      conversationId,
    });
  } catch (error) {
    console.warn("Conversation runs could not be loaded", error);
    return null;
  }
}

function assistantDisplayText(item: ConversationDisplayItemSummary): string {
  return item.content;
}

export function displayItemToChatMessage(
  item: ConversationDisplayItemSummary,
  team: ChatTeamId,
  conversationId?: string,
  runMessages: readonly ConversationTranscriptEntryDto[] = [],
): StableChatMessage | null {
  const identity = chatMessageIdentityFromDisplayItem(item, conversationId);
  const actor = actorRefFromUnknown(item.payload.author);

  if (item.role === "user") {
    if (hiddenTranscriptDisplayItem(item)) {
      return null;
    }

    const files = filesFromPayloadAttachments(item.payload);
    return {
      ...(actor ? { actor } : {}),
      author: "user",
      createdAt: item.created_at,
      ...(files?.length ? { files } : {}),
      ...(item.payload.attachments_withheld === true
        ? { attachmentsWithheld: true }
        : {}),
      id: item.sequence,
      ...identity,
      team,
      text: item.content,
    };
  }

  if (item.role !== "assistant" && item.role !== "status") {
    return null;
  }

  const richContent =
    parseCeoDiscoveryMessagePayload(item.payload) ??
    parseCeoCompanyPlanMessagePayload(item.payload) ??
    undefined;
  const durableTrace = runtimeTraceFromPayload(item.payload);
  const activeSkills = richContent?.runtime_trace?.active_skills.length
    ? richContent.runtime_trace.active_skills
    : durableTrace.activeSkills;
  const runTimeline = runTimelineFromRunMessages(runMessages, activeSkills);
  const activeTools = mergeTraceList(
    richContent?.runtime_trace?.used_tools.length
      ? richContent.runtime_trace.used_tools
      : durableTrace.activeTools,
    runTimeline.activeTools,
  );
  const runtimeActivities = runtimeActivitiesFromTools(
    activeTools,
    activeSkills,
    "before_message",
  );
  const mergedRuntimeActivities = mergeRuntimeActivities(
    runtimeActivities,
    mergeRuntimeActivities(
      runTimeline.runtimeActivities,
      mergeRuntimeActivities(
        durableTrace.runtimeActivities,
        runtimeActivitiesFromTrace(
          richContent?.runtime_trace?.tool_activities,
          activeSkills,
        ),
      ),
    ),
  );

  return {
    ...(activeSkills?.length ? { activeSkills } : {}),
    ...(activeTools?.length ? { activeTools } : {}),
    ...(actor ? { actor } : {}),
    author: "team",
    createdAt: item.created_at,
    ...(runTimeline.contentBlocks?.length
      ? { contentBlocks: runTimeline.contentBlocks }
      : {}),
    ...(runTimeline.generatedFiles?.length
      ? { generatedFiles: runTimeline.generatedFiles }
      : {}),
    id: item.sequence,
    ...identity,
    richContent,
    ...(mergedRuntimeActivities?.length
      ? { runtimeActivities: mergedRuntimeActivities }
      : {}),
    team,
    text: assistantDisplayText(item),
    ...(runTimeline.userInputBoundaries?.length
      ? { userInputBoundaries: runTimeline.userInputBoundaries }
      : {}),
  };
}

export function displayItemsToChatMessages(
  items: readonly ConversationDisplayItemSummary[],
  team: ChatTeamId,
  conversationId?: string,
  runTimelines: Readonly<
    Record<string, readonly ConversationTranscriptEntryDto[]>
  > = {},
): StableChatMessage[] {
  const messages: StableChatMessage[] = [];
  const assistantSequencesByRun = indexAssistantSequencesByRun(items);
  let pendingToolTrace: DisplayToolTrace | undefined;

  items.forEach((item) => {
    if (item.role === "tool") {
      pendingToolTrace = mergeDisplayToolTrace(
        pendingToolTrace,
        displayToolTraceFromItem(item),
      );
      return;
    }

    const runId = runIdFromDisplayItem(item);
    const runMessages =
      runId === undefined
        ? undefined
        : runTimelineSegmentForMessage(
            runTimelines[runId] ?? [],
            item.sequence,
            assistantSequencesByRun.get(runId) ?? [],
          );
    const message = displayItemToChatMessage(
      item,
      team,
      conversationId,
      runMessages,
    );
    if (!message) {
      return;
    }

    if (message.author === "team" && pendingToolTrace) {
      messages.push(
        mergeDisplayToolTraceIntoMessage(message, pendingToolTrace),
      );
      pendingToolTrace = undefined;
      return;
    }

    if (message.author === "user" && pendingToolTrace) {
      messages.push(
        traceOnlyChatMessage(
          pendingToolTrace,
          team,
          item.sequence,
          conversationId,
        ),
      );
      pendingToolTrace = undefined;
    }

    messages.push(message);
  });

  if (pendingToolTrace) {
    messages.push(
      traceOnlyChatMessage(pendingToolTrace, team, 0, conversationId),
    );
  }

  // One conversation, one sequence: position is the item's own `seq`, which is
  // `id` on every durable row here. Nothing is spliced against a turn key any
  // more, because the stream already records the order things happened in.
  return messages.sort((left, right) => left.id - right.id);
}

function indexAssistantSequencesByRun(
  items: readonly ConversationDisplayItemSummary[],
) {
  const sequencesByRun = new Map<string, number[]>();
  for (const item of items) {
    if (item.role !== "assistant") {
      continue;
    }
    const runId = runIdFromDisplayItem(item);
    if (runId === undefined) {
      continue;
    }
    const sequences = sequencesByRun.get(runId) ?? [];
    sequences.push(item.sequence);
    sequencesByRun.set(runId, sequences);
  }
  return sequencesByRun;
}

function displayToolTraceFromItem(
  item: ConversationDisplayItemSummary,
): DisplayToolTrace | undefined {
  const toolName = toolNameFromDisplayToolItem(item);
  if (!toolName) {
    return undefined;
  }

  return {
    activeTools: [toolName],
    lastSequence: item.sequence,
    runtimeActivities: appendRuntimeToolActivity(undefined, {
      callId: stringFromUnknown(item.payload.call_id),
      input: inputFromDisplayToolItem(item),
      itemId:
        stringFromUnknown(item.payload.item_id) ??
        stringFromUnknown(item.item_id),
      output: outputFromDisplayToolItem(item),
      position: "before_message",
      status:
        stringFromUnknown(item.payload.status) ??
        stringFromUnknown(item.content),
      toolName,
    }),
    turnKey: stringFromUnknown(item.turn_id),
  };
}

function toolNameFromDisplayToolItem(
  item: ConversationDisplayItemSummary,
): string | undefined {
  const payloadType = stringFromUnknown(item.payload.type);
  const explicitToolName =
    stringFromUnknown(item.payload.tool_name) ??
    stringFromUnknown(item.payload.name) ??
    stringFromUnknown(item.payload.execution);

  if (explicitToolName) {
    return normalizeDisplayToolName(explicitToolName);
  }

  if (payloadType === "web_search_call") {
    return "web_search";
  }
  if (payloadType === "image_generation_call") {
    return "generate_image";
  }
  if (
    payloadType === "tool_search_call" ||
    payloadType === "tool_search_output"
  ) {
    return "tool_search";
  }
  if (
    payloadType === "function_call_output" ||
    payloadType === "tool_result" ||
    payloadType === "custom_tool_call_output"
  ) {
    return "tool";
  }

  return undefined;
}

function normalizeDisplayToolName(value: string) {
  if (value === "image_generation_call") {
    return "generate_image";
  }
  if (value === "web_search_call") {
    return "web_search";
  }
  return value;
}

function inputFromDisplayToolItem(
  item: ConversationDisplayItemSummary,
): string | undefined {
  const payloadType = stringFromUnknown(item.payload.type);
  if (payloadType === "function_call") {
    return stringFromUnknown(item.payload.arguments);
  }
  if (payloadType === "custom_tool_call") {
    return stringFromUnknown(item.payload.input);
  }

  const argumentsValue = item.payload.arguments;
  if (argumentsValue !== undefined) {
    return stringifyDisplayPayloadValue(argumentsValue);
  }

  const actionValue = item.payload.action;
  if (actionValue !== undefined) {
    return stringifyDisplayPayloadValue(actionValue);
  }

  return undefined;
}

function outputFromDisplayToolItem(
  item: ConversationDisplayItemSummary,
): string | undefined {
  if (item.payload.output !== undefined) {
    return stringifyDisplayPayloadValue(item.payload.output);
  }
  if (item.payload.result !== undefined) {
    return stringifyDisplayPayloadValue(item.payload.result);
  }
  return undefined;
}

function mergeDisplayToolTrace(
  current: DisplayToolTrace | undefined,
  next: DisplayToolTrace | undefined,
): DisplayToolTrace | undefined {
  if (!current) {
    return next;
  }
  if (!next) {
    return current;
  }
  return {
    activeTools: mergeTraceList(current.activeTools, next.activeTools),
    lastSequence: Math.max(current.lastSequence ?? 0, next.lastSequence ?? 0),
    runtimeActivities: mergeRuntimeActivities(
      current.runtimeActivities,
      next.runtimeActivities,
    ),
    turnKey: current.turnKey ?? next.turnKey,
  };
}

function mergeDisplayToolTraceIntoMessage(
  message: ChatMessage,
  trace: DisplayToolTrace,
): ChatMessage {
  return {
    ...message,
    activeTools: mergeTraceList(message.activeTools, trace.activeTools),
    runtimeActivities: mergeRuntimeActivities(
      message.runtimeActivities,
      trace.runtimeActivities,
    ),
  };
}

function traceOnlyChatMessage(
  trace: DisplayToolTrace,
  team: ChatTeamId,
  fallbackId = 0,
  conversationId?: string,
): ChatMessage {
  return {
    activeTools: trace.activeTools,
    author: "team",
    ...(conversationId ? { conversationId } : {}),
    id: trace.lastSequence ?? fallbackId,
    isStreaming: true,
    runtimeActivities: trace.runtimeActivities,
    team,
    text: "",
    ...(trace.turnKey ? { turnKey: trace.turnKey } : {}),
  };
}

function appendAssistantTerminalTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  text: string,
) {
  if (!text) {
    return blocks;
  }

  const next = blocks ? [...blocks] : [];
  const separator = assistantTextFromBlocks(next).trim() ? "\n\n" : "";
  next.push({
    id: `text:terminal:${next.length}`,
    itemId: "terminal",
    text: `${separator}${text}`,
    type: "text",
  });
  return next;
}

function contentBlocksForCanonicalText(
  blocks: ChatMessageContentBlock[] | undefined,
  canonicalText: string,
) {
  if (!blocks?.length) {
    return undefined;
  }

  const streamedText = assistantTextFromBlocks(blocks);
  if (!canonicalText.trim() || streamedText === canonicalText) {
    return blocks;
  }
  if (canonicalText.startsWith(streamedText)) {
    return appendAssistantTextBlock(
      blocks,
      canonicalText.slice(streamedText.length),
      undefined,
    );
  }

  return undefined;
}

export function shouldReplayDurableHistoryAfterFailedReceipt(
  receipt: ConversationMessageReceipt,
): boolean {
  const persistedFailureMessages = new Set([
    "agent service is unavailable",
    `agent ${"run"}${"time"} is unavailable`,
  ]);
  return !receipt.ok && persistedFailureMessages.has(receipt.error ?? "");
}

type DurableConversationTranscript = {
  timelines: Record<string, DurableRunTimeline>;
  throughSeq: number;
};

async function loadDurableConversationTranscript(
  companyId: string,
  conversationId: string,
  current: Readonly<Record<string, DurableRunTimeline>>,
  includedRunIds: readonly string[],
  currentThroughSeq: number,
  reloadFromStart: boolean,
): Promise<DurableConversationTranscript> {
  const itemsByRun = new Map(
    includedRunIds.flatMap((runId) => {
      const timeline = current[runId];
      return timeline
        ? [[runId, new Map(timeline.items.map((item) => [item.seq, item]))]]
        : [];
    }),
  );
  let afterSeq = reloadFromStart ? 0 : currentThroughSeq;

  while (true) {
    const page = await conversationService.getTranscript(
      companyId,
      conversationId,
      {
        afterSeq,
        limit: RUN_TRANSCRIPT_PAGE_SIZE,
      },
    );
    let nextAfterSeq = afterSeq;
    for (const item of page.items) {
      if (item.seq <= nextAfterSeq) {
        throw new Error("Conversation transcript page is not ascending");
      }
      const runItems = itemsByRun.get(item.runId) ?? new Map();
      runItems.set(item.seq, item);
      itemsByRun.set(item.runId, runItems);
      nextAfterSeq = item.seq;
    }

    if (
      nextAfterSeq === afterSeq &&
      (page.hasMore || afterSeq < page.throughSeq)
    ) {
      throw new Error("Conversation transcript pagination did not advance");
    }
    if (page.throughSeq < nextAfterSeq) {
      throw new Error("Conversation transcript head does not cover its page");
    }
    afterSeq = nextAfterSeq;
    if (page.hasMore || afterSeq < page.throughSeq) {
      continue;
    }
    return {
      timelines: Object.fromEntries(
        [...itemsByRun].map(([runId, items]) => {
          const ordered = [...items.values()].sort(
            (left, right) => left.seq - right.seq,
          );
          return [
            runId,
            {
              hasOlder: false,
              items: ordered,
              throughSeq: ordered.at(-1)?.seq ?? 0,
            },
          ];
        }),
      ),
      throughSeq: page.throughSeq,
    };
  }
}

export function useAgentRealtimeChat({
  assistantActor,
  conversationKey,
  loadInitialConversation,
  loadInitialTaskWorkspace,
  loadActiveRunIds,
  messageStore,
  resolveConversation,
  taskId,
  team,
  unavailableMessage = "The agent is not connected right now.",
}: UseAgentRealtimeChatOptions) {
  const dispatch = useAppDispatch();
  const reduxStore = useStore<RootState>();
  const [loadHistoryQuery] =
    workspaceApi.useLazyGetWorkspaceConversationHistoryQuery();
  const [connectionStatus, setConnectionStatus] =
    useState<AgentRealtimeConnectionStatus>("idle");
  const [activeConversationId, setActiveConversationId] = useState<
    string | null
  >(null);
  const activeConversationIdRef = useRef<string | null>(null);
  const activeTeamRef = useRef<ChatTeamId>(team);
  const conversationByTeamRef = useRef<Record<string, string>>({});
  const teamByConversationRef = useRef<Record<string, ChatTeamId>>({});
  const conversationSocketRef = useRef<ActiveConversationSocket | null>(null);
  const durableHistoryReplayRef = useRef<DurableHistoryReplay | null>(null);
  // The conversation's Runs as last listed, plus the ones lifecycle frames
  // reported since, joined into durable history so failures survive a reload
  // and a scroll-back.
  const runsByConversationRef = useRef<Record<string, RunFailureSource[]>>({});
  const rememberConversationRun = useCallback(
    (conversationId: string, run: RunFailureSource) => {
      runsByConversationRef.current[conversationId] = mergeConversationRuns(
        runsByConversationRef.current[conversationId] ?? [],
        [run],
      );
    },
    [],
  );
  const durableHistoryByConversationRef = useRef<
    Record<string, DurableConversationHistory>
  >({});
  const durableRunTimelineByRunRef = useRef<Record<string, DurableRunTimeline>>(
    {},
  );
  const durableConversationTranscriptLoadRef = useRef<
    Map<string, Promise<void>>
  >(new Map());
  const durableConversationTranscriptThroughSeqRef = useRef<
    Record<string, number>
  >({});
  const taskWorkspaceLoadByKeyRef = useRef<
    Map<string, Promise<TaskWorkspaceResponse | null>>
  >(new Map());
  const appliedTaskWorkspaceByKeyRef = useRef<Record<string, string>>({});
  const assistantActorByConversationRef = useRef<Record<string, ActorRefDto>>(
    {},
  );
  const historyLoadInFlightRef = useRef<Set<string>>(new Set());
  const pendingHistoryTraceByConversationRef = useRef<
    Record<string, RuntimeTraceSnapshot>
  >({});
  const pendingUserMessagesByConversationRef = useRef<
    Record<string, StableChatMessage[]>
  >({});
  const streamingKeyByConversationRef = useRef<Record<string, string>>({});
  const streamingByKeyRef = useRef<Record<string, StreamingAssistantState>>({});
  const completedByKeyRef = useRef<Record<string, StreamingAssistantState>>({});
  const storedMessageIndexRef = useRef(new StableChatMessageIndex());
  const messageCounterRef = useRef(0);
  const [historyPagination, setHistoryPagination] =
    useState<ChatHistoryPaginationState>({
      conversationId: null,
      error: null,
      hasOlder: false,
      isLoading: false,
    });

  const appendStoredMessage = useCallback(
    (message: ChatMessage) => {
      const stableMessage = message as StableChatMessage;
      storedMessageIndexRef.current.remember(stableMessage);
      if (messageStore?.appendMessage) {
        messageStore.appendMessage(message);
        return;
      }
      dispatch(appendChatMessage(message));
    },
    [dispatch, messageStore],
  );

  const setStoredMessages = useCallback(
    (messageTeam: ChatTeamId, messages: ChatMessage[]) => {
      storedMessageIndexRef.current.replaceTeam(
        messageTeam,
        messages as StableChatMessage[],
      );
      if (messageStore?.setMessages) {
        messageStore.setMessages(messageTeam, messages);
        return;
      }
      dispatch(setTeamChatMessages({ messages, team: messageTeam }));
    },
    [dispatch, messageStore],
  );

  const updateStoredMessage = useCallback(
    (messageTeam: ChatTeamId, id: number, changes: ChatMessageChanges) => {
      const assistantChanges = {
        author: "team" as const,
        ...changes,
      };
      storedMessageIndexRef.current.update(messageTeam, id, assistantChanges);
      if (messageStore?.updateMessage) {
        messageStore.updateMessage(messageTeam, id, assistantChanges);
        return;
      }
      dispatch(
        updateChatMessage({ changes: assistantChanges, id, team: messageTeam }),
      );
    },
    [dispatch, messageStore],
  );

  const nextAssistantMessageId = useCallback(() => {
    messageCounterRef.current += 1;
    return Date.now() + messageCounterRef.current;
  }, []);

  const resumeRun = useCallback(
    (
      socket: ConversationSocketSession,
      runId: string,
      conversationId: string,
    ) => {
      const message = storedMessageIndexRef.current.findByAgentRunId(runId);
      return socket.resumeRun(runId, {
        ...(message?.conversationId === conversationId &&
        message.clientMessageId
          ? { clientRequestId: message.clientMessageId }
          : {}),
        conversationId,
      });
    },
    [],
  );

  const ensureStreamingMessage = useCallback(
    (
      messageTeam: ChatTeamId,
      key: string,
      {
        actor,
        identity,
        isThinking = true,
      }: {
        actor?: ActorRefDto;
        identity?: ChatMessageIdentity;
        isThinking?: boolean;
      } = {},
    ): StreamingAssistantState => {
      const existing = streamingByKeyRef.current[key];

      if (existing) {
        const shouldAttachActor = Boolean(actor && !existing.actor);
        if (identity || shouldAttachActor) {
          streamingByKeyRef.current[key] = {
            ...existing,
            ...(shouldAttachActor ? { actor } : {}),
            ...identity,
          };
          if (shouldAttachActor && actor) {
            updateStoredMessage(existing.team, existing.id, { actor });
          }
          return streamingByKeyRef.current[key];
        }
        return existing;
      }

      const nextIdentity = identity;

      // A row already committed for this Run — by its message id, or by the
      // Run itself when a chat opens or reconnects while the Run is live —
      // is the bubble to keep streaming into. Appending beside it would
      // show the agent's words twice (CV-FR9: the committed item supersedes
      // scratch). A row bound to another live key stays that key's.
      const storedEnvelope = adoptableStoredEnvelope(
        storedMessageIndexRef.current,
        streamingByKeyRef.current,
        messageTeam,
        nextIdentity,
      );
      if (storedEnvelope) {
        const restored: StreamingAssistantState = {
          ...storedEnvelope,
          ...nextIdentity,
          actor: storedEnvelope.actor ?? actor,
          isThinking: storedEnvelope.isThinking ?? isThinking,
          team: messageTeam,
          text: storedEnvelope.text,
        };
        streamingByKeyRef.current[key] = restored;
        updateStoredMessage(messageTeam, storedEnvelope.id, {
          ...nextIdentity,
          ...(restored.actor ? { actor: restored.actor } : {}),
          isStreaming: true,
          isThinking: restored.isThinking,
        });
        return restored;
      }

      const message: StableChatMessage = {
        ...createTeamChatMessage({
          ...nextIdentity,
          id: nextAssistantMessageId(),
          isThinking,
          isStreaming: true,
          team: messageTeam,
          text: "",
        }),
        ...(actor ? { actor } : {}),
        ...(nextIdentity?.messageId
          ? { messageId: nextIdentity.messageId }
          : {}),
        ...(nextIdentity?.messageSequence === undefined
          ? {}
          : { messageSequence: nextIdentity.messageSequence }),
      };
      const next = {
        ...nextIdentity,
        ...(actor ? { actor } : {}),
        id: message.id,
        isThinking,
        team: messageTeam,
        text: "",
      };

      streamingByKeyRef.current[key] = next;
      appendStoredMessage(message);
      return next;
    },
    [appendStoredMessage, nextAssistantMessageId, updateStoredMessage],
  );

  const renderRunEvent = useCallback(
    (
      event: ConversationRunStreamEvent,
      messageTeam: ChatTeamId,
    ): { key: string; state: StreamingAssistantState } | undefined => {
      const key = conversationRunStreamingKey(
        messageTeam,
        event,
        streamingByKeyRef.current,
      );
      if (completedByKeyRef.current[key] && !streamingByKeyRef.current[key]) {
        return undefined;
      }

      if (event.kind === "message") {
        durableRunTimelineByRunRef.current[event.runId] =
          mergeDurableRunTimeline(
            durableRunTimelineByRunRef.current[event.runId],
            event.message,
          );
      }
      const timeline = durableRunTimelineByRunRef.current[event.runId];
      if (
        event.kind === "scratch_reset" &&
        !timeline &&
        !streamingByKeyRef.current[key]
      ) {
        return undefined;
      }

      const identity: ChatMessageIdentity = {
        ...(event.clientRequestId
          ? { clientMessageId: event.clientRequestId }
          : {}),
        conversationId: event.conversationId,
        runId: event.runId,
      };
      const actor =
        assistantActorByConversationRef.current[event.conversationId] ??
        assistantActor;
      const current = ensureStreamingMessage(messageTeam, key, {
        actor,
        identity,
      });
      // A bubble that adopted a committed row shows that row's segment of
      // the Run — everything after the previous committed agent message —
      // so the earlier rows keep their own words and nothing repeats.
      const display = runTimelineFromRunMessages(
        current.messageSequence === undefined
          ? (timeline?.items ?? [])
          : liveRunTimelineSegment(
              timeline?.items ?? [],
              current.messageSequence,
              committedAgentSequencesForRun(
                storedMessageIndexRef.current,
                messageTeam,
                event.runId,
              ),
            ),
        current.activeSkills,
      );
      let contentBlocks = display.contentBlocks;
      if (event.scratch.thinking) {
        contentBlocks = completeReasoningTextBlock(
          contentBlocks,
          event.scratch.thinking,
          `scratch:thinking:${event.runId}`,
        );
      }
      if (event.scratch.text) {
        contentBlocks = completeAssistantTextBlock(
          contentBlocks,
          event.scratch.text,
          `scratch:text:${event.runId}`,
        );
      }
      const text = assistantTextFromBlocks(contentBlocks);
      const activeTools = mergeTraceList(
        current.activeTools,
        display.activeTools,
      );
      const runtimeActivities = mergeRuntimeActivities(
        current.runtimeActivities,
        display.runtimeActivities,
      );
      const isThinking = agentIsThinkingAfterRunEvent(event);
      const runCreatedAt =
        (event.kind === "lifecycle" ? event.createdAt : undefined) ??
        storedMessageIndexRef.current.findByAgentRunId(event.runId)
          ?.runCreatedAt ??
        runsByConversationRef.current[event.conversationId]?.find(
          (run) => run.id === event.runId,
        )?.createdAt;
      const next: StreamingAssistantState = {
        ...current,
        ...identity,
        ...(activeTools?.length ? { activeTools } : {}),
        ...(contentBlocks?.length ? { contentBlocks } : {}),
        ...(display.generatedFiles?.length
          ? { generatedFiles: display.generatedFiles }
          : {}),
        isThinking,
        ...(runtimeActivities?.length ? { runtimeActivities } : {}),
        text,
      };
      streamingByKeyRef.current[key] = next;
      streamingKeyByConversationRef.current[
        conversationStreamingAliasKey(messageTeam, event.conversationId)
      ] = key;
      updateStoredMessage(current.team, current.id, {
        ...identity,
        activeTools,
        contentBlocks,
        generatedFiles: display.generatedFiles,
        isThinking,
        isStreaming: true,
        ...(event.kind === "lifecycle" ? { runStatus: event.status } : {}),
        ...(runCreatedAt ? { runCreatedAt } : {}),
        runtimeActivities,
        text,
        timestamp: formatChatTimestamp(),
      });
      return { key, state: next };
    },
    [assistantActor, ensureStreamingMessage, updateStoredMessage],
  );

  // A retry child changes what its parent's failure row should say: "retrying"
  // while the child is live, "retried" once it has ended. The failed Run's
  // row is the team row indexed by its Run id — a synthetic failure row
  // carries the Run id too.
  const markParentRetry = useCallback(
    (parentRunId: string, active: boolean) => {
      const row = storedMessageIndexRef.current.findByAgentRunId(parentRunId);
      const failure = row?.runFailure;
      if (
        !row ||
        !failure ||
        (failure.retried && failure.retryActive === active)
      ) {
        return;
      }
      updateStoredMessage(row.team, row.id, {
        runFailure: { ...failure, retried: true, retryActive: active },
      });
    },
    [updateStoredMessage],
  );

  // A lifecycle frame never names the runtime, so a failure only frames
  // reported cannot tell whether the Run ran on this viewer's computer, and
  // its card reads the neutral sentence. Read that Run — a run read names
  // `runtimeId` to the runtime's registrant alone (ADR 0014) — and word the
  // card from it, so the person who ran it gets the fix steps. A failed read
  // keeps the neutral sentence.
  const proveFailedRunComputer = useCallback(
    async (conversationId: string, runId: string) => {
      let run: RunDto;
      try {
        run = await runService.getRun(requireActiveCompanyId(), runId);
      } catch (error) {
        console.warn("The failed run could not be read", error);
        return;
      }
      rememberConversationRun(conversationId, run);
      const row = storedMessageIndexRef.current.findByAgentRunId(runId);
      const failure = row?.runFailure;
      const ranOnViewerComputer = Boolean(run.runtimeId);
      if (
        !row ||
        !failure ||
        failure.runId !== runId ||
        failure.ranOnViewerComputer === ranOnViewerComputer
      ) {
        return;
      }
      updateStoredMessage(row.team, row.id, {
        runFailure: { ...failure, ranOnViewerComputer },
      });
    },
    [rememberConversationRun, updateStoredMessage],
  );

  const handleRunEvent = useCallback(
    (event: ConversationRunStreamEvent) => {
      const messageTeam =
        teamByConversationRef.current[event.conversationId] ??
        activeTeamRef.current;
      const rendered = renderRunEvent(event, messageTeam);
      if (
        event.kind === "lifecycle" &&
        event.status === "queued" &&
        event.parentRunId
      ) {
        // A retry child was enqueued: the parent's failure row should say
        // the server is trying again rather than asking the person to.
        rememberConversationRun(
          event.conversationId,
          runFailureSourceFromLifecycle(event),
        );
        markParentRetry(event.parentRunId, true);
      }
      if (
        event.kind === "lifecycle" &&
        event.parentRunId &&
        TERMINAL_RUN_STATUSES.has(event.status)
      ) {
        markParentRetry(event.parentRunId, false);
      }
      if (
        !rendered ||
        event.kind !== "lifecycle" ||
        (event.status !== "completed" &&
          event.status !== "failed" &&
          event.status !== "cancelled")
      ) {
        return;
      }

      const hasDurableText = rendered.state.text.trim().length > 0;
      const terminalText =
        event.status === "cancelled" && !hasDurableText ? stoppedMessage() : "";
      const contentBlocks = terminalText
        ? appendAssistantTerminalTextBlock(
            rendered.state.contentBlocks,
            terminalText,
          )
        : rendered.state.contentBlocks;
      const completed: StreamingAssistantState = {
        ...rendered.state,
        contentBlocks,
        isThinking: false,
        text: assistantTextFromBlocks(contentBlocks) || rendered.state.text,
      };
      rememberConversationRun(
        event.conversationId,
        runFailureSourceFromLifecycle(event),
      );
      // The merged record, which keeps a runtime an earlier Run list named.
      const conversationRuns =
        runsByConversationRef.current[event.conversationId] ?? [];
      const terminalRun = conversationRuns.find(
        (run) => run.id === event.runId,
      );
      const runFailure =
        event.status === "failed" && terminalRun
          ? runFailureFromRun(
              terminalRun,
              conversationRuns.filter((run) => run.parentRunId === event.runId),
            )
          : undefined;
      updateStoredMessage(completed.team, completed.id, {
        contentBlocks,
        isThinking: false,
        isStreaming: false,
        runStatus: event.status,
        ...(runFailure ? { runFailure } : {}),
        text: completed.text,
        timestamp: formatChatTimestamp(),
      });
      // No Run list named this Run's runtime yet — the usual case for a Run
      // this chat just started — so read it before the card can say whose
      // computer the fix is on.
      if (runFailure && terminalRun?.runtimeId === undefined) {
        void proveFailedRunComputer(event.conversationId, event.runId);
      }
      retireCompletedStreamingAliases(
        streamingByKeyRef.current,
        completedByKeyRef.current,
        streamingKeyByConversationRef.current,
        rendered.key,
        completed,
      );
      if (messageTeam === CEO_CHAT_TEAM_ID) {
        rememberPendingHistoryTrace(
          pendingHistoryTraceByConversationRef.current,
          event.conversationId,
          completed,
        );
      }
    },
    [proveFailedRunComputer, renderRunEvent, updateStoredMessage],
  );

  const disconnectConversationSocket = useCallback(
    (conversationId?: string) => {
      const activeSocket = conversationSocketRef.current;

      if (!activeSocket) {
        return;
      }

      if (conversationId && activeSocket.conversationId !== conversationId) {
        return;
      }

      conversationSocketRef.current = null;
      activeSocket.unsubscribe();
      activeSocket.unsubscribeRecovery();
      activeSocket.unsubscribeStatus();
      activeSocket.socket.disconnect();
    },
    [],
  );

  const ensureConversationSocket = useCallback(
    (conversationId: string): ConversationSocketSession => {
      const currentSocket = conversationSocketRef.current;

      if (currentSocket && currentSocket.conversationId !== conversationId) {
        disconnectConversationSocket(currentSocket.conversationId);
      }

      let activeSocket = conversationSocketRef.current;
      if (!activeSocket) {
        const socket = createConversationSocketSession(taskId, conversationId);
        const recoverConversation = () => {
          void (async () => {
            const messageTeam =
              teamByConversationRef.current[conversationId] ??
              activeTeamRef.current;
            try {
              await durableHistoryReplayRef.current?.(
                messageTeam,
                conversationId,
              );
              if (!loadActiveRunIds) {
                return;
              }
              const runIds = await loadActiveRunIds(conversationId);
              await Promise.all(
                runIds.map((runId) => resumeRun(socket, runId, conversationId)),
              );
            } catch {
              // The shared socket owns reconnect. Recovery is bounded to one
              // durable replay and one active-run discovery per signal.
            }
          })();
        };
        activeSocket = {
          conversationId,
          socket,
          unsubscribe: socket.onRunEvent(handleRunEvent),
          unsubscribeRecovery: socket.onRecovery(recoverConversation),
          unsubscribeStatus: socket.onStatus(setConnectionStatus),
        };
        conversationSocketRef.current = activeSocket;
      }

      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);
      return activeSocket.socket;
    },
    [
      disconnectConversationSocket,
      handleRunEvent,
      loadActiveRunIds,
      resumeRun,
      taskId,
    ],
  );

  const publishHistoryPagination = useCallback(
    (
      conversationId: string,
      error: string | null = null,
      isLoading = false,
    ) => {
      if (activeConversationIdRef.current !== conversationId) {
        return;
      }
      const history = durableHistoryByConversationRef.current[conversationId];
      setHistoryPagination({
        conversationId,
        error,
        hasOlder: history?.hasOlder ?? false,
        isLoading,
      });
    },
    [],
  );

  const hydrateHistoryRunTimelines = useCallback(
    async (
      conversationId: string,
      items: readonly ConversationDisplayItemSummary[],
    ) => {
      const runIds = [
        ...new Set(
          items.flatMap((item) => {
            const runId = runIdFromDisplayItem(item);
            return runId === undefined ? [] : [runId];
          }),
        ),
      ];
      if (runIds.length === 0) return;
      const pending =
        durableConversationTranscriptLoadRef.current.get(conversationId);
      if (pending) {
        await pending;
        return;
      }
      const reloadFromStart = runIds.some(
        (runId) => durableRunTimelineByRunRef.current[runId]?.hasOlder === true,
      );
      const load = (async () => {
        const transcript = await loadDurableConversationTranscript(
          requireActiveCompanyId(),
          conversationId,
          durableRunTimelineByRunRef.current,
          runIds,
          durableConversationTranscriptThroughSeqRef.current[conversationId] ??
            0,
          reloadFromStart,
        );
        for (const [runIdText, timeline] of Object.entries(
          transcript.timelines,
        )) {
          const runId = runIdText;
          let merged = durableRunTimelineByRunRef.current[runId];
          for (const item of timeline.items) {
            merged = mergeDurableRunTimeline(merged, item);
          }
          durableRunTimelineByRunRef.current[runId] = {
            ...merged,
            hasOlder: false,
            items: merged?.items ?? [],
            throughSeq: merged?.throughSeq ?? 0,
          };
        }
        for (const runId of runIds) {
          durableRunTimelineByRunRef.current[runId] ??= {
            hasOlder: false,
            items: [],
            throughSeq: 0,
          };
        }
        durableConversationTranscriptThroughSeqRef.current[conversationId] =
          transcript.throughSeq;
      })();
      durableConversationTranscriptLoadRef.current.set(conversationId, load);
      try {
        await load;
      } finally {
        if (
          durableConversationTranscriptLoadRef.current.get(conversationId) ===
          load
        ) {
          durableConversationTranscriptLoadRef.current.delete(conversationId);
        }
      }
    },
    [],
  );

  const applyDurableHistory = useCallback(
    (
      messageTeam: ChatTeamId,
      conversationId: string,
      history: DurableConversationHistory,
    ) => {
      const mappedMessages = attachRunFailures(
        displayItemsToChatMessages(
          history.items,
          messageTeam,
          conversationId,
          Object.fromEntries(
            Object.entries(durableRunTimelineByRunRef.current).map(
              ([runId, timeline]) => [runId, timeline.items],
            ),
          ),
        ),
        runsByConversationRef.current[conversationId] ?? [],
        messageTeam,
        conversationId,
        { hasOlder: history.hasOlder },
      );
      const pendingTrace =
        pendingHistoryTraceByConversationRef.current[conversationId];
      const pendingUserMessages =
        pendingUserMessagesByConversationRef.current[conversationId];
      const mergeResult = mergePendingHistoryReplay(
        mappedMessages,
        pendingUserMessages,
        pendingTrace,
      );
      for (const durableMessage of mergeResult.messages) {
        const stableMessage = durableMessage as StableChatMessage;
        if (stableMessage.author !== "team" || !stableMessage.messageId) {
          continue;
        }
        const completed: StreamingAssistantState = {
          activeSkills: stableMessage.activeSkills,
          activeTools: stableMessage.activeTools,
          afterUserInputRequestId: stableMessage.afterUserInputRequestId,
          actor: stableMessage.actor,
          clientMessageId: stableMessage.clientMessageId,
          contentBlocks: stableMessage.contentBlocks,
          conversationId: stableMessage.conversationId,
          generatedFiles: stableMessage.generatedFiles,
          id: stableMessage.id,
          isThinking: false,
          itemId: stableMessage.itemId,
          messageId: stableMessage.messageId,
          messageSequence: stableMessage.messageSequence,
          runId: stableMessage.runId,
          runtimeActivities: stableMessage.runtimeActivities,
          team: stableMessage.team,
          text: stableMessage.text,
          turnId: stableMessage.turnId,
          turnKey: stableMessage.turnKey,
        };
        const matchingEntry = Object.entries(streamingByKeyRef.current).find(
          ([, streaming]) => {
            if (!sameStreamingAssistant(streaming, completed)) return false;
            return Boolean(
              (streaming.messageId &&
                streaming.messageId === completed.messageId) ||
              (streaming.runId !== undefined &&
                streaming.runId === completed.runId),
            );
          },
        );
        if (!matchingEntry) {
          continue;
        }
        // Only a Run known to be over retires its bubble; a committed row of
        // a Run still streaming becomes that bubble instead, so the events
        // that follow patch the row rather than being dropped as late.
        const runStatus =
          completed.runId === undefined
            ? undefined
            : runsByConversationRef.current[conversationId]?.find(
                (run) => run.id === completed.runId,
              )?.status;
        if (runStatus !== undefined && TERMINAL_RUN_STATUSES.has(runStatus)) {
          retireCompletedStreamingAliases(
            streamingByKeyRef.current,
            completedByKeyRef.current,
            streamingKeyByConversationRef.current,
            matchingEntry[0],
            completed,
          );
        } else {
          rebindStreamingStateToDurableRow(
            streamingByKeyRef.current,
            matchingEntry[0],
            completed,
          );
        }
      }
      const messages = mergeActiveStreamingMessages(
        mergeResult.messages,
        activeStreamingStatesForConversation(
          streamingByKeyRef.current,
          messageTeam,
          conversationId,
        ),
        pendingUserMessages,
      );

      setStoredMessages(messageTeam, messages);
      if (mergeResult.traceResolved) {
        delete pendingHistoryTraceByConversationRef.current[conversationId];
      }
      if (mergeResult.userMessagesResolved && mergeResult.traceResolved) {
        delete pendingUserMessagesByConversationRef.current[conversationId];
      }
    },
    [setStoredMessages],
  );

  const loadTaskWorkspace = useCallback(
    async (messageTeam: ChatTeamId) => {
      if (!loadInitialTaskWorkspace) {
        return null;
      }
      const cacheKey = conversationKey ?? messageTeam;
      const existing = taskWorkspaceLoadByKeyRef.current.get(cacheKey);
      if (existing) {
        return existing;
      }
      const pending = loadInitialTaskWorkspace();
      taskWorkspaceLoadByKeyRef.current.set(cacheKey, pending);
      try {
        return await pending;
      } catch (error) {
        if (taskWorkspaceLoadByKeyRef.current.get(cacheKey) === pending) {
          taskWorkspaceLoadByKeyRef.current.delete(cacheKey);
        }
        throw error;
      }
    },
    [conversationKey, loadInitialTaskWorkspace],
  );

  const applyTaskWorkspace = useCallback(
    (
      messageTeam: ChatTeamId,
      workspace: TaskWorkspaceResponse,
      shouldApply: () => boolean = () => true,
    ) => {
      if (!shouldApply()) {
        return null;
      }
      const conversationId = workspace.conversation.id;
      // The Agent doing the work streams the replies, never the member it
      // runs for (ADR 0014).
      if (workspace.task.execution.agent) {
        assistantActorByConversationRef.current[conversationId] =
          workspace.task.execution.agent;
      } else {
        delete assistantActorByConversationRef.current[conversationId];
      }
      const cacheKey = conversationKey ?? messageTeam;
      conversationByTeamRef.current[cacheKey] = conversationId;
      teamByConversationRef.current[conversationId] = messageTeam;
      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);

      if (appliedTaskWorkspaceByKeyRef.current[cacheKey] === conversationId) {
        publishHistoryPagination(conversationId);
        return conversationId;
      }

      for (const timeline of workspace.runTimelines) {
        durableRunTimelineByRunRef.current[timeline.runId] = {
          hasOlder: timeline.hasMore,
          items: timeline.items,
          throughSeq: timeline.throughSeq,
        };
      }
      const items = conversationService.displayItemsFromMessageViews(
        workspace.messages,
      );
      const earliestLoadedSeq = items[0]?.sequence ?? null;
      const history: DurableConversationHistory = {
        earliestAvailableSeq: workspace.earliestAvailableSeq,
        earliestLoadedSeq,
        hasOlder:
          earliestLoadedSeq !== null &&
          earliestLoadedSeq > workspace.earliestAvailableSeq,
        items,
        throughSeq: workspace.throughSeq,
      };
      durableHistoryByConversationRef.current[conversationId] = history;
      appliedTaskWorkspaceByKeyRef.current[cacheKey] = conversationId;
      applyDurableHistory(messageTeam, conversationId, history);
      publishHistoryPagination(conversationId);
      // The workspace read carries active Runs only; failed ones are joined
      // in when the conversation converges and lists its Runs.
      return conversationId;
    },
    [applyDurableHistory, conversationKey, publishHistoryPagination],
  );

  useEffect(() => {
    taskWorkspaceLoadByKeyRef.current.clear();
    appliedTaskWorkspaceByKeyRef.current = {};
  }, [loadInitialTaskWorkspace]);

  const loadConversationHistory = useCallback(
    async (
      messageTeam: ChatTeamId,
      conversationId: string,
      {
        shouldApply = () => true,
        revalidateMessages = false,
      }: { shouldApply?: () => boolean; revalidateMessages?: boolean } = {},
    ) => {
      const historyArgument = {
        companyId: requireActiveCompanyId(),
        conversationId,
      };
      if (!taskId && !durableHistoryByConversationRef.current[conversationId]) {
        const cached =
          workspaceApi.endpoints.getWorkspaceConversationHistory.select(
            historyArgument,
          )(reduxStore.getState()).data;
        if (cached && shouldApply()) {
          durableHistoryByConversationRef.current[conversationId] = cached;
          applyDurableHistory(messageTeam, conversationId, cached);
          publishHistoryPagination(conversationId);
        }
      }
      // The Run list rides beside the transcript rather than gating it: the
      // history renders as soon as it lands and failed Runs are joined in
      // once the list arrives.
      const runsRequest = loadConversationRuns(conversationId).then((runs) => {
        if (!runs) return;
        runsByConversationRef.current[conversationId] = mergeConversationRuns(
          runsByConversationRef.current[conversationId] ?? [],
          runs,
        );
        const current = durableHistoryByConversationRef.current[conversationId];
        if (
          current &&
          shouldApply() &&
          activeConversationIdRef.current === conversationId
        ) {
          applyDurableHistory(messageTeam, conversationId, current);
        }
      });
      void runsRequest;
      const existing = durableHistoryByConversationRef.current[conversationId];
      // Attachment changes do not advance the message sequence. Recovery must
      // reread every loaded message, including older pages, before catching up.
      const afterSeq =
        revalidateMessages && existing?.earliestLoadedSeq != null
          ? existing.earliestLoadedSeq - 1
          : existing?.throughSeq;
      const catchup =
        taskId && afterSeq !== undefined
          ? await conversationService.loadTaskMessagesAfter(taskId, afterSeq)
          : null;
      if (!shouldApply()) return conversationId;
      const latest =
        catchup ??
        (taskId
          ? await conversationService.loadLatestTaskMessages(
              taskId,
              conversationId,
            )
          : await loadHistoryQuery(historyArgument).unwrap());
      if (!shouldApply()) {
        return conversationId;
      }
      const current = durableHistoryByConversationRef.current[conversationId];
      const history =
        catchup && current
          ? appendNewerHistoryWindow(current, catchup)
          : taskId && existing
            ? latest
            : mergeLatestHistoryWindow(current, latest);
      durableHistoryByConversationRef.current[conversationId] = history;

      const cacheKey = conversationKey ?? messageTeam;

      conversationByTeamRef.current[cacheKey] = conversationId;
      teamByConversationRef.current[conversationId] = messageTeam;
      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);
      applyDurableHistory(messageTeam, conversationId, history);
      publishHistoryPagination(conversationId);
      // The reply is already durable. Tool and reasoning detail enrich it
      // afterwards instead of keeping the old Thinking bubble on screen.
      void hydrateHistoryRunTimelines(conversationId, history.items)
        .then(() => {
          const latestHistory =
            durableHistoryByConversationRef.current[conversationId];
          if (
            latestHistory &&
            shouldApply() &&
            activeConversationIdRef.current === conversationId
          ) {
            applyDurableHistory(messageTeam, conversationId, latestHistory);
          }
        })
        .catch((error) => {
          console.warn("Conversation run details could not be loaded", error);
        });
      return conversationId;
    },
    [
      applyDurableHistory,
      conversationKey,
      hydrateHistoryRunTimelines,
      loadHistoryQuery,
      publishHistoryPagination,
      reduxStore,
      taskId,
    ],
  );
  durableHistoryReplayRef.current = (messageTeam, conversationId) =>
    loadConversationHistory(messageTeam, conversationId, {
      revalidateMessages: true,
    });

  const connectAndConvergeConversation = useCallback(
    async (
      messageTeam: ChatTeamId,
      conversationId: string,
      socket: ConversationSocketSession,
      shouldContinue: () => boolean = () => true,
    ) => {
      setConnectionStatus("connecting");
      try {
        await Promise.all([
          socket.connect(),
          loadConversationHistory(messageTeam, conversationId, {
            shouldApply: shouldContinue,
          }),
        ]);
        if (!shouldContinue()) return;

        if (loadActiveRunIds) {
          const runIds = await loadActiveRunIds(conversationId);
          if (!shouldContinue()) return;
          await Promise.allSettled(
            runIds.map((runId) => resumeRun(socket, runId, conversationId)),
          );
        }
        if (shouldContinue()) {
          setConnectionStatus("connected");
        }
      } catch {
        if (shouldContinue()) {
          setConnectionStatus("error");
        }
      }
    },
    [loadActiveRunIds, loadConversationHistory, resumeRun],
  );

  const loadOlderMessages = useCallback(async () => {
    const conversationId = activeConversationIdRef.current;
    if (!conversationId || historyLoadInFlightRef.current.has(conversationId)) {
      return;
    }
    const history = durableHistoryByConversationRef.current[conversationId];
    if (!history?.hasOlder || history.earliestLoadedSeq === null) {
      publishHistoryPagination(conversationId);
      return;
    }

    const requestedBeforeSeq = history.earliestLoadedSeq;
    const messageTeam =
      teamByConversationRef.current[conversationId] ?? activeTeamRef.current;
    historyLoadInFlightRef.current.add(conversationId);
    publishHistoryPagination(conversationId, null, true);
    let loadError: string | null = null;

    try {
      const older = taskId
        ? await conversationService.loadTaskMessagesBefore(
            taskId,
            requestedBeforeSeq,
            history.earliestAvailableSeq,
          )
        : await conversationService.loadMessagesBefore(
            conversationId,
            requestedBeforeSeq,
            history.earliestAvailableSeq,
          );
      await hydrateHistoryRunTimelines(conversationId, older.items);
      const current = durableHistoryByConversationRef.current[conversationId];
      if (current?.earliestLoadedSeq === requestedBeforeSeq) {
        const nextHistory = prependOlderHistoryWindow(current, older);
        durableHistoryByConversationRef.current[conversationId] = nextHistory;
        applyDurableHistory(messageTeam, conversationId, nextHistory);
      }
    } catch {
      loadError = HISTORY_LOAD_ERROR;
    } finally {
      historyLoadInFlightRef.current.delete(conversationId);
      publishHistoryPagination(conversationId, loadError);
    }
  }, [
    applyDurableHistory,
    hydrateHistoryRunTimelines,
    publishHistoryPagination,
    taskId,
  ]);

  const ensureConversation = useCallback(
    async (
      messageTeam: ChatTeamId = team,
      beforeHistoryLoad?: (conversationId: string) => void,
    ) => {
      activeTeamRef.current = messageTeam;
      const cacheKey = conversationKey ?? messageTeam;
      const existingConversationId = conversationByTeamRef.current[cacheKey];
      if (existingConversationId) {
        activeConversationIdRef.current = existingConversationId;
        setActiveConversationId(existingConversationId);
        ensureConversationSocket(existingConversationId);
        publishHistoryPagination(existingConversationId);
        // Opening the chat and socket recovery already reconcile history.
        // Sending to an established chat must not wait for that read chain.
        return existingConversationId;
      }

      if (loadInitialTaskWorkspace) {
        const workspace = await loadTaskWorkspace(messageTeam);
        if (workspace) {
          const workspaceConversationId = workspace.conversation.id;
          beforeHistoryLoad?.(workspaceConversationId);
          const conversationId = applyTaskWorkspace(messageTeam, workspace);
          if (conversationId) {
            const socket = ensureConversationSocket(conversationId);
            await connectAndConvergeConversation(
              messageTeam,
              conversationId,
              socket,
            );
            return conversationId;
          }
        }
      }

      const conversationId = await resolveConversation(messageTeam);
      if (!conversationId) {
        setConnectionStatus("error");
        throw new Error("Agent conversation is unavailable.");
      }

      beforeHistoryLoad?.(conversationId);
      const socket = ensureConversationSocket(conversationId);
      await connectAndConvergeConversation(messageTeam, conversationId, socket);
      return conversationId;
    },
    [
      applyTaskWorkspace,
      connectAndConvergeConversation,
      conversationKey,
      ensureConversationSocket,
      loadInitialTaskWorkspace,
      loadTaskWorkspace,
      publishHistoryPagination,
      resolveConversation,
      team,
    ],
  );

  useEffect(() => {
    activeTeamRef.current = team;
  }, [team]);

  useEffect(() => {
    if (!loadInitialConversation && !loadInitialTaskWorkspace) {
      return undefined;
    }

    let cancelled = false;

    async function load() {
      try {
        if (loadInitialTaskWorkspace) {
          const workspace = await loadTaskWorkspace(team);
          if (!workspace || cancelled) {
            return;
          }
          const conversationId = applyTaskWorkspace(
            team,
            workspace,
            () => !cancelled,
          );
          if (!conversationId || cancelled) {
            return;
          }

          const socket = ensureConversationSocket(conversationId);
          await connectAndConvergeConversation(
            team,
            conversationId,
            socket,
            () => !cancelled,
          );
          return;
        }

        const conversationId = await loadInitialConversation?.();
        if (!conversationId || cancelled) {
          return;
        }
        const socket = ensureConversationSocket(conversationId);
        await connectAndConvergeConversation(
          team,
          conversationId,
          socket,
          () => !cancelled,
        );
      } catch {
        if (!cancelled) {
          setConnectionStatus("error");
        }
      }
    }

    void load();
    return () => {
      cancelled = true;
    };
  }, [
    applyTaskWorkspace,
    connectAndConvergeConversation,
    ensureConversationSocket,
    loadInitialConversation,
    loadInitialTaskWorkspace,
    loadTaskWorkspace,
    team,
  ]);

  useEffect(
    () => () => {
      historyLoadInFlightRef.current.clear();
      durableRunTimelineByRunRef.current = {};
      durableConversationTranscriptLoadRef.current.clear();
      durableConversationTranscriptThroughSeqRef.current = {};
      taskWorkspaceLoadByKeyRef.current.clear();
      appliedTaskWorkspaceByKeyRef.current = {};
      assistantActorByConversationRef.current = {};
      storedMessageIndexRef.current.clear();
      disconnectConversationSocket();
    },
    [disconnectConversationSocket],
  );

  const removeOptimisticSend = useCallback(
    (
      messageTeam: ChatTeamId,
      clientMessageId: string,
      assistantKey: string,
      conversationId?: string,
    ) => {
      delete streamingByKeyRef.current[assistantKey];
      delete completedByKeyRef.current[assistantKey];
      for (const [alias, key] of Object.entries(
        streamingKeyByConversationRef.current,
      )) {
        if (key === assistantKey) {
          delete streamingKeyByConversationRef.current[alias];
        }
      }
      if (conversationId) {
        removePendingUserMessage(
          pendingUserMessagesByConversationRef.current,
          conversationId,
          clientMessageId,
        );
      }
      setStoredMessages(
        messageTeam,
        storedMessageIndexRef.current
          .listTeam(messageTeam)
          .filter((item) => item.clientMessageId !== clientMessageId),
      );
    },
    [setStoredMessages],
  );

  const sendAgentMessage = useCallback(
    async (message: ChatMessage, options: SendAgentMessageOptions = {}) => {
      const content =
        message.text.trim() ||
        (message.artifactIds?.length
          ? "Attached artifacts."
          : message.files?.length
            ? "Attached files."
            : "");

      if (!content) {
        return;
      }

      let resolvedConversationId: string | undefined;
      const shouldDisplayUserMessage =
        options.displayUserMessage !== false && !options.hiddenTranscript;
      const clientMessageId =
        options.clientMessageId ??
        `${options.hiddenTranscript ? HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX : "ui-"}${message.id}`;
      const assistantKey = clientStreamingKey(message.team, clientMessageId);
      const visibleUserMessage = shouldDisplayUserMessage
        ? {
            ...message,
            clientMessageId,
          }
        : null;

      if (visibleUserMessage) {
        appendStoredMessage(visibleUserMessage);
      }
      const optimisticAssistant = ensureStreamingMessage(
        message.team,
        assistantKey,
        {
          actor: assistantActor,
          identity: {
            clientMessageId,
          },
          isThinking: true,
        },
      );

      try {
        const attachOptimisticMessagesToConversation = (
          conversationId: string,
        ) => {
          if (visibleUserMessage) {
            rememberPendingUserMessage(
              pendingUserMessagesByConversationRef.current,
              conversationId,
              {
                ...visibleUserMessage,
                conversationId,
              },
            );
          }
          const currentAssistant =
            streamingByKeyRef.current[assistantKey] ?? optimisticAssistant;
          const actor =
            assistantActorByConversationRef.current[conversationId] ??
            currentAssistant.actor ??
            assistantActor;
          if (actor) {
            assistantActorByConversationRef.current[conversationId] = actor;
          }
          streamingByKeyRef.current[assistantKey] = {
            ...currentAssistant,
            ...(actor ? { actor } : {}),
            conversationId,
          };
          updateStoredMessage(message.team, currentAssistant.id, {
            ...(actor ? { actor } : {}),
            clientMessageId,
            conversationId,
            isStreaming: true,
            isThinking: currentAssistant.isThinking,
          });
          streamingKeyByConversationRef.current[
            conversationStreamingAliasKey(message.team, conversationId)
          ] = assistantKey;
        };
        const conversationId = await ensureConversation(
          message.team,
          attachOptimisticMessagesToConversation,
        );
        resolvedConversationId = conversationId;
        const socket = ensureConversationSocket(conversationId);
        void socket.connect().catch(() => undefined);
        const resourceAttachments = await uploadMessageResources({
          clientMessageId,
          conversationId,
          files: message.files,
          onFileTransfer: options.onFileTransfer,
        });
        attachOptimisticMessagesToConversation(conversationId);

        const receipt = await socket.sendMessage({
          attachmentIds: resourceAttachments.map((attachment) => attachment.id),
          artifactIds: message.artifactIds,
          clientRequestId: clientMessageId,
          content,
          conversationId,
          requestKey: options.requestKey,
        });

        if (!receipt.ok) {
          throw new ConversationMessageRejectedError(receipt);
        }
        if (
          receipt.disposition === "queued" ||
          receipt.disposition === "removed"
        ) {
          if (receipt.disposition === "queued" && receipt.queueItem) {
            publishQueuedReceipt({ conversationId, item: receipt.queueItem });
          }
          removeOptimisticSend(
            message.team,
            clientMessageId,
            assistantKey,
            conversationId,
          );
          // The receipt already updated the queue. Release the composer on
          // acceptance while history catches up for this same chat session.
          void loadConversationHistory(message.team, conversationId, {
            shouldApply: () => conversationSocketRef.current?.socket === socket,
          }).catch(() => undefined);
          setConnectionStatus(
            receipt.realtimeConnected ? "connected" : "connecting",
          );
          return;
        }
        const assistant = streamingByKeyRef.current[assistantKey];
        if (assistant && receipt.message) {
          streamingByKeyRef.current[assistantKey] = {
            ...assistant,
            runId: receipt.message.runId,
          };
          updateStoredMessage(message.team, assistant.id, {
            runId: receipt.message.runId,
          });
        }
        // The send receipt names the item the user's words became. Stamping it
        // onto the optimistic bubble is what lets the durable replay match the
        // two rows by key instead of by comparing their text.
        if (visibleUserMessage && receipt.message) {
          const replayedMessage = storedMessageIndexRef.current.findByMessageId(
            receipt.message.id,
          );
          const identity = {
            messageId: receipt.message.id,
            messageSequence: receipt.message.seq,
            runId: receipt.message.runId,
          };
          stampPendingUserMessage(
            pendingUserMessagesByConversationRef.current,
            conversationId,
            clientMessageId,
            identity,
          );
          if (replayedMessage && replayedMessage.id !== visibleUserMessage.id) {
            // Recovery may already have shown the committed message after a
            // lost response. The retry confirms that row, not a second bubble.
            removePendingUserMessage(
              pendingUserMessagesByConversationRef.current,
              conversationId,
              clientMessageId,
            );
            setStoredMessages(
              visibleUserMessage.team,
              storedMessageIndexRef.current
                .listTeam(visibleUserMessage.team)
                .filter((item) => item.id !== visibleUserMessage.id),
            );
          } else {
            storedMessageIndexRef.current.update(
              visibleUserMessage.team,
              visibleUserMessage.id,
              { ...identity, author: "user" },
            );
            if (messageStore?.updateMessage) {
              messageStore.updateMessage(
                visibleUserMessage.team,
                visibleUserMessage.id,
                identity,
              );
            } else {
              dispatch(
                updateChatMessage({
                  changes: identity,
                  id: visibleUserMessage.id,
                  team: visibleUserMessage.team,
                }),
              );
            }
          }
        }
        setConnectionStatus(
          receipt.realtimeConnected ? "connected" : "connecting",
        );
      } catch (error) {
        const failure = describeConversationSendFailure(
          error,
          unavailableMessage,
        );
        removeOptimisticSend(
          message.team,
          clientMessageId,
          assistantKey,
          resolvedConversationId,
        );
        if (resolvedConversationId) {
          if (failure.status === 409) {
            requestConversationRunQueueRefresh(resolvedConversationId);
          }
          await loadConversationHistory(
            message.team,
            resolvedConversationId,
          ).catch(() => undefined);
        }
        if (failure.status === 404 || failure.status === 409) {
          dispatch(
            boardlyApi.util.invalidateTags([
              "Conversation",
              "Message",
              "Run",
              "Task",
            ]),
          );
        }
        if (failure.status === null) {
          setConnectionStatus("error");
        }
        throw error;
      }
    },
    [
      appendStoredMessage,
      assistantActor,
      dispatch,
      ensureConversation,
      ensureStreamingMessage,
      ensureConversationSocket,
      loadConversationHistory,
      messageStore,
      removeOptimisticSend,
      setStoredMessages,
      unavailableMessage,
    ],
  );

  const openConversation = useCallback(
    async (conversationId: string, messageTeam: ChatTeamId = team) => {
      activeTeamRef.current = messageTeam;
      const socket = ensureConversationSocket(conversationId);
      await connectAndConvergeConversation(messageTeam, conversationId, socket);
      return conversationId;
    },
    [connectAndConvergeConversation, ensureConversationSocket, team],
  );

  return {
    activeConversationId,
    connectionStatus,
    ensureConversation,
    hasOlderMessages: historyPagination.hasOlder,
    historyLoadError: historyPagination.error,
    isLoadingOlderMessages: historyPagination.isLoading,
    loadOlderMessages,
    openConversation,
    sendAgentMessage,
  };
}

// Retry receipts live only as long as the draft's file objects. They keep a
// partially successful upload from creating another copy on the next send.
const uploadedDraftFiles = new WeakMap<
  NonNullable<ChatMessage["files"]>[number],
  {
    companyId: string;
    conversationId: string;
    attachments: ResourceAttachmentSnapshot[];
  }
>();

async function uploadMessageResources({
  clientMessageId,
  conversationId,
  files,
  onFileTransfer,
}: {
  clientMessageId: string;
  conversationId: string;
  files: ChatMessage["files"];
  onFileTransfer?: FileTransferHandler;
}) {
  if (!files?.length) {
    return [];
  }
  const companyId = requireActiveCompanyId();
  const results = await Promise.allSettled(
    files.map(async (file, index) => {
      const cached = uploadedDraftFiles.get(file);
      if (
        cached?.companyId === companyId &&
        cached.conversationId === conversationId
      ) {
        onFileTransfer?.(index, { status: "uploaded", progress: 100 });
        return cached.attachments;
      }
      try {
        const uploadFile = await fileFromPromptInputPart(file);
        const response = await cockpitService.uploadResources({
          clientMessageId,
          conversationId,
          companyId,
          files: [uploadFile],
          onFileTransfer: (_index, transfer) =>
            onFileTransfer?.(index, transfer),
        });
        const attachments = response.attachments?.length
          ? response.attachments
          : response.resources.map((resource) =>
              cockpitService.resourceAttachmentSnapshot(resource),
            );
        uploadedDraftFiles.set(file, {
          companyId,
          conversationId,
          attachments,
        });
        return attachments;
      } catch (error) {
        onFileTransfer?.(index, {
          status: "error",
          error: "Upload failed. Try again.",
        });
        throw error;
      }
    }),
  );
  const failure = results.find((result) => result.status === "rejected");
  if (failure) throw failure.reason;
  return results.flatMap(
    (result) =>
      (result as PromiseFulfilledResult<ResourceAttachmentSnapshot[]>).value,
  );
}

async function fileFromPromptInputPart(
  file: NonNullable<ChatMessage["files"]>[number],
): Promise<File> {
  const filename = file.filename || "attachment";
  const contentType = file.mediaType || "application/octet-stream";
  if (!file.url) {
    return fileFromBlob(new Blob([], { type: contentType }), filename);
  }
  const response = await fetch(file.url);
  const blob = await response.blob();
  return fileFromBlob(
    blob,
    filename,
    file.mediaType || blob.type || contentType,
  );
}

function fileFromBlob(
  blob: Blob,
  filename: string,
  contentType = blob.type,
): File {
  if (typeof File === "function") {
    return new File([blob], filename, {
      type: contentType || "application/octet-stream",
    });
  }

  const fallback = blob as File;
  Object.defineProperties(fallback, {
    lastModified: {
      configurable: true,
      value: Date.now(),
    },
    name: {
      configurable: true,
      value: filename,
    },
  });
  return fallback;
}

function rememberPendingHistoryTrace(
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

function rememberPendingUserMessage(
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

function removePendingUserMessage(
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

function stampPendingUserMessage(
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

function mergePendingHistoryReplay(
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

function activeStreamingStatesForConversation(
  streamingByKey: Record<string, StreamingAssistantState>,
  team: ChatTeamId,
  conversationId: string,
) {
  return Object.values(streamingByKey).filter(
    (streaming) =>
      streaming.team === team && streaming.conversationId === conversationId,
  );
}

function mergeActiveStreamingMessages(
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
