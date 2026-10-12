import { apiClient } from "@/lib/api-client";
import type {
  AcceptCommerceTermsRequest,
  ApproveCommerceManifestRequest,
  CommerceAccountSession,
  CommerceDashboardLink,
  CommerceEnvironment,
  CommerceManifestPage,
  CommerceManifestStatus,
  CommerceOnboardingLink,
  CommerceSummary,
  CommerceTermsAcceptance,
  CreateCommerceAccountSessionRequest,
} from "@/features/revenue/model/commerce";

function commercePath(companyId: string) {
  return `/companies/${encodeURIComponent(companyId)}/commerce`;
}

class CommerceService {
  getSummary(companyId: string) {
    return apiClient.get<CommerceSummary>(`${commercePath(companyId)}/summary`);
  }

  acceptTerms(companyId: string, request: AcceptCommerceTermsRequest) {
    return apiClient.post<CommerceTermsAcceptance>(
      `${commercePath(companyId)}/terms-acceptances`,
      request,
    );
  }

  createAccountSession(
    companyId: string,
    environment: CommerceEnvironment,
    request: CreateCommerceAccountSessionRequest,
  ) {
    return apiClient.post<CommerceAccountSession>(
      `${commercePath(companyId)}/accounts/${environment}/account-session`,
      request,
    );
  }

  createOnboardingLink(
    companyId: string,
    environment: CommerceEnvironment,
    request: CreateCommerceAccountSessionRequest,
  ) {
    return apiClient.post<CommerceOnboardingLink>(
      `${commercePath(companyId)}/accounts/${environment}/onboarding-link`,
      request,
    );
  }

  createDashboardLink(
    companyId: string,
    environment: CommerceEnvironment,
    request: CreateCommerceAccountSessionRequest,
  ) {
    return apiClient.post<CommerceDashboardLink>(
      `${commercePath(companyId)}/accounts/${environment}/dashboard-link`,
      request,
    );
  }

  listManifests(companyId: string, cursor: string | null) {
    return apiClient.get<CommerceManifestPage>(
      `${commercePath(companyId)}/manifests`,
      cursor ? { params: { cursor } } : undefined,
    );
  }

  approveAndPublishManifest(
    companyId: string,
    manifestId: string,
    request: ApproveCommerceManifestRequest,
  ) {
    return apiClient.post<CommerceManifestStatus>(
      `${commercePath(companyId)}/manifests/${encodeURIComponent(manifestId)}/approve-publish`,
      request,
    );
  }
}

export const commerceService = new CommerceService();
