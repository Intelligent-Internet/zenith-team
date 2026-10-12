import { apiClient } from "@/lib/api-client";
import type { operations as ApiOperations } from "@/shared/api/generated/api.generated";
import type {
  RuntimeDto,
  RuntimeListResponse,
  RuntimeModelDiscoveryRequest,
  RuntimeRemovalRequest,
  RuntimeRemovalResponse,
  RuntimeRenameRequest,
} from "@/shared/api/types";

type ListRuntimeOptions = NonNullable<
  ApiOperations["listRuntimes"]["parameters"]["query"]
>;

class RuntimeService {
  async listRuntimes(
    companyId: string,
    options: ListRuntimeOptions = {},
  ): Promise<RuntimeListResponse> {
    const response = await apiClient.get<RuntimeListResponse>(
      `/companies/${encodeURIComponent(companyId)}/runtimes`,
      { params: options },
    );
    return response;
  }

  async getRuntime(companyId: string, runtimeId: string): Promise<RuntimeDto> {
    return apiClient.get<RuntimeDto>(runtimePath(companyId, runtimeId));
  }

  async renameRuntime(
    companyId: string,
    runtimeId: string,
    payload: RuntimeRenameRequest,
  ): Promise<RuntimeDto> {
    return apiClient.patch<RuntimeDto>(
      runtimePath(companyId, runtimeId),
      payload,
    );
  }

  // The plan-less removal for a runtime nothing is bound to; answers 204.
  async deleteRuntime(companyId: string, runtimeId: string): Promise<void> {
    await apiClient.delete<void>(runtimePath(companyId, runtimeId));
  }

  // Removal says where the agents you run here go: another runtime of yours
  // of the same provider, or nowhere (ADR 0014).
  async removeRuntime(
    companyId: string,
    runtimeId: string,
    payload: RuntimeRemovalRequest,
  ): Promise<RuntimeRemovalResponse> {
    return apiClient.post<RuntimeRemovalResponse>(
      `${runtimePath(companyId, runtimeId)}/remove`,
      payload,
    );
  }

  // Asks the machine behind the runtime to probe its provider setup again
  // (version, sign-in). 202 with no body: the daemon's fresh registration is
  // the answer, and it arrives through daemon.roster_changed.
  async refreshRuntime(companyId: string, runtimeId: string): Promise<void> {
    await apiClient.post<void>(`${runtimePath(companyId, runtimeId)}/refresh`);
  }

  async discoverModels(
    companyId: string,
    runtimeId: string,
    signal?: AbortSignal,
  ): Promise<RuntimeModelDiscoveryRequest> {
    return apiClient.post<RuntimeModelDiscoveryRequest>(
      `${runtimePath(companyId, runtimeId)}/models`,
      undefined,
      { signal },
    );
  }

  async getModelDiscovery(
    companyId: string,
    runtimeId: string,
    requestId: string,
    signal?: AbortSignal,
  ): Promise<RuntimeModelDiscoveryRequest> {
    return apiClient.get<RuntimeModelDiscoveryRequest>(
      `${runtimePath(companyId, runtimeId)}/models/${encodeURIComponent(requestId)}`,
      { signal },
    );
  }

  async discoverModelCatalog(
    companyId: string,
    runtimeId: string,
    signal: AbortSignal,
  ): Promise<RuntimeModelDiscoveryRequest> {
    let result = await this.discoverModels(companyId, runtimeId, signal);
    const deadline = Date.now() + 95_000;
    while (
      (result.status === "pending" || result.status === "running") &&
      Date.now() < deadline
    ) {
      if (!(await waitForModelPoll(signal))) break;
      result = await this.getModelDiscovery(
        companyId,
        runtimeId,
        result.requestId,
        signal,
      );
    }
    return result;
  }
}

export const runtimeService = new RuntimeService();

function runtimePath(companyId: string, runtimeId: string) {
  return `/companies/${encodeURIComponent(companyId)}/runtimes/${encodeURIComponent(runtimeId)}`;
}

function waitForModelPoll(signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(false);
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", stopWaiting);
      resolve(true);
    }, 500);
    const stopWaiting = () => {
      clearTimeout(timer);
      resolve(false);
    };
    signal.addEventListener("abort", stopWaiting, { once: true });
  });
}
