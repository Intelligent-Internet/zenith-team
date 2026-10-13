import { clearSessionSnapshot } from "@/shared/session/session";
import { clearStoredAccessToken } from "@/lib/auth-token";
import { resetAppState } from "@/state/reducer";
import store, { persistor } from "@/state/store";
import { clearDocumentCache } from "@/features/project-canvas/documents/document-cache";

type ClearInvalidClientSessionStateOptions = {
  dispatchReset?: () => void;
  purgePersistedState?: () => Promise<void>;
};

function dispatchResetAppState() {
  store.dispatch(resetAppState());
}

async function purgeReduxState() {
  await persistor.purge();
}

export async function clearInvalidClientSessionState({
  dispatchReset = dispatchResetAppState,
  purgePersistedState = purgeReduxState,
}: ClearInvalidClientSessionStateOptions = {}) {
  clearStoredAccessToken();
  clearSessionSnapshot();
  dispatchReset();
  await Promise.all([purgePersistedState(), clearDocumentCache()]);
}
