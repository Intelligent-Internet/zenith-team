import type { SessionSnapshot } from "@/shared/session/session";
import { readStorageItem } from "@/shared/lib/browser-storage";

// The screens of onboarding, in order. One list is the whole step
// machine: the page renders `screen`, the progress dots count this array, and
// nothing else gets to hold a second opinion about where the founder is.
export const onboardingScreens = [
  "intro",
  "company",
  "agent",
  "connections",
  "team",
  "initializing",
] as const;

export type OnboardingScreen = (typeof onboardingScreens)[number];

// How far the founder has moved within the position the server knows about.
// The server records the coarse position (`nextStep`); these are the steps
// that happen between two server writes and would otherwise be lost on
// reload.
export type OnboardingStage = {
  introSeen: boolean;
  connectionsReached: boolean;
  teamReached: boolean;
  initializing?: boolean;
  reviewingSetup?: boolean;
  companyId?: string;
};

export const initialOnboardingStage: OnboardingStage = {
  introSeen: false,
  connectionsReached: false,
  teamReached: false,
};

// resolveOnboardingScreen derives the screen from server truth first and local
// progress second. The server owns whether the Company and its root Agent
// exist; progress through connection and team setup stays local until completion.
export function resolveOnboardingScreen(
  session: SessionSnapshot,
  stage: OnboardingStage,
): OnboardingScreen {
  if (stage.reviewingSetup && session.company?.id) return "agent";
  // Pending only until research has been started; once it runs server-side the
  // founder is never held here.
  if (session.company?.initializationPending) return "initializing";
  if (
    session.nextStep === "agent_setup" ||
    (session.company?.id && stage.companyId === session.company.id)
  ) {
    if (stage.initializing) return "initializing";
    if (stage.teamReached) return "team";
    return stage.connectionsReached ? "connections" : "agent";
  }
  return stage.introSeen ? "company" : "intro";
}

export function onboardingStageKey(session: SessionSnapshot) {
  return `boardly:onboarding:stage:${session.email ?? "anonymous"}`;
}

export function onboardingDraftKey(session: SessionSnapshot) {
  return `boardly:onboarding:draft:${session.email ?? "anonymous"}`;
}

// Creating Andrew completes the server's agent-setup stamp before the local
// connection and team steps finish. Keep an in-progress onboarding reload here.
export function hasPendingCompanyOnboarding(session: SessionSnapshot) {
  if (!session.company?.id) return false;
  try {
    const stage = JSON.parse(
      readStorageItem("session", onboardingStageKey(session)) || "null",
    ) as OnboardingStage | null;
    return stage?.companyId === session.company.id;
  } catch {
    return false;
  }
}
