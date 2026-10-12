import { skipToken } from "@reduxjs/toolkit/query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type PropsWithChildren,
} from "react";
import { useLocation } from "react-router";
import {
  markSessionCompanyInitializationPending,
  refreshSessionSnapshot,
} from "@/app/session/session";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import {
  useListCompanyAgentsQuery,
  useListAgentRuntimesQuery,
} from "@/features/agent-setup/api/ceo-agent-api";
import { useRuntimeModelDiscovery } from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import { findCeoAgentInPage } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  useGetCompanyInitializationQuery,
  useAdvanceCompanyInitializationMutation,
} from "@/features/onboarding/api/initialization-api";
import { initializationStatus } from "@/features/onboarding/model/initialization-status";
import {
  boardlyApi,
  getBoardlyQueryErrorMessage,
} from "@/shared/api/boardly-api";
import { useAppDispatch } from "@/state";

const InitializationContext = createContext<ReturnType<
  typeof useInitializationController
> | null>(null);

export function CompanyInitializationProvider({ children }: PropsWithChildren) {
  const session = useSessionSnapshot();
  return (
    <ScopedInitializationProvider
      key={`${session.userId ?? "anonymous"}:${session.company?.id ?? "none"}`}
    >
      {children}
    </ScopedInitializationProvider>
  );
}

function ScopedInitializationProvider({ children }: PropsWithChildren) {
  const controller = useInitializationController();
  return (
    <InitializationContext.Provider value={controller}>
      {children}
    </InitializationContext.Provider>
  );
}

export function useCompanyInitialization() {
  return useContext(InitializationContext);
}

function useInitializationController() {
  const session = useSessionSnapshot();
  const location = useLocation();
  const dispatch = useAppDispatch();
  const companyId = session.company?.id ?? "";
  const canManage = Boolean(
    session.status === "authenticated" &&
    session.company?.permissions?.includes("agents.manage"),
  );
  const [tracking, setTracking] = useState(false);
  const [ready, setReady] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const observing = Boolean(
    companyId &&
    canManage &&
    (session.company?.initializationPending ||
      location.pathname === "/onboarding" ||
      tracking),
  );
  const progress = useGetCompanyInitializationQuery(
    observing ? companyId : skipToken,
    { pollingInterval: ready ? 0 : 2000, refetchOnMountOrArgChange: true },
  );
  const state = observing ? progress.currentData : undefined;
  const hasReceipt = Boolean(state?.conversationId);
  const roster = useListCompanyAgentsQuery(companyId, {
    skip: !observing,
    pollingInterval: ready ? 0 : 5000,
  });
  const runtimes = useListAgentRuntimesQuery(companyId, {
    skip: !observing || ready,
    pollingInterval: 5000,
  });
  const ceo = findCeoAgentInPage(roster.currentData);
  const runtime =
    runtimes.currentData?.items.find(
      (item) => item.id === ceo?.binding?.runtime.id,
    ) ?? null;
  const needsModel =
    state?.phase === "not_started" ||
    (state?.phase === "research" &&
      (state.status === "failed" || state.status === "awaiting_model"));
  const [discoveryEnabled, setDiscoveryEnabled] = useState(false);
  const discovery = useRuntimeModelDiscovery({
    companyId,
    runtime: observing && needsModel && discoveryEnabled ? runtime : null,
    requireFreshRequest: true,
  });
  const luna = discovery.models.find((model) =>
    /^(?:luna|gpt-\d+(?:\.\d+)?-luna)$/.test(model.id),
  );
  const [advance, mutation] = useAdvanceCompanyInitializationMutation();
  const actionInFlight = useRef(false);
  const [requesting, setRequesting] = useState(false);
  const [requestedResearch, setRequestedResearch] = useState<{
    retry: boolean;
  } | null>(null);
  const [error, setError] = useState("");
  const completionObserved = useRef(false);

  useEffect(() => {
    if (!state?.conversationId || !session.userId) return;
    setTracking(true);
    const completed =
      state.phase === "ready" && !session.company?.initializationPending;
    setReady(completed);
    // A saved active receipt proves pending work. Only the refreshed Company
    // projection can clear it: phase=ready may precede the worker's final commit.
    if (state.phase !== "ready")
      markSessionCompanyInitializationPending(companyId, session.userId);
    if (completed && !completionObserved.current) {
      completionObserved.current = true;
      setError("");
      dispatch(boardlyApi.util.invalidateTags(["Company", "Conversation"]));
    }
  }, [
    companyId,
    dispatch,
    session.company?.initializationPending,
    session.userId,
    state,
  ]);

  useEffect(() => {
    if (state?.phase !== "ready" || !session.company?.initializationPending)
      return;
    let cancelled = false;
    let refreshing = false;
    const refresh = async () => {
      if (refreshing) return;
      refreshing = true;
      try {
        await refreshSessionSnapshot();
      } catch {
        if (!cancelled)
          setError(
            "Your welcome is ready, but completion could not be confirmed. Check your connection and try again.",
          );
      } finally {
        refreshing = false;
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2500);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [session.company?.initializationPending, state?.phase]);

  const continuePreparation = useCallback(async () => {
    if (!observing || actionInFlight.current) return;
    actionInFlight.current = true;
    setRequesting(true);
    setError("");
    setDismissed(false);
    try {
      let current = await progress.refetch().unwrap();
      if (current.phase === "not_started") {
        // Reserve first so leaving this screen cannot lose the preparation.
        current = await advance({
          companyId,
          start: true,
          request: { retry: false },
        }).unwrap();
      }
      if (
        current.phase === "research" &&
        (current.status === "awaiting_model" || current.status === "failed")
      ) {
        setRequestedResearch({ retry: current.status === "failed" });
        setDiscoveryEnabled(true);
        if (discoveryEnabled) discovery.refresh();
      } else if (current.status === "failed") {
        await advance({ companyId, request: { retry: true } }).unwrap();
      } else if (current.phase === "ready") {
        await Promise.all([
          roster.refetch().unwrap(),
          refreshSessionSnapshot(),
        ]);
      }
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    } finally {
      actionInFlight.current = false;
      setRequesting(false);
    }
  }, [
    advance,
    companyId,
    discovery,
    discoveryEnabled,
    observing,
    progress,
    roster,
  ]);

  // Only an explicit start/continue/retry authorizes model discovery and this
  // POST. Research-to-welcome and completion are advanced by the server worker.
  useEffect(() => {
    if (
      !requestedResearch ||
      !needsModel ||
      !luna ||
      discovery.status !== "completed" ||
      !discovery.requestId ||
      actionInFlight.current
    )
      return;
    actionInFlight.current = true;
    setRequestedResearch(null);
    void advance({
      companyId,
      request: {
        retry: requestedResearch.retry,
        modelDiscoveryId: discovery.requestId,
      },
    })
      .unwrap()
      .then(() => setDiscoveryEnabled(false))
      .catch((cause) => setError(getBoardlyQueryErrorMessage(cause)))
      .finally(() => {
        actionInFlight.current = false;
      });
  }, [
    advance,
    companyId,
    discovery.requestId,
    discovery.status,
    luna,
    needsModel,
    requesting,
    requestedResearch,
  ]);

  const modelError =
    needsModel && discoveryEnabled
      ? discovery.status === "completed" && !luna
        ? "This Codex runtime offers no Luna model. Update Codex on that computer, then try again."
        : ["failed", "timeout", "unavailable"].includes(discovery.status)
          ? discovery.message ||
            "We couldn’t check the available models. Try again."
          : ""
      : "";
  const failure = ready
    ? ""
    : error ||
      modelError ||
      (progress.error ? getBoardlyQueryErrorMessage(progress.error) : "") ||
      (roster.error ? getBoardlyQueryErrorMessage(roster.error) : "") ||
      (runtimes.error ? getBoardlyQueryErrorMessage(runtimes.error) : "") ||
      (needsModel && !roster.isLoading && roster.currentData && !ceo?.binding
        ? "Your CEO needs a Codex connection before continuing."
        : "") ||
      (state?.status === "failed"
        ? state.message || "Preparation could not finish. Try again."
        : "");
  const offline =
    state?.status === "waiting_runtime" ||
    Boolean(needsModel && runtime && runtime.status !== "online");
  const working =
    requesting ||
    mutation.isLoading ||
    Boolean(requestedResearch && discovery.status === "pending");
  const ceoName = ceo?.name || "Your CEO";
  const statusText = failure
    ? "Preparation needs your attention"
    : offline
      ? "Waiting for your computer to reconnect"
      : working && needsModel
        ? `Getting ${ceoName} ready`
        : state?.phase === "ready" && !ready
          ? "Finishing your welcome"
          : initializationStatus(state, ceoName);

  return {
    companyId,
    state,
    ceo,
    ceoName,
    needsModel,
    hasReceipt,
    completed: ready,
    conversationPending:
      hasReceipt &&
      (state?.phase !== "ready" ||
        Boolean(session.company?.initializationPending)),
    failure,
    offline,
    working,
    waitingForStart:
      state?.status === "awaiting_model" && !requestedResearch && !working,
    statusText,
    continuePreparation,
    canContinue: Boolean(
      failure ||
      (ready && !ceo) ||
      state?.phase === "not_started" ||
      state?.status === "awaiting_model",
    ),
    showWorkspaceStatus:
      observing &&
      (hasReceipt || Boolean(session.company?.initializationPending)) &&
      !dismissed,
    dismiss: () => setDismissed(true),
  };
}
