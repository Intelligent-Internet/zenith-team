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
import { lunaModelId } from "@/features/onboarding/model/research-model";
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
  // Once the research Run exists the server owns it: it works without a
  // client, writes the Wiki, and notifies the founder. This controller only
  // gets a receipt to that point, so it stops observing there.
  const started = Boolean(state?.runId);
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
  const needsModel = state?.phase === "not_started";
  const [discoveryEnabled, setDiscoveryEnabled] = useState(false);
  const discovery = useRuntimeModelDiscovery({
    companyId,
    runtime: observing && needsModel && discoveryEnabled ? runtime : null,
    requireFreshRequest: true,
  });
  const luna = discovery.models.find((model) => lunaModelId.test(model.id));
  const [advance, mutation] = useAdvanceCompanyInitializationMutation();
  const actionInFlight = useRef(false);
  const [requesting, setRequesting] = useState(false);
  const [requestedResearch, setRequestedResearch] = useState(false);
  const [error, setError] = useState("");
  const completionObserved = useRef(false);

  useEffect(() => {
    if (!state?.conversationId || !session.userId) return;
    setTracking(!started);
    const completed = started && !session.company?.initializationPending;
    setReady(completed);
    // A receipt without a research Run is pending work. Only the refreshed
    // Company projection clears it, so a started Run may precede that commit.
    if (!started)
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
    started,
    state?.conversationId,
  ]);

  useEffect(() => {
    if (!started || !session.company?.initializationPending) return;
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
            "Your company research has started, but we could not confirm it. Check your connection and try again.",
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
  }, [session.company?.initializationPending, started]);

  const continuePreparation = useCallback(async () => {
    if (!observing || actionInFlight.current) return;
    actionInFlight.current = true;
    setRequesting(true);
    setError("");
    setDismissed(false);
    try {
      let current = await progress.refetch().unwrap();
      if (!current.conversationId) {
        // Reserve first so leaving this screen cannot lose the preparation.
        current = await advance({
          companyId,
          start: true,
          request: { retry: false },
        }).unwrap();
      }
      if (current.phase === "not_started") {
        setRequestedResearch(true);
        setDiscoveryEnabled(true);
        if (discoveryEnabled) discovery.refresh();
      } else {
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

  // Only an explicit start or continue authorizes model discovery and this
  // POST. Everything after the Run is created is advanced by the server.
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
    setRequestedResearch(false);
    void advance({
      companyId,
      request: { retry: false, modelDiscoveryId: discovery.requestId },
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
      "";
  const offline = Boolean(needsModel && runtime && runtime.status !== "online");
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
        : started
          ? "Your company research is running"
          : initializationStatus(state, ceoName);

  return {
    companyId,
    state,
    ceo,
    ceoName,
    needsModel,
    hasReceipt,
    started,
    // The CEO's chat is held only until the research Run exists.
    conversationPending: hasReceipt && !started,
    failure,
    offline,
    working,
    waitingForStart: needsModel && hasReceipt && !requestedResearch && !working,
    statusText,
    continuePreparation,
    canContinue: Boolean(
      !started && (failure || state?.phase === "not_started"),
    ),
    showWorkspaceStatus:
      observing &&
      !started &&
      (hasReceipt || Boolean(session.company?.initializationPending)) &&
      !dismissed,
    dismiss: () => setDismissed(true),
  };
}
