import { useCallback, useEffect, useId, useRef } from "react";

import { readSessionSnapshot } from "@/shared/session/session";
import { useReadConversationNotificationsMutation } from "@/features/notifications/api/notifications-api";
import {
  clearVisibleConversation,
  setVisibleConversation,
} from "@/state/slice/conversation-dock";
import store from "@/state/store";

export function useReadConversationNotificationsOnFocus(
  conversationId?: string | null,
) {
  const panelId = useId();
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [readConversationNotifications] =
    useReadConversationNotificationsMutation();
  const inFlightKeysRef = useRef(new Set<string>());

  const readIfFocused = useCallback(() => {
    if (
      !companyId ||
      !conversationId ||
      document.visibilityState !== "visible" ||
      !document.hasFocus() ||
      store.getState().conversationDock.visibleConversation?.panelId !== panelId
    ) {
      return;
    }

    const key = `${companyId}:${conversationId}`;
    if (inFlightKeysRef.current.has(key)) {
      return;
    }

    inFlightKeysRef.current.add(key);
    void readConversationNotifications({ companyId, conversationId })
      .unwrap()
      .catch(() => undefined)
      .finally(() => {
        inFlightKeysRef.current.delete(key);
      });
  }, [companyId, conversationId, panelId, readConversationNotifications]);

  useEffect(() => {
    if (!companyId || !conversationId) return;

    // Register the rendered panel, not a remembered tab or a hidden chat hook.
    store.dispatch(
      setVisibleConversation({ companyId, conversationId, panelId }),
    );
    readIfFocused();
    window.addEventListener("focus", readIfFocused);
    document.addEventListener("visibilitychange", readIfFocused);
    return () => {
      window.removeEventListener("focus", readIfFocused);
      document.removeEventListener("visibilitychange", readIfFocused);
      store.dispatch(clearVisibleConversation(panelId));
    };
  }, [companyId, conversationId, panelId, readIfFocused]);

  return readIfFocused;
}
