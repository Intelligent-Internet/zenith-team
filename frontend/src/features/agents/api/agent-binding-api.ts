import { agentBindingService } from "@/shared/api/agent-binding.service";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { AgentBindingPage, AgentDto } from "@/shared/api/types";

type CompanyArguments = { companyId: string };
type BindArguments = CompanyArguments & { agentId: string; runtimeId: string };
type UnbindArguments = CompanyArguments & { agentId: string };

// Setting an Agent up changes what the viewer can do with it everywhere: the
// roster row, the chat composer, the run button, and the plugins and
// connectors that are read off the runtime it now names (ADR 0014). The
// mutations therefore invalidate those four tags rather than patching each
// screen by hand.
export const agentBindingApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listMyAgentBindings: builder.query<
      AgentBindingPage,
      CompanyArguments & { runtimeId?: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "AgentBinding" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeBoardlyQuery(
          () => agentBindingService.listMyBindings(companyId, { runtimeId }),
          "The agents you have set up could not be loaded.",
        ),
    }),
    bindAgent: builder.mutation<AgentDto, BindArguments>({
      invalidatesTags: (_result, error, { companyId }) =>
        error
          ? []
          : [
              { id: companyId, type: "Agent" },
              { id: companyId, type: "AgentBinding" },
              { id: companyId, type: "Connector" },
              { id: companyId, type: "Plugin" },
              // A Task's readiness is its assignee's setup of its Agent.
              { id: companyId, type: "Task" },
            ],
      queryFn: ({ companyId, agentId, runtimeId }) =>
        executeBoardlyQuery(
          () => agentBindingService.bindAgent(companyId, agentId, runtimeId),
          "The agent could not be set up on that computer.",
        ),
    }),
    unbindAgent: builder.mutation<void, UnbindArguments>({
      invalidatesTags: (_result, error, { companyId }) =>
        error
          ? []
          : [
              { id: companyId, type: "Agent" },
              { id: companyId, type: "AgentBinding" },
              { id: companyId, type: "Connector" },
              { id: companyId, type: "Plugin" },
              // A Task's readiness is its assignee's setup of its Agent.
              { id: companyId, type: "Task" },
            ],
      queryFn: ({ companyId, agentId }) =>
        executeBoardlyQuery(
          () => agentBindingService.unbindAgent(companyId, agentId),
          "The agent could not be removed from your computer.",
        ),
    }),
  }),
});

export const {
  useListMyAgentBindingsQuery,
  useBindAgentMutation,
  useUnbindAgentMutation,
} = agentBindingApi;
