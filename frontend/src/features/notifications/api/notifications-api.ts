import { notificationService } from "@/features/notifications/api/notification.service";
import { huddleNotificationCopy } from "@/features/notifications/lib/huddle-notification-copy";
import {
  discussionNotificationCopy,
  type DiscussionNotificationCopy,
} from "@/features/notifications/lib/discussion-notification-copy";
import { apiClient } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import type {
  InboxCounts,
  InboxItem,
  InboxListOptions,
  InboxMutationResult,
  InboxPage,
  InboxReadAllResult,
  NotificationMutation,
} from "@/features/notifications/model/notification";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";

type ListInboxArgument = InboxListOptions & { companyId: string };
type GetInboxItemArgument = { companyId: string; itemId: string };
type DiscussionPreviewArgument = GetInboxItemArgument & {
  userId: string;
  companyName: string;
  conversationId: string;
  messageId: string;
  discussionType: "channel" | "human_direct";
};
type HuddlePreviewArgument = GetInboxItemArgument & {
  userId: string;
  companyName: string;
  invitationId: string;
};
type ReadConversationNotificationsArgument = {
  companyId: string;
  conversationId: string;
};
type MutateInboxItemArgument = GetInboxItemArgument & {
  expectedRevision: number;
  mutation: NotificationMutation;
};

function listTagId(argument: ListInboxArgument) {
  return `${argument.companyId}:list:${argument.status ?? "active"}:${argument.kind ?? "any"}`;
}

export const notificationsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getHuddleNotificationPreview: builder.query<
      { title: string; body: string } | null,
      HuddlePreviewArgument
    >({
      keepUnusedDataFor: 0,
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(async () => {
          const history = await apiClient.get<
            components["schemas"]["HuddleInvitation"][]
          >(
            `/companies/${encodeURIComponent(arg.companyId)}/huddles/invitations/history`,
            { signal, timeout: 8_000 },
          );
          const invitation = history.find((row) => row.id === arg.invitationId);
          return invitation?.status === "expired"
            ? huddleNotificationCopy(invitation, arg.companyName, true)
            : null;
        }, "Huddle preview could not be loaded."),
    }),
    getDiscussionNotificationPreview: builder.query<
      (DiscussionNotificationCopy & { conversationRevision: number }) | null,
      DiscussionPreviewArgument
    >({
      // Each receipt/user has its own transient query. Never reuse chat caches
      // as permission to quote a message in an OS notification.
      keepUnusedDataFor: 0,
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(async () => {
          const companyPath = `/companies/${encodeURIComponent(arg.companyId)}`;
          const conversationId = encodeURIComponent(arg.conversationId);
          const [context, detail] = await Promise.all([
            apiClient.get<components["schemas"]["DiscussionContext"]>(
              `${companyPath}/conversations/${conversationId}/discussion-messages/${encodeURIComponent(arg.messageId)}/context`,
              { signal, timeout: 8_000 },
            ),
            apiClient.get<
              | components["schemas"]["ChannelDetail"]
              | components["schemas"]["HumanDirectDetail"]
            >(
              `${companyPath}/${arg.discussionType === "channel" ? "channels" : "human-conversations"}/${conversationId}`,
              { signal, timeout: 8_000 },
            ),
          ]);
          const message =
            context.items.find((item) => item.id === arg.messageId) ??
            (context.root?.id === arg.messageId ? context.root : null);
          if (!message || message.deletedAt || message.type !== "user_message")
            return null;
          return {
            ...discussionNotificationCopy(
              { conversation: detail.conversation, message },
              arg.companyName,
            ),
            // Both parallel reads must cover any change observed while loading.
            conversationRevision: Math.min(
              context.conversationRevision,
              detail.conversation.conversationRevision,
            ),
          };
        }, "Message preview could not be loaded."),
    }),
    listNotifications: builder.query<InboxPage, ListInboxArgument>({
      providesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "Inbox" },
        { id: listTagId(argument), type: "Inbox" },
      ],
      queryFn: ({ companyId, ...options }) =>
        executeZenithTeamQuery(
          () => notificationService.list(companyId, options),
          "Notifications could not be loaded.",
        ),
    }),
    getNotificationCounts: builder.query<InboxCounts, string>({
      providesTags: (_result, _error, companyId) => [
        { id: `${companyId}:counts`, type: "Inbox" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => notificationService.counts(companyId),
          "Notification count could not be loaded.",
        ),
    }),
    getNotification: builder.query<InboxItem, GetInboxItemArgument>({
      providesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "Inbox" },
        { id: `${argument.companyId}:item:${argument.itemId}`, type: "Inbox" },
      ],
      queryFn: ({ companyId, itemId }) =>
        executeZenithTeamQuery(
          () => notificationService.get(companyId, itemId),
          "Notification could not be loaded.",
        ),
    }),
    mutateNotification: builder.mutation<
      InboxMutationResult,
      MutateInboxItemArgument
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: "", type: "Inbox" },
        { id: ":counts", type: "Inbox" },
        { id: argument.companyId, type: "Inbox" },
        { id: `${argument.companyId}:counts`, type: "Inbox" },
        { id: `${argument.companyId}:item:${argument.itemId}`, type: "Inbox" },
      ],
      queryFn: ({ companyId, itemId, mutation, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            notificationService.mutate(
              companyId,
              itemId,
              mutation,
              expectedRevision,
            ),
          "Notification could not be updated.",
        ),
    }),
    readAllNotifications: builder.mutation<InboxReadAllResult, string>({
      invalidatesTags: () => ["Inbox"],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => notificationService.readAll(companyId),
          "Notifications could not be marked as read.",
        ),
    }),
    readConversationNotifications: builder.mutation<
      InboxReadAllResult,
      ReadConversationNotificationsArgument
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: "", type: "Inbox" },
        { id: ":counts", type: "Inbox" },
        { id: companyId, type: "Inbox" },
        { id: `${companyId}:counts`, type: "Inbox" },
      ],
      queryFn: ({ companyId, conversationId }) =>
        executeZenithTeamQuery(
          () => notificationService.readConversation(companyId, conversationId),
          "Chat notifications could not be marked as read.",
        ),
    }),
  }),
});

export const {
  useGetNotificationCountsQuery,
  useGetNotificationQuery,
  useLazyListNotificationsQuery,
  useListNotificationsQuery,
  useMutateNotificationMutation,
  useReadAllNotificationsMutation,
  useReadConversationNotificationsMutation,
} = notificationsApi;
