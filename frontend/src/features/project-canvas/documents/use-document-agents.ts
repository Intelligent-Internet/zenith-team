import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { agentAvailabilityLabel } from "@/features/agents/model/agent-binding";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { useGetProjectQuery } from "@/features/company-plan/api/project-api";
import { useProjectAccess } from "@/shared/permissions";
import type { CanvasScope } from "../api/canvas-api";

export function useCanInvokeDocumentAgents(scope?: CanvasScope) {
  const project = useGetProjectQuery(
    { companyId: scope?.companyId ?? "", id: scope?.projectId ?? "" },
    { skip: !scope },
  );
  const access = useProjectAccess(project.currentData);
  return !!scope && !project.isError && access.can("project.tasks.execute");
}

export function useDocumentAgents(scope?: CanvasScope) {
  const canInvoke = useCanInvokeDocumentAgents(scope);
  const companyId = canInvoke ? scope?.companyId : undefined;
  const query = useListCompanyAgentsQuery(companyId ?? "", {
    skip: !companyId,
    refetchOnMountOrArgChange: true,
  });
  const agents: ChatMentionMember[] = (
    canInvoke ? (query.currentData?.items ?? []) : []
  )
    .filter((agent) => !agent.archivedAt)
    .map((agent) => ({
      id: agent.id,
      name: agent.name,
      description: agent.title,
      kind: "agent",
      unavailable: agent.access.execute
        ? undefined
        : agentAvailabilityLabel(agent),
    }));
  return {
    agents,
    canInvoke,
    loading: query.isLoading,
    failed: canInvoke && query.isError,
    retry: query.refetch,
  };
}
