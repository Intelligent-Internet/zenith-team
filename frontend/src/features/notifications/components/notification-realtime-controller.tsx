import { useEffect, useRef } from "react";
import { companyService } from "@/shared/api/company.service";
import { notificationService } from "@/features/notifications/api/notification.service";
import { activateAccountNotification } from "@/features/notifications/lib/account-notification-navigation";
import { useNavigate } from "react-router";

import {
  reportDesktopDiagnostic,
  setDesktopWorkbenchStatus,
  shouldUseDesktopHashRouter,
  showDesktopNotification,
} from "@/shared/desktop/desktop-environment.service";
import {
  isDesktopNotificationKindEnabled,
  loadDesktopWorkbenchPreferences,
} from "@/app/desktop/desktop-workbench-preferences";
import { readSessionSnapshot } from "@/shared/session/session";
import {
  notificationsApi,
  useGetNotificationCountsQuery,
} from "@/features/notifications/api/notifications-api";
import { selectChannelTitle } from "@/features/discussions/model/channel-titles";
import { sanitizeNotificationPath } from "@/features/notifications/lib/notification-navigation";
import { toDesktopNotificationKind } from "@/features/notifications/lib/desktop-notification-kind";
import { desktopNotificationCopy } from "@/features/notifications/lib/desktop-notification-copy";
import {
  ACCOUNT_INBOX_SCOPE,
  isNotificationKind,
  namedDiscussionNotice,
  type NotificationKind,
} from "@/features/notifications/model/notification";
import { isRetryableBoardlyQueryError } from "@/shared/api/boardly-api";
import {
  boardlyRealtimeClient,
  realtimeEventConversationId,
  type BoardlyRealtimeEvent,
} from "@/shared/realtime/realtime-client.service";
import { toast } from "@/shared/ui/sonner";
import store from "@/state/store";

const MAX_RECENT_PRESENTATION_IDS = 100;
const RECENT_PRESENTATION_STORAGE_PREFIX =
  "boardly.desktop.notifications.presented.v2";

type NotificationPresentation = {
  body: string;
  conversationId?: string;
  threadRootId?: string;
  messageId?: string;
  invitationId?: string;
  discussionType?: "channel" | "human_direct";
  deepLink?: string;
  kind: NotificationKind;
  notificationId: string;
  title: string;
};

export function NotificationRealtimeController() {
  const navigate = useNavigate();
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;
  const session = readSessionSnapshot();
  const userId = session.userId;
  const userNamespace =
    session.userId ?? session.email?.trim().toLowerCase() ?? "unknown-user";
  const isDesktop = shouldUseDesktopHashRouter();
  const countsQuery = useGetNotificationCountsQuery(ACCOUNT_INBOX_SCOPE, {
    refetchOnMountOrArgChange: 60,
  });
  const unreadCount = countsQuery.isError
    ? 0
    : (countsQuery.currentData?.unreadCount ?? 0);

  useEffect(() => {
    if (!isDesktop) {
      return;
    }
    void setDesktopWorkbenchStatus({
      unreadCount,
    });
  }, [unreadCount, isDesktop]);

  useEffect(() => {
    if (!userId) {
      return undefined;
    }

    const storageKey = `${RECENT_PRESENTATION_STORAGE_PREFIX}:${encodeURIComponent(userNamespace)}`;
    const presentedIds = readRecentPresentationIds(
      isDesktop ? storageKey : undefined,
    );
    let disposed = false;
    let accessRevision = 0;
    const pendingPreviews = new Set<{
      abort: () => void;
      discussion?: {
        companyId: string;
        conversationId: string;
        revision: number;
      };
    }>();

    const refreshAuthoritativeInbox = () => {
      store.dispatch(notificationsApi.util.invalidateTags(["Inbox"]));
    };

    const handlePresentation = async (event: BoardlyRealtimeEvent) => {
      const targetCompanyId = event.companyId;
      const presentation = parseNotificationPresentation(event);
      const accessAtStart = accessRevision;
      // Reconnect converges counts, without turning old events into a toast storm.
      if (
        !presentation ||
        Date.now() - Date.parse(event.createdAt) > 60_000 ||
        presentedIds.has(presentation.notificationId)
      )
        return;
      presentedIds.add(presentation.notificationId);
      pruneRecentPresentationIds(presentedIds);
      if (isDesktop) {
        writeRecentPresentationIds(storageKey, presentedIds);
      }

      let companyName: string;
      let itemRevision: number;
      try {
        const [company, item] = await Promise.all([
          targetCompanyId
            ? companyService.getCompanyById(targetCompanyId)
            : Promise.resolve(null),
          notificationService.get(
            targetCompanyId ?? ACCOUNT_INBOX_SCOPE,
            presentation.notificationId,
          ),
        ]);
        if (
          disposed ||
          readSessionSnapshot().userId !== userId ||
          !item.unread ||
          item.archivedAt
        )
          return;
        itemRevision = item.revision;
        companyName = company?.name ?? "Zenith Team";
      } catch {
        presentedIds.delete(presentation.notificationId);
        if (isDesktop) {
          writeRecentPresentationIds(storageKey, presentedIds);
        }
        refreshAuthoritativeInbox();
        return;
      }
      const { title } = desktopNotificationCopy(presentation, companyName);
      // Inbox/in-app copy uses loaded channel names. Native previews below
      // reread the message and conversation through their authorized endpoints.
      const body =
        presentation.kind === "discussion_mention" &&
        targetCompanyId &&
        presentation.conversationId
          ? namedDiscussionNotice(
              presentation.body,
              selectChannelTitle(
                store.getState(),
                targetCompanyId,
                presentation.conversationId,
              ),
            )
          : presentation.body;

      const acknowledgeVisibleConversation = () => {
        const visible = store.getState().conversationDock.visibleConversation;
        if (
          !targetCompanyId ||
          !presentation.conversationId ||
          document.visibilityState !== "visible" ||
          visible?.companyId !== targetCompanyId ||
          visible.conversationId !== presentation.conversationId ||
          (presentation.kind === "discussion_mention" && !visible.discussion)
        ) {
          return false;
        }

        if (presentation.kind === "discussion_mention") {
          // A focused channel stays quiet, but an unopened thread stays unread.
          if (
            presentation.threadRootId &&
            visible.threadRootId !== presentation.threadRootId
          )
            return true;
          void notificationService
            .mutate(
              targetCompanyId,
              presentation.notificationId,
              "read",
              itemRevision,
            )
            .then(refreshAuthoritativeInbox)
            .catch(() => undefined);
          return true;
        }
        void store
          .dispatch(
            notificationsApi.endpoints.readConversationNotifications.initiate(
              {
                companyId: targetCompanyId,
                conversationId: presentation.conversationId,
              },
              { track: false },
            ),
          )
          .unwrap()
          .catch(() => undefined);
        return true;
      };

      const openNotification = () => {
        void activateAccountNotification({
          userId,
          notificationId: presentation.notificationId,
        }).then((path) => {
          if (path && readSessionSnapshot().userId === userId)
            navigateRef.current(path);
        });
      };

      const showInAppNotification = () => {
        if (
          presentation.kind === "calendar_reminder" ||
          presentation.kind === "calendar_update"
        )
          return;
        toast(title, {
          action: {
            label: "Open",
            onClick: openNotification,
          },
          description: body,
        });
      };
      if (!isDesktop) {
        if (!document.hasFocus() || !acknowledgeVisibleConversation()) {
          showInAppNotification();
        }
        return;
      }

      // Electron main owns the authoritative native-window focus check.
      // DOM focus can remain true after the OS has moved focus to another app.
      const preferences = loadDesktopWorkbenchPreferences();
      const desktopKind = toDesktopNotificationKind(presentation.kind);
      const allowNative = isDesktopNotificationKindEnabled(
        preferences,
        desktopKind,
      );
      let nativeCopy = { title, body };
      if (
        allowNative &&
        presentation.kind === "discussion_mention" &&
        targetCompanyId &&
        presentation.conversationId &&
        presentation.messageId &&
        presentation.discussionType
      ) {
        const request = store.dispatch(
          notificationsApi.endpoints.getDiscussionNotificationPreview.initiate(
            {
              companyId: targetCompanyId,
              companyName,
              userId,
              itemId: presentation.notificationId,
              conversationId: presentation.conversationId,
              messageId: presentation.messageId,
              discussionType: presentation.discussionType,
            },
            { subscribe: false, forceRefetch: true },
          ),
        );
        const pendingPreview = {
          abort: request.abort,
          discussion: {
            companyId: targetCompanyId,
            conversationId: presentation.conversationId,
            revision: 0,
          },
        };
        pendingPreviews.add(pendingPreview);
        try {
          const preview = await request.unwrap();
          // A change hint can arrive after one of the parallel reads completes.
          if (
            !preview ||
            preview.conversationRevision < pendingPreview.discussion.revision
          )
            return;
          nativeCopy = preview;
        } catch (error) {
          if (!isRetryableBoardlyQueryError(error)) {
            refreshAuthoritativeInbox();
            return;
          }
          // A network failure can still show the bodyless receipt, without
          // taking a private channel name from a potentially stale cache.
          nativeCopy = { title, body: presentation.body };
        } finally {
          pendingPreviews.delete(pendingPreview);
          request.unsubscribe();
        }
      }
      if (
        allowNative &&
        presentation.kind === "huddle_missed" &&
        targetCompanyId &&
        presentation.invitationId
      ) {
        const request = store.dispatch(
          notificationsApi.endpoints.getHuddleNotificationPreview.initiate(
            {
              companyId: targetCompanyId,
              companyName,
              userId,
              itemId: presentation.notificationId,
              invitationId: presentation.invitationId,
            },
            { subscribe: false, forceRefetch: true },
          ),
        );
        pendingPreviews.add(request);
        try {
          const preview = await request.unwrap();
          if (preview) nativeCopy = preview;
        } catch (error) {
          if (!isRetryableBoardlyQueryError(error)) {
            refreshAuthoritativeInbox();
            return;
          }
          // The durable receipt names the caller without quoting a source chat.
        } finally {
          pendingPreviews.delete(request);
          request.unsubscribe();
        }
      }
      if (
        disposed ||
        readSessionSnapshot().userId !== userId ||
        ((presentation.kind === "discussion_mention" ||
          presentation.kind === "huddle_missed") &&
          accessAtStart !== accessRevision) ||
        Date.now() - Date.parse(event.createdAt) > 60_000
      )
        return;
      const visible = store.getState().conversationDock.visibleConversation;
      void showDesktopNotification({
        suppressWhenFocused:
          presentation.kind !== "discussion_mention" ||
          (document.visibilityState === "visible" &&
            visible?.discussion === true &&
            visible.companyId === targetCompanyId &&
            visible.conversationId === presentation.conversationId),
        allowNative,
        body: nativeCopy.body,
        id: presentation.notificationId,
        kind: desktopKind,
        path: sanitizeNotificationPath(
          presentation.deepLink,
          presentation.kind,
        ),
        title: nativeCopy.title,
        companyId: targetCompanyId ?? undefined,
        userId: session.userId,
      })
        .then((result) => {
          if (disposed) return;
          if (!result.ok) {
            reportDesktopDiagnostic({
              kind: "desktop-notification-skipped",
              message: result.reason,
              source: "notification-realtime-controller",
            });
            showInAppNotification();
            return;
          }
          if (result.disposition === "suppressed-focused") {
            if (!acknowledgeVisibleConversation()) showInAppNotification();
          }
        })
        .catch(() => {
          if (disposed) return;
          reportDesktopDiagnostic({
            kind: "desktop-notification-skipped",
            message: "The native notification bridge did not respond.",
            source: "notification-realtime-controller",
          });
          showInAppNotification();
        });
    };

    const unsubscribeEvent = boardlyRealtimeClient.onEvent((event) => {
      if (event.type === "conversation.discussion_changed") {
        const conversationId = realtimeEventConversationId(event);
        const revision = event.payload.revision;
        if (typeof revision === "number") {
          pendingPreviews.forEach(({ discussion }) => {
            if (
              discussion &&
              discussion.companyId === event.companyId &&
              discussion.conversationId === conversationId
            ) {
              discussion.revision = Math.max(discussion.revision, revision);
            }
          });
        }
      }
      if (
        event.type === "conversation.discussion_access_revoked" ||
        event.type === "auth.company_access_changed" ||
        event.type === "auth.company_access_revoked" ||
        event.type === "project.access_changed" ||
        event.type === "artifact.access_changed"
      ) {
        accessRevision += 1;
        pendingPreviews.forEach((request) => request.abort());
      }
      if (event.type === "notification.issued") {
        void handlePresentation(event);
      }
      if (
        event.type === "auth.company_access_changed" ||
        event.type === "auth.company_access_revoked"
      ) {
        store.dispatch(
          notificationsApi.util.invalidateTags(["Inbox", "Company"]),
        );
      }
    });
    // RealtimeSyncController owns initial and recovery cache convergence.
    // Presentation subscriptions must survive navigation without refetching.

    return () => {
      disposed = true;
      pendingPreviews.forEach((request) => request.abort());
      unsubscribeEvent();
    };
  }, [isDesktop, userId, userNamespace]);

  useEffect(
    () => () => {
      if (isDesktop) {
        void setDesktopWorkbenchStatus({ unreadCount: 0 });
      }
    },
    [isDesktop],
  );

  return null;
}

function parseNotificationPresentation(
  event: BoardlyRealtimeEvent,
): NotificationPresentation | null {
  if (event.type !== "notification.issued") {
    return null;
  }
  const notificationId = event.payload?.notificationId;
  const kind = event.payload?.kind;
  const title = event.payload?.title;
  const body = event.payload?.body;
  const deepLink = event.payload?.deepLink;
  if (
    typeof notificationId !== "string" ||
    !isNotificationKind(kind) ||
    typeof title !== "string" ||
    typeof body !== "string" ||
    (deepLink !== undefined && typeof deepLink !== "string")
  ) {
    return null;
  }
  const params = new URL(
    sanitizeNotificationPath(deepLink, kind),
    "https://boardly.internal",
  ).searchParams;
  const conversationId =
    kind === "run_completed" ||
    kind === "run_failed" ||
    kind === "discussion_mention"
      ? params.get("conversationId")
      : null;
  const discussionType = params.get("discussionType");
  return {
    body,
    ...(kind === "huddle_missed"
      ? { invitationId: params.get("invitationId") ?? undefined }
      : {}),
    ...(kind === "discussion_mention" && typeof deepLink === "string"
      ? {
          threadRootId: params.get("threadRootId") ?? undefined,
          messageId: params.get("messageId") ?? undefined,
          ...(discussionType === "channel" || discussionType === "human_direct"
            ? { discussionType }
            : {}),
        }
      : {}),
    ...(conversationId ? { conversationId } : {}),
    ...(typeof deepLink === "string" ? { deepLink } : {}),
    kind,
    notificationId,
    title,
  };
}

function readRecentPresentationIds(storageKey?: string) {
  if (!storageKey || typeof window === "undefined") {
    return new Set<string>();
  }
  try {
    const parsed = JSON.parse(window.localStorage.getItem(storageKey) ?? "[]");
    return new Set<string>(
      Array.isArray(parsed)
        ? parsed.filter((value): value is string => typeof value === "string")
        : [],
    );
  } catch {
    return new Set<string>();
  }
}

function writeRecentPresentationIds(storageKey: string, ids: Set<string>) {
  try {
    window.localStorage.setItem(storageKey, JSON.stringify([...ids]));
  } catch {
    // Presentation history is a best-effort replay guard.
  }
}

function pruneRecentPresentationIds(ids: Set<string>) {
  while (ids.size > MAX_RECENT_PRESENTATION_IDS) {
    const oldest = ids.values().next().value;
    if (typeof oldest !== "string") {
      break;
    }
    ids.delete(oldest);
  }
}
