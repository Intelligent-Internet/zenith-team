import type { CompanyInitialization } from "@/features/onboarding/api/initialization-api";

// The status of getting the company research started. Once it runs, the Wiki
// shows its progress, so this only describes the steps before that.
export function initializationStatus(
  state: CompanyInitialization | undefined,
  ceoName: string,
) {
  if (!state) return "Checking your saved progress";
  if (state.phase === "not_started")
    return state.status === "awaiting_model"
      ? "Research is ready to start"
      : "Ready to research your company";
  if (state.status === "waiting_runtime")
    return "Waiting for your computer to reconnect";
  if (state.status === "failed") return "Research needs your attention";
  return `${ceoName} is researching your company`;
}
