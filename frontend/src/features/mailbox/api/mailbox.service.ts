import { apiClient } from "@/lib/api-client";
import type {
  MailboxAttachmentDownload,
  MailboxMessage,
  MailboxMessageDetail,
  MailboxMessagePage,
  MailboxState,
} from "@/features/mailbox/model/mailbox";

type MailboxMessageDetailResponse = Omit<
  MailboxMessageDetail,
  "attachments" | "bcc" | "cc" | "replyTo" | "to"
> & {
  attachments?: MailboxMessageDetail["attachments"] | null;
  bcc?: string[] | null;
  cc?: string[] | null;
  replyTo?: string[] | null;
  to?: string[] | null;
};

function mailboxPath(companyId: string) {
  return `/companies/${encodeURIComponent(companyId)}/mailbox`;
}

function messagePath(companyId: string, messageId: string) {
  return `${mailboxPath(companyId)}/messages/${encodeURIComponent(messageId)}`;
}

class MailboxService {
  getState(companyId: string): Promise<MailboxState> {
    return apiClient.get<MailboxState>(mailboxPath(companyId));
  }

  create(companyId: string, localPart: string): Promise<MailboxState> {
    return apiClient.post<MailboxState>(mailboxPath(companyId), { localPart });
  }

  listMessages(
    companyId: string,
    options: { cursor?: string; limit?: number } = {},
  ): Promise<MailboxMessagePage> {
    return apiClient.get<MailboxMessagePage>(
      `${mailboxPath(companyId)}/messages`,
      { params: options },
    );
  }

  async getMessage(
    companyId: string,
    messageId: string,
  ): Promise<MailboxMessageDetail> {
    const response = await apiClient.get<MailboxMessageDetailResponse>(
      messagePath(companyId, messageId),
    );
    return normalizeMailboxMessageDetail(response);
  }

  markRead(companyId: string, messageId: string): Promise<MailboxMessage> {
    return apiClient.post<MailboxMessage>(
      `${messagePath(companyId, messageId)}/read`,
      {},
    );
  }

  getAttachment(
    companyId: string,
    messageId: string,
    attachmentId: string,
  ): Promise<MailboxAttachmentDownload> {
    return apiClient.get<MailboxAttachmentDownload>(
      `${messagePath(companyId, messageId)}/attachments/${encodeURIComponent(attachmentId)}`,
    );
  }
}

function normalizeMailboxMessageDetail(
  response: MailboxMessageDetailResponse,
): MailboxMessageDetail {
  return {
    ...response,
    attachments: response.attachments ?? [],
    bcc: response.bcc ?? [],
    cc: response.cc ?? [],
    replyTo: response.replyTo ?? [],
    to: response.to ?? [],
  };
}

export const mailboxService = new MailboxService();
