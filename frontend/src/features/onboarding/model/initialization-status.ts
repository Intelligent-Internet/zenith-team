import type { CompanyInitialization } from "@/features/onboarding/api/initialization-api";

export function initializationStatus(
  state: CompanyInitialization | undefined,
  ceoName: string,
) {
  if (!state) return "Checking your saved progress";
  if (state.phase === "ready")
    return "Your business context and welcome are ready";
  if (state.phase === "not_started")
    return "Ready to prepare your business context";
  if (state.phase === "research" && state.status === "completed")
    return "Saving your business context";

  const step = state.phase === "greeting" ? "welcome" : "research";
  switch (state.status) {
    case "awaiting_model":
      return "Research is ready to start";
    case "queued":
      return `Your ${step} is queued`;
    case "dispatched":
      return `Starting your ${step}`;
    case "deferred":
      return `Waiting to retry your ${step}`;
    case "waiting_local_directory":
      return "Waiting for your company workspace to become available";
    case "waiting_runtime":
      return "Waiting for your computer to reconnect";
    case "failed":
      return "Preparation needs your attention";
    default:
      return state.phase === "greeting"
        ? `${ceoName} is preparing your first conversation`
        : `${ceoName} is researching your business`;
  }
}
