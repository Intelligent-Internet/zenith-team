import {
  appendRuntimeToolActivity,
  normalizeRuntimeToolName,
} from "@/features/chat-panel/model/chat-runtime-trace";
import {
  assistantTextFromBlocks,
  completeAssistantTextBlock,
  completeReasoningTextBlock,
  upsertRuntimeActivityBlock,
} from "@/features/chat-panel/model/canonical-content-blocks";
import type {
  ChatGeneratedFile,
  ChatMessage,
  ChatMessageContentBlock,
  ChatRuntimeActivity,
} from "@/shared/chat/types";
import type {
  ConversationDisplayItemSummary,
  ConversationHistoryWindow,
  ConversationTranscriptEntryDto,
} from "@/shared/api/types";

export type DisplayRuntimeTrace = {
  activeSkills?: string[];
  activeTools?: string[];
  runtimeActivities?: ChatRuntimeActivity[];
};
export type DisplayRunTimeline = {
  activeTools?: string[];
  contentBlocks?: ChatMessageContentBlock[];
  generatedFiles?: ChatGeneratedFile[];
  runtimeActivities?: ChatRuntimeActivity[];
  userInputBoundaries?: ChatMessage["userInputBoundaries"];
};
type RunItemPart = ConversationTranscriptEntryDto["parts"][number];
type RunTimelineBuilder = {
  activeSkills?: string[];
  activeTools?: string[];
  contentBlocks?: ChatMessageContentBlock[];
  generatedFiles?: ChatGeneratedFile[];
  pendingFileChanges: Map<string, ChatGeneratedFile[]>;
  runtimeActivities?: ChatRuntimeActivity[];
};
export type DurableConversationHistory = ConversationHistoryWindow;
export type DurableRunTimeline = {
  hasOlder?: boolean;
  items: ConversationTranscriptEntryDto[];
  throughSeq: number;
};

function stringListFromUnknown(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }

  const seen = new Set<string>();
  const normalized = value.flatMap((item) => {
    if (typeof item !== "string") {
      return [];
    }
    const text = item.trim();
    if (!text || seen.has(text)) {
      return [];
    }
    seen.add(text);
    return [text];
  });

  return normalized.length ? normalized : undefined;
}

export function runtimeTraceFromPayload(
  payload: ConversationDisplayItemSummary["payload"],
): DisplayRuntimeTrace {
  const value = payload.runtime_trace;
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {};
  }

  const trace = value as Record<string, unknown>;
  const activeSkills = stringListFromUnknown(trace.active_skills);
  return {
    activeSkills,
    activeTools: stringListFromUnknown(trace.used_tools),
    runtimeActivities: runtimeActivitiesFromTrace(
      trace.tool_activities,
      activeSkills,
    ),
  };
}

export function runtimeActivitiesFromTrace(
  value: unknown,
  activeSkills: string[] | undefined,
): ChatRuntimeActivity[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }

  return value.reduce<ChatRuntimeActivity[] | undefined>((activities, item) => {
    if (!runtimeTraceRecord(item)) {
      return activities;
    }
    const toolName = stringFromUnknown(item.tool_name);
    if (!toolName) {
      return activities;
    }
    return appendRuntimeToolActivity(activities, {
      activeSkills,
      callId: stringFromUnknown(item.call_id),
      input: stringFromUnknown(item.input),
      itemId: stringFromUnknown(item.item_id),
      output: stringFromUnknown(item.output),
      position: "before_message",
      status: stringFromUnknown(item.status),
      toolName,
    });
  }, undefined);
}

export function runTimelineFromRunMessages(
  runMessages: readonly ConversationTranscriptEntryDto[],
  activeSkills: string[] | undefined,
): DisplayRunTimeline {
  const timeline: RunTimelineBuilder = {
    activeSkills,
    pendingFileChanges: new Map(),
  };
  [...runMessages]
    .sort((left, right) => left.seq - right.seq)
    .forEach((message) => appendRunTimelineMessage(timeline, message));

  return {
    activeTools: timeline.activeTools,
    contentBlocks: timeline.contentBlocks,
    generatedFiles: timeline.generatedFiles,
    runtimeActivities: timeline.runtimeActivities,
  };
}

// The slice of a Run's timeline that belongs to one committed agent
// message: everything after the previous committed message of that Run, and
// — when a later one exists — nothing after this one. The message itself
// must be in the timeline, or there is no segment to show.
export function runTimelineSegmentForMessage(
  runMessages: readonly ConversationTranscriptEntryDto[],
  messageSequence: number,
  messageSequences: readonly number[],
): ConversationTranscriptEntryDto[] {
  if (
    !messageSequences.includes(messageSequence) ||
    !runMessages.some((item) => item.seq === messageSequence)
  ) {
    return [];
  }
  const hasLaterMessage = messageSequences.some(
    (sequence) => sequence > messageSequence,
  );
  return liveRunTimelineSegment(
    runMessages,
    messageSequence,
    messageSequences,
  ).filter((item) => !hasLaterMessage || item.seq <= messageSequence);
}

// The core of every segment: everything in a Run's timeline after the
// previous committed agent message, in sequence order, with no upper bound.
// That is exactly what a live bubble that adopted the Run's latest row
// shows while the tail is still arriving — without requiring the row's own
// item to have landed yet, since a catch-up replays the Run from its first
// item. runTimelineSegmentForMessage adds the upper bound for history.
export function liveRunTimelineSegment(
  runMessages: readonly ConversationTranscriptEntryDto[],
  adoptedSequence: number,
  committedMessageSequences: readonly number[],
): ConversationTranscriptEntryDto[] {
  const previousMessageSequence = committedMessageSequences
    .filter((sequence) => sequence < adoptedSequence)
    .reduce<number | undefined>(
      (latest, sequence) =>
        latest === undefined || sequence > latest ? sequence : latest,
      undefined,
    );
  return [...runMessages]
    .sort((left, right) => left.seq - right.seq)
    .filter(
      (item) =>
        previousMessageSequence === undefined ||
        item.seq > previousMessageSequence,
    );
}

function appendRunTimelineMessage(
  timeline: RunTimelineBuilder,
  message: ConversationTranscriptEntryDto,
): void {
  // Tool errors belong to the tool disclosure, not the agent's reply. Match
  // by parts because history reads derive the role from the stored item type.
  const toolError = message.parts.some((part) => part.type === "toolResult")
    ? message.parts
        .filter((part) => part.type === "error")
        .map((part) => part.text)
        .join("\n\n")
    : undefined;
  message.parts.forEach((part, partIndex) => {
    const partId =
      message.parts.length === 1
        ? message.itemKey
        : `${message.itemKey}:${partIndex.toString()}`;
    if (part.type === "error" && toolError !== undefined) return;
    if (part.type === "text" || part.type === "error") {
      timeline.contentBlocks = completeAssistantTextBlock(
        timeline.contentBlocks,
        part.text,
        partId,
        message.seq,
      );
      return;
    }
    if (part.type === "reasoning") {
      timeline.contentBlocks = completeReasoningTextBlock(
        timeline.contentBlocks,
        part.text,
        partId,
        message.seq,
      );
      return;
    }
    appendRunToolPart(timeline, message, part, toolError);
  });
}

function appendRunToolPart(
  timeline: RunTimelineBuilder,
  message: ConversationTranscriptEntryDto,
  part: Extract<RunItemPart, { type: "toolCall" | "toolResult" }>,
  toolError: string | undefined,
): void {
  const currentActivity = findRuntimeActivity(timeline.runtimeActivities, {
    callId: part.callId,
  });
  const startedInput =
    part.type === "toolCall" && part.input !== undefined
      ? stringifyDisplayPayloadValue(part.input)
      : undefined;
  const toolName = normalizeRuntimeToolName(
    part.name ?? currentActivity?.toolName,
    undefined,
    startedInput,
  );
  if (!toolName) return;

  if (part.type === "toolCall" && part.input !== undefined) {
    const generatedFiles = generatedFilesFromChangeInput(
      part.callId,
      part.input,
      message.seq,
    );
    if (generatedFiles.length) {
      timeline.pendingFileChanges.set(part.callId, generatedFiles);
    }
  } else if (part.type === "toolResult") {
    const generatedFiles = timeline.pendingFileChanges.get(part.callId);
    timeline.pendingFileChanges.delete(part.callId);
    if (generatedFiles) {
      timeline.generatedFiles = mergeGeneratedFiles(
        timeline.generatedFiles,
        generatedFiles.map((file) => ({ ...file, eventSequence: message.seq })),
      );
    }
  }

  const nextActivities = appendRuntimeToolActivity(timeline.runtimeActivities, {
    activeSkills: timeline.activeSkills,
    callId: part.callId,
    error: part.type === "toolResult" ? toolError : currentActivity?.error,
    exitCode:
      part.type === "toolResult" ? part.exitCode : currentActivity?.exitCode,
    input: part.type === "toolCall" ? startedInput : currentActivity?.input,
    itemId: part.callId,
    output: part.type === "toolResult" ? part.output : currentActivity?.output,
    position: assistantTextFromBlocks(timeline.contentBlocks).trim()
      ? "after_message"
      : "before_message",
    status:
      part.type === "toolResult"
        ? part.exitCode !== undefined && part.exitCode !== 0
          ? "failed"
          : (part.status ?? (toolError ? "failed" : "completed"))
        : "running",
    toolName,
  });
  const nextActivity = findRuntimeActivity(nextActivities, {
    callId: part.callId,
    itemId: part.callId,
    toolName,
  });
  timeline.runtimeActivities = nextActivities;
  timeline.activeTools = mergeTraceList(timeline.activeTools, [toolName]);
  if (nextActivity) {
    timeline.contentBlocks = upsertRuntimeActivityBlock(
      timeline.contentBlocks,
      nextActivity.id,
      currentActivity?.id,
      message.seq,
    );
  }
}

// The files a file-changing tool call touched, from its recorded input.
// A Codex patch_apply input is `{changes: [{path, kind, diff|content,
// move_path?}], truncated?}` — one call, every file — so each entry becomes
// its own generated file, keyed by the call id and its position; a moved
// file is listed at its new path. A deleted file is nothing to open and is
// left out. The flat `{path, changeKind}` shape is the older single-file
// form, kept as a fallback.
function generatedFilesFromChangeInput(
  callId: string,
  input: Record<string, unknown>,
  eventSequence?: number,
): ChatGeneratedFile[] {
  const changes = Array.isArray(input.changes)
    ? input.changes.flatMap((change, index) => {
        if (!runtimeTraceRecord(change)) return [];
        const movePath = stringFromUnknown(change.move_path);
        return [
          {
            id: `${callId}:${index.toString()}`,
            kind: movePath ? "rename" : stringFromUnknown(change.kind),
            path: movePath ?? stringFromUnknown(change.path),
          },
        ];
      })
    : [
        {
          id: callId,
          kind:
            stringFromUnknown(input.changeKind) ??
            stringFromUnknown(input.operation),
          path: stringFromUnknown(input.path),
        },
      ];
  return changes.flatMap(({ id, kind, path }) => {
    const operation = generatedFileOperation(kind);
    if (!path || !operation) return [];
    return [
      {
        ...(eventSequence !== undefined ? { eventSequence } : {}),
        id,
        itemId: callId,
        operation,
        path,
        truncated: input.truncated === true,
      },
    ];
  });
}

function generatedFileOperation(
  kind: string | undefined,
): ChatGeneratedFile["operation"] | undefined {
  switch (kind) {
    case "add":
    case "create":
      return "create";
    case "update":
    case "modify":
      return "modify";
    case "rename":
    case "move":
      return "rename";
    default:
      return undefined;
  }
}

export function mergeGeneratedFiles(
  current: ChatGeneratedFile[] | undefined,
  incoming: ChatGeneratedFile | ChatGeneratedFile[] | undefined,
): ChatGeneratedFile[] | undefined {
  const nextFiles = Array.isArray(incoming)
    ? incoming
    : incoming
      ? [incoming]
      : [];
  if (!current?.length && !nextFiles.length) {
    return undefined;
  }

  const merged = [...(current ?? [])];
  nextFiles.forEach((file) => {
    const existingIndex = merged.findIndex(
      (candidate) => candidate.path === file.path,
    );
    if (existingIndex >= 0) {
      merged[existingIndex] = file;
      return;
    }
    merged.push(file);
  });
  return merged;
}

export function runtimeTraceRecord(
  value: unknown,
): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function stringFromUnknown(value: unknown) {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized = value.trim();
  return normalized || undefined;
}

export function stringifyDisplayPayloadValue(
  value: unknown,
): string | undefined {
  if (typeof value === "string") {
    return value.trim() || undefined;
  }
  try {
    return JSON.stringify(value);
  } catch {
    return undefined;
  }
}

function findRuntimeActivity(
  activities: ChatRuntimeActivity[] | undefined,
  {
    callId,
    itemId,
    toolName,
  }: { callId?: string; itemId?: string; toolName?: string },
) {
  if (!activities?.length) {
    return undefined;
  }

  const newestFirst = [...activities].reverse();
  const identifiedByCallId = callId
    ? newestFirst.find((activity) => activity.callId === callId)
    : undefined;
  const identifiedByItemId = itemId
    ? newestFirst.find((activity) => activity.itemId === itemId)
    : undefined;
  const identified = identifiedByCallId ?? identifiedByItemId;

  if (identified || !toolName) {
    return identified;
  }

  return newestFirst.find(
    (activity) =>
      activity.toolName === toolName && !activity.callId && !activity.itemId,
  );
}

export function mergeDurableRunTimeline(
  current: DurableRunTimeline | undefined,
  message: ConversationTranscriptEntryDto,
): DurableRunTimeline {
  const bySequence = new Map(
    (current?.items ?? []).map((item) => [item.seq, item]),
  );
  bySequence.set(message.seq, message);
  const items = [...bySequence.values()].sort(
    (left, right) => left.seq - right.seq,
  );
  return {
    hasOlder: current?.hasOlder,
    items,
    throughSeq: Math.max(current?.throughSeq ?? 0, message.seq),
  };
}

function normalizeRealtimeList(
  value: string[] | undefined,
): string[] | undefined {
  if (!value?.length) {
    return undefined;
  }

  const seen = new Set<string>();
  const normalized = value.flatMap((item) => {
    const text = item.trim();
    if (!text || seen.has(text)) {
      return [];
    }
    seen.add(text);
    return [text];
  });
  return normalized.length > 0 ? normalized : undefined;
}

export function mergeTraceList(
  current: string[] | undefined,
  next: string[] | undefined,
): string[] | undefined {
  const normalizedCurrent = normalizeRealtimeList(current) ?? [];
  const normalizedNext = normalizeRealtimeList(next) ?? [];

  if (!normalizedCurrent.length && !normalizedNext.length) {
    return undefined;
  }

  return normalizeRealtimeList([...normalizedCurrent, ...normalizedNext]);
}

function mergeHistoryItems(
  existing: readonly ConversationDisplayItemSummary[],
  incoming: readonly ConversationDisplayItemSummary[],
) {
  const bySequence = new Map<number, ConversationDisplayItemSummary>();
  existing.forEach((item) => bySequence.set(item.sequence, item));
  incoming.forEach((item) => bySequence.set(item.sequence, item));
  return [...bySequence.values()].sort(
    (left, right) => left.sequence - right.sequence,
  );
}

export function mergeLatestHistoryWindow(
  existing: DurableConversationHistory | undefined,
  latest: ConversationHistoryWindow,
): DurableConversationHistory {
  if (existing && latest.throughSeq < existing.throughSeq) {
    return existing;
  }
  if (!existing || !existing.items.length || !latest.items.length) {
    return latest;
  }

  const existingLastSeq = existing.items[existing.items.length - 1]?.sequence;
  const latestFirstSeq = latest.items[0]?.sequence;
  if (
    existingLastSeq === undefined ||
    latestFirstSeq === undefined ||
    latestFirstSeq > existingLastSeq + 1
  ) {
    return latest;
  }

  // Realtime may already have committed rows beyond this HTTP snapshot.
  // Retain them while the snapshot cursor catches up.
  const items = mergeHistoryItems(existing.items, latest.items).filter(
    (item) => item.sequence >= latest.earliestAvailableSeq,
  );
  const earliestLoadedSeq = items[0]?.sequence ?? null;
  return {
    earliestAvailableSeq: latest.earliestAvailableSeq,
    earliestLoadedSeq,
    hasOlder:
      earliestLoadedSeq !== null &&
      earliestLoadedSeq > latest.earliestAvailableSeq,
    items,
    throughSeq: latest.throughSeq,
  };
}

export function prependOlderHistoryWindow(
  current: DurableConversationHistory,
  older: ConversationHistoryWindow,
): DurableConversationHistory {
  const items = mergeHistoryItems(older.items, current.items);
  const earliestLoadedSeq = items[0]?.sequence ?? null;
  return {
    earliestAvailableSeq: older.earliestAvailableSeq,
    earliestLoadedSeq,
    hasOlder:
      earliestLoadedSeq !== null &&
      earliestLoadedSeq > older.earliestAvailableSeq,
    items,
    throughSeq: current.throughSeq,
  };
}

// Forward paging proves the entire interval was read. Run detail may occupy
// the sequence numbers between messages, and an empty catch-up keeps history.
export function appendNewerHistoryWindow(
  current: DurableConversationHistory,
  newer: ConversationHistoryWindow,
): DurableConversationHistory {
  if (newer.throughSeq < current.throughSeq) return current;
  const items = mergeHistoryItems(current.items, newer.items).filter(
    (item) => item.sequence >= newer.earliestAvailableSeq,
  );
  const earliestLoadedSeq = items[0]?.sequence ?? null;
  return {
    earliestAvailableSeq: newer.earliestAvailableSeq,
    earliestLoadedSeq,
    hasOlder:
      earliestLoadedSeq !== null &&
      earliestLoadedSeq > newer.earliestAvailableSeq,
    items,
    throughSeq: newer.throughSeq,
  };
}
