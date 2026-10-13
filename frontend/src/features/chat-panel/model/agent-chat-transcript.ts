import { canonicalUuid } from "@/shared/lib/uuid";
import { formatChatTimestamp } from "@/features/chat-panel/utils";
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
} from "@/features/chat-panel/model/canonical-content-blocks";
import { type StableChatMessage } from "@/features/chat-panel/model/chat-message-convergence";
import {
  mergeTraceList,
  runTimelineFromRunMessages,
  runTimelineSegmentForMessage,
  runtimeActivitiesFromTrace,
  runtimeTraceFromPayload,
  runtimeTraceRecord,
  stringFromUnknown,
  stringifyDisplayPayloadValue,
} from "@/features/chat-panel/model/chat-timeline";
import { type ChatTeamId } from "@/shared/chat/chat-team";
import type {
  ChatMessage,
  ChatMessageContentBlock,
  ChatRunFailure,
  ChatRuntimeActivity,
} from "@/shared/chat/types";
import type {
  ActorRefDto,
  AgentProvider,
  ConversationDisplayItemSummary,
  ConversationTranscriptEntryDto,
  RunDto,
  RunStatusDto,
} from "@/shared/api/types";
import {
  runCancellationCopy,
  runFailureCopy,
} from "@/shared/agent-execution/run-access-reasons";
import {
  type ConversationMessageReceipt,
  type ConversationRunStreamEvent,
} from "@/shared/realtime/conversation-socket.service";

export const HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX = "ui-control-";

export function agentIsThinkingAfterRunEvent(
  event: ConversationRunStreamEvent,
) {
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

type DisplayToolTrace = {
  activeTools?: string[];
  lastSequence?: number;
  runtimeActivities?: ChatRuntimeActivity[];
  turnKey?: string;
};

export type ChatMessageIdentity = Pick<
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
export function runIdFromDisplayItem(item: ConversationDisplayItemSummary) {
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
export type RunFailureSource = Pick<
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
export function runFailureSourceFromLifecycle(
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

export function runFailureFromRun(
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

export function findLastIndex<T>(
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
export function mergeConversationRuns(
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

export const TERMINAL_RUN_STATUSES = new Set<RunDto["status"]>([
  "completed",
  "failed",
  "cancelled",
]);

export function stoppedMessage(): string {
  return "Stopped.";
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

export function appendAssistantTerminalTextBlock(
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

export function contentBlocksForCanonicalText(
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
