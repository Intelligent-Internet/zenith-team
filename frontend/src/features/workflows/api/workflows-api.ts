import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type {
  Workflow,
  WorkflowCatalog,
  WorkflowReadiness,
} from "@/shared/api/types";
import { workflowService } from "@/shared/api/workflow.service";

type WorkflowArguments = {
  companyId: string;
  workflowKey: string;
};

type WorkflowReadinessArguments = WorkflowArguments & {
  agentId: string;
};

// Reopening an older readiness snapshot revalidates provider freshness.
// Committed discovery, bindings and runtime status refresh mounted queries.
export const WORKFLOW_READINESS_REMOUNT_REFETCH_SECONDS = 10;

export const workflowsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkflows: builder.query<WorkflowCatalog, string>({
      providesTags: (_result, _error, companyId) => [
        { id: `${companyId}:list`, type: "Workflow" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => workflowService.list(companyId),
          "Workflows could not be loaded.",
          { retry: true },
        ),
    }),
    // No retry: an unknown key is an expected 404 from a stale deep link,
    // and asking twice would only delay the dialog's answer.
    getWorkflow: builder.query<Workflow, WorkflowArguments>({
      providesTags: (_result, _error, { companyId, workflowKey }) => [
        { id: `${companyId}:${workflowKey}`, type: "Workflow" },
      ],
      queryFn: ({ companyId, workflowKey }) =>
        executeBoardlyQuery(
          () => workflowService.get(companyId, workflowKey),
          "This workflow could not be loaded.",
        ),
    }),
    // Tagged with the Company's Connector and Plugin catalogues, so a
    // plugin refresh (which re-discovers connectors on desktop), an install
    // or a reconnect re-reads every open readiness answer; and with the
    // viewer's Agent setups, because readiness is read off the viewer's own
    // binding of the Agent (ADR 0014). No retry: an older server without
    // the route answers 404, and the dialog then keeps its static app chips.
    getWorkflowReadiness: builder.query<
      WorkflowReadiness,
      WorkflowReadinessArguments
    >({
      providesTags: (_result, _error, { agentId, companyId, workflowKey }) => [
        { id: companyId, type: "AgentBinding" },
        { id: companyId, type: "Connector" },
        { id: companyId, type: "Plugin" },
        { id: companyId, type: "Runtime" },
        {
          id: `${companyId}:${workflowKey}:readiness:${agentId}`,
          type: "Workflow",
        },
      ],
      queryFn: ({ agentId, companyId, workflowKey }) =>
        executeBoardlyQuery(
          () => workflowService.readiness(companyId, workflowKey, agentId),
          "App readiness could not be loaded.",
        ),
    }),
  }),
});

export const {
  useGetWorkflowQuery,
  useGetWorkflowReadinessQuery,
  useListWorkflowsQuery,
} = workflowsApi;
