import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import {
  readTutorialTourState,
  tutorialTourStorageKey,
  TUTORIAL_TOUR_VERSION,
  writeTutorialTourState,
  type TutorialTourState,
  type TutorialTourStatus,
} from "@/features/tutorial-tour/model/tutorial-tour-storage";

type TutorialTourContextValue = {
  replayRequest: number;
  requestReplay: () => void;
  state: TutorialTourState | null;
  updateStatus: (status: TutorialTourStatus) => void;
};

const TutorialTourContext = createContext<TutorialTourContextValue | null>(
  null,
);

export function TutorialTourProvider({
  children,
  companyId,
  userId,
}: {
  children: ReactNode;
  companyId?: string;
  userId?: string;
}) {
  const storageKey =
    userId && companyId ? tutorialTourStorageKey(userId, companyId) : null;
  const [state, setState] = useState<TutorialTourState | null>(() =>
    readTutorialTourState(userId, companyId),
  );
  const [replayRequest, setReplayRequest] = useState(0);

  useEffect(() => {
    setState(readTutorialTourState(userId, companyId));
  }, [companyId, userId]);

  useEffect(() => {
    if (!storageKey) return;

    const handleStorage = (event: StorageEvent) => {
      if (event.key === storageKey) {
        setState(readTutorialTourState(userId, companyId));
      }
    };
    window.addEventListener("storage", handleStorage);
    return () => window.removeEventListener("storage", handleStorage);
  }, [companyId, storageKey, userId]);

  const persist = useCallback(
    (next: TutorialTourState) => {
      if (!userId || !companyId) return;
      writeTutorialTourState(userId, companyId, next);
      setState(next);
    },
    [companyId, userId],
  );

  const updateStatus = useCallback(
    (status: TutorialTourStatus) => {
      persist({
        ...(status === "eligible"
          ? {}
          : { finishedAt: new Date().toISOString() }),
        status,
        version: TUTORIAL_TOUR_VERSION,
      });
    },
    [persist],
  );

  const value = useMemo<TutorialTourContextValue>(
    () => ({
      replayRequest,
      requestReplay: () => setReplayRequest((request) => request + 1),
      state,
      updateStatus,
    }),
    [replayRequest, state, updateStatus],
  );

  return (
    <TutorialTourContext.Provider value={value}>
      {children}
    </TutorialTourContext.Provider>
  );
}

export function useTutorialTour() {
  const value = useContext(TutorialTourContext);
  if (!value) {
    throw new Error("useTutorialTour must be used inside TutorialTourProvider");
  }
  return value;
}
