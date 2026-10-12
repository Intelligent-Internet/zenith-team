import { apiClient } from "@/lib/api-client";
import type {
  CompanyResourceCreateRequest,
  CompanyResourceDto,
  CompanyResourcePageDto,
  CompanyResourceUpdateRequest,
  EntityRevisionRequest,
} from "@/shared/api/types";

class CompanyResourceService {
  async listResources(
    companyId: string,
    options: { daemonId?: string } = {},
  ): Promise<CompanyResourcePageDto> {
    return apiClient.get<CompanyResourcePageDto>(
      this.collectionPath(companyId),
      { params: { daemon_id: options.daemonId } },
    );
  }

  async getResource(
    companyId: string,
    resourceId: string,
  ): Promise<CompanyResourceDto> {
    return apiClient.get<CompanyResourceDto>(
      `${this.collectionPath(companyId)}/${encodeURIComponent(resourceId)}`,
    );
  }

  async createResource(
    companyId: string,
    payload: CompanyResourceCreateRequest,
  ): Promise<CompanyResourceDto> {
    return apiClient.post<CompanyResourceDto>(
      this.collectionPath(companyId),
      payload,
    );
  }

  async updateResource(
    companyId: string,
    resourceId: string,
    payload: CompanyResourceUpdateRequest,
  ): Promise<CompanyResourceDto> {
    return apiClient.patch<CompanyResourceDto>(
      `${this.collectionPath(companyId)}/${encodeURIComponent(resourceId)}`,
      payload,
    );
  }

  async deleteResource(
    companyId: string,
    resourceId: string,
    payload: EntityRevisionRequest,
  ): Promise<void> {
    return apiClient.delete<void>(
      `${this.collectionPath(companyId)}/${encodeURIComponent(resourceId)}`,
      { data: payload },
    );
  }

  private collectionPath(companyId: string) {
    return `/companies/${encodeURIComponent(companyId)}/resources`;
  }
}

export const companyResourceService = new CompanyResourceService();
