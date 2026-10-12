import { apiClient } from "@/lib/api-client";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type {
  ApprovalCancelRequest,
  ApprovalDto,
  ApprovalListQuery,
  ApprovalPage,
  ApprovalResolveRequest,
  ApprovalStats,
  ApprovalStatsQuery,
} from "@/shared/api/types";

class ApprovalService {
  list(
    query: ApprovalListQuery = {},
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalPage> {
    return apiClient.get<ApprovalPage>(
      `/companies/${encodeURIComponent(companyId)}/approvals`,
      { params: query },
    );
  }

  stats(
    query: ApprovalStatsQuery = {},
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalStats> {
    return apiClient.get<ApprovalStats>(
      `/companies/${encodeURIComponent(companyId)}/approvals/stats`,
      { params: query },
    );
  }

  get(
    approvalId: string,
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalDto> {
    return apiClient.get<ApprovalDto>(
      `/companies/${encodeURIComponent(companyId)}/approvals/${encodeURIComponent(approvalId)}`,
    );
  }

  approve(
    approvalId: string,
    request: ApprovalResolveRequest,
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalDto> {
    return this.resolve(approvalId, "approve", request, companyId);
  }

  reject(
    approvalId: string,
    request: ApprovalResolveRequest,
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalDto> {
    return this.resolve(approvalId, "reject", request, companyId);
  }

  cancel(
    approvalId: string,
    request: ApprovalCancelRequest,
    companyId = requireActiveCompanyId(),
  ): Promise<ApprovalDto> {
    return apiClient.post<ApprovalDto>(
      `/companies/${encodeURIComponent(companyId)}/approvals/${encodeURIComponent(approvalId)}/cancel`,
      request,
    );
  }

  private resolve(
    approvalId: string,
    action: "approve" | "reject",
    request: ApprovalResolveRequest,
    companyId: string,
  ): Promise<ApprovalDto> {
    return apiClient.post<ApprovalDto>(
      `/companies/${encodeURIComponent(companyId)}/approvals/${encodeURIComponent(approvalId)}/${action}`,
      request,
    );
  }
}

export const approvalService = new ApprovalService();
