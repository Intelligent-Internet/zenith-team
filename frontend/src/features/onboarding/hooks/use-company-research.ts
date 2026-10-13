import { useEffect, useState } from "react";
import {
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/features/agent-setup/api/ceo-agent-api";
import { useRuntimeModelDiscovery } from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import { findCeoAgentInPage } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  useAdvanceCompanyInitializationMutation,
  useGetCompanyInitializationQuery,
} from "@/features/onboarding/api/initialization-api";
import { lunaModelId } from "@/features/onboarding/model/research-model";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { useCan } from "@/shared/permissions";

const pollIntervalMs = 5000;
// Every Run status that has not reached a terminal outcome.
const activeStatuses = [
  "queued",
  "dispatched",
  "deferred",
  "waiting_runtime",
  "waiting_local_directory",
  "running",
];

/**
 * The founder's onboarding research once it has started: the shell status
 * strip, the Wiki banner, the Wiki rail item and the research chat all read
 * it. Only agents.manage may read the initialization, so anybody else (or an
 * error) gets nothing. Every subscriber shares one cached query that polls
 * only while the research is live; written pages reach the Wiki through its
 * own realtime events.
 */
export function useCompanyResearch(companyId: string) {
  const canRead = useCan("agents.manage");
  const [polling, setPolling] = useState(0);
  const progress = useGetCompanyInitializationQuery(companyId, {
    skip: !canRead || !companyId,
    pollingInterval: polling,
    refetchOnMountOrArgChange: true,
  });
  const state = canRead && !progress.isError ? progress.currentData : undefined;
  const active =
    state?.phase === "research" && activeStatuses.includes(state.status);
  useEffect(() => {
    setPolling(active ? pollIntervalMs : 0);
  }, [active]);
  return {
    state,
    active,
    failed: state?.phase === "research" && state.status === "failed",
    completed: state?.phase === "ready",
    waiting: state?.status === "waiting_runtime",
    pages: state?.pagesWritten ?? 0,
    overviewPageId: state?.overviewPageId ?? null,
  };
}

/**
 * Retrying a failed research starts a new Run, which needs a fresh model
 * discovery on the CEO's computer. Mount it only while the research has
 * failed: it asks that computer for its models as soon as it mounts.
 */
export function useCompanyResearchRetry(companyId: string) {
  const roster = useListCompanyAgentsQuery(companyId);
  const runtimes = useListAgentRuntimesQuery(companyId);
  const ceo = findCeoAgentInPage(roster.currentData);
  const runtime =
    runtimes.currentData?.items.find(
      (item) => item.id === ceo?.binding?.runtime.id,
    ) ?? null;
  const discovery = useRuntimeModelDiscovery({ companyId, runtime });
  const luna = discovery.models.find((model) => lunaModelId.test(model.id));
  const [advance, mutation] = useAdvanceCompanyInitializationMutation();
  const [error, setError] = useState("");
  const hint =
    runtime && runtime.status !== "online"
      ? "Reconnect your computer to retry."
      : discovery.status === "completed" && !luna
        ? "No Luna model is available on your computer."
        : "";

  const retry = async () => {
    setError("");
    try {
      await advance({
        companyId,
        request: { retry: true, modelDiscoveryId: discovery.requestId },
      }).unwrap();
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
  };

  return {
    retry,
    ready: discovery.status === "completed" && Boolean(luna),
    busy: mutation.isLoading,
    hint,
    error,
  };
}
