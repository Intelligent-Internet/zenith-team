import { mailboxService } from "@/features/mailbox/api/mailbox.service";
import type {
  MailboxAttachmentDownload,
  MailboxMessage,
  MailboxMessageDetail,
  MailboxMessagePage,
  MailboxState,
} from "@/features/mailbox/model/mailbox";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";

type CompanyMailboxArgument = { companyId: string };
type MessageArgument = CompanyMailboxArgument & { messageId: string };
type ListMessagesArgument = CompanyMailboxArgument & {
  cursor?: string;
  limit?: number;
};
type AttachmentArgument = MessageArgument & { attachmentId: string };

const stateTag = (companyId: string) => `${companyId}:state`;
const messagesTag = (companyId: string) => `${companyId}:messages`;
const messageTag = (companyId: string, messageId: string) =>
  `${companyId}:message:${messageId}`;

export function mailboxInvalidationTags(
  companyId: string,
  messageIds: readonly string[] = [],
) {
  return [
    { id: stateTag(companyId), type: "Mailbox" as const },
    { id: messagesTag(companyId), type: "Mailbox" as const },
    ...[...new Set(messageIds)].map((messageId) => ({
      id: messageTag(companyId, messageId),
      type: "Mailbox" as const,
    })),
  ];
}

export const mailboxApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getMailboxState: builder.query<MailboxState, string>({
      providesTags: (_result, _error, companyId) => [
        { id: stateTag(companyId), type: "Mailbox" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => mailboxService.getState(companyId),
          "Mailbox could not be loaded.",
          { retry: true },
        ),
    }),
    createMailbox: builder.mutation<
      MailboxState,
      CompanyMailboxArgument & { localPart: string }
    >({
      invalidatesTags: (result, error, { companyId }) => {
        if (error?.code === "mailbox_already_configured") {
          return mailboxInvalidationTags(companyId);
        }
        return result ? [{ id: messagesTag(companyId), type: "Mailbox" }] : [];
      },
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            mailboxApi.util.upsertQueryData("getMailboxState", companyId, data),
          );
        } catch {
          // Existing mailboxes are invalidated above; creation errors stay on the form.
        }
      },
      queryFn: ({ companyId, localPart }) =>
        executeBoardlyQuery(
          () => mailboxService.create(companyId, localPart),
          "Mailbox could not be created.",
        ),
    }),
    listMailboxMessages: builder.query<
      MailboxMessagePage,
      ListMessagesArgument
    >({
      providesTags: (result, _error, { companyId }) => [
        { id: messagesTag(companyId), type: "Mailbox" },
        ...(result?.messages.map((message) => ({
          id: messageTag(companyId, message.id),
          type: "Mailbox" as const,
        })) ?? []),
      ],
      queryFn: ({ companyId, ...options }) =>
        executeBoardlyQuery(
          () => mailboxService.listMessages(companyId, options),
          "Mailbox messages could not be loaded.",
          { retry: true },
        ),
    }),
    getMailboxMessage: builder.query<MailboxMessageDetail, MessageArgument>({
      providesTags: (_result, _error, { companyId, messageId }) => [
        { id: messageTag(companyId, messageId), type: "Mailbox" },
      ],
      queryFn: ({ companyId, messageId }) =>
        executeBoardlyQuery(
          () => mailboxService.getMessage(companyId, messageId),
          "Mailbox message could not be loaded.",
        ),
    }),
    markMailboxMessageRead: builder.mutation<MailboxMessage, MessageArgument>({
      invalidatesTags: (result, error, { companyId, messageId }) =>
        result && !error ? mailboxInvalidationTags(companyId, [messageId]) : [],
      queryFn: ({ companyId, messageId }) =>
        executeBoardlyQuery(
          () => mailboxService.markRead(companyId, messageId),
          "Mailbox message could not be marked as read.",
        ),
    }),
    downloadMailboxAttachment: builder.mutation<
      MailboxAttachmentDownload,
      AttachmentArgument
    >({
      queryFn: ({ attachmentId, companyId, messageId }) =>
        executeBoardlyQuery(
          () =>
            mailboxService.getAttachment(companyId, messageId, attachmentId),
          "Mailbox attachment could not be opened.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const {
  useCreateMailboxMutation,
  useDownloadMailboxAttachmentMutation,
  useGetMailboxMessageQuery,
  useGetMailboxStateQuery,
  useLazyListMailboxMessagesQuery,
  useListMailboxMessagesQuery,
  useMarkMailboxMessageReadMutation,
} = mailboxApi;
