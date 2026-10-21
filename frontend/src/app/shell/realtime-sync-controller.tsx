import { useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router";
import { clearInvalidClientSessionState } from "@/app/session/client-session-state";
import {
  applyRealtimeCacheEvent,
  convergeRealtimeCaches,
  createRealtimeRefreshQueue,
} from "@/app/shell/realtime-sync/realtime-cache-sync";
import { zenithTeamRealtimeClient } from "@/shared/realtime/realtime-client.service";
import store, { useAppDispatch } from "@/state/store";

export function RealtimeSyncController({
  companyId,
}: {
  companyId: string | undefined;
}) {
  const dispatch = useAppDispatch();
  const location = useLocation();
  const navigate = useNavigate();
  const pathnameRef = useRef(location.pathname);
  pathnameRef.current = location.pathname;
  const searchRef = useRef(location.search);
  searchRef.current = location.search;
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;

  useEffect(() => {
    if (!companyId) {
      zenithTeamRealtimeClient.stop();
      return undefined;
    }

    const refreshQueue = createRealtimeRefreshQueue({
      companyId,
      dispatch,
    });
    const convergeCaches = () => {
      refreshQueue.dispose();
      convergeRealtimeCaches(dispatch, companyId);
    };
    let initialConnectionConverged = false;
    const removeEventListener = zenithTeamRealtimeClient.onEvent((event) => {
      applyRealtimeCacheEvent(
        {
          companyId,
          dispatch,
          getState: store.getState,
          pathname: pathnameRef.current,
          requestRefresh: refreshQueue.request,
          requestWikiRefresh: refreshQueue.requestWiki,
          leave: (destination) => {
            const to = destination({
              pathname: pathnameRef.current,
              search: searchRef.current,
            });
            if (!to) return false;
            navigateRef.current(to, { replace: true });
            return true;
          },
        },
        event,
      );
    });
    const removeRecoveryListener =
      zenithTeamRealtimeClient.onRecovery(convergeCaches);
    const removeAuthenticationFailureListener =
      zenithTeamRealtimeClient.onAuthenticationFailure(() => {
        void clearInvalidClientSessionState().catch(() => undefined);
      });
    const removeStatusListener = zenithTeamRealtimeClient.onStatus(
      (snapshot) => {
        // An event can land after the initial HTTP reads but before the socket
        // opens. Reconcile once on first readiness to close that snapshot gap;
        // later reconnects converge through the recovery listener above.
        if (snapshot.status === "ready" && !initialConnectionConverged) {
          initialConnectionConverged = true;
          convergeCaches();
        }
        if (
          snapshot.status === "reconnecting" ||
          snapshot.status === "terminal"
        ) {
          console.warn(`[realtime] connection ${snapshot.status}`, snapshot);
        }
      },
    );

    // Connectivity returning is the moment a waiting reconnect should run:
    // without these nudges a slow-tier retry sits out its full delay after a
    // laptop wake or a network drop even though the link is already back.
    const retryNow = () => zenithTeamRealtimeClient.retryNow();
    const reconnectNow = () => zenithTeamRealtimeClient.reconnectNow();
    const retryWhenVisible = () => {
      if (document.visibilityState === "visible") retryNow();
    };
    window.addEventListener("online", reconnectNow);
    window.addEventListener("focus", retryNow);
    document.addEventListener("visibilitychange", retryWhenVisible);

    zenithTeamRealtimeClient.start(companyId);
    return () => {
      window.removeEventListener("online", reconnectNow);
      window.removeEventListener("focus", retryNow);
      document.removeEventListener("visibilitychange", retryWhenVisible);
      removeEventListener();
      removeRecoveryListener();
      removeAuthenticationFailureListener();
      removeStatusListener();
      refreshQueue.dispose();
      zenithTeamRealtimeClient.stop();
    };
  }, [companyId, dispatch]);

  return null;
}
