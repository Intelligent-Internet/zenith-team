import type { ChatPanelSurface } from "@/features/chat-panel/model/chat-panel-surface";
import type { ChatFocusedMessageTarget } from "@/features/chat-panel/types";
import type { ScheduleRunRecord } from "@/shared/api/schedule.service";

type ScheduleRunChatCandidate = {
  conversationId: string;
  messageId?: string | null;
  messageSeq?: number | null;
};

export type ScheduleRunChatTarget = {
  conversationId: string;
  focusedMessage: ChatFocusedMessageTarget;
  surface: Extract<ChatPanelSurface, "history">;
};

export type ScheduleRunChatState = {
  loadError: string | null;
  retryRevision: number;
  selectedFiringId: string | null;
};

type ScheduleRunChatAction =
  | { firingId: string; type: "opened" }
  | { type: "closed" }
  | { message: string; type: "load_failed" }
  | { type: "load_succeeded" }
  | { type: "retried" };

export const initialScheduleRunChatState: ScheduleRunChatState = {
  loadError: null,
  retryRevision: 0,
  selectedFiringId: null,
};

export function scheduleRunChatReducer(
  state: ScheduleRunChatState,
  action: ScheduleRunChatAction,
): ScheduleRunChatState {
  switch (action.type) {
    case "opened":
      return {
        loadError: null,
        retryRevision: 0,
        selectedFiringId: action.firingId,
      };
    case "closed":
      return initialScheduleRunChatState;
    case "load_failed":
      return { ...state, loadError: action.message };
    case "load_succeeded":
      return state.loadError === null ? state : { ...state, loadError: null };
    case "retried":
      return {
        ...state,
        loadError: null,
        retryRevision: state.retryRevision + 1,
      };
  }
}

export type ScheduleRunChatSelection = {
  focusedHistoryRequestKey: string;
  target: ScheduleRunChatTarget;
};

export function resolveScheduleRunChatSelection(
  state: ScheduleRunChatState,
  runs: readonly ScheduleRunRecord[],
): ScheduleRunChatSelection | null {
  const run = runs.find(
    (candidate) => candidate.firingId === state.selectedFiringId,
  );
  if (!run) {
    return null;
  }
  const target = resolveScheduleRunChatTarget(run);
  if (!target) {
    return null;
  }
  return {
    focusedHistoryRequestKey: [
      run.firingId,
      run.disposition,
      run.runId ?? "no-run",
      run.runStatus ?? "no-status",
      state.retryRevision,
    ].join(":"),
    target,
  };
}

// A materialized firing's prompt Message is the exact authorization and
// pagination anchor for Schedule history. The Schedule Conversation remains
// private infrastructure and is not opened as an ordinary direct chat.
export function resolveScheduleRunChatTarget(
  run: ScheduleRunChatCandidate,
): ScheduleRunChatTarget | null {
  if (!run.conversationId || !run.messageId || !run.messageSeq) {
    return null;
  }
  return {
    conversationId: run.conversationId,
    focusedMessage: {
      conversationId: run.conversationId,
      messageId: run.messageId,
      messageSeq: run.messageSeq,
    },
    surface: "history",
  };
}
