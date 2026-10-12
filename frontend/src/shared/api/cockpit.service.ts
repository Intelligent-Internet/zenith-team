import { apiClient } from "@/lib/api-client";
import { readSessionSnapshot } from "@/app/session/session";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import { hasPermission } from "@/shared/permissions";
import { taskToTaskSummary, agentToAgentSummary } from "@/shared/api/adapters";
import { attachmentService } from "@/shared/api/attachment.service";
import type { FileTransferHandler } from "@/shared/chat/attachment-preview";
import { companyService } from "@/shared/api/company.service";
import { conversationService } from "@/shared/api/conversation.service";
import { taskService } from "@/shared/api/task.service";
import type {
  AgentDto,
  CockpitResponse,
  ConversationSummary,
  ResourceAttachmentSnapshot,
  ResourceSummary,
  ResourceUploadResponse,
  TaskSummary,
} from "@/shared/api/types";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";
import type { operations as ApiOperations } from "@/shared/api/generated/api.generated";

type CompanyBootstrapResponse =
  ApiOperations["getCompanyBootstrap"]["responses"][200]["content"]["application/json"];

type WorkReadQuery = {
  agentId?: string | null;
};

class CockpitService {
  async getCockpit(
    companyId = requireActiveCompanyId(),
  ): Promise<CockpitResponse> {
    const activeCompany = readSessionSnapshot().company;
    if (
      activeCompany?.id === companyId &&
      hasPermission(activeCompany, "agents.manage")
    ) {
      return this.getOwnerCockpit(companyId);
    }
    return this.getLegacyCockpit(companyId);
  }

  private async getOwnerCockpit(companyId: string): Promise<CockpitResponse> {
    const bootstrap = await apiClient.get<CompanyBootstrapResponse>(
      `/companies/${encodeURIComponent(companyId)}/bootstrap`,
    );
    const ceo = requireCeo(bootstrap.ceoAgent ? [bootstrap.ceoAgent] : []);
    return {
      ceo_agent: agentToAgentSummary(ceo),
      company_id: bootstrap.company.id,
      counters: {
        active_tasks: bootstrap.activeTaskCount,
        open_risks: 0,
        pending_approvals: 0,
      },
      credits: { balance: 0, version: bootstrap.company.revision },
      default_conversation: bootstrap.ceoConversationId
        ? {
            ...emptyConversationSummary(),
            conversation_id: bootstrap.ceoConversationId,
            subject_id: ceo.id,
            subject_type: "agent",
          }
        : emptyConversationSummary(),
    };
  }

  private async getLegacyCockpit(companyId: string): Promise<CockpitResponse> {
    const [company, agentPage, taskPage, conversations] = await Promise.all([
      companyService.getCompanyById(companyId),
      agentDirectoryService.listAgents(companyId),
      taskService.listTasks(companyId),
      conversationService.listDirectConversations(companyId),
    ]);
    const ceo = requireCeo(agentPage.items);
    const direct = conversations.items.find(
      (item) => item.agent?.id === ceo.id,
    );
    const activeTasks = taskPage.items.filter(
      (task) => task.status === "in_progress",
    ).length;
    return {
      ceo_agent: agentToAgentSummary(ceo),
      company_id: company.id,
      counters: {
        active_tasks: activeTasks,
        open_risks: 0,
        pending_approvals: 0,
      },
      credits: { balance: 0, version: company.revision },
      default_conversation: direct
        ? directConversationSummary(direct)
        : emptyConversationSummary(),
    };
  }

  async startBuilding(
    companyId = requireActiveCompanyId(),
  ): Promise<ConversationSummary> {
    const [agentPage, conversations] = await Promise.all([
      agentDirectoryService.listAgents(companyId),
      conversationService.listDirectConversations(companyId),
    ]);
    const ceo = requireCeo(agentPage.items);
    const existing = conversations.items.find(
      (item) => item.agent?.id === ceo.id,
    );
    if (existing) return directConversationSummary(existing);
    const created = await conversationService.createDirectConversation(
      companyId,
      {
        title: "CEO chat",
        agentId: ceo.id,
      },
    );
    return directConversationSummary(created);
  }

  async listTasks(query: WorkReadQuery = {}): Promise<TaskSummary[]> {
    const companyId = requireActiveCompanyId();
    const page = await taskService.listTasks(companyId, {
      ...(query.agentId
        ? { assignee_id: query.agentId, assignee_kind: "agent" as const }
        : {}),
    });
    return page.items.map(taskToTaskSummary);
  }

  async uploadResources({
    files,
    onFileTransfer,
    companyId = requireActiveCompanyId(),
  }: {
    clientMessageId?: string | null;
    conversationId: string;
    files: File[];
    onFileTransfer?: FileTransferHandler;
    companyId?: string;
  }): Promise<ResourceUploadResponse> {
    return attachmentService.uploadFiles(companyId, files, onFileTransfer);
  }

  async getResourceBlob(contentUrl: string): Promise<Blob> {
    return apiClient.get<Blob>(contentUrl, { responseType: "blob" });
  }

  resourceAttachmentSnapshot(
    resource: ResourceSummary,
  ): ResourceAttachmentSnapshot {
    return {
      asset_type: resourceAssetType(resource),
      content_type: resource.mime_type,
      created_at: resource.created_at,
      file_name: resource.title,
      file_size: resource.size_bytes,
      id: resource.resource_id,
      is_public: false,
      source:
        resource.source_type === "generated_image" ||
        resource.source_type === "tool_output"
          ? "generated"
          : resource.source_type === "user_upload"
            ? "user_upload"
            : "system",
      url: resource.content_url,
    };
  }
}

export const cockpitService = new CockpitService();

function requireCeo(agents: AgentDto[]) {
  const ceo = agents.find(
    (agent) => agentToAgentSummary(agent).agent_role === "ceo",
  );
  if (!ceo) throw new Error("CEO Agent is unavailable.");
  return ceo;
}

function directConversationSummary(conversation: {
  agent?: { id: string } | null;
  agentId?: string;
  createdAt: string;
  id: string;
  title: string | null;
  updatedAt: string;
}): ConversationSummary {
  return {
    conversation_id: conversation.id,
    created_at: conversation.createdAt,
    status: "open",
    subject_id: conversation.agentId ?? conversation.agent?.id ?? null,
    subject_type: "agent",
    title: conversation.title,
    updated_at: conversation.updatedAt,
  };
}

function emptyConversationSummary(): ConversationSummary {
  return {
    conversation_id: "",
    created_at: "",
    status: "open",
    subject_id: null,
    subject_type: null,
    title: null,
    updated_at: "",
  };
}

function resourceAssetType(
  resource: ResourceSummary,
): NonNullable<ResourceAttachmentSnapshot["asset_type"]> {
  if (resource.preview_kind === "image") return "image";
  if (resource.preview_kind === "video") return "video";
  if (resource.preview_kind === "audio") return "audio";
  if (resource.preview_kind === "code") return "code";
  if (
    resource.preview_kind === "document" ||
    resource.preview_kind === "pdf" ||
    resource.preview_kind === "presentation" ||
    resource.preview_kind === "spreadsheet" ||
    resource.preview_kind === "markdown" ||
    resource.preview_kind === "text"
  ) {
    return "document";
  }
  return "other";
}
