import type { companyService } from "@/shared/api/company.service";

export type AnalyticsCalendarQuery = Parameters<
  typeof companyService.getAnalyticsSummary
>[1] & { companyId: string };
