import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { companyService } from "@/shared/api/company.service";
import type {
  CompanyUsagePeriod,
  CompanyUsageReport,
  AgentUsageReport,
  AnalyticsSummary,
  AnalyticsSeries,
  AnalyticsBreakdown,
  AnalyticsFailures,
  AnalyticsTaskUsage,
  AnalyticsBudget,
  AnalyticsBudgetInput,
  AnalyticsBudgetPage,
  AnalyticsHeatmap,
} from "@/shared/api/types";

export const analyticsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getWorkspaceAgentUsage: builder.query<
      AgentUsageReport,
      {
        companyId: string;
        agentId: string;
        startAt: string;
        endAt: string;
        timeZone: string;
        provider?: string;
        model?: string;
        memberId?: string;
        projectId?: string;
        taskId?: string;
        runType?: string;
        runtimeId?: string;
        bucket: "day" | "month";
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Run" },
        { id: companyId, type: "Agent" },
      ],
      queryFn: ({ companyId, agentId, ...period }) =>
        executeBoardlyQuery(
          () => companyService.getAgentUsage(companyId, agentId, period),
          "Compute usage could not be loaded.",
        ),
    }),
    getWorkspaceCompanyUsage: builder.query<
      CompanyUsageReport,
      { companyId: string; period: CompanyUsagePeriod; timeZone: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Run" },
        { id: companyId, type: "Agent" },
      ],
      queryFn: ({ companyId, period, timeZone }) =>
        executeBoardlyQuery(
          () => companyService.getCompanyUsage(companyId, period, timeZone),
          "Company usage could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsSummary: builder.query<
      AnalyticsSummary,
      Parameters<typeof companyService.getAnalyticsSummary>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsSummary(companyId, query),
          "Analytics summary could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsSeries: builder.query<
      AnalyticsSeries,
      Parameters<typeof companyService.getAnalyticsSeries>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsSeries(companyId, query),
          "Analytics trend could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsBreakdown: builder.query<
      AnalyticsBreakdown,
      Parameters<typeof companyService.getAnalyticsBreakdown>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsBreakdown(companyId, query),
          "Analytics breakdown could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsFailures: builder.query<
      AnalyticsFailures,
      Parameters<typeof companyService.getAnalyticsFailures>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsFailures(companyId, query),
          "Analytics reliability could not be loaded.",
        ),
    }),
    getWorkspaceTaskUsage: builder.query<
      AnalyticsTaskUsage,
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId, taskId }) => [
        { id: companyId, type: "Analytics" },
        { id: taskId, type: "Task" },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(
          () => companyService.getTaskUsage(companyId, taskId),
          "Task usage could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsBudgets: builder.query<AnalyticsBudgetPage, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsBudgets(companyId),
          "Analytics budgets could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsHeatmap: builder.query<
      AnalyticsHeatmap,
      Parameters<typeof companyService.getAnalyticsHeatmap>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsHeatmap(companyId, query),
          "Analytics heatmap could not be loaded.",
        ),
    }),
    putWorkspaceAnalyticsBudget: builder.mutation<
      AnalyticsBudget,
      { companyId: string; payload: AnalyticsBudgetInput }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, payload }) =>
        executeBoardlyQuery(
          () => companyService.putAnalyticsBudget(companyId, payload),
          "Analytics budget could not be saved.",
        ),
    }),
  }),
});

export const {
  useGetWorkspaceAnalyticsSummaryQuery,
  useGetWorkspaceAnalyticsSeriesQuery,
  useGetWorkspaceAnalyticsBreakdownQuery,
  useGetWorkspaceAnalyticsFailuresQuery,
  useGetWorkspaceAnalyticsBudgetsQuery,
  useGetWorkspaceAnalyticsHeatmapQuery,
  usePutWorkspaceAnalyticsBudgetMutation,
  useGetWorkspaceTaskUsageQuery,
  useGetWorkspaceCompanyUsageQuery,
  useGetWorkspaceAgentUsageQuery,
} = analyticsApi;
