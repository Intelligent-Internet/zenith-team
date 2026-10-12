import { useEffect, useRef, useState } from "react";
import { chatAvatarRunId, chatAvatarState } from "../model/chat-avatar-state";

/** Celebrate a Run observed finishing here, never an already-complete history item. */
export function useChatAvatarState(
  scope: string,
  input: Parameters<typeof chatAvatarState>[0],
) {
  const state = chatAvatarState(input);
  const latest = input.messages.at(-1);
  const runId = chatAvatarRunId(input.currentRun, input.messages);
  const clientMessageId = latest?.clientMessageId;
  const observed = useRef<{
    scope: string;
    active: boolean;
    runId?: string;
    clientMessageId?: string;
  }>({ scope, active: false });
  const [celebrating, setCelebrating] = useState<{
    scope: string;
    runId?: string;
  } | null>(null);

  useEffect(() => {
    if (observed.current.scope !== scope) {
      observed.current = { scope, active: false };
      setCelebrating(null);
    }
    const previous = observed.current;
    if ((state === "thinking" || state === "working") && !input.continuing) {
      observed.current = { scope, active: true, runId, clientMessageId };
      setCelebrating(null);
    } else if (
      state === "needs-input" ||
      state === "error" ||
      input.continuing
    ) {
      observed.current = { scope, active: false };
      setCelebrating(null);
    } else if (
      state === "done" &&
      previous.active &&
      ((runId && previous.runId === runId) ||
        (clientMessageId && previous.clientMessageId === clientMessageId))
    ) {
      // Queue and transcript updates can arrive separately. Remember the active
      // Run across that gap, and consume its reaction exactly once.
      observed.current = { scope, active: false };
      setCelebrating({ scope, runId });
    }
  }, [scope, state, runId, clientMessageId, input.continuing]);

  useEffect(() => {
    if (!celebrating) return;
    const timer = window.setTimeout(() => setCelebrating(null), 5000);
    return () => window.clearTimeout(timer);
  }, [celebrating]);

  return state === "done" &&
    !(celebrating?.scope === scope && celebrating.runId === runId)
    ? "idle"
    : state;
}
