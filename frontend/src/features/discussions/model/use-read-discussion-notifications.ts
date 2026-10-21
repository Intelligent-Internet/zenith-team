import { useEffect } from "react";
import { notificationsApi } from "@/features/notifications/api/notifications-api";
import store from "@/state/store";

// Entry/refocus consumes the shared Inbox cache. Realtime patches new arrivals;
// reconnect invalidates it before the next read so focus never forces a GET.
export function useReadDiscussionNotifications(
  companyId: string,
  conversationId: string,
  panelId: string,
  enabled: boolean,
  threadRootId?: string,
) {
  useEffect(() => {
    if (!enabled) return;
    let disposed = false;
    let running = false;
    const focused = () =>
      !disposed &&
      document.visibilityState === "visible" &&
      document.hasFocus() &&
      store.getState().conversationDock.visibleConversation?.panelId ===
        panelId;
    const read = async () => {
      if (running || !focused()) return;
      running = true;
      try {
        let cursor: string | undefined;
        do {
          const request = store.dispatch(
            notificationsApi.endpoints.listNotifications.initiate(
              {
                companyId,
                status: "unread",
                kind: "discussion_mention",
                limit: 100,
                cursor,
              },
              { subscribe: false },
            ),
          );
          const page = await request.unwrap();
          for (const item of page.items) {
            if (!focused()) return;
            if (!item.deepLink) continue;
            const params = new URL(item.deepLink, "https://zenith-team.internal")
              .searchParams;
            if (params.get("conversationId") !== conversationId) continue;
            const root = params.get("threadRootId");
            if (root && root !== threadRootId) continue;
            const mutation = store.dispatch(
              notificationsApi.endpoints.mutateNotification.initiate(
                {
                  companyId,
                  itemId: item.id,
                  mutation: "read",
                  expectedRevision: item.revision,
                },
                { track: false },
              ),
            );
            try {
              await mutation.unwrap();
            } catch {
              /* Reconcile revision races on the next focus. */
            }
          }
          cursor = page.nextCursor ?? undefined;
        } while (cursor && focused());
      } catch {
        /* Offline/revoked reads retry on the next focus. */
      } finally {
        running = false;
      }
    };
    const onFocus = () => {
      void read();
    };
    onFocus();
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onFocus);
    return () => {
      disposed = true;
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onFocus);
    };
  }, [companyId, conversationId, panelId, enabled, threadRootId]);
}
