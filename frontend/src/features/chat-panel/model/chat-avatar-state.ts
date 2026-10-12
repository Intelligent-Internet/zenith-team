import type { ConversationCurrentRun } from "@/shared/api/types";
import type { ChatMessage } from "@/shared/chat/types";
import type { FoldAvatarState } from "@/shared/ui/agent-avatar/fold-avatar";
import {
  mergeRuntimeActivities,
  runtimeActivityIsInProgress,
} from "./chat-runtime-trace";

export function chatAvatarRunId(
  currentRun: ConversationCurrentRun | null,
  messages: readonly ChatMessage[],
) {
  const latest = messages.at(-1);
  // The new live reply can arrive before the queue projection catches up.
  if (latest?.author === "team" && (latest.isThinking || latest.isStreaming))
    return latest.runId;
  return currentRun?.id ?? latest?.runId;
}

export function chatAvatarState({
  currentRun,
  messages,
  unavailable,
  needsInput,
  continuing,
}: {
  currentRun: ConversationCurrentRun | null;
  messages: readonly ChatMessage[];
  unavailable: boolean;
  needsInput: boolean;
  continuing: boolean;
}): FoldAvatarState {
  if (continuing) return "thinking";
  if (needsInput) return "needs-input";

  const latest = messages[messages.length - 1];
  // A new optimistic reply has no Run yet. Do not let an older completion
  // override its immediate feedback while the send is being acknowledged.
  if (
    latest?.author === "team" &&
    !latest.runId &&
    (latest.isThinking || latest.isStreaming)
  )
    return latest.isThinking ? "thinking" : "working";
  if (unavailable) return "sleeping";
  const runId = chatAvatarRunId(currentRun, messages);
  // A failed/recovered tool or an older Run must never drive the current face.
  const runMessages = runId
    ? messages.filter((message) => message.runId === runId)
    : latest
      ? [latest]
      : [];
  const newestFirst = [...runMessages].reverse();
  const status =
    newestFirst.find((message) => message.runStatus)?.runStatus ??
    (currentRun?.id === runId ? currentRun?.status : undefined);
  if (status === "completed") return "done";
  if (status === "failed") return "error";
  if (status === "cancelled") return "idle";
  if (
    status === "queued" ||
    status === "deferred" ||
    status === "waiting_local_directory"
  )
    return "idle";

  const assistant = newestFirst.find((message) => message.author === "team");
  const active =
    status === "running" ||
    status === "dispatched" ||
    runMessages.some((message) => message.isStreaming || message.isThinking);
  if (!active) return "idle";
  const activities = runMessages.reduce(
    (current, message) =>
      mergeRuntimeActivities(current, message.runtimeActivities),
    undefined as ChatMessage["runtimeActivities"],
  );
  if (activities?.some(runtimeActivityIsInProgress)) return "working";
  const blocks = assistant?.contentBlocks;
  if (
    assistant?.isThinking ||
    blocks?.[blocks.length - 1]?.type === "reasoning"
  )
    return "thinking";
  if (assistant?.isStreaming && assistant.text.trim()) return "working";
  return "thinking";
}
