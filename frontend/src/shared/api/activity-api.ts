import {
  activityService,
  type ActivityCalendarRequest,
  type ActivityCalendarSummary,
  type ActivityListOptions,
  type ActivityPage,
} from "@/shared/api/activity.service";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import { companyService } from "@/shared/api/company.service";
import { cockpitService } from "@/shared/api/cockpit.service";
import type { ActivityInsight } from "@/shared/api/types";

export const activityApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getWorkspaceActivityInsight: builder.query<ActivityInsight, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Run" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => companyService.getActivityInsight(companyId),
          "Activity insight could not be loaded.",
        ),
    }),
    getWorkspaceCockpit: builder.query({
      providesTags: (_result, _error, companyId: string) => [
        { id: `${companyId}:cockpit`, type: "Company" },
        { id: companyId, type: "Company" },
        { id: companyId, type: "Agent" },
        { id: companyId, type: "Task" },
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => cockpitService.getCockpit(companyId),
          `Cockpit for Company ${companyId} could not be loaded.`,
        ),
    }),
    listWorkspaceActivity: builder.query<
      Awaited<ReturnType<typeof activityService.listCompanyActivity>>,
      string
    >({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => activityService.listCompanyActivity(companyId),
          "Activity could not be loaded.",
        ),
    }),
    listWorkspaceActivityPages: builder.infiniteQuery<
      ActivityPage,
      { companyId: string; options: Omit<ActivityListOptions, "cursor"> },
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (lastPage) => lastPage.nextCursor ?? undefined,
      },
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: ({ queryArg: { companyId, options }, pageParam }) =>
        executeZenithTeamQuery(
          () =>
            activityService.listCompanyActivityPage(companyId, {
              ...options,
              cursor: pageParam ?? undefined,
            }),
          "Activity could not be loaded.",
        ),
    }),
    getWorkspaceActivityCalendar: builder.query<
      ActivityCalendarSummary,
      ActivityCalendarRequest
    >({
      keepUnusedDataFor: 86_400,
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: (request) =>
        executeZenithTeamQuery(
          () => activityService.getCompanyActivityCalendar(request),
          "Activity calendar could not be loaded.",
        ),
    }),
  }),
});

export const {
  useGetWorkspaceActivityCalendarQuery,
  useGetWorkspaceActivityInsightQuery,
  useLazyGetWorkspaceActivityCalendarQuery,
  useGetWorkspaceCockpitQuery,
  useListWorkspaceActivityQuery,
  useListWorkspaceActivityPagesInfiniteQuery,
} = activityApi;
