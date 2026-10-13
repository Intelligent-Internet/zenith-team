import { useGetRuntimeQuery } from "@/shared/api/agent-runtime-api";
import type { AgentDto } from "@/shared/api/types";
import { useLocalDaemonId } from "./use-local-daemon-id";

// useLocalRuntime says whether the computer the VIEWER runs this Agent on is
// the one this desktop's daemon registered, and online — the only runtime
// the local daemon can operate tools on or re-discover for (ADR 0014). It
// reads that runtime row and follows the desktop daemon's status. The read
// is skipped when the viewer could not act on the answer (`enabled` false)
// and when they have not set the Agent up anywhere.
export function useLocalRuntime(agent: AgentDto, enabled: boolean) {
  const { daemonId } = useLocalDaemonId();
  const boundRuntimeId = agent.binding?.runtime.id ?? "";
  const args = { companyId: agent.companyId, runtimeId: boundRuntimeId };
  const runtime = useGetRuntimeQuery(args, {
    refetchOnMountOrArgChange: 30,
    skip: !enabled || !boundRuntimeId,
  });
  const selected = runtime.currentData;
  const local = Boolean(
    selected && selected.daemonId === daemonId && selected.status === "online",
  );
  return { local, refetch: runtime.refetch, selected };
}
