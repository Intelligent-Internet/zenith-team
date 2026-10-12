import { useCallback, useEffect, useRef, useState } from "react";
import {
  connectDesktopConnector,
  openDesktopExternal,
  readDesktopConnectorStatus,
  shouldUseDesktopHashRouter,
} from "@/app/desktop/desktop-environment.service";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import {
  findCeoAgentInPage,
  runtimeMachineName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { useLocalRuntime } from "@/features/agents/hooks/use-local-runtime";
import { useRefreshPluginsMutation } from "@/features/settings/api/plugins-api";
import {
  CONNECTION_CHECK_MS,
  CONNECTION_WAIT_MS,
  PluginInstallFlow,
} from "@/features/settings/components/plugin-install-flow";
import {
  startPluginInstall,
  usePluginInstallSession,
} from "@/features/settings/model/plugin-install-session";
import type { AgentDto, PluginItemV1 } from "@/shared/api/types";
import { AppAvatar } from "@/shared/ui/app-avatar";
import { ArrowRight, RefreshCwIcon } from "@/shared/ui/icons";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { OnboardingActions } from "@/features/onboarding/company/components/onboarding-actions";
import { useConnectedAppsQuery } from "@/features/onboarding/api/connected-apps-api";
import { useOnboardingPluginsQuery } from "@/features/onboarding/api/onboarding-plugins-api";
import { onboardingSources } from "@/features/onboarding/model/onboarding-sources";

type OnboardingConnectionsPanelProps = {
  companyId: string;
  completionError?: string;
  onComplete: () => void | Promise<void>;
  onSkipCompletion: () => void;
};

export function OnboardingConnectionsPanel(
  props: OnboardingConnectionsPanelProps,
) {
  const agents = useListCompanyAgentsQuery(props.companyId);
  const agent = findCeoAgentInPage(agents.currentData);
  if (!agent) {
    return (
      <p className="onboarding-connections__state" role="status">
        {agents.isError ? (
          "Andrew could not be loaded. Refresh the page to try again."
        ) : (
          <PluginLoadingIndicator label="Loading Andrew’s connections" />
        )}
      </p>
    );
  }
  return (
    <OnboardingPluginChoices
      {...props}
      agent={agent}
      key={`${agent.id}:${agent.binding?.runtime.id ?? ""}`}
    />
  );
}

function OnboardingPluginChoices({
  companyId,
  agent,
  completionError = "",
  onComplete,
  onSkipCompletion,
}: OnboardingConnectionsPanelProps & { agent: AgentDto }) {
  const desktop = shouldUseDesktopHashRouter();
  const { local, selected } = useLocalRuntime(agent, desktop);
  const runtimeId = agent.binding?.runtime.id ?? "";
  // The daemon publishes its catalog on its own once it sees the runtime, so
  // a catalog still missing a card is polled closely until it arrives.
  const [catalogComplete, setCatalogComplete] = useState(false);
  const catalog = useOnboardingPluginsQuery(
    { companyId, runtimeId },
    {
      skip: !runtimeId,
      refetchOnFocus: true,
      refetchOnMountOrArgChange: true,
      pollingInterval: catalogComplete ? 15_000 : 3_000,
      skipPollingIfUnfocused: true,
    },
  );
  // An installed plugin is not a usable app: Gmail and Drive install
  // without ever being signed in, so each card reads its app's connection.
  const connectedApps = useConnectedAppsQuery(
    { companyId, runtimeId },
    {
      skip: !runtimeId,
      refetchOnFocus: true,
      refetchOnMountOrArgChange: true,
      pollingInterval: 15_000,
      skipPollingIfUnfocused: true,
    },
  );
  const [linkedApps, setLinkedApps] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const [startingSignIn, setStartingSignIn] = useState("");
  const [signIn, setSignIn] = useState<{
    app: ConnectorResolveIdentity;
    name: string;
    since: number;
  } | null>(null);
  const [refreshPlugins, refreshState] = useRefreshPluginsMutation();
  const installSession = usePluginInstallSession();
  const [errorMessage, setErrorMessage] = useState("");
  const [isFinishing, setIsFinishing] = useState(false);
  const discoveryAttempted = useRef(false);
  const [discoveryRunning, setDiscoveryRunning] = useState(false);
  const missingPlugins = missingOnboardingPlugins(catalog.currentData?.plugins);
  useEffect(() => {
    setCatalogComplete(Boolean(catalog.currentData) && !missingPlugins);
  }, [catalog.currentData, missingPlugins]);
  const completing = installSession.kind !== "closed" || isFinishing;
  // Background discovery leaves a listed plugin connectable; only an
  // explicit Check again holds the cards.
  const busy = completing || (refreshState.isLoading && !discoveryRunning);

  // Answers the refetched catalog, or null when the refresh failed.
  const refresh = useCallback(
    async (force = true) => {
      if (!runtimeId) return null;
      setErrorMessage("");
      try {
        if (local)
          await refreshPlugins({
            companyId,
            runtimeId,
            provider: "codex",
            force,
          }).unwrap();
        const [refreshed] = await Promise.all([
          catalog.refetch().unwrap(),
          connectedApps.refetch().unwrap(),
        ]);
        return refreshed;
      } catch (error) {
        setErrorMessage(getBoardlyQueryErrorMessage(error));
        return null;
      }
    },
    [
      companyId,
      runtimeId,
      local,
      refreshPlugins,
      catalog.refetch,
      connectedApps.refetch,
    ],
  );

  // A new Company may have a runtime before its first catalog is published,
  // and a published one goes stale. Republishing the daemon's catalog is
  // cheap, so it starts at once; only a catalog still missing the onboarding
  // plugins pays for a provider refetch. Subsequent failures need an
  // explicit retry.
  useEffect(() => {
    if (!local || !runtimeId || discoveryAttempted.current) return;
    discoveryAttempted.current = true;
    setDiscoveryRunning(true);
    void (async () => {
      const synced = await refresh(false);
      if (
        synced &&
        (!synced.observedAt || missingOnboardingPlugins(synced.plugins))
      ) {
        await refresh(true);
      }
      setDiscoveryRunning(false);
    })();
  }, [local, runtimeId, refresh]);

  const discovering =
    refreshState.isLoading ||
    discoveryRunning ||
    (local && Boolean(runtimeId) && !discoveryAttempted.current);
  const checkingRuntime = desktop && Boolean(runtimeId) && !selected;
  const loadingPlugins = checkingRuntime || catalog.isLoading || discovering;
  const setupNotice = !runtimeId
    ? "Andrew has no computer selected. Return to agent setup and select your Codex runtime."
    : !desktop
      ? "Open Zenith Team Desktop on the computer where you set up Codex to discover and connect plugins."
      : !selected
        ? ""
        : selected.status !== "online"
          ? `${runtimeMachineName(selected)} is offline. Start Zenith Team and sign in to Codex on that computer, then check again.`
          : !local
            ? `Open Zenith Team on ${runtimeMachineName(selected)} to discover and connect its plugins.`
            : discovering
              ? ""
              : !catalog.currentData?.observedAt
                ? "Codex has not reported its plugin catalog yet. Choose Check again to retry discovery."
                : missingPlugins
                  ? "Some plugins are not listed in this Codex catalog. Check again to refresh them."
                  : "";
  const connectedAppIds = new Set(
    connectedApps.currentData?.connectors.map((app) => app.nativeId),
  );
  const refetchConnectedApps = connectedApps.refetch;

  // The sign-in page has no way back into Zenith Team, so the daemon is asked
  // live, on a timer and whenever the window regains focus, until it links.
  useEffect(() => {
    if (!signIn) return;
    let cancelled = false;
    const check = async () => {
      const status = await readDesktopConnectorStatus({
        companyId,
        runtimeId,
        provider: "codex",
        nativeId: signIn.app.nativeId,
        source: signIn.app.source,
      });
      if (cancelled) return;
      if (status.ok && status.value.connected) {
        setLinkedApps((current) => new Set(current).add(signIn.app.nativeId));
        setSignIn(null);
        void refetchConnectedApps();
      } else if (Date.now() - signIn.since > CONNECTION_WAIT_MS) {
        setSignIn(null);
        setErrorMessage(
          `${signIn.name} is still not signed in. Choose Sign in to try again.`,
        );
      }
    };
    const onFocus = () => void check();
    const timer = window.setInterval(onFocus, CONNECTION_CHECK_MS);
    window.addEventListener("focus", onFocus);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      window.removeEventListener("focus", onFocus);
    };
  }, [signIn, companyId, runtimeId, refetchConnectedApps]);

  const startSignIn = async (name: string, app: ConnectorResolveIdentity) => {
    setErrorMessage("");
    setStartingSignIn(app.nativeId);
    try {
      const operation = await connectDesktopConnector({
        companyId,
        runtimeId,
        provider: "codex",
        nativeId: app.nativeId,
        source: app.source,
      });
      if (!operation.ok) throw new Error(operation.reason);
      if (operation.value.state === "failed") {
        throw new Error(`${name} could not be connected.`);
      }
      if (operation.value.continueUrl) {
        const opened = await openDesktopExternal(operation.value.continueUrl);
        if (!opened.ok) throw new Error(opened.reason);
      }
      setSignIn({ app, name, since: Date.now() });
    } catch (error) {
      setErrorMessage(
        error instanceof Error && error.message
          ? error.message
          : `${name} could not be connected.`,
      );
    } finally {
      setStartingSignIn("");
    }
  };

  const finish = async () => {
    setIsFinishing(true);
    setErrorMessage("");
    try {
      await onComplete();
    } catch {
      setErrorMessage("Setup could not be completed. Please try again.");
    } finally {
      setIsFinishing(false);
    }
  };
  return (
    <div className="onboarding-connections">
      <PluginInstallFlow companyId={companyId} />
      {setupNotice ? (
        <p className="onboarding-connections__notice" role="status">
          {setupNotice}
        </p>
      ) : null}
      <div className="onboarding-connections__grid">
        {onboardingSources.map((choice) => {
          const plugin = catalog.currentData?.plugins.find(
            (item) =>
              item.provider === "codex" && item.nativeId === choice.nativeId,
          );
          const installed = plugin?.installed === true;
          const pending =
            installSession.kind !== "closed" &&
            installSession.target.plugin.id === plugin?.id;
          const app = hostedApp(plugin);
          const appConnected =
            !!app &&
            (connectedAppIds.has(app.nativeId) || linkedApps.has(app.nativeId));
          const checkingApp =
            installed && !!app && !appConnected && connectedApps.isLoading;
          const needsSignIn =
            installed && !!app && !appConnected && !checkingApp;
          const ready = installed && (!app || appConnected);
          const waiting = !!app && signIn?.app.nativeId === app.nativeId;
          const starting = !!app && startingSignIn === app.nativeId;
          // A listed plugin is usable while discovery still runs.
          const loading = loadingPlugins && !plugin;
          return (
            <article
              aria-busy={pending || loading || waiting || checkingApp}
              className="onboarding-connection-card"
              data-connected={ready || undefined}
              data-attention={needsSignIn || undefined}
              key={choice.nativeId}
            >
              <AppAvatar name={choice.name} brand={choice.brand} size={40} />
              <div>
                <strong>{choice.name}</strong>
                <span>
                  {waiting
                    ? "Finish signing in, then come back"
                    : needsSignIn
                      ? "Sign in so Andrew can use it"
                      : ready
                        ? "Ready for Andrew"
                        : choice.description}
                </span>
              </div>
              {needsSignIn ? (
                <button
                  type="button"
                  disabled={busy || !local || waiting || starting}
                  onClick={() => {
                    if (app) void startSignIn(choice.name, app);
                  }}
                >
                  {waiting || starting ? (
                    <PluginLoadingIndicator
                      label={`Waiting for ${choice.name} sign-in`}
                    />
                  ) : (
                    "Sign in"
                  )}
                </button>
              ) : (
                <button
                  type="button"
                  disabled={
                    busy ||
                    !local ||
                    !plugin ||
                    installed ||
                    plugin.action !== "install"
                  }
                  onClick={() => {
                    if (!plugin || !selected) return;
                    startPluginInstall({
                      companyId,
                      runtimeId,
                      provider: "codex",
                      computer: runtimeMachineName(selected),
                      plugin,
                      avatar: { name: choice.name, brand: choice.brand },
                    });
                  }}
                >
                  {pending ? (
                    <PluginLoadingIndicator
                      label={`Connecting ${choice.name}`}
                    />
                  ) : checkingApp ? (
                    <PluginLoadingIndicator label={`Checking ${choice.name}`} />
                  ) : ready ? (
                    "Connected"
                  ) : loading ? (
                    <PluginLoadingIndicator label={`Loading ${choice.name}`} />
                  ) : catalog.isError ? (
                    "Could not load"
                  ) : !plugin ? (
                    "Not discovered"
                  ) : plugin.action !== "install" ? (
                    "Unavailable"
                  ) : (
                    "Connect"
                  )}
                </button>
              )}
            </article>
          );
        })}
      </div>
      {completionError ? (
        <p className="workspace-onboarding__error" role="alert">
          {completionError} Your company information is saved. Try again, or
          continue and message Andrew yourself.
        </p>
      ) : errorMessage || catalog.isError ? (
        <p className="workspace-onboarding__error" role="alert">
          {errorMessage || getBoardlyQueryErrorMessage(catalog.error)}
        </p>
      ) : null}
      <OnboardingActions
        start={
          <button
            type="button"
            className="onboarding-button onboarding-button--ghost"
            disabled={busy || !runtimeId || catalog.isFetching}
            onClick={() => void refresh()}
          >
            <RefreshCwIcon
              aria-hidden="true"
              className={
                catalog.isFetching
                  ? "animate-spin motion-reduce:animate-none"
                  : undefined
              }
              size={17}
            />
            Check again
          </button>
        }
      >
        {completionError ? (
          <button
            type="button"
            className="onboarding-button onboarding-button--secondary"
            disabled={completing}
            onClick={onSkipCompletion}
          >
            Continue anyway
          </button>
        ) : null}
        <button
          type="button"
          className="onboarding-button workspace-onboarding__submit"
          disabled={completing}
          onClick={() => void finish()}
        >
          {isFinishing ? (
            <PluginLoadingIndicator label="Continuing" />
          ) : (
            <>
              {completionError ? "Try again" : "Continue"}
              <ArrowRight aria-hidden="true" size={17} />
            </>
          )}
        </button>
      </OnboardingActions>
    </div>
  );
}

type ConnectorResolveIdentity = PluginItemV1["connectorIdentities"][number];

function missingOnboardingPlugins(plugins?: readonly PluginItemV1[]) {
  return onboardingSources.some(
    (choice) =>
      !plugins?.some(
        (plugin) =>
          plugin.provider === "codex" && plugin.nativeId === choice.nativeId,
      ),
  );
}

// The hosted app a plugin brings, whose sign-in is what makes it usable.
function hostedApp(plugin?: PluginItemV1) {
  return plugin?.connectorIdentities.find(
    (identity) => identity.source === "codex_app",
  );
}

function PluginLoadingIndicator({ label }: { label: string }) {
  return (
    <>
      <RefreshCwIcon
        aria-hidden="true"
        className="animate-spin motion-reduce:animate-none"
        size={17}
      />
      <span className="sr-only">{label}</span>
    </>
  );
}
