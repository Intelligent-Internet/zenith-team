import { apiClient } from "@/lib/api-client";
import type {
  CompanySearchPage,
  CompanySearchRequest,
} from "@/shared/api/types";

class CompanySearchService {
  async search(
    companyId: string,
    request: CompanySearchRequest,
    signal?: AbortSignal,
  ): Promise<CompanySearchPage> {
    return apiClient.post<CompanySearchPage>(
      `/companies/${encodeURIComponent(companyId)}/search`,
      request,
      { signal },
    );
  }
}

export const companySearchService = new CompanySearchService();
