import { useListAgentRuntimesQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { useRuntimeModelDiscovery } from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import type { RuntimeDto } from "@/shared/api/types";

/** Keep the viewer's catalogs warm before any model picker opens. */
export function RuntimeModelPreloader({ companyId }: { companyId: string }) {
  const runtimes = useListAgentRuntimesQuery(companyId);
  return runtimes.currentData?.items
    .filter((runtime) => runtime.status === "online")
    .map((runtime) => (
      <RuntimeModelSubscription
        key={runtime.id}
        companyId={companyId}
        runtime={runtime}
      />
    ));
}

function RuntimeModelSubscription({
  companyId,
  runtime,
}: {
  companyId: string;
  runtime: RuntimeDto;
}) {
  useRuntimeModelDiscovery({ companyId, runtime });
  return null;
}
