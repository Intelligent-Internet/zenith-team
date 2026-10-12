import { getApiErrorMessage } from "@/lib/api-client";

export const ONBOARDING_COMPLETION_FALLBACK_MESSAGE =
  "Unable to complete onboarding. Please try again.";

export function getOnboardingCompletionErrorMessage(error: unknown): string {
  return getApiErrorMessage(error, ONBOARDING_COMPLETION_FALLBACK_MESSAGE);
}
