import {
  readStorageItem,
  removeStorageItem,
  writeStorageItem,
} from "@/shared/lib/browser-storage";

export type AuthViewMode =
  | "login"
  | "signup"
  | "verify_email"
  | "forgot_password"
  | "forgot_password_sent"
  | "reset_password"
  | "reset_password_sent";

export type PendingEmailVerificationSource = "signup" | "login" | "reload";

export type PendingEmailVerification = {
  attemptsRemaining?: number;
  email: string;
  verificationId?: string;
  expiresAt?: string;
  maxAttempts?: number;
  resendAvailableAt?: string;
  source: PendingEmailVerificationSource;
  createdAt: string;
};

export type PendingEmailVerificationInput = {
  attemptsRemaining?: number | null;
  email: string;
  verificationId?: string | null;
  expiresAt?: string | null;
  maxAttempts?: number | null;
  resendAvailableAt?: string | null;
  source: Exclude<PendingEmailVerificationSource, "reload">;
};

export const PENDING_EMAIL_VERIFICATION_STORAGE_KEY =
  "zenith-team.pending-email-verification.v1";

function normalizeOptionalText(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function isExpired(expiresAt: string | undefined, now: Date): boolean {
  if (!expiresAt) {
    return false;
  }

  const expiresAtTime = Date.parse(expiresAt);
  return Number.isFinite(expiresAtTime) && expiresAtTime <= now.getTime();
}

function normalizeAttemptBudget(
  attemptsRemaining: unknown,
  maxAttempts: unknown,
): Pick<PendingEmailVerification, "attemptsRemaining" | "maxAttempts"> {
  if (
    typeof attemptsRemaining !== "number" ||
    typeof maxAttempts !== "number" ||
    !Number.isInteger(attemptsRemaining) ||
    !Number.isInteger(maxAttempts) ||
    attemptsRemaining < 0 ||
    maxAttempts < 1 ||
    attemptsRemaining > maxAttempts
  ) {
    return {};
  }

  return {
    attemptsRemaining,
    maxAttempts,
  };
}

export function createPendingEmailVerification({
  attemptsRemaining,
  email,
  verificationId,
  expiresAt,
  maxAttempts,
  resendAvailableAt,
  source,
}: PendingEmailVerificationInput): PendingEmailVerification {
  return {
    ...normalizeAttemptBudget(attemptsRemaining, maxAttempts),
    email: email.trim().toLowerCase(),
    verificationId: normalizeOptionalText(verificationId),
    expiresAt: normalizeOptionalText(expiresAt),
    resendAvailableAt: normalizeOptionalText(resendAvailableAt),
    source,
    createdAt: new Date().toISOString(),
  };
}

export function readPendingEmailVerification(
  now = new Date(),
): PendingEmailVerification | null {
  const rawRecord = readStorageItem(
    "session",
    PENDING_EMAIL_VERIFICATION_STORAGE_KEY,
  );

  if (!rawRecord) {
    return null;
  }

  try {
    const record = JSON.parse(rawRecord) as Partial<PendingEmailVerification>;
    const email = normalizeOptionalText(record.email)?.toLowerCase();
    const createdAt = normalizeOptionalText(record.createdAt);

    if (!email || !createdAt || isExpired(record.expiresAt, now)) {
      clearPendingEmailVerification();
      return null;
    }

    return {
      ...normalizeAttemptBudget(record.attemptsRemaining, record.maxAttempts),
      email,
      verificationId: normalizeOptionalText(record.verificationId),
      expiresAt: normalizeOptionalText(record.expiresAt),
      resendAvailableAt: normalizeOptionalText(record.resendAvailableAt),
      source:
        record.source === "signup"
          ? "signup"
          : record.source === "login"
            ? "login"
            : "reload",
      createdAt,
    };
  } catch {
    clearPendingEmailVerification();
    return null;
  }
}

export function writePendingEmailVerification(
  pendingVerification: PendingEmailVerification,
): void {
  writeStorageItem(
    "session",
    PENDING_EMAIL_VERIFICATION_STORAGE_KEY,
    JSON.stringify(pendingVerification),
  );
}

export function clearPendingEmailVerification(): void {
  removeStorageItem("session", PENDING_EMAIL_VERIFICATION_STORAGE_KEY);
}

export function getResendWaitSeconds(
  pendingVerification: PendingEmailVerification | null,
  now = new Date(),
): number {
  if (!pendingVerification?.resendAvailableAt) {
    return 0;
  }

  const resendAt = Date.parse(pendingVerification.resendAvailableAt);

  if (!Number.isFinite(resendAt)) {
    return 0;
  }

  return Math.max(0, Math.ceil((resendAt - now.getTime()) / 1000));
}
