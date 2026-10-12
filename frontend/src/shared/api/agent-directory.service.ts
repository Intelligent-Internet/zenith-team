import { apiClient } from "@/lib/api-client";
import type {
  EntityRevisionRequest,
  AgentPage,
  AgentCreateRequest,
  AgentDto,
  AgentRuntimeUpdateRequest,
  AgentUpdateRequest,
} from "@/shared/api/types";
import { withAgentBindingDefaults } from "@/shared/api/agent-binding-defaults";

class AgentDirectoryService {
  async listAgents(
    companyId: string,
    options: {
      includeArchived?: boolean;
      reportsToAgentId?: string;
    } = {},
  ): Promise<AgentPage> {
    const page = await apiClient.get<AgentPage>(
      `/companies/${encodeURIComponent(companyId)}/agents`,
      {
        params: {
          include_archived: options.includeArchived,
          reports_to_agent_id: options.reportsToAgentId,
        },
      },
    );
    return {
      ...page,
      items: page.items.map(withAgentBindingDefaults),
    };
  }

  async getAgent(companyId: string, agentId: string): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.get<AgentDto>(
        `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}`,
      ),
    );
  }

  async updateAgent(
    companyId: string,
    agentId: string,
    payload: AgentRuntimeUpdateRequest | AgentUpdateRequest,
  ): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.patch<AgentDto>(
        `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}`,
        payload,
      ),
    );
  }

  async createAgent(
    companyId: string,
    payload: AgentCreateRequest,
  ): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.post<AgentDto>(
        `/companies/${encodeURIComponent(companyId)}/agents`,
        payload,
      ),
    );
  }

  async archiveAgent(
    companyId: string,
    agentId: string,
    payload: EntityRevisionRequest,
  ): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.post<AgentDto>(
        `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}/archive`,
        payload,
      ),
    );
  }

  async restoreAgent(
    companyId: string,
    agentId: string,
    payload: EntityRevisionRequest,
  ): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.post<AgentDto>(
        `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}/restore`,
        payload,
      ),
    );
  }
}

export const agentDirectoryService = new AgentDirectoryService();
