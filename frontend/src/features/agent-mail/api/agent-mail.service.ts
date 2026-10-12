import { apiClient } from "@/lib/api-client";
import type {
  AgentMailDraft,
  AgentMailDraftDelivery,
  AgentMailDraftGroup,
  AgentMailDraftVersion,
  UpdateAgentMailDraftInput,
} from "@/features/agent-mail/model/agent-mail";

type AgentMailDraftGroupsResponse = {
  groups: AgentMailDraftGroupResponse[];
};

type AgentMailDraftDeliveryResponse = Omit<
  AgentMailDraftDelivery,
  "cc" | "to"
> & {
  cc: string[] | null;
  to: string[] | null;
};

type AgentMailDraftResponse = Omit<AgentMailDraft, "deliveries"> & {
  deliveries: AgentMailDraftDeliveryResponse[];
};

type AgentMailDraftGroupResponse = Omit<
  AgentMailDraftGroup,
  "drafts" | "sourceRunId"
> & {
  drafts: AgentMailDraftResponse[];
  sourceRunId: string;
};

const MAX_SOURCE_RUN_IDS_PER_REQUEST = 100;

function conversationPath(companyId: string, conversationId: string): string {
  return `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}`;
}

function draftPath(
  companyId: string,
  conversationId: string,
  draftId: string,
): string {
  return `${conversationPath(companyId, conversationId)}/mail-drafts/${encodeURIComponent(draftId)}`;
}

class AgentMailService {
  async listGroups(
    companyId: string,
    conversationId: string,
    sourceRunIds: string[],
  ): Promise<AgentMailDraftGroup[]> {
    const uniqueSourceRunIds = [...new Set(sourceRunIds)];
    const groups: AgentMailDraftGroup[] = [];
    for (const sourceRunIdBatch of chunk(
      uniqueSourceRunIds,
      MAX_SOURCE_RUN_IDS_PER_REQUEST,
    )) {
      const search = new URLSearchParams({
        source_run_ids: sourceRunIdBatch.join(","),
      });
      const response = await apiClient.get<AgentMailDraftGroupsResponse>(
        `${conversationPath(companyId, conversationId)}/mail-draft-groups?${search.toString()}`,
      );
      groups.push(...response.groups.map(normalizeDraftGroup));
    }
    return groups;
  }

  async updateDraft(
    companyId: string,
    conversationId: string,
    draftId: string,
    input: UpdateAgentMailDraftInput,
  ): Promise<AgentMailDraft> {
    const draft = await apiClient.put<AgentMailDraftResponse>(
      draftPath(companyId, conversationId, draftId),
      input,
    );
    return normalizeDraft(draft);
  }

  deleteDraft(
    companyId: string,
    conversationId: string,
    draftId: string,
    expectedRevision: number,
  ): Promise<void> {
    return apiClient.delete<void>(
      draftPath(companyId, conversationId, draftId),
      { data: { expectedRevision } },
    );
  }

  async sendDraft(
    companyId: string,
    conversationId: string,
    draftId: string,
    expectedRevision: number,
  ): Promise<AgentMailDraft> {
    const draft = await apiClient.post<AgentMailDraftResponse>(
      `${draftPath(companyId, conversationId, draftId)}/send`,
      { expectedRevision },
    );
    return normalizeDraft(draft);
  }

  async sendGroup(
    companyId: string,
    conversationId: string,
    groupId: string,
    drafts: AgentMailDraftVersion[],
  ): Promise<AgentMailDraftGroup> {
    const group = await apiClient.post<AgentMailDraftGroupResponse>(
      `${conversationPath(companyId, conversationId)}/mail-draft-groups/${encodeURIComponent(groupId)}/send`,
      { drafts },
    );
    return normalizeDraftGroup(group);
  }
}

function chunk<T>(values: T[], size: number): T[][] {
  const result: T[][] = [];
  for (let index = 0; index < values.length; index += size) {
    result.push(values.slice(index, index + size));
  }
  return result;
}

function normalizeDraftGroup(
  group: AgentMailDraftGroupResponse,
): AgentMailDraftGroup {
  return {
    ...group,
    drafts: group.drafts.map(normalizeDraft),
    sourceRunId: String(group.sourceRunId),
  };
}

function normalizeDraft(draft: AgentMailDraftResponse): AgentMailDraft {
  return {
    ...draft,
    deliveries: draft.deliveries.map((delivery) => ({
      ...delivery,
      cc: Array.isArray(delivery.cc) ? delivery.cc : [],
      to: Array.isArray(delivery.to) ? delivery.to : [],
    })),
  };
}

export const agentMailService = new AgentMailService();
