import {
  createPendingEmailVerification,
  type PendingEmailVerification,
  type PendingEmailVerificationInput,
} from "@/features/auth/model/auth-view-state";
import {
  getApiErrorBody,
  getApiErrorCode,
  getApiErrorMessage,
  isApiClientError,
  isApiUnreachableError,
  type ApiErrorBody,
} from "@/lib/api-client";
import { isDesktopSessionNotSaved } from "@/shared/api/desktop-auth-error";
import type {
  AuthErrorCode,
  EmailOtpResendResponse,
  PendingEmailVerificationResponse,
  PendingVerificationStatusResponse,
} from "@/shared/api/types";

type PendingVerificationSource = PendingEmailVerificationInput["source"];

type AuthPendingVerificationBody = ApiErrorBody & {
  email?: string;
  expiresAt?: string | null;
  resendAvailableAt?: string | null;
  verificationId?: string | null;
};

export function getAuthErrorCode(error: unknown): AuthErrorCode | null {
  const code = getApiErrorCode(error);
  return typeof code === "string"
    ? (code.toUpperCase() as AuthErrorCode)
    : null;
}

// A desktop sign-in the server accepted but this computer could not save
// (secure storage is unavailable). The details were right, so the words
// never say otherwise; after a sign-up or a verified code the account
// exists, so they say that too.
export type SessionNotSavedStep = "signin" | "signup" | "verify";

export function getSessionNotSavedMessage(step: SessionNotSavedStep): string {
  switch (step) {
    case "signup":
      return "Account created. Can't save sign-in here.";
    case "verify":
      return "Email verified. Can't save sign-in here.";
    default:
      return "Can't save sign-in on this computer.";
  }
}

// The sentence a sign-in screen shows for a failure. The server's own
// detail is a sentence written for a person when it refused on purpose
// (wrong password, taken email, invalid input); an outage or an unreachable
// server is not, so those get their own words and the requestId to quote
// (docs/api/errors.md: retry once, then surface the requestId).
export function getAuthErrorMessage(error: unknown, fallback: string): string {
  if (isDesktopSessionNotSaved(error)) {
    return getSessionNotSavedMessage("signin");
  }
  if (isApiUnreachableError(error)) {
    return isApiClientError(error) && error.status === null && error.message
      ? error.message
      : "Zenith Team could not be reached. Check your connection and try again.";
  }
  if (isApiClientError(error) && error.status !== null && error.status >= 500) {
    const reference = error.body?.requestId
      ? ` Reference ${error.body.requestId}.`
      : "";
    return `Zenith Team is having trouble right now. Try again in a moment.${reference}`;
  }
  return getApiErrorMessage(error, fallback);
}

export function getGoogleOAuthCallbackErrorMessage(
  code: string | null,
): string {
  switch (code) {
    case "oauth_provider_denied":
      return "Google sign-in was canceled. Try again when you're ready.";
    case "oauth_flow_invalid":
      return "Your Google sign-in session expired or is invalid. Please try again.";
    case "oauth_not_configured":
      return "Google sign-in is currently unavailable. Please use email to sign in.";
    case "oauth_provider_unavailable":
      return "Google could not be reached. Please try again in a moment.";
    case "oauth_email_unverified":
      return "Please verify your email with Google before signing in.";
    case "oauth_account_conflict":
      return "This Google account conflicts with an existing account. Please use your original sign-in method.";
    case "account_disabled":
      return "Your account is disabled. Please contact support.";
    default:
      return "Google sign-in could not be completed. Please try again or use email.";
  }
}

export function createPendingVerificationFromSignupResponse(
  response: PendingEmailVerificationResponse,
  source: PendingVerificationSource,
): PendingEmailVerification {
  return createPendingEmailVerification({
    attemptsRemaining: response.attemptsRemaining,
    email: response.email,
    verificationId: response.verificationId,
    expiresAt: response.expiresAt,
    maxAttempts: response.maxAttempts,
    resendAvailableAt: response.resendAvailableAt,
    source,
  });
}

function normalizePendingVerificationSource(
  source: PendingEmailVerification["source"],
): PendingVerificationSource {
  return source === "signup" ? "signup" : "login";
}

export function mergePendingVerificationAfterResend(
  current: PendingEmailVerification,
  response: EmailOtpResendResponse,
): PendingEmailVerification {
  return createPendingEmailVerification({
    attemptsRemaining: response.attemptsRemaining,
    email: current.email,
    verificationId: response.verificationId,
    expiresAt: response.expiresAt,
    maxAttempts: response.maxAttempts,
    resendAvailableAt: response.resendAvailableAt,
    source: normalizePendingVerificationSource(current.source),
  });
}

export function mergePendingVerificationAfterStatusCheck(
  current: PendingEmailVerification,
  response: Extract<
    PendingVerificationStatusResponse,
    { status: "pending_verification" }
  >,
): PendingEmailVerification {
  return createPendingEmailVerification({
    attemptsRemaining: response.attemptsRemaining,
    email: response.email || current.email,
    verificationId: response.verificationId,
    expiresAt: response.expiresAt,
    maxAttempts: response.maxAttempts,
    resendAvailableAt: response.resendAvailableAt,
    source: normalizePendingVerificationSource(current.source),
  });
}

export function extractPendingVerificationFromError(
  error: unknown,
  fallbackEmail: string,
  source: PendingVerificationSource,
): PendingEmailVerification | null {
  if (getAuthErrorCode(error) !== "EMAIL_NOT_VERIFIED") {
    return null;
  }

  const body = getApiErrorBody(error) as AuthPendingVerificationBody | null;

  return createPendingEmailVerification({
    email: body?.email ?? fallbackEmail,
    verificationId: body?.verificationId,
    expiresAt: body?.expiresAt,
    resendAvailableAt: body?.resendAvailableAt,
    source,
  });
}
