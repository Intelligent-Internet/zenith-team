import { getNextRoute } from "@/app/session/session-routing";
import { type SessionSnapshot } from "@/shared/session/session";
import { invitationReturnPath } from "@/features/company-invitations/model/join-continuation";
import {
  readStorageJson,
  removeStorageItem,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

const AUTH_CONTINUATION_STORAGE_KEY = "zenith-team.auth-continuation.v1";
const AUTH_CONTINUATION_TTL_MS = 10 * 60 * 1_000;

type AuthContinuation = {
  expiresAt: number;
  returnPath: string;
};

function isSafeReturnPath(returnPath: string) {
  return returnPath.startsWith("/") && !returnPath.startsWith("//");
}

function normalizeAuthContinuation(value: unknown): AuthContinuation | null {
  if (!value || typeof value !== "object") {
    return null;
  }

  const continuation = value as Partial<AuthContinuation>;
  if (
    typeof continuation.expiresAt !== "number" ||
    !Number.isFinite(continuation.expiresAt) ||
    typeof continuation.returnPath !== "string" ||
    !isSafeReturnPath(continuation.returnPath)
  ) {
    return null;
  }

  return {
    expiresAt: continuation.expiresAt,
    returnPath: continuation.returnPath,
  };
}

export function writeAuthContinuation(returnPath: string) {
  if (!isSafeReturnPath(returnPath)) {
    return false;
  }

  return writeStorageJson("session", AUTH_CONTINUATION_STORAGE_KEY, {
    expiresAt: Date.now() + AUTH_CONTINUATION_TTL_MS,
    returnPath,
  } satisfies AuthContinuation);
}

export function readAuthContinuationReturnPath() {
  const continuation = readStorageJson<AuthContinuation | null>(
    "session",
    AUTH_CONTINUATION_STORAGE_KEY,
    normalizeAuthContinuation,
    null,
  );

  if (!continuation || continuation.expiresAt <= Date.now()) {
    return null;
  }

  return continuation.returnPath;
}

export function clearAuthContinuation() {
  removeStorageItem("session", AUTH_CONTINUATION_STORAGE_KEY);
}

export function getAuthReturnRoute(session: SessionSnapshot) {
  const nextRoute = getNextRoute(session);
  return (
    invitationReturnPath() ??
    (nextRoute === "/app" ? readAuthContinuationReturnPath() : null) ??
    nextRoute
  );
}
