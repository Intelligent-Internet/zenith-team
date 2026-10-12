import { apiClient } from "@/lib/api-client";
import type {
  Workflow,
  WorkflowCatalog,
  WorkflowReadiness,
} from "@/shared/api/types";

// The Workflow catalog the installed release ships. The server reads it from
// the same embedded files a Task Run loads playbooks from, so the page never
// offers a Workflow the release cannot run.
class WorkflowService {
  list(companyId: string): Promise<WorkflowCatalog> {
    return apiClient.get<WorkflowCatalog>(this.collectionPath(companyId));
  }

  get(companyId: string, workflowKey: string): Promise<Workflow> {
    return apiClient.get<Workflow>(this.workflowPath(companyId, workflowKey));
  }

  // Which of the Workflow's apps would reach a Run of this Agent now. Any
  // member may read the states; the connector and plugin ids an owner or
  // admin needs to fix one arrive only for holders of the manage permissions.
  readiness(
    companyId: string,
    workflowKey: string,
    agentId: string,
  ): Promise<WorkflowReadiness> {
    return apiClient.get<WorkflowReadiness>(
      `${this.workflowPath(companyId, workflowKey)}/readiness`,
      { params: { agent_id: agentId } },
    );
  }

  private collectionPath(companyId: string) {
    return `/companies/${encodeURIComponent(companyId)}/workflows`;
  }

  private workflowPath(companyId: string, workflowKey: string) {
    return `${this.collectionPath(companyId)}/${encodeURIComponent(workflowKey)}`;
  }
}

export const workflowService = new WorkflowService();
