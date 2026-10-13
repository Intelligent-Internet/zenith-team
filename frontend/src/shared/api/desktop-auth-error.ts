import type {
  DesktopAuthFailure,
  DesktopAuthFailureKind,
} from "../../../electron/shared/desktop-bridge.types";

// A failed desktop auth bridge call. It keeps the main process's `kind`, so
// a sign-in screen can tell a sign-in this computer could not save from a
// refusal and word it for its own step (auth-api-utils.ts).
export class DesktopAuthBridgeError extends Error {
  readonly kind: DesktopAuthFailureKind | null;

  constructor(failure: DesktopAuthFailure) {
    super(failure.reason);
    this.name = "DesktopAuthBridgeError";
    this.kind = failure.kind ?? null;
  }
}

/**
 * The server accepted the sign-in, but this computer could not save it
 * (secure storage is unavailable). The password, code or Google account
 * was right, and the main process already ended that server session.
 */
export function isDesktopSessionNotSaved(error: unknown): boolean {
  return (
    error instanceof DesktopAuthBridgeError &&
    error.kind === "session_not_saved"
  );
}
