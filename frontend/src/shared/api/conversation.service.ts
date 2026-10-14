import type { operations as ApiOperations } from "@/shared/api/generated/api.generated";
import { apiClient } from "@/lib/api-client";
import { toResourceAttachmentSnapshot } from "@/shared/api/attachment.service";
import { withConversationAgentBindingDefaults } from "@/shared/api/agent-binding-defaults";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type { TaskWorkspaceResponse } from "@/shared/api/task.service";
import type {
  ConversationHistoryWindow,
  ConversationTranscriptListOptions,
  ConversationTranscriptPageDto,
  ConversationMessageListOptions,
  ConversationMessageContextPage,
  ConversationMessagePage,
  ConversationStateDto,
  ConversationStateUpdateRequest,
  CreateDirectConversationRequest,
  RenameDirectConversationRequest,
  DirectConversationDto,
  DirectConversationPage,
  ConversationDisplayItemSummary,
  ConversationMessageView,
  ConversationRunQueueOrderRequest,
  ConversationRunQueueProjection,
  DirectConversationDetail,
  SendConversationMessageRequest,
  SendConversationMessageResponse,
} from "@/shared/api/types";

export const CONVERSATION_HISTORY_PAGE_SIZE = 50;

class ConversationService {
  async listMessages(
    conversationId: string,
  ): Promise<ConversationDisplayItemSummary[]> {
    return (await this.loadLatestMessages(conversationId)).items;
  }

  async loadMessageContext(
    conversationId: string,
    messageId: string,
    limit = CONVERSATION_HISTORY_PAGE_SIZE,
    signal?: AbortSignal,
  ): Promise<ConversationMessageContextPage> {
    return apiClient.get<ConversationMessageContextPage>(
      `/conversations/${encodeURIComponent(conversationId)}/messages/${encodeURIComponent(messageId)}/context`,
      { params: { limit }, signal },
    );
  }

  displayItemsFromMessageViews(
    items: readonly ConversationMessageView[],
  ): ConversationDisplayItemSummary[] {
    return items.map(({ attachments, attachmentsWithheld, ...message }) => ({
      content: message.content,
      skillReferences: message.skillReferences,
      created_at: message.createdAt,
      event_id: message.id,
      event_name:
        message.author.kind === "user" ? "user_message" : "agent_message",
      item_id: message.id,
      payload: {
        attachments: attachments.map(toResourceAttachmentSnapshot),
        // A Project file this viewer may not open was left out (ADR 0017).
        attachments_withheld: attachmentsWithheld,
        author: message.author,
        run_id: message.runId,
      },
      role: message.author.kind === "user" ? "user" : "assistant",
      sequence: message.seq,
    }));
  }

  async listTaskDisplayItems(
    taskId: string,
    conversationId: string,
  ): Promise<ConversationDisplayItemSummary[]> {
    return (await this.loadLatestTaskMessages(taskId, conversationId)).items;
  }

  // The newest page is a COUNT of messages, so it has to be taken off the end
  // of the timeline rather than carved out of a seq range: one conversation
  // stream numbers a run's reasoning and tool items too, so no range chosen in
  // advance holds a known number of messages, and `(throughSeq - limit,
  // throughSeq]` is empty whenever the newest run was busy. The two composite
  // reads below are the ones that page backwards; the cursored `/messages`
  // list stays the forward `after_seq` catch-up path.
  async loadLatestMessages(
    conversationId: string,
    limit = CONVERSATION_HISTORY_PAGE_SIZE,
  ): Promise<ConversationHistoryWindow> {
    const detail = await apiClient.get<DirectConversationDetail>(
      `/companies/${encodeURIComponent(requireActiveCompanyId())}/conversations/${encodeURIComponent(conversationId)}`,
      { params: { limit } },
    );
    return this.toHistoryWindow({
      earliestAvailableSeq: detail.earliestAvailableSeq,
      items: detail.messages,
      throughSeq: detail.throughSeq,
    });
  }

  async loadLatestTaskMessages(
    taskId: string,
    _conversationId: string,
    limit = CONVERSATION_HISTORY_PAGE_SIZE,
  ): Promise<ConversationHistoryWindow> {
    const workspace = await apiClient.get<TaskWorkspaceResponse>(
      `/companies/${encodeURIComponent(requireActiveCompanyId())}/tasks/${encodeURIComponent(taskId)}/workspace`,
      { params: { message_limit: limit } },
    );
    return this.toHistoryWindow({
      earliestAvailableSeq: workspace.earliestAvailableSeq,
      items: workspace.messages,
      throughSeq: workspace.throughSeq,
    });
  }

  async loadTaskMessagesAfter(
    taskId: string,
    afterSeq: number,
  ): Promise<ConversationHistoryWindow | null> {
    const items: ConversationMessageView[] = [];
    let throughSeq: number | undefined;
    let cursor = afterSeq;
    while (true) {
      const page = await this.listTaskMessages(taskId, {
        afterSeq: cursor,
        throughSeq,
        limit: CONVERSATION_HISTORY_PAGE_SIZE,
      });
      if (
        page.throughSeq < afterSeq ||
        page.earliestAvailableSeq > afterSeq + 1
      ) {
        return null;
      }
      // Freeze the first read's head so a busy Run cannot extend this replay.
      throughSeq ??= page.throughSeq;
      items.push(...page.items);
      const lastSeq = page.items.at(-1)?.seq;
      if (
        page.items.length < CONVERSATION_HISTORY_PAGE_SIZE ||
        lastSeq === throughSeq
      ) {
        return this.toHistoryWindow({ ...page, items, throughSeq });
      }
      if (lastSeq === undefined || lastSeq <= cursor) {
        throw new Error("Conversation message pagination did not advance");
      }
      cursor = lastSeq;
    }
  }

  async loadMessagesBefore(
    conversationId: string,
    beforeSeq: number,
    earliestAvailableSeq: number,
    limit = CONVERSATION_HISTORY_PAGE_SIZE,
  ): Promise<ConversationHistoryWindow> {
    return this.loadHistoryBefore(
      `/conversations/${encodeURIComponent(conversationId)}/messages`,
      beforeSeq,
      earliestAvailableSeq,
      limit,
    );
  }

  async loadTaskMessagesBefore(
    taskId: string,
    beforeSeq: number,
    earliestAvailableSeq: number,
    limit = CONVERSATION_HISTORY_PAGE_SIZE,
  ): Promise<ConversationHistoryWindow> {
    return this.loadHistoryBefore(
      `/tasks/${encodeURIComponent(taskId)}/messages`,
      beforeSeq,
      earliestAvailableSeq,
      limit,
    );
  }

  private async loadHistoryBefore(
    path: string,
    beforeSeq: number,
    earliestAvailableSeq: number,
    limit: number,
  ): Promise<ConversationHistoryWindow> {
    const throughSeq = beforeSeq - 1;
    if (throughSeq < earliestAvailableSeq) {
      return {
        earliestAvailableSeq,
        earliestLoadedSeq: null,
        hasOlder: false,
        items: [],
        throughSeq,
      };
    }

    // A bound with no floor asks the server for the newest `limit` messages at
    // or below it. Computing a floor here is what broke this read: invisible
    // run detail shares the sequence, so `throughSeq - limit` can span a
    // stretch holding no messages at all and the page comes back empty while
    // older messages still exist.
    const page = await this.listMessagePage(path, { limit, throughSeq });
    return this.toHistoryWindow(page);
  }

  private async listMessagePage(
    path: string,
    options: ConversationMessageListOptions,
  ): Promise<ConversationMessagePage> {
    return apiClient.get<ConversationMessagePage>(path, {
      params: {
        after_seq: options.afterSeq,
        limit: options.limit,
        through_seq: options.throughSeq,
      },
    });
  }

  private toHistoryWindow(
    page: Pick<
      ConversationMessagePage,
      "earliestAvailableSeq" | "items" | "throughSeq"
    >,
  ): ConversationHistoryWindow {
    const items = this.toDisplayItems(page);
    const earliestLoadedSeq = items[0]?.sequence ?? null;
    return {
      earliestAvailableSeq: page.earliestAvailableSeq,
      earliestLoadedSeq,
      hasOlder:
        earliestLoadedSeq !== null &&
        earliestLoadedSeq > page.earliestAvailableSeq,
      items,
      throughSeq: page.throughSeq,
    };
  }

  private toDisplayItems(
    page: Pick<ConversationMessagePage, "items">,
  ): ConversationDisplayItemSummary[] {
    return this.displayItemsFromMessageViews(page.items);
  }

  async listDirectConversations(
    companyId: string,
    includeArchived = false,
  ): Promise<DirectConversationPage> {
    const page = await apiClient.get<DirectConversationPage>(
      `/companies/${encodeURIComponent(companyId)}/conversations`,
      { params: includeArchived ? { include_archived: true } : undefined },
    );
    return {
      ...page,
      items: page.items.map(withConversationAgentBindingDefaults),
    };
  }

  async listStates(
    companyId: string,
  ): Promise<
    ApiOperations["listConversationStates"]["responses"][200]["content"]["application/json"]
  > {
    return apiClient.get(
      `/companies/${encodeURIComponent(companyId)}/conversations/states`,
    );
  }

  async listPins(
    companyId: string,
  ): Promise<
    ApiOperations["listConversationPins"]["responses"][200]["content"]["application/json"]
  > {
    return apiClient.get(
      `/companies/${encodeURIComponent(companyId)}/conversations/pins`,
    );
  }

  async createDirectConversation(
    companyId: string,
    payload: CreateDirectConversationRequest,
  ): Promise<DirectConversationDto> {
    return apiClient.post<DirectConversationDto>(
      `/companies/${encodeURIComponent(companyId)}/conversations`,
      payload,
    );
  }

  async renameDirectConversation(
    companyId: string,
    conversationId: string,
    payload: RenameDirectConversationRequest,
  ): Promise<DirectConversationDto> {
    return apiClient.patch<DirectConversationDto>(
      `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}`,
      payload,
    );
  }

  async getTranscript(
    companyId: string,
    conversationId: string,
    options: ConversationTranscriptListOptions = {},
  ): Promise<ConversationTranscriptPageDto> {
    return apiClient.get<ConversationTranscriptPageDto>(
      `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}/transcript`,
      {
        params: {
          ...(options.afterSeq === undefined
            ? {}
            : { after_seq: options.afterSeq }),
          ...(options.limit === undefined ? {} : { limit: options.limit }),
        },
      },
    );
  }

  async postMessage(
    conversationId: string,
    payload: SendConversationMessageRequest,
  ): Promise<SendConversationMessageResponse> {
    return apiClient.post<SendConversationMessageResponse>(
      `/conversations/${encodeURIComponent(conversationId)}/messages`,
      payload,
    );
  }

  async listTaskMessages(
    taskId: string,
    options: ConversationMessageListOptions = {},
  ): Promise<ConversationMessagePage> {
    return this.listMessagePage(
      `/tasks/${encodeURIComponent(taskId)}/messages`,
      options,
    );
  }

  async postTaskMessage(
    taskId: string,
    payload: SendConversationMessageRequest,
  ): Promise<SendConversationMessageResponse> {
    return apiClient.post<SendConversationMessageResponse>(
      `/tasks/${encodeURIComponent(taskId)}/messages`,
      payload,
    );
  }

  async getRunQueue(
    conversationId: string,
    signal?: AbortSignal,
  ): Promise<ConversationRunQueueProjection> {
    return apiClient.get<ConversationRunQueueProjection>(
      `/conversations/${encodeURIComponent(conversationId)}/run-queue`,
      { signal },
    );
  }

  async reorderRunQueue(
    conversationId: string,
    payload: ConversationRunQueueOrderRequest,
  ): Promise<ConversationRunQueueProjection> {
    return apiClient.put<ConversationRunQueueProjection>(
      `/conversations/${encodeURIComponent(conversationId)}/run-queue/order`,
      payload,
    );
  }

  async deleteRunQueueItem(
    conversationId: string,
    queueItemId: string,
    expectedRevision: number,
  ): Promise<ConversationRunQueueProjection> {
    return apiClient.delete<ConversationRunQueueProjection>(
      `/conversations/${encodeURIComponent(conversationId)}/run-queue/${encodeURIComponent(queueItemId)}`,
      { data: { expectedRevision } },
    );
  }

  async getConversationState(
    conversationId: string,
  ): Promise<ConversationStateDto> {
    return apiClient.get<ConversationStateDto>(
      `/conversations/${encodeURIComponent(conversationId)}/state`,
    );
  }

  async updateConversationState(
    conversationId: string,
    payload: ConversationStateUpdateRequest,
  ): Promise<ConversationStateDto> {
    return apiClient.patch<ConversationStateDto>(
      `/conversations/${encodeURIComponent(conversationId)}/state`,
      payload,
    );
  }
}

export const conversationService = new ConversationService();
