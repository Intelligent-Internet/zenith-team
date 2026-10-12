import { apiClient } from "@/lib/api-client";
import type {
  ActivityInsight,
  CompanyBootstrap,
  CompanyUsagePeriod,
  CompanyUsageReport,
  CompanyOnboardingRequest,
  CompanyOnboardingResult,
  CompanyPage,
  CompanyDetail,
  CompanyStatistics,
  CompanySettingsProfileRequest,
  CompanySettingsProfileResponse,
  ProactiveModeDto,
  UpdateProactiveModeRequest,
  AgentUsageReport,
  AnalyticsBreakdown,
  AnalyticsFailures,
  AnalyticsSeries,
  AnalyticsSummary,
  AnalyticsTaskUsage,
  AnalyticsBudget,
  AnalyticsBudgetInput,
  AnalyticsBudgetPage,
  AnalyticsHeatmap,
} from "@/shared/api/types";

type AnalyticsFilters = {
  scope: "company" | "mine";
  startAt: string;
  endAt: string;
  timeZone: string;
  provider?: string;
  model?: string;
  agentId?: string;
  memberId?: string;
  projectId?: string;
  taskId?: string;
  runType?: string;
  runtimeId?: string;
};

function analyticsFilterParams(query: AnalyticsFilters) {
  return {
    scope: query.scope,
    start_at: query.startAt,
    end_at: query.endAt,
    time_zone: query.timeZone,
    provider: query.provider,
    model: query.model,
    agent_id: query.agentId,
    member_id: query.memberId,
    project_id: query.projectId,
    task_id: query.taskId,
    run_type: query.runType,
    runtime_id: query.runtimeId,
  };
}

class CompanyService {
  async getProactiveMode(companyId: string): Promise<ProactiveModeDto> {
    return apiClient.get<ProactiveModeDto>(
      `/companies/${encodeURIComponent(companyId)}/proactive-mode`,
    );
  }

  async updateProactiveMode(
    companyId: string,
    payload: UpdateProactiveModeRequest,
  ): Promise<ProactiveModeDto> {
    return apiClient.patch<ProactiveModeDto>(
      `/companies/${encodeURIComponent(companyId)}/proactive-mode`,
      payload,
    );
  }

  async listCompanies(): Promise<CompanyPage> {
    return apiClient.get<CompanyPage>("/companies");
  }

  async onboardCompany(
    payload: CompanyOnboardingRequest,
  ): Promise<CompanyOnboardingResult> {
    let company: CompanyDetail;
    try {
      company = await apiClient.post<CompanyDetail>("/companies", payload);
    } catch (error) {
      // A lost POST receipt or a failed journey read may leave a committed
      // company. The caller retains its slug across retries.
      const page = await this.listCompanies().catch(() => null);
      const existing = page?.items.find(
        (item) => item.slug === payload.slug && item.role === "owner",
      );
      if (!existing) throw error;
      const recovered = await this.getCompanyById(existing.id);
      if (
        recovered.name !== payload.name ||
        recovered.businessContext !== payload.businessContext
      )
        throw error;
      company = recovered;
    }
    return { company };
  }

  // Keeps uploaded files as the Company's founder-supplied source material.
  // Until this runs the upload is an orphan the server sweeps away, so it is
  // what makes a founder's document durable and readable by every Agent.
  async addReferenceDocuments(
    companyId: string,
    attachmentIds: string[],
  ): Promise<void> {
    await apiClient.post(
      `/companies/${encodeURIComponent(companyId)}/reference-documents`,
      { attachmentIds },
    );
  }

  // The owner's one read of everything a freshly created Company owns,
  // including the CEO Conversation the founder lands in.
  async getBootstrap(companyId: string): Promise<CompanyBootstrap> {
    return apiClient.get<CompanyBootstrap>(
      `/companies/${encodeURIComponent(companyId)}/bootstrap`,
    );
  }

  async getCompanyById(companyId: string): Promise<CompanyDetail> {
    return apiClient.get<CompanyDetail>(
      `/companies/${encodeURIComponent(companyId)}`,
    );
  }

  async deleteCompany(
    companyId: string,
    expectedRevision: number,
  ): Promise<void> {
    return apiClient.delete<void>(
      `/companies/${encodeURIComponent(companyId)}`,
      { data: { expectedRevision } },
    );
  }

  async updateCompanyProfile(
    companyId: string,
    payload: CompanySettingsProfileRequest,
  ): Promise<CompanyDetail> {
    return apiClient.patch<CompanySettingsProfileResponse>(
      `/companies/${encodeURIComponent(companyId)}/profile`,
      payload,
    );
  }

  async getCompanyStatistics(companyId: string): Promise<CompanyStatistics> {
    return apiClient.get<CompanyStatistics>(
      `/companies/${encodeURIComponent(companyId)}/statistics`,
    );
  }

  async getAgentUsage(
    companyId: string,
    agentId: string,
    period: {
      startAt: string;
      endAt: string;
      timeZone: string;
      bucket: "day" | "month";
    },
  ): Promise<AgentUsageReport> {
    return apiClient.get<AgentUsageReport>(
      `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}/usage`,
      {
        params: {
          start_at: period.startAt,
          end_at: period.endAt,
          time_zone: period.timeZone,
          bucket: period.bucket,
        },
      },
    );
  }

  async getCompanyUsage(
    companyId: string,
    period: CompanyUsagePeriod,
    timeZone: string,
  ): Promise<CompanyUsageReport> {
    return apiClient.get<CompanyUsageReport>(
      `/companies/${encodeURIComponent(companyId)}/usage`,
      { params: { period, time_zone: timeZone } },
    );
  }

  async getActivityInsight(companyId: string): Promise<ActivityInsight> {
    return apiClient.get<ActivityInsight>(
      `/companies/${encodeURIComponent(companyId)}/me/activity-insight`,
    );
  }

  async getAnalyticsSummary(
    companyId: string,
    query: AnalyticsFilters,
  ): Promise<AnalyticsSummary> {
    return apiClient.get<AnalyticsSummary>(
      `/companies/${encodeURIComponent(companyId)}/analytics/summary`,
      {
        params: {
          ...analyticsFilterParams(query),
        },
      },
    );
  }

  async getAnalyticsSeries(
    companyId: string,
    query: AnalyticsFilters & { metric: string; bucket: string },
  ): Promise<AnalyticsSeries> {
    return apiClient.get<AnalyticsSeries>(
      `/companies/${encodeURIComponent(companyId)}/analytics/series`,
      {
        params: {
          ...analyticsFilterParams(query),
          metric: query.metric,
          bucket: query.bucket,
        },
      },
    );
  }

  async getAnalyticsBreakdown(
    companyId: string,
    query: AnalyticsFilters & {
      by: string;
      sort: string;
      limit: number;
      cursor?: string;
    },
  ): Promise<AnalyticsBreakdown> {
    return apiClient.get<AnalyticsBreakdown>(
      `/companies/${encodeURIComponent(companyId)}/analytics/breakdown`,
      {
        params: {
          ...analyticsFilterParams(query),
          by: query.by,
          sort: query.sort,
          limit: query.limit,
          cursor: query.cursor,
        },
      },
    );
  }

  async getAnalyticsFailures(
    companyId: string,
    query: AnalyticsFilters,
  ): Promise<AnalyticsFailures> {
    return apiClient.get<AnalyticsFailures>(
      `/companies/${encodeURIComponent(companyId)}/analytics/failures`,
      {
        params: {
          ...analyticsFilterParams(query),
        },
      },
    );
  }

  async getTaskUsage(
    companyId: string,
    taskId: string,
  ): Promise<AnalyticsTaskUsage> {
    return apiClient.get<AnalyticsTaskUsage>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/usage`,
    );
  }

  async getAnalyticsBudgets(companyId: string): Promise<AnalyticsBudgetPage> {
    return apiClient.get<AnalyticsBudgetPage>(
      `/companies/${encodeURIComponent(companyId)}/analytics/budget`,
    );
  }

  async getAnalyticsHeatmap(
    companyId: string,
    query: AnalyticsFilters,
  ): Promise<AnalyticsHeatmap> {
    return apiClient.get<AnalyticsHeatmap>(
      `/companies/${encodeURIComponent(companyId)}/analytics/heatmap`,
      {
        params: {
          ...analyticsFilterParams(query),
        },
      },
    );
  }

  async putAnalyticsBudget(
    companyId: string,
    payload: AnalyticsBudgetInput,
  ): Promise<AnalyticsBudget> {
    return apiClient.put<AnalyticsBudget>(
      `/companies/${encodeURIComponent(companyId)}/analytics/budget`,
      payload,
    );
  }
}

export const companyService = new CompanyService();
