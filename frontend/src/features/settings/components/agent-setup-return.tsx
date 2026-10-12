import { Link, useSearchParams } from "react-router";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { ArrowLeft } from "@/shared/ui/icons";

export function AgentSetupReturn() {
  const [params] = useSearchParams();
  const agentId = params.get("returnAgentId");
  const companyId = useSessionSnapshot().company?.id ?? "";
  const roster = useListCompanyAgentsQuery(companyId, {
    skip: !companyId || !agentId,
  });
  const agent =
    !roster.isError &&
    roster.currentData?.items.find((item) => item.id === agentId);
  if (!agent) return null;
  return (
    <Link
      className="dashboard-button w-fit"
      to={`/agents/${agent.id}?tab=capabilities&section=connections`}
    >
      <ArrowLeft aria-hidden size={14} /> Back to {agent.name}
    </Link>
  );
}
