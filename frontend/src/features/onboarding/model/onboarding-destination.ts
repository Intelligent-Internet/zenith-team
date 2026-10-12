import type { CompanyOnboardingResult } from "@/shared/api/types";

export type CompanyOnboardingDestination = "agent_setup" | "workspace";

// A Company reaches the workspace once it owns a root Agent. Before that the
// founder still has to pick a runtime, so agent setup is the only screen that
// can do anything.
export function getCompanyOnboardingDestination(
  result: Pick<CompanyOnboardingResult, "company">,
): CompanyOnboardingDestination {
  return result.company.agentSetupCompletedAt ? "workspace" : "agent_setup";
}
