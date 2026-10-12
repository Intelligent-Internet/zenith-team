import { apiClient } from "@/lib/api-client";
import type {
  components as ApiComponents,
  operations as ApiOperations,
} from "@/shared/api/generated/api.generated";

export const DAILY_BRIEF_PRESET_KEY = "daily_brief_v1";

export type OnceScheduleTrigger =
  ApiComponents["schemas"]["ScheduleOnceTrigger"];
export type CronScheduleTrigger =
  ApiComponents["schemas"]["ScheduleCronTrigger"];
export type ScheduleTrigger = ApiComponents["schemas"]["ScheduleTrigger"];
export type ScheduledTaskStatus = ApiComponents["schemas"]["ScheduleStatus"];
export type ScheduleSourceSnapshot = ApiComponents["schemas"]["ScheduleSource"];
export type ScheduledTask = ApiComponents["schemas"]["Schedule"];
export type ScheduleDraft = ApiComponents["schemas"]["ScheduleDraftRequest"];
export type SchedulePreviewRequest =
  ApiComponents["schemas"]["SchedulePreviewRequest"];
export type SchedulePreviewResponse =
  ApiComponents["schemas"]["SchedulePreviewResponse"];
export type ScheduleCreateRequest =
  ApiComponents["schemas"]["ScheduleCreateRequest"];
export type ScheduleUpdateRequest =
  ApiComponents["schemas"]["ScheduleUpdateRequest"];
export type ScheduleLifecycleRequest =
  ApiComponents["schemas"]["ScheduleLifecycleRequest"];
export type ScheduleRunDisposition =
  ApiComponents["schemas"]["ScheduleRunDisposition"];
export type ScheduleRunRecord = ApiComponents["schemas"]["ScheduleRunRecord"];
export type SchedulePage = ApiComponents["schemas"]["SchedulePage"];
export type ScheduleRunPage = ApiComponents["schemas"]["ScheduleRunPage"];
export type ScheduleRunNowRequest =
  ApiComponents["schemas"]["ScheduleRunNowRequest"];
export type ScheduleListRequest = NonNullable<
  ApiOperations["listSchedules"]["parameters"]["query"]
>;
export type ScheduleRunListRequest = NonNullable<
  ApiOperations["listScheduleRuns"]["parameters"]["query"]
>;

class ScheduleService {
  preview(
    companyId: string,
    request: SchedulePreviewRequest,
  ): Promise<SchedulePreviewResponse> {
    return apiClient.post<SchedulePreviewResponse>(
      `${this.collectionPath(companyId)}/preview`,
      request,
    );
  }

  create(
    companyId: string,
    request: ScheduleCreateRequest,
  ): Promise<ScheduledTask> {
    return apiClient.post<ScheduledTask>(
      this.collectionPath(companyId),
      request,
    );
  }

  list(
    companyId: string,
    request: ScheduleListRequest = {},
  ): Promise<SchedulePage> {
    return apiClient.get<SchedulePage>(this.collectionPath(companyId), {
      params: request,
    });
  }

  get(companyId: string, scheduleId: string): Promise<ScheduledTask> {
    return apiClient.get<ScheduledTask>(
      this.schedulePath(companyId, scheduleId),
    );
  }

  update(
    companyId: string,
    scheduleId: string,
    request: ScheduleUpdateRequest,
  ): Promise<ScheduledTask> {
    return apiClient.patch<ScheduledTask>(
      this.schedulePath(companyId, scheduleId),
      request,
    );
  }

  pause(
    companyId: string,
    scheduleId: string,
    request: ScheduleLifecycleRequest,
  ): Promise<ScheduledTask> {
    return this.lifecycle(companyId, scheduleId, "pause", request);
  }

  resume(
    companyId: string,
    scheduleId: string,
    request: ScheduleLifecycleRequest,
  ): Promise<ScheduledTask> {
    return this.lifecycle(companyId, scheduleId, "resume", request);
  }

  cancel(
    companyId: string,
    scheduleId: string,
    request: ScheduleLifecycleRequest,
  ): Promise<ScheduledTask> {
    return this.lifecycle(companyId, scheduleId, "cancel", request);
  }

  runNow(
    companyId: string,
    scheduleId: string,
    request: ScheduleRunNowRequest,
  ): Promise<ScheduleRunRecord> {
    return apiClient.post<ScheduleRunRecord>(
      `${this.schedulePath(companyId, scheduleId)}/runs`,
      request,
    );
  }

  listRuns(
    companyId: string,
    scheduleId: string,
    request: ScheduleRunListRequest = {},
  ): Promise<ScheduleRunPage> {
    return apiClient.get<ScheduleRunPage>(
      `${this.schedulePath(companyId, scheduleId)}/runs`,
      { params: request },
    );
  }

  private lifecycle(
    companyId: string,
    scheduleId: string,
    operation: "cancel" | "pause" | "resume",
    request: ScheduleLifecycleRequest,
  ): Promise<ScheduledTask> {
    return apiClient.post<ScheduledTask>(
      `${this.schedulePath(companyId, scheduleId)}/${operation}`,
      request,
    );
  }

  private collectionPath(companyId: string) {
    return `/companies/${encodeURIComponent(companyId)}/schedules`;
  }

  private schedulePath(companyId: string, scheduleId: string) {
    return `${this.collectionPath(companyId)}/${encodeURIComponent(scheduleId)}`;
  }
}

export const scheduleService = new ScheduleService();
