import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type {
  EntityRevisionRequest,
  AgentCreateRequest,
  AgentDto,
  AgentUpdateRequest,
} from "@/shared/api/types";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";

export const agentApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    createWorkspaceAgent: builder.mutation<
      Awaited<ReturnType<typeof agentDirectoryService.createAgent>>,
      { companyId: string; request: AgentCreateRequest }
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "Agent" },
      ],
      queryFn: (argument) =>
        executeZenithTeamQuery(
          () =>
            agentDirectoryService.createAgent(
              argument.companyId,
              argument.request,
            ),
          "Agent could not be created.",
        ),
    }),
    updateWorkspaceAgent: builder.mutation<
      Awaited<ReturnType<typeof agentDirectoryService.updateAgent>>,
      {
        companyId: string;
        request: AgentUpdateRequest;
        agentId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "Agent" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: AgentUpdateRequest;
        agentId: string;
      }) =>
        executeZenithTeamQuery(
          () =>
            agentDirectoryService.updateAgent(
              argument.companyId,
              argument.agentId,
              argument.request,
            ),
          "Agent configuration could not be saved.",
        ),
    }),
    archiveWorkspaceAgent: agentLifecycleMutation(builder, "archive"),
    restoreWorkspaceAgent: agentLifecycleMutation(builder, "restore"),
  }),
});

function agentLifecycleMutation(
  builder: Parameters<
    typeof zenithTeamApi.injectEndpoints
  >[0]["endpoints"] extends (builder: infer T) => unknown
    ? T
    : never,
  operation: "archive" | "restore",
) {
  return builder.mutation<
    AgentDto,
    { companyId: string; agentId: string; request: EntityRevisionRequest }
  >({
    invalidatesTags: (_result, _error, argument: { companyId: string }) => [
      { id: argument.companyId, type: "Agent" },
    ],
    queryFn: (argument: {
      companyId: string;
      request: EntityRevisionRequest;
      agentId: string;
    }) =>
      executeZenithTeamQuery(
        () =>
          operation === "archive"
            ? agentDirectoryService.archiveAgent(
                argument.companyId,
                argument.agentId,
                argument.request,
              )
            : agentDirectoryService.restoreAgent(
                argument.companyId,
                argument.agentId,
                argument.request,
              ),
        `Agent could not be ${operation === "archive" ? "archived" : "restored"}.`,
      ),
  });
}

export const {
  useArchiveWorkspaceAgentMutation,
  useCreateWorkspaceAgentMutation,
  useRestoreWorkspaceAgentMutation,
  useUpdateWorkspaceAgentMutation,
} = agentApi;
