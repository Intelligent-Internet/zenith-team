import { useEffect, useState } from "react";

import {
  getCeoAgentApiErrorMessage,
  useRefreshRuntimeMutation,
} from "@/features/agent-setup/api/ceo-agent-api";
import { toast } from "@/shared/ui/sonner";

// How long a "Refresh setup" control holds its checking state after the
// daemon was asked: the fresh registration usually lands within a few
// seconds and the roster refetch on daemon.roster_changed shows it, so the
// hold only has to make a second click pointless.
const RUNTIME_REFRESH_SETTLE_MS = 6000;

// One "ask this computer to check its provider setup again" control: the
// mutation, a per-target pending state that settles on its own, and the
// toasts. Both the roster page and the runtime page render it.
export function useRuntimeSetupRefresh(companyId: string) {
  const [refreshRuntime] = useRefreshRuntimeMutation();
  const [pendingTarget, setPendingTarget] = useState<string | null>(null);
  useEffect(() => {
    if (!pendingTarget) return;
    const timer = setTimeout(
      () => setPendingTarget(null),
      RUNTIME_REFRESH_SETTLE_MS,
    );
    return () => clearTimeout(timer);
  }, [pendingTarget]);

  // target is what the caller marks pending (a daemon id or a runtime id);
  // runtimeId is any online runtime of that computer, since one request
  // covers every provider on the machine.
  const refresh = async (target: string, runtimeId: string) => {
    setPendingTarget(target);
    try {
      await refreshRuntime({ companyId, runtimeId }).unwrap();
      toast.success("Checking provider setup on this computer…");
    } catch (error) {
      setPendingTarget(null);
      toast.error(
        getCeoAgentApiErrorMessage(
          error,
          "The computer could not be asked to refresh its setup.",
        ),
      );
    }
  };

  return {
    isPending: (target: string) => pendingTarget === target,
    refresh,
  };
}
