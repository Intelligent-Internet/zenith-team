import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { agentAvailabilityLabel } from "@/features/agents/model/agent-binding";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { useGetProjectQuery } from "@/features/company-plan/api/project-api";
import { useProjectAccess } from "@/shared/permissions";
import { useLiveArtifactAccessQuery } from "../api/artifact-access-api";
export type DocumentAgentScope = {
  companyId: string;
  projectId?: string;
  pageId?: string;
};

export function useCanInvokeDocumentAgents(scope?: DocumentAgentScope) {
  const project = useGetProjectQuery(
    { companyId: scope?.companyId ?? "", id: scope?.projectId ?? "" },
    { skip: !scope?.projectId },
  );
  const page = useLiveArtifactAccessQuery(
    { companyId: scope?.companyId ?? "", artifactId: scope?.pageId ?? "" },
    { skip: !scope?.pageId || !!scope?.projectId },
  );
  const access = useProjectAccess(project.currentData);
  if (scope?.pageId && !scope.projectId)
    return !page.isError && !!page.currentData?.canEdit;
  return !!scope && !project.isError && access.can("project.tasks.execute");
}

export function useDocumentAgents(scope?: DocumentAgentScope) {
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
