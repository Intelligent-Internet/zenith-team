import type { components as ApiComponents } from "@/shared/api/generated/api.generated";
import { activityDateKey } from "@/shared/api/activity.service";
import { activityToActivityEvent } from "@/shared/api/adapters";
import { conversationService } from "@/shared/api/conversation.service";
import type { ConversationDisplayItemSummary } from "@/shared/api/types";
import { workspaceApi } from "@/shared/api/workspace-api";
import type { BoardlyRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import type { RealtimeSyncContext } from "@/app/shell/realtime-sync/realtime-sync-context";
import { boardlyApi } from "@/shared/api/boardly-api";
import { realtimeEventConversationId } from "@/shared/realtime/realtime-client.service";

type ActivityEntry = ApiComponents["schemas"]["ActivityEntry"];
type Attachment = ApiComponents["schemas"]["Attachment"];
type MessageCreatedPayload = ApiComponents["schemas"]["MessageCreatedPayload"];
type MessageUpdatedPayload = ApiComponents["schemas"]["MessageUpdatedPayload"];

export function applyConversationCacheEvent(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
) {
  switch (event.type) {
    case "conversation.discussion_changed": {
      const conversationId = realtimeEventConversationId(event);
      if (conversationId) {
        context.dispatch(
          boardlyApi.util.invalidateTags([
            { type: "Artifact", id: `${context.companyId}:${conversationId}` },
          ]),
        );
      }
      return true;
    }
    case "conversation.created":
    case "conversation.updated":
      invalidateConversationProjection(context, event);
      context.requestRefresh("directConversations", "taskWorkspaces");
      return true;
    case "conversation.read":
      context.requestRefresh("directConversations");
      return true;
    case "message.created": {
      const message = event.payload as MessageCreatedPayload;
      // A Project file on the message follows the Project's readers, so the
      // frame carried no files at all: refetch this viewer's own read, whose
      // attachmentsWithheld says whether a file was left out for them.
      if (message.attachmentsWithheld) {
        refetchMessages(context, message.conversationId);
      } else updateMessage(context, message);
      context.requestRefresh("taskWorkspaces");
      return true;
    }
    case "message.updated": {
      // A file on the message joined or left a Project; the refetched read
      // shows the files this viewer may open and flags any left out.
      const { conversationId } = event.payload as MessageUpdatedPayload;
      refetchMessages(context, conversationId);
      return true;
    }
    case "activity.created":
      updateActivity(context, event.payload as ActivityEntry);
      return true;
    case "attachment.completed":
      updateAttachment(context, event.payload as Attachment);
      context.requestRefresh("taskWorkspaces");
      return true;
    case "attachment.deleted":
      removeAttachment(
        context,
        event.payload as {
          attachmentId: string;
          conversationId?: string;
          messageId?: string;
          revision: number;
        },
      );
      context.requestRefresh("taskWorkspaces");
      return true;
    default:
      return false;
  }
}

function invalidateConversationProjection(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
) {
  const conversationId = realtimeEventConversationId(event);
  if (!conversationId) return;
  context.dispatch(
    boardlyApi.util.invalidateTags([
      { id: conversationId, type: "Conversation" },
      { id: context.companyId, type: "TaskReport" },
    ]),
  );
}

function updateAttachment(
  context: RealtimeSyncContext,
  attachment: Attachment,
) {
  updateCachedAttachments(context, undefined, (attachments) => {
    const index = attachments.findIndex(
      (candidate) => candidate.id === attachment.id,
    );
    if (index !== -1 && attachments[index].revision <= attachment.revision) {
      attachments[index] = attachment;
    }
  });
}

function removeAttachment(
  context: RealtimeSyncContext,
  receipt: {
    attachmentId: string;
    conversationId?: string;
    messageId?: string;
    revision: number;
  },
) {
  updateCachedAttachments(
    context,
    receipt.conversationId,
    (attachments, message) => {
      if (receipt.messageId && message.item_id !== receipt.messageId) return;
      const index = attachments.findIndex(
        (candidate) => candidate.id === receipt.attachmentId,
      );
      if (index !== -1 && attachments[index].revision <= receipt.revision) {
        attachments.splice(index, 1);
      }
    },
  );
}

function updateCachedAttachments(
  context: RealtimeSyncContext,
  conversationId: string | undefined,
  update: (
    attachments: Attachment[],
    message: ConversationDisplayItemSummary,
  ) => void,
) {
  for (const argument of workspaceApi.util.selectCachedArgsForQuery(
    context.getState(),
    "getWorkspaceConversationHistory",
  )) {
    if (
      argument.companyId !== context.companyId ||
      (conversationId && argument.conversationId !== conversationId)
    )
      continue;
    context.dispatch(
      workspaceApi.util.updateQueryData(
        "getWorkspaceConversationHistory",
        argument,
        (history) => {
          for (const message of history.items) {
            const attachments = message.payload.attachments;
            if (Array.isArray(attachments)) {
              update(attachments as Attachment[], message);
            }
          }
        },
      ),
    );
  }
  for (const cachedConversationId of workspaceApi.util.selectCachedArgsForQuery(
    context.getState(),
    "listWorkspaceMessages",
  )) {
    if (conversationId && cachedConversationId !== conversationId) continue;
    context.dispatch(
      workspaceApi.util.updateQueryData(
        "listWorkspaceMessages",
        cachedConversationId,
        (items) => {
          if (!items) return;
          for (const message of items) {
            const attachments = message.payload.attachments;
            if (Array.isArray(attachments)) {
              update(attachments as Attachment[], message);
            }
          }
        },
      ),
    );
  }
}

function refetchMessages(context: RealtimeSyncContext, conversationId: string) {
  context.dispatch(
    boardlyApi.util.invalidateTags([{ id: conversationId, type: "Message" }]),
  );
}

function updateMessage(
  context: RealtimeSyncContext,
  message: MessageCreatedPayload,
) {
  const displayItem = conversationService.displayItemsFromMessageViews([
    message,
  ])[0];
  context.dispatch(
    workspaceApi.util.updateQueryData(
      "getWorkspaceConversationHistory",
      { companyId: context.companyId, conversationId: message.conversationId },
      (history) => {
        if (
          history.earliestLoadedSeq !== null &&
          displayItem.sequence < history.earliestLoadedSeq
        )
          return;
        upsertDisplayItem(history.items, displayItem);
        history.earliestLoadedSeq = history.items[0]?.sequence ?? null;
        // A frame proves this row, not every row before it. Only an HTTP
        // snapshot advances the history cursor used for reconciliation.
      },
    ),
  );
  context.dispatch(
    workspaceApi.util.updateQueryData(
      "listWorkspaceMessages",
      message.conversationId,
      (items) => {
        if (!items) return;
        upsertDisplayItem(items, displayItem);
      },
    ),
  );
}

function upsertDisplayItem(
  items: ConversationDisplayItemSummary[],
  displayItem: ConversationDisplayItemSummary,
) {
  const index = items.findIndex(
    (candidate) => candidate.event_id === displayItem.event_id,
  );
  if (index === -1) {
    items.push(displayItem);
    items.sort(
      (left, right) =>
        left.sequence - right.sequence ||
        left.event_id.localeCompare(right.event_id),
    );
  } else {
    items[index] = displayItem;
  }
}

function updateActivity(context: RealtimeSyncContext, activity: ActivityEntry) {
  const summary = activityToActivityEvent(activity);
  context.dispatch(
    workspaceApi.util.updateQueryData(
      "listWorkspaceActivity",
      context.companyId,
      (items) => {
        const activities = items;
        if (
          !activities.some(
            (candidate) => candidate.event_id === summary.event_id,
          )
        ) {
          activities.unshift(summary);
          activities.splice(100);
        }
      },
    ),
  );

  for (const request of workspaceApi.util.selectCachedArgsForQuery(
    context.getState(),
    "getWorkspaceActivityCalendar",
  )) {
    if (request.companyId !== context.companyId) continue;
    const createdAt = Date.parse(activity.createdAt);
    if (
      createdAt < Date.parse(request.startAt) ||
      createdAt >= Date.parse(request.endAt)
    ) {
      continue;
    }
    const dateKey = activityDateKey(activity.createdAt, request.timeZone);
    if (!dateKey) continue;
    context.dispatch(
      workspaceApi.util.updateQueryData(
        "getWorkspaceActivityCalendar",
        request,
        (calendar) => {
          calendar.countsByDate[dateKey] =
            (calendar.countsByDate[dateKey] ?? 0) + 1;
          calendar.syncedAt = activity.createdAt;
        },
      ),
    );
  }
}
