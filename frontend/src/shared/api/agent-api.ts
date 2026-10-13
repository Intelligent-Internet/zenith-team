import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type {
  EntityRevisionRequest,
  AgentCreateRequest,
  AgentDto,
  AgentUpdateRequest,
} from "@/shared/api/types";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";

export const agentApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    createWorkspaceAgent: builder.mutation<
      Awaited<ReturnType<typeof agentDirectoryService.createAgent>>,
      { companyId: string; request: AgentCreateRequest }
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "Agent" },
      ],
      queryFn: (argument) =>
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
    typeof boardlyApi.injectEndpoints
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
      executeBoardlyQuery(
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
