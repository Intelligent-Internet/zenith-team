// Plain words for the two ways Project access (ADR 0017) ends a Run. The
// server writes its own sentence for a failure (`failureMessage`); these are
// the fallback for `no_eligible_resolver` and the only copy for
// `project_access_revoked`, which is a cancellation marker in the Run's
// `error` and has no sentence of its own.

import { NO_ELIGIBLE_RESOLVER_COPY } from "@/lib/api-error-copy";

export const PROJECT_ACCESS_REVOKED_COPY =
  "Stopped: the person this run works for lost access to this project.";

/** The failure sentence to show, preferring the server's own. */
export function runFailureCopy(
  reason: string | null | undefined,
  message: string | null | undefined,
): string | null {
  const text = message?.trim();
  if (text) return text;
  return reason === "no_eligible_resolver" ? NO_ELIGIBLE_RESOLVER_COPY : null;
}

/** Why the server cancelled a Run, when Project access was the reason. */
export function runCancellationCopy(run: {
  status: string;
  error?: string | null;
}): string | null {
  return run.status === "cancelled" && run.error === "project_access_revoked"
    ? PROJECT_ACCESS_REVOKED_COPY
    : null;
}
