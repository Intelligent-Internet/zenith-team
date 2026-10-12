import { authService } from "@/shared/api/auth.service";
import { isLoginResponse } from "@/shared/api/auth-response";
import { clearStoredAccessToken } from "@/lib/auth-token";
import { isAuthenticationFailure } from "@/lib/api-client";
import { companyService } from "@/shared/api/company.service";
import { pendingMemberAgentSetupPath } from "@/features/company-invitations/model/member-agent-setup";
import type {
  AuthSessionResponse,
  AuthUserResponse,
  ClientLoginResponse,
  CompanyDetail,
  CompanyOnboardingResult,
} from "@/shared/api/types";
import {
  readStorageItem,
  readStorageJson,
  removeStorageItem,
  writeStorageItem,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

export const SESSION_STORAGE_KEY = "boardly.local-session.v1";
export const SESSION_CHANGE_EVENT = "boardly-session-change";
// The company each user last worked in on this device, one key per user. It
// outlives sign-out on purpose: sign-out clears the session snapshot, and
// without it the next sign-in would open the oldest company instead.
const LAST_COMPANY_STORAGE_KEY_PREFIX = "boardly.last-company.v1:";

type SessionStorageEvent = Pick<StorageEvent, "key" | "newValue">;
let sessionSnapshotVersion = 0;

export type OnboardingStep = "agent_setup" | "company" | "complete";
export type SessionProvider = "google" | "password";

export type UserOnboardingDraft = {
  displayName: string;
  role: string;
  timezone?: string;
};

export type CompanyOnboardingDraft = {
  agentSetupCompleted?: boolean;
  initializationPending?: boolean;
  id?: string;
  role?: CompanyDetail["role"];
  // The server-resolved permission set for `role`; every UI gate reads it.
  permissions?: CompanyDetail["permissions"];
  workspaceName: string;
  stageId?: string;
  stageLabel?: string;
};

export type SessionSnapshot = {
  status: "unauthenticated" | "authenticated";
  avatarUrl?: string;
  provider?: SessionProvider;
  email?: string;
  displayName?: string;
  userId?: string;
  user?: UserOnboardingDraft;
  company?: CompanyOnboardingDraft;
  nextStep: OnboardingStep;
};

export const unauthenticatedSession: SessionSnapshot = {
  status: "unauthenticated",
  nextStep: "company",
};

function emitSessionChange() {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new Event(SESSION_CHANGE_EVENT));
  }
}

function markSessionSnapshotChanged() {
  sessionSnapshotVersion += 1;
}

function normalizeSession(value: unknown): SessionSnapshot {
  if (!value || typeof value !== "object") {
    return unauthenticatedSession;
  }

  const session = value as Partial<SessionSnapshot>;

  if (session.status !== "authenticated") {
    return unauthenticatedSession;
  }

  if (!session.email || !session.displayName) {
    return unauthenticatedSession;
  }

  return {
    status: "authenticated",
    avatarUrl:
      typeof session.avatarUrl === "string" ? session.avatarUrl : undefined,
    provider: toSessionProvider(session.provider),
    email: session.email,
    displayName: session.displayName,
    userId: typeof session.userId === "string" ? session.userId : undefined,
    user: session.user,
    company: session.company,
    nextStep:
      session.nextStep === "complete" || session.nextStep === "agent_setup"
        ? session.nextStep
        : "company",
  };
}

export function readSessionSnapshot(): SessionSnapshot {
  return readStorageJson(
    "local",
    SESSION_STORAGE_KEY,
    normalizeSession,
    unauthenticatedSession,
  );
}

export function writeSessionSnapshot(session: SessionSnapshot) {
  writeStorageJson("local", SESSION_STORAGE_KEY, session);
  rememberSessionCompany(session);
  markSessionSnapshotChanged();
  emitSessionChange();
}

function lastCompanyStorageKey(userId: string): string {
  return `${LAST_COMPANY_STORAGE_KEY_PREFIX}${userId}`;
}

// Every change of the session's company goes through writeSessionSnapshot, so
// recording it here covers sign-in, switching, onboarding a new company and
// replacing a deleted one.
function rememberSessionCompany(session: SessionSnapshot) {
  const companyId = session.company?.id;
  if (session.status !== "authenticated" || !session.userId || !companyId) {
    return;
  }
  const key = lastCompanyStorageKey(session.userId);
  if (readStorageItem("local", key) !== companyId) {
    writeStorageItem("local", key, companyId);
  }
}

function readRememberedCompanyId(userId: string | undefined) {
  return userId
    ? (readStorageItem("local", lastCompanyStorageKey(userId)) ?? undefined)
    : undefined;
}

// A non-owner never sees agent setup; an owner has finished it once the
// Company carries the server's stamp.
function companyAgentSetupCompleted(
  company: Pick<CompanyDetail, "agentSetupCompletedAt" | "role">,
): boolean {
  return company.role !== "owner" || company.agentSetupCompletedAt !== null;
}

// Keep a valid preference, otherwise prefer a company with completed setup.
// Preserve the API order within each group and leave every company available
// in the switcher.
function pickSessionCompany<
  Company extends Pick<CompanyDetail, "agentSetupCompletedAt" | "id" | "role">,
>(
  companies: Company[],
  preferredIds: Array<string | undefined>,
): Company | undefined {
  for (const id of preferredIds) {
    const preferred = id
      ? companies.find((candidate) => candidate.id === id)
      : undefined;
    if (preferred) {
      return preferred;
    }
  }
  return (
    companies.find((company) => company.agentSetupCompletedAt !== null) ??
    companies[0]
  );
}

export function updateSessionUserSummary(
  user: Pick<AuthUserResponse, "avatarUrl" | "displayName" | "email" | "id">,
): SessionSnapshot {
  const current = readSessionSnapshot();
  if (current.status !== "authenticated") {
    return current;
  }
  const next: SessionSnapshot = {
    ...current,
    avatarUrl: user.avatarUrl ?? undefined,
    displayName: user.displayName,
    email: user.email,
    userId: user.id,
    user: current.user
      ? { ...current.user, displayName: user.displayName }
      : current.user,
  };
  writeSessionSnapshot(next);
  return next;
}

export function updateSessionCompanySummary(
  company: Pick<CompanyDetail, "id" | "name">,
): SessionSnapshot {
  const current = readSessionSnapshot();
  if (
    current.status !== "authenticated" ||
    current.company?.id !== company.id
  ) {
    return current;
  }
  const next: SessionSnapshot = {
    ...current,
    company: {
      ...current.company,
      workspaceName: company.name,
    },
  };
  writeSessionSnapshot(next);
  return next;
}

/**
 * Applies a role change the server pushed for the active company. Only the
 * role and its resolved permissions move; onboarding state stays, so a
 * promotion never drops a member into founder setup.
 */
export function updateSessionCompanyAccess(
  companyId: string,
  access: Pick<CompanyDetail, "role" | "permissions">,
): SessionSnapshot {
  const current = readSessionSnapshot();
  if (current.status !== "authenticated" || current.company?.id !== companyId) {
    return current;
  }
  const next: SessionSnapshot = {
    ...current,
    company: {
      ...current.company,
      role: access.role,
      permissions: access.permissions,
    },
  };
  writeSessionSnapshot(next);
  return next;
}

export async function writeSessionFromLoginResponse(
  response: ClientLoginResponse | AuthSessionResponse,
  provider?: SessionProvider,
): Promise<SessionSnapshot> {
  const requestVersion = sessionSnapshotVersion;
  const currentSession = readSessionSnapshot();
  const currentCompanyId =
    currentSession.userId === response.user.id
      ? currentSession.company?.id
      : undefined;
  const session = createSessionFromAuth(response, provider);
  const page =
    "companies" in response
      ? response.companies
      : await companyService.listCompanies();

  if (requestVersion !== sessionSnapshotVersion) {
    return readSessionSnapshot();
  }

  // A sign-in after sign-out has no snapshot company, so the user's last
  // company on this device comes next.
  const company = pickSessionCompany(page.items, [
    currentCompanyId,
    readRememberedCompanyId(session.userId),
  ]);
  // The Company row carries its own setup stamp, so resolving the session
  // costs no extra request.
  const agentSetupCompleted = company
    ? companyAgentSetupCompleted(company)
    : false;
  if (requestVersion !== sessionSnapshotVersion) {
    return readSessionSnapshot();
  }
  const resolvedSession: SessionSnapshot = {
    ...session,
    company: company
      ? {
          agentSetupCompleted,
          id: company.id,
          role: company.role,
          permissions: company.permissions,
          initializationPending: company.initializationPending,
          workspaceName: company.name,
        }
      : undefined,
    nextStep: company
      ? agentSetupCompleted
        ? "complete"
        : "agent_setup"
      : "company",
  };

  writeSessionSnapshot(resolvedSession);
  return resolvedSession;
}

export async function loginWithGoogleAuthorizationCode({
  code,
  redirectUri,
  state,
}: {
  code: string;
  redirectUri: string;
  state: string | null;
}): Promise<SessionSnapshot> {
  void code;
  void redirectUri;
  void state;
  const result = await authService.getSession();
  return writeSessionFromLoginResponse(result, "google");
}

export async function loginWithPassword(
  email: string,
  password: string,
): Promise<SessionSnapshot> {
  const result = await authService.loginWithPassword({ email, password });
  if (!isLoginResponse(result)) {
    throw new Error(
      "Email verification is required before creating a session.",
    );
  }
  return writeSessionFromLoginResponse(result, "password");
}

export async function createPasswordAccount({
  displayName,
  email,
  password,
}: {
  displayName: string;
  email: string;
  password: string;
}): Promise<SessionSnapshot> {
  const result = await authService.signup({
    displayName,
    email,
    password,
  });
  if (!isLoginResponse(result)) {
    throw new Error(
      "Email verification is required before creating a session.",
    );
  }
  return writeSessionFromLoginResponse(result, "password");
}

export async function refreshSessionSnapshot(): Promise<SessionSnapshot> {
  const requestVersion = sessionSnapshotVersion;
  try {
    const apiSession = await refreshBackendSession();
    if (requestVersion !== sessionSnapshotVersion) {
      return readSessionSnapshot();
    }

    return await writeSessionFromLoginResponse(apiSession);
  } catch (error) {
    if (requestVersion !== sessionSnapshotVersion) {
      return readSessionSnapshot();
    }
    throw error;
  }
}

export function completeCompanyOnboarding(
  result: CompanyOnboardingResult,
): SessionSnapshot {
  const currentSession = readSessionSnapshot();
  const agentSetupCompleted = result.company.agentSetupCompletedAt !== null;
  const session: SessionSnapshot = {
    ...currentSession,
    status: "authenticated",
    provider: currentSession.provider ?? "password",
    company: {
      agentSetupCompleted,
      id: result.company.id,
      role: result.company.role,
      permissions: result.company.permissions,
      initializationPending: result.company.initializationPending,
      workspaceName: result.company.name,
    },
    nextStep: agentSetupCompleted ? "complete" : "agent_setup",
  };

  writeSessionSnapshot(session);
  return session;
}

export function markSessionCompanyInitializationPending(
  companyId: string,
  userId: string,
): void {
  const currentSession = readSessionSnapshot();
  if (
    currentSession.status !== "authenticated" ||
    currentSession.userId !== userId ||
    currentSession.company?.id !== companyId ||
    currentSession.company.initializationPending === true
  )
    return;

  writeSessionSnapshot({
    ...currentSession,
    company: { ...currentSession.company, initializationPending: true },
  });
}

export function clearSessionSnapshot() {
  // Also retain the company from sessions saved before this preference existed.
  rememberSessionCompany(readSessionSnapshot());
  removeStorageItem("local", SESSION_STORAGE_KEY);
  markSessionSnapshotChanged();
  emitSessionChange();
}

export function syncCrossTabSessionStorageEvent(
  event: SessionStorageEvent,
): boolean {
  if (event.key !== SESSION_STORAGE_KEY) {
    return false;
  }

  markSessionSnapshotChanged();

  if (event.newValue === null) {
    clearStoredAccessToken();
  }

  return true;
}

export function getNextRoute(session: SessionSnapshot): string {
  if (session.status !== "authenticated") {
    return "/login";
  }

  const memberSetup = pendingMemberAgentSetupPath(
    session.userId,
    session.company?.id,
  );
  if (memberSetup) return memberSetup;

  if (session.nextStep === "company" || session.nextStep === "agent_setup") {
    return "/onboarding";
  }

  return "/app";
}

function createSessionFromAuth(
  apiSession: AuthSessionResponse | ClientLoginResponse,
  provider?: SessionProvider,
): SessionSnapshot {
  const currentSession = readSessionSnapshot();
  const user = apiSession.user;
  const session: SessionSnapshot = {
    status: "authenticated",
    avatarUrl: user.avatarUrl ?? undefined,
    provider: provider ?? currentSession.provider ?? "password",
    email: user.email,
    displayName: user.displayName,
    userId: user.id,
    user: {
      displayName: user.displayName,
      role: currentSession.user?.role ?? "Founder",
      timezone: currentSession.user?.timezone,
    },
    company: {
      ...currentSession.company,
      workspaceName: currentSession.company?.workspaceName || "Company",
    },
    nextStep:
      currentSession.status === "authenticated"
        ? currentSession.nextStep
        : "company",
  };

  return session;
}

async function refreshBackendSession(): Promise<AuthSessionResponse> {
  try {
    return await authService.getSession();
  } catch (error) {
    if (!isAuthenticationFailure(error)) throw error;
    const response = await authService.refreshToken();
    return { user: response.user };
  }
}

function toSessionProvider(provider: unknown): SessionProvider {
  return provider === "google" ? "google" : "password";
}

export async function activateSessionCompany(
  company: CompanyDetail,
  expectedUserId: string,
) {
  const version = sessionSnapshotVersion;
  const current = readSessionSnapshot();
  if (current.status !== "authenticated" || current.userId !== expectedUserId)
    return null;
  const agentSetupCompleted = companyAgentSetupCompleted(company);
  if (version !== sessionSnapshotVersion) return null;
  const next: SessionSnapshot = {
    ...current,
    company: {
      id: company.id,
      role: company.role,
      permissions: company.permissions,
      initializationPending: company.initializationPending,
      workspaceName: company.name,
      agentSetupCompleted,
    },
    nextStep: agentSetupCompleted ? "complete" : "agent_setup",
  };
  writeSessionSnapshot(next);
  return next;
}

export function replaceDeletedSessionCompany(
  deletedCompanyId: string,
  expectedUserId: string,
  nextCompany?: CompanyDetail,
): SessionSnapshot | null {
  const current = readSessionSnapshot();
  if (
    current.status !== "authenticated" ||
    current.userId !== expectedUserId ||
    current.company?.id !== deletedCompanyId
  ) {
    return null;
  }

  const agentSetupCompleted = nextCompany
    ? companyAgentSetupCompleted(nextCompany)
    : false;
  const next: SessionSnapshot = {
    ...current,
    company: nextCompany
      ? {
          id: nextCompany.id,
          role: nextCompany.role,
          permissions: nextCompany.permissions,
          initializationPending: nextCompany.initializationPending,
          workspaceName: nextCompany.name,
          agentSetupCompleted,
        }
      : undefined,
    nextStep: nextCompany
      ? agentSetupCompleted
        ? "complete"
        : "agent_setup"
      : "company",
  };
  writeSessionSnapshot(next);
  return next;
}
