import { getApiErrorMessage } from "@/lib/api-client";
import { agentBindingService } from "@/shared/api/agent-binding.service";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
  type ZenithTeamApiError,
} from "@/shared/api/zenith-team-api";
import { runService } from "@/shared/api/run.service";
import { runtimeService } from "@/shared/api/runtime.service";
import type {
  AgentBindingPage,
  AgentCreateRequest,
  AgentDto,
  AgentPage,
  AgentRuntimeUpdateRequest,
  RunDto,
  RunPageDto,
  RuntimeDto,
  RuntimeListResponse,
  RuntimeModelDiscoveryRequest,
  RuntimeRemovalRequest,
  RuntimeRemovalResponse,
  RuntimeRenameRequest,
} from "@/shared/api/types";

export type CreateCeoAgentArgs = {
  companyId: string;
  payload: AgentCreateRequest;
};

export type UpdateCeoAgentArgs = {
  agentId: string;
  companyId: string;
  payload: AgentRuntimeUpdateRequest;
};

export type RuntimeArgs = {
  companyId: string;
  runtimeId: string;
};

export type RenameRuntimeArgs = RuntimeArgs & {
  payload: RuntimeRenameRequest;
};

export type RemoveRuntimeArgs = RuntimeArgs & {
  payload: RuntimeRemovalRequest;
};

export type RuntimeModelCatalogArgs = RuntimeArgs &
  Pick<RuntimeDto, "provider" | "providerVersion"> & {
    userId: string;
    signInState: NonNullable<RuntimeDto["signIn"]>["state"];
  };

export type CeoAgentApiError = ZenithTeamApiError;

export const agentRuntimeApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listAgentRecentRuns: builder.infiniteQuery<
      RunPageDto,
      { companyId: string; agentId: string; runType?: RunDto["runType"] },
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
      },
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Run" },
      ],
      queryFn: ({ queryArg, pageParam }) =>
        executeZenithTeamQuery(
          () =>
            runService.listRuns(queryArg.companyId, {
              agentId: queryArg.agentId,
              runType: queryArg.runType,
              cursor: pageParam ?? undefined,
              limit: 20,
            }),
          "Run history could not be loaded.",
        ),
    }),
    listAgentRuntimes: builder.query<RuntimeListResponse, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Runtime" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => runtimeService.listRuntimes(companyId),
          "Agent runtimes could not be loaded.",
        ),
    }),
    // The runtime detail is three reads, one request each: the row, the
    // caller's bindings on it, and the runs on the machine. They cache
    // independently and the realtime roster refresh refetches all three.
    getRuntime: builder.query<RuntimeDto, RuntimeArgs>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Runtime" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeZenithTeamQuery(
          () => runtimeService.getRuntime(companyId, runtimeId),
          "The runtime could not be loaded.",
        ),
    }),
    // The agents the caller set up on this computer: their own bindings on
    // it, and nobody else's (ADR 0014). This is exactly the set a removal
    // moves or drops. Names come from the company roster.
    listRuntimeAgents: builder.query<AgentBindingPage, RuntimeArgs>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Runtime" },
        { id: companyId, type: "AgentBinding" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeZenithTeamQuery(
          () => agentBindingService.listMyBindings(companyId, { runtimeId }),
          "The agents on this runtime could not be loaded.",
        ),
    }),
    listRuntimeActiveRuns: builder.query<RunDto[], RuntimeArgs>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Runtime" },
        { id: companyId, type: "Run" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeZenithTeamQuery(
          () => runService.listAllRuns(companyId, { active: true, runtimeId }),
          "The runs on this runtime could not be loaded.",
        ),
    }),
    // The rename lands on the cached roster and detail rows at once; the
    // agent rosters embed `runtime.customName`, so they refetch, and the
    // realtime `daemon.roster_changed{action: update}` converges the rest.
    renameRuntime: builder.mutation<RuntimeDto, RenameRuntimeArgs>({
      invalidatesTags: (_result, error, { companyId }) =>
        error ? [] : [{ id: companyId, type: "Agent" }],
      async onQueryStarted(
        { companyId, payload },
        { dispatch, getState, queryFulfilled },
      ) {
        let renamed: RuntimeDto;
        try {
          renamed = (await queryFulfilled).data;
        } catch {
          return;
        }
        const appliesToRuntime = (runtime: RuntimeDto) =>
          runtime.id === renamed.id ||
          (payload.applyToMachine && runtime.daemonId === renamed.daemonId);
        dispatch(
          agentRuntimeApi.util.updateQueryData(
            "listAgentRuntimes",
            companyId,
            (page) => {
              for (const runtime of page.items) {
                if (appliesToRuntime(runtime)) {
                  runtime.customName = renamed.customName;
                }
              }
            },
          ),
        );
        for (const argument of agentRuntimeApi.util.selectCachedArgsForQuery(
          getState(),
          "getRuntime",
        )) {
          if (argument.companyId !== companyId) continue;
          dispatch(
            agentRuntimeApi.util.updateQueryData(
              "getRuntime",
              argument,
              (runtime) => {
                if (runtime.id === renamed.id) return renamed;
                if (appliesToRuntime(runtime)) {
                  runtime.customName = renamed.customName;
                }
                return undefined;
              },
            ),
          );
        }
      },
      queryFn: ({ companyId, payload, runtimeId }) =>
        executeZenithTeamQuery(
          () => runtimeService.renameRuntime(companyId, runtimeId, payload),
          "The machine could not be renamed.",
        ),
    }),
    // Removal moves or drops the caller's bindings on the machine, so the
    // runtime-scoped connector and plugin catalogues change with the roster
    // (as `updateCeoAgent`), and the moved agents' queued runs changed
    // `runtimeId` or were cancelled. A 409 means the roster the client acted
    // on is stale — the runtime came online — so it refetches.
    deleteRuntime: builder.mutation<void, RuntimeArgs>({
      invalidatesTags: runtimeRemovalTags,
      queryFn: ({ companyId, runtimeId }) =>
        executeZenithTeamQuery(
          () => runtimeService.deleteRuntime(companyId, runtimeId),
          "The runtime could not be removed.",
        ),
    }),
    removeRuntime: builder.mutation<RuntimeRemovalResponse, RemoveRuntimeArgs>({
      invalidatesTags: runtimeRemovalTags,
      queryFn: ({ companyId, payload, runtimeId }) =>
        executeZenithTeamQuery(
          () => runtimeService.removeRuntime(companyId, runtimeId, payload),
          "The runtime could not be removed.",
        ),
    }),
    listCompanyAgents: builder.query<AgentPage, string>({
      providesTags: (_result, _error, companyId) => [
        { id: `${companyId}:roster`, type: "Agent" },
        { id: companyId, type: "Agent" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () =>
            agentDirectoryService.listAgents(companyId, {
              includeArchived: true,
            }),
          "CEO Agent request failed.",
        ),
    }),
    createCeoAgent: builder.mutation<AgentDto, CreateCeoAgentArgs>({
      invalidatesTags: (_result, error, { companyId }) =>
        !error || error.status === 409
          ? [{ id: companyId, type: "Agent" }]
          : [],
      queryFn: ({ companyId, payload }) =>
        executeZenithTeamQuery(
          () => agentDirectoryService.createAgent(companyId, payload),
          "CEO Agent request failed.",
        ),
    }),
    updateCeoAgent: builder.mutation<AgentDto, UpdateCeoAgentArgs>({
      invalidatesTags: (_result, error, { companyId }) =>
        !error || error.status === 409
          ? [
              { id: companyId, type: "Agent" },
              { id: companyId, type: "Connector" },
              { id: companyId, type: "Plugin" },
            ]
          : [],
      queryFn: ({ companyId, payload, agentId }) =>
        executeZenithTeamQuery(
          () => agentDirectoryService.updateAgent(companyId, agentId, payload),
          "CEO Agent request failed.",
        ),
    }),
    // The roster converges through daemon.roster_changed once the daemon
    // re-registers; nothing is invalidated here because a 202 only means the
    // machine was asked.
    refreshRuntime: builder.mutation<void, RuntimeArgs>({
      queryFn: ({ companyId, runtimeId }) =>
        executeZenithTeamQuery(
          () => runtimeService.refreshRuntime(companyId, runtimeId),
          "The computer could not be asked to refresh its setup.",
        ),
    }),
    // One cache entry owns the complete discovery, including polling. Several
    // pickers on the same runtime share its in-flight request and last success.
    // Roster heartbeats must not invalidate this relatively stable catalogue.
    getRuntimeModelCatalog: builder.query<
      RuntimeModelDiscoveryRequest,
      RuntimeModelCatalogArgs
    >({
      async queryFn({ companyId, runtimeId }, { signal }) {
        const response = await executeZenithTeamQuery(
          () =>
            runtimeService.discoverModelCatalog(companyId, runtimeId, signal),
          "Runtime models could not be discovered. Your saved model is unchanged.",
        );
        if (response.error || !response.data) return response;
        const result = response.data;
        if (result.status === "completed") return response;
        // Failed refreshes leave the previous successful catalogue in RTK.
        return {
          error: {
            status: null,
            code:
              result.error?.code ??
              (result.status === "failed"
                ? "discovery_failed"
                : "model_discovery_timeout"),
            message:
              result.error?.message ??
              (result.status === "failed"
                ? "Runtime models could not be discovered. Refresh to try again."
                : "Model discovery is taking longer than expected. Refresh to try again."),
          },
        };
      },
    }),
  }),
  overrideExisting: false,
});

export const {
  useCreateCeoAgentMutation,
  useDeleteRuntimeMutation,
  useRefreshRuntimeMutation,
  useGetRuntimeQuery,
  useLazyListCompanyAgentsQuery,
  useListAgentRuntimesQuery,
  useListAgentRecentRunsInfiniteQuery,
  useListCompanyAgentsQuery,
  useListRuntimeActiveRunsQuery,
  useListRuntimeAgentsQuery,
  useRemoveRuntimeMutation,
  useRenameRuntimeMutation,
  useUpdateCeoAgentMutation,
} = agentRuntimeApi;

function runtimeRemovalTags(
  _result: unknown,
  error: ZenithTeamApiError | undefined,
  { companyId }: RuntimeArgs,
) {
  if (!error) {
    return [
      { id: companyId, type: "Runtime" as const },
      { id: companyId, type: "Agent" as const },
      { id: companyId, type: "AgentBinding" as const },
      { id: companyId, type: "Connector" as const },
      { id: companyId, type: "Plugin" as const },
      { id: companyId, type: "Run" as const },
    ];
  }
  return error.status === 409
    ? [{ id: companyId, type: "Runtime" as const }]
    : [];
}

export function getCeoAgentApiErrorMessage(
  error: unknown,
  fallback: string,
): string {
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string" &&
    error.message.trim()
  ) {
    return error.message.trim();
  }

  return getApiErrorMessage(error, fallback);
}
