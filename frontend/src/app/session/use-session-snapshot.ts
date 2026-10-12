import { useSyncExternalStore } from "react";

import {
  SESSION_CHANGE_EVENT,
  readSessionSnapshot,
  syncCrossTabSessionStorageEvent,
} from "@/app/session/session";

export function subscribeToSessionChange(onStoreChange: () => void) {
  const handleStorageChange = (event: StorageEvent) => {
    if (syncCrossTabSessionStorageEvent(event)) {
      onStoreChange();
    }
  };

  window.addEventListener("storage", handleStorageChange);
  window.addEventListener(SESSION_CHANGE_EVENT, onStoreChange);

  return () => {
    window.removeEventListener("storage", handleStorageChange);
    window.removeEventListener(SESSION_CHANGE_EVENT, onStoreChange);
  };
}

let cachedSessionSnapshot = readSessionSnapshot();
let cachedSessionValue = JSON.stringify(cachedSessionSnapshot);

export function getSessionSnapshot() {
  const nextSession = readSessionSnapshot();
  const nextValue = JSON.stringify(nextSession);

  if (nextValue !== cachedSessionValue) {
    cachedSessionSnapshot = nextSession;
    cachedSessionValue = nextValue;
  }

  return cachedSessionSnapshot;
}

/**
 * The reactive session snapshot. Components that gate on the active
 * company's role or permissions read it here so a role change pushed over
 * the socket re-renders them; `readSessionSnapshot()` is for code that
 * runs outside React.
 */
export function useSessionSnapshot() {
  return useSyncExternalStore(
    subscribeToSessionChange,
    getSessionSnapshot,
    getSessionSnapshot,
  );
}
