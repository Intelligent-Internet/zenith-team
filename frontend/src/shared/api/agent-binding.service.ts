import { apiClient } from "@/lib/api-client";
import { withAgentBindingDefaults } from "@/shared/api/agent-binding-defaults";
import type { AgentBindingPage, AgentDto } from "@/shared/api/types";

// A binding is one member's placement of an Agent on one of their own
// runtimes (ADR 0014). Every call here is about the caller's own setup: the
// server never accepts, or reveals, anybody else's.
class AgentBindingService {
  async listMyBindings(
    companyId: string,
    options: { runtimeId?: string } = {},
  ): Promise<AgentBindingPage> {
    return apiClient.get<AgentBindingPage>(
      `/companies/${encodeURIComponent(companyId)}/agent-bindings`,
      { params: { runtime_id: options.runtimeId } },
    );
  }

  // Sets the Agent up on the given runtime, or moves it there from another
  // of the caller's runtimes. Answers the Agent as the caller now sees it.
  async bindAgent(
    companyId: string,
    agentId: string,
    runtimeId: string,
  ): Promise<AgentDto> {
    return withAgentBindingDefaults(
      await apiClient.put<AgentDto>(bindingPath(companyId, agentId), {
        runtimeId,
      }),
    );
  }

  // Idempotent: an Agent the caller never set up is already unbound.
  async unbindAgent(companyId: string, agentId: string): Promise<void> {
    await apiClient.delete<void>(bindingPath(companyId, agentId));
  }
}

export const agentBindingService = new AgentBindingService();

function bindingPath(companyId: string, agentId: string) {
  return `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}/binding`;
}
