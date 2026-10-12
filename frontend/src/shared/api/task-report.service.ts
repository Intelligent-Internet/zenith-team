import { apiClient } from "@/lib/api-client";
import type {
  SendTaskReportsRequest,
  SendTaskReportsResponse,
  TaskReportPreviewDto,
} from "@/shared/api/types";

class TaskReportService {
  preview(
    companyId: string,
    conversationId: string,
    signal?: AbortSignal,
  ): Promise<TaskReportPreviewDto> {
    return apiClient.get<TaskReportPreviewDto>(
      taskReportPath(companyId, conversationId),
      { signal },
    );
  }

  send(
    companyId: string,
    conversationId: string,
    request: SendTaskReportsRequest,
  ): Promise<SendTaskReportsResponse> {
    return apiClient.post<SendTaskReportsResponse>(
      `${taskReportPath(companyId, conversationId)}/send`,
      request,
    );
  }
}

function taskReportPath(companyId: string, conversationId: string) {
  return `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}/task-reports`;
}

export const taskReportService = new TaskReportService();
