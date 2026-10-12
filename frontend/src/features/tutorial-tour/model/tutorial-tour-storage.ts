import {
  readStorageJson,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

export const TUTORIAL_TOUR_VERSION = 1;

export type TutorialTourStatus = "completed" | "eligible" | "skipped";

export type TutorialTourState = {
  finishedAt?: string;
  status: TutorialTourStatus;
  version: typeof TUTORIAL_TOUR_VERSION;
};

const fallbackTourState: TutorialTourState | null = null;

export function tutorialTourStorageKey(userId: string, companyId: string) {
  return `zenith:tutorial-tour:${TUTORIAL_TOUR_VERSION}:${encodeURIComponent(userId)}:${encodeURIComponent(companyId)}`;
}

function normalizeTutorialTourState(value: unknown): TutorialTourState | null {
  if (!value || typeof value !== "object") return null;

  const state = value as Partial<TutorialTourState>;
  if (
    state.version !== TUTORIAL_TOUR_VERSION ||
    (state.status !== "eligible" &&
      state.status !== "completed" &&
      state.status !== "skipped")
  ) {
    return null;
  }

  return {
    ...(typeof state.finishedAt === "string"
      ? { finishedAt: state.finishedAt }
      : {}),
    status: state.status,
    version: TUTORIAL_TOUR_VERSION,
  };
}

export function readTutorialTourState(
  userId?: string,
  companyId?: string,
): TutorialTourState | null {
  if (!userId || !companyId) return null;

  return readStorageJson(
    "local",
    tutorialTourStorageKey(userId, companyId),
    normalizeTutorialTourState,
    fallbackTourState,
  );
}

export function writeTutorialTourState(
  userId: string,
  companyId: string,
  state: TutorialTourState,
) {
  return writeStorageJson(
    "local",
    tutorialTourStorageKey(userId, companyId),
    state,
  );
}

export function markTutorialTourEligible(userId?: string, companyId?: string) {
  if (!userId || !companyId) return;

  const current = readTutorialTourState(userId, companyId);
  if (current) return;

  writeTutorialTourState(userId, companyId, {
    status: "eligible",
    version: TUTORIAL_TOUR_VERSION,
  });
}
