import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import { runService } from "@/shared/api/run.service";
import type { RunDto } from "@/shared/api/types";

export function workActiveRunsTag(companyId: string) {
  return { type: "Run" as const, id: `${companyId}:active-tasks` };
}

export const workNextUpApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkActiveRuns: builder.query<RunDto[], string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Run" },
        workActiveRunsTag(companyId),
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(async () => {
          const runs = await runService.listAllRuns(companyId, {
            active: true,
          });
          return runs.filter((run) => run.taskId !== null);
        }, "Active task runs could not be loaded."),
    }),
  }),
  overrideExisting: false,
});

export const { useListWorkActiveRunsQuery } = workNextUpApi;
