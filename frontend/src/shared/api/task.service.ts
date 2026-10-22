import { apiClient } from "@/lib/api-client";
import type { operations as ApiOperations } from "@/shared/api/generated/api.generated";
import type {
  TaskCreateRequest,
  TaskDependencySetDto,
  TaskDependencyGraphDto,
  TaskDto,
  TaskListOptions,
  TaskPageDto,
  TaskStartExecutionRequest,
  TaskStartExecutionResponse,
  TaskUpdateRequest,
  TaskUpdateResponse,
} from "@/shared/api/types";

export type TaskWorkspaceResponse =
  ApiOperations["getTaskWorkspace"]["responses"][200]["content"]["application/json"];

class TaskService {
  async listDependencyGraph(
    companyId: string,
  ): Promise<TaskDependencyGraphDto> {
    return apiClient.get<TaskDependencyGraphDto>(
      `/companies/${encodeURIComponent(companyId)}/task-dependencies`,
    );
  }
  async listTasks(
    companyId: string,
    options: TaskListOptions = {},
    signal?: AbortSignal,
  ): Promise<TaskPageDto> {
    return apiClient.get<TaskPageDto>(
      `/companies/${encodeURIComponent(companyId)}/tasks`,
      { params: options, signal },
    );
  }

  async getTask(
    companyId: string,
    taskId: string,
    signal?: AbortSignal,
  ): Promise<TaskDto> {
    return apiClient.get<TaskDto>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}`,
      { signal },
    );
  }

  async getWorkspace(
    companyId: string,
    taskId: string,
  ): Promise<TaskWorkspaceResponse> {
    return apiClient.get<TaskWorkspaceResponse>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/workspace`,
    );
  }

  async listDependencies(
    companyId: string,
    taskId: string,
  ): Promise<TaskDependencySetDto> {
    return apiClient.get<TaskDependencySetDto>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/dependencies`,
    );
  }

  async createTask(
    companyId: string,
    payload: TaskCreateRequest,
  ): Promise<TaskDto> {
    return apiClient.post<TaskDto>(
      `/companies/${encodeURIComponent(companyId)}/tasks`,
      payload,
    );
  }

  async updateTask(
    companyId: string,
    taskId: string,
    payload: TaskUpdateRequest,
  ): Promise<TaskUpdateResponse> {
    return apiClient.patch<TaskUpdateResponse>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}`,
      payload,
    );
  }

  async startExecution(
    companyId: string,
    taskId: string,
    payload: TaskStartExecutionRequest,
  ): Promise<TaskStartExecutionResponse> {
    return apiClient.post<TaskStartExecutionResponse>(
      `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/runs`,
      payload,
    );
  }
}

export const taskService = new TaskService();
