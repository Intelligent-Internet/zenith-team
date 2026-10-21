import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";

const resultSelectionApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getTaskSelectedOutputs: builder.query<
      components["schemas"]["ResolvedArtifacts"],
      { companyId: string; ids: string[] }
    >({
      providesTags: [{ type: "Artifact" }],
      queryFn: ({ companyId, ids }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["ResolvedArtifacts"]>(
              `/companies/${encodeURIComponent(companyId)}/artifacts/resolve`,
              {
                params: new URLSearchParams(ids.map((id) => ["id", id])),
                signal,
              },
            ),
          "Selected results could not be loaded.",
        ),
    }),
    getTaskResultSelection: builder.query<
      components["schemas"]["TaskResultSelection"],
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { type: "Task", id: `${companyId}:list` },
        { type: "Deliverable", id: `${companyId}:results` },
        { type: "Deliverable" },
      ],
      queryFn: ({ companyId, taskId }, { signal }) =>
        executeZenithTeamQuery(async () => {
          const page = await apiClient.get<
            components["schemas"]["TaskResultPage"]
          >(
            `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/results`,
            { params: { limit: 1 }, signal },
          );
          return page.selection;
        }, "Result selection could not be loaded."),
    }),
  }),
});

export const {
  useGetTaskResultSelectionQuery,
  useGetTaskSelectedOutputsQuery,
} = resultSelectionApi;
