import { notificationsApi } from "@/features/notifications/api/notifications-api";
import type {
  InboxCounts,
  InboxItem,
  InboxListOptions,
} from "@/features/notifications/model/notification";
import type { ZenithTeamRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import {
  replaceVersioned,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";

// The item frames carry the recipient's refreshed counts beside the row; there
// is no counts-only event to listen for.
type InboxItemEventPayload = InboxItem & { counts: InboxCounts };

export function applyInboxCacheEvent(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  switch (event.type) {
    case "inbox.created":
    case "inbox.updated": {
      const { counts, ...item } = event.payload as InboxItemEventPayload;
      updateInboxItem(context, item);
      if (event.type === "inbox.created" && item.kind === "calendar_update") {
        context.dispatch(
          notificationsApi.util.invalidateTags([
            { type: "Calendar", id: "events" },
          ]),
        );
      }
      if (context.companyId) updateInboxCounts(context, counts);
      context.dispatch(
        notificationsApi.util.invalidateTags([
          { type: "Inbox", id: ":counts" },
        ]),
      );
      return true;
    }
    default:
      return false;
  }
}

function updateInboxItem(context: RealtimeSyncContext, item: InboxItem) {
  const state = context.getState();
  for (const argument of notificationsApi.util.selectCachedArgsForQuery(
    state,
    "listNotifications",
  )) {
    if (argument.companyId && argument.companyId !== context.companyId)
      continue;
    context.dispatch(
      notificationsApi.util.updateQueryData(
        "listNotifications",
        argument,
        (page) => {
          const index = page.items.findIndex(
            (candidate) => candidate.id === item.id,
          );
          if (!inboxItemMatches(item, argument)) {
            if (index !== -1 && page.items[index].revision <= item.revision) {
              page.items.splice(index, 1);
            }
            return;
          }
          if (index === -1 && argument.cursor) return;
          replaceVersioned(page.items, item);
          page.items.sort(
            (left, right) =>
              Date.parse(right.createdAt) - Date.parse(left.createdAt),
          );
          page.items.splice(argument.limit ?? 50);
        },
      ),
    );
  }

  for (const companyId of new Set([context.companyId, ""]))
    context.dispatch(
      notificationsApi.util.updateQueryData(
        "getNotification",
        { companyId, itemId: item.id },
        (current) => {
          if (current.revision <= item.revision) {
            Object.assign(current, item);
          }
        },
      ),
    );
}

function updateInboxCounts(context: RealtimeSyncContext, counts: InboxCounts) {
  context.dispatch(
    notificationsApi.util.updateQueryData(
      "getNotificationCounts",
      context.companyId,
      (current) => {
        if (current.countsVersion <= counts.countsVersion) {
          Object.assign(current, counts);
        }
      },
    ),
  );
}

function inboxItemMatches(item: InboxItem, options: InboxListOptions) {
  if (options.kind && item.kind !== options.kind) return false;
  switch (options.status ?? "active") {
    case "all":
      return true;
    case "archived":
      return item.archivedAt !== null;
    case "unread":
      return item.unread;
    case "active":
      return item.archivedAt === null;
  }
}
