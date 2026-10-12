import { apiClient } from "@/lib/api-client";
import type { RunDto, RunStatusDto, RunPageDto } from "@/shared/api/types";

export type RunListOptions = {
  active?: boolean;
  runType?: RunDto["runType"];
  revision?: number;
  from?: string;
  to?: string;
  agentId?: string;
  conversationId?: string;
  cursor?: string;
  limit?: number;
  runtimeId?: string;
  status?: RunStatusDto | readonly RunStatusDto[];
  taskId?: string;
};

class RunService {
  async listRuns(
    companyId: string,
    options: RunListOptions = {},
  ): Promise<RunPageDto> {
    return apiClient.get<RunPageDto>(
      `/companies/${encodeURIComponent(companyId)}/runs`,
      { params: runListParams(options) },
    );
  }

  async listAllRuns(
    companyId: string,
    options: Omit<RunListOptions, "cursor" | "limit"> = {},
  ): Promise<RunDto[]> {
    const items: RunDto[] = [];
    const visitedCursors = new Set<string>();
    let cursor: string | undefined;
    do {
      const page = await this.listRuns(companyId, {
        ...options,
        ...(cursor ? { cursor } : {}),
        limit: 100,
      });
      items.push(...page.items);
      if (page.nextCursor === null) break;
      if (!page.nextCursor || visitedCursors.has(page.nextCursor)) {
        throw new Error("Run pagination returned an invalid cursor");
      }
      visitedCursors.add(page.nextCursor);
      cursor = page.nextCursor;
    } while (true);
    return items;
  }

  async getRun(companyId: string, runId: string): Promise<RunDto> {
    return apiClient.get<RunDto>(
      `/companies/${encodeURIComponent(companyId)}/runs/${runId}`,
    );
  }

  async cancelRun(companyId: string, runId: string): Promise<RunDto> {
    return apiClient.post<RunDto>(
      `/companies/${encodeURIComponent(companyId)}/runs/${runId}/cancel`,
    );
  }
}

function runListParams(options: RunListOptions) {
  const status = Array.isArray(options.status)
    ? options.status.join(",")
    : options.status;
  return {
    ...(options.active === undefined ? {} : { active: options.active }),
    ...(options.runType ? { run_type: options.runType } : {}),
    ...(options.revision === undefined ? {} : { revision: options.revision }),
    ...(options.from ? { from: options.from } : {}),
    ...(options.to ? { to: options.to } : {}),
    ...(options.agentId ? { agent_id: options.agentId } : {}),
    ...(options.conversationId
      ? { conversation_id: options.conversationId }
      : {}),
    ...(options.cursor ? { cursor: options.cursor } : {}),
    ...(options.limit === undefined ? {} : { limit: options.limit }),
    ...(options.runtimeId ? { runtime_id: options.runtimeId } : {}),
    ...(status ? { status } : {}),
    ...(options.taskId ? { task_id: options.taskId } : {}),
  };
}

export const runService = new RunService();
