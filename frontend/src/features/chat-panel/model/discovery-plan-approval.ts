import type { ChatCompanyModeSelection } from "@/features/chat-panel/types";

export const DISCOVERY_PLAN_APPROVAL_SIGNAL_TYPE =
  "discovery_plan_approved";

export function createDiscoveryPlanApprovalMessage(
  mode: ChatCompanyModeSelection,
) {
  return JSON.stringify({
    approved: true,
    hidden_from_transcript: true,
    selected_mode: mode,
    type: DISCOVERY_PLAN_APPROVAL_SIGNAL_TYPE,
  });
}
