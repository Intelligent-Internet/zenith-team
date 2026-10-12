import { apiClient } from "@/lib/api-client";
import {
  type InboxCounts,
  type InboxItem,
  type InboxListOptions,
  type InboxMutationResult,
  type InboxPage,
  type InboxReadAllResult,
  type NotificationMutation,
} from "@/features/notifications/model/notification";

function inboxPath(companyId: string) {
  return companyId
    ? `/companies/${encodeURIComponent(companyId)}/inbox`
    : "/me/inbox";
}

class NotificationService {
  list(companyId: string, options: InboxListOptions = {}): Promise<InboxPage> {
    return apiClient.get<InboxPage>(inboxPath(companyId), {
      params: options,
    });
  }

  counts(companyId: string): Promise<InboxCounts> {
    return apiClient.get<InboxCounts>(`${inboxPath(companyId)}/counts`);
  }

  get(companyId: string, itemId: string): Promise<InboxItem> {
    return apiClient.get<InboxItem>(
      `${inboxPath(companyId)}/${encodeURIComponent(itemId)}`,
    );
  }

  mutate(
    companyId: string,
    itemId: string,
    mutation: NotificationMutation,
    expectedRevision: number,
  ): Promise<InboxMutationResult> {
    return apiClient.post<InboxMutationResult>(
      `${inboxPath(companyId)}/${encodeURIComponent(itemId)}/${mutation}`,
      { expectedRevision },
    );
  }

  readAll(companyId: string): Promise<InboxReadAllResult> {
    return apiClient.post<InboxReadAllResult>(
      `${inboxPath(companyId)}/read-all`,
      {},
    );
  }

  readConversation(
    companyId: string,
    conversationId: string,
  ): Promise<InboxReadAllResult> {
    return apiClient.post<InboxReadAllResult>(`${inboxPath(companyId)}/read`, {
      conversationId,
    });
  }
}

export const notificationService = new NotificationService();
