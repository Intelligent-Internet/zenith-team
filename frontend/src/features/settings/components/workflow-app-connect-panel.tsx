import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type Ref,
} from "react";
import { Link } from "react-router";

import {
  connectDesktopConnector,
  openDesktopExternal,
  readDesktopConnectorStatus,
  shouldUseDesktopHashRouter,
} from "@/shared/desktop/desktop-environment.service";
import {
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import {
  providerLabel,
  runtimeMachineName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import { useLocalRuntime } from "@/features/agents/hooks/use-local-runtime";
import { useReconnectConnectorMutation } from "@/features/settings/api/connector-reviews-api";
import {
  useGetPluginQuery,
  useRefreshPluginsMutation,
} from "@/features/settings/api/plugins-api";
import {
  CONNECTION_CHECK_MS,
  CONNECTION_WAIT_MS,
} from "@/features/settings/components/plugin-install-flow";
import {
  pluginInstallBusy,
  pluginInstallProgress,
  startPluginInstall,
  usePluginInstallSession,
} from "@/features/settings/model/plugin-install-session";
import {
  useGetWorkflowQuery,
  useGetWorkflowReadinessQuery,
  WORKFLOW_READINESS_REMOUNT_REFETCH_SECONDS,
} from "@/features/workflows/api/workflows-api";
import { workflowConnectorDisplayFor } from "@/features/workflows/model/workflow-catalog";
import type {
  AgentDto,
  ConnectorProvider,
  WorkflowAppConnector,
  WorkflowAppPlugin,
  WorkflowAppReadiness,
  WorkflowConnectorType,
  WorkflowReadiness,
} from "@/shared/api/types";
import { useCan } from "@/shared/permissions";
import { AppAvatar, pluginAvatarProps } from "@/shared/ui/app-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Icon } from "@/shared/ui/icon";
import {
  ArrowLeft,
  ArrowUpRight,
  Ban,
  Check,
  Ellipsis,
  RefreshCwIcon,
  Workflow,
} from "@/shared/ui/icons";

// Where the daemon itself sends people to manage a Claude connector: Claude.ai
// for its cloud connectors, Claude Code's MCP guide for plugins and servers.
const CLAUDE_CONNECTORS_URL = "https://claude.com/connectors";
const CLAUDE_CODE_MCP_URL = "https://code.claude.com/docs/en/mcp";

// While a connected app waits for its computer's report, readiness is read
// on this beat: the daemon re-reads its linked apps in seconds once the live
// check sees one connect. After LINKED_WAIT_MS without the report, Check
// again takes over.
const LINKED_WAIT_MS = 60_000;
// How long the line says "Not connected yet" after Check again.
const NOT_CONNECTED_FLASH_MS = 3_000;

type PanelActionButton = "primary" | "check" | "open";

/** An app whose connect page is open in the browser. */
type ConnectionWatch = {
  nativeId: string;
  since: number;
  /** The connect page, which **Open browser** opens again. */
  continueUrl?: string;
};

/** An app the live check saw connect, while readiness catches up. */
type LinkedApp = {
  nativeId: string;
  since: number;
};

// When the panel can't check an app live, it checks when the window
// regains focus.
function focusCheckNotice(appLabel: string) {
  return `Finish connecting ${appLabel} in your browser, then come back. Zenith Team checks again when this window is active.`;
}

type WorkflowAppConnectPanelProps = {
  agentId: string;
  app: WorkflowConnectorType;
  companyId: string;
  /** Shows the pinned plugin's details, where it is uninstalled. */
  onShowPluginDetails?: (plugin: WorkflowAppPlugin) => void;
  workflowKey: string;
};

/**
 * The top of Settings › Plugins when the Workflows launch dialog sent the
 * viewer here for one app. It reads the same readiness the chip
 * showed, for the same Agent on the viewer's own computer (ADR 0014), and
 * offers what that state needs: set the Agent up there first, connect or
 * install the app (every member's, on their own computer), review its
 * changed tools (an owner's or admin's), or check again — or, for a
 * connected app, manage it: its account in the provider, its tools, turning
 * it off for the Agent, and its plugin. The way back reopens the Workflow,
 * whose dialog restores the saved answers.
 */
export function WorkflowAppConnectPanel({
  agentId,
  app,
  companyId,
  onShowPluginDetails,
  workflowKey,
}: WorkflowAppConnectPanelProps) {
  const workflowQuery = useGetWorkflowQuery(
    { companyId, workflowKey },
    { skip: !companyId },
  );
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const agent = agentsQuery.currentData?.items.find(
    (candidate) => candidate.id === agentId && !candidate.archivedAt,
  );
  const workflowName = workflowQuery.currentData?.name;
  const workflowIcon = workflowQuery.currentData?.icon;
  const workflowPath = `/workflows/${encodeURIComponent(workflowKey)}`;
  const appLabel = workflowConnectorDisplayFor(app).label;

  const back = (
    <Link className="workflow-app-connect__back" to={workflowPath}>
      <ArrowLeft aria-hidden className="size-4" />
      Back to {workflowName ?? "the workflow"}
    </Link>
  );

  if (!agent) {
    const loading = !agentsQuery.currentData && !agentsQuery.isError;
    return (
      <PanelFrame
        app={app}
        back={back}
        line={
          loading
            ? "Loading the Agent this workflow runs as…"
            : agentsQuery.isError
              ? "Agents could not be loaded. Go back to the workflow and try again."
              : "The Agent chosen for this workflow is no longer available. Go back and pick another."
        }
        state={loading ? "loading" : "error"}
        title={
          <h3>{loading ? `Checking ${appLabel}…` : `Connect ${appLabel}`}</h3>
        }
        workflowIcon={workflowIcon}
      />
    );
  }

  return (
    <WorkflowAppConnectBody
      agent={agent}
      app={app}
      back={back}
      companyId={companyId}
      workflowIcon={workflowIcon}
      workflowKey={workflowKey}
      workflowName={workflowName}
      workflowPath={workflowPath}
      onShowPluginDetails={onShowPluginDetails}
    />
  );
}

function WorkflowAppConnectBody({
  agent,
  app,
  back,
  companyId,
  onShowPluginDetails,
  workflowIcon,
  workflowKey,
  workflowName,
  workflowPath,
}: {
  agent: AgentDto;
  app: WorkflowConnectorType;
  back: ReactNode;
  companyId: string;
  onShowPluginDetails?: (plugin: WorkflowAppPlugin) => void;
  workflowIcon?: string;
  workflowKey: string;
  workflowName?: string;
  workflowPath: string;
}) {
  const isDesktop = shouldUseDesktopHashRouter();
  // Keep the connection receipt until discovery confirms it through realtime.
  const [linkedApp, setLinkedApp] = useState<LinkedApp | null>(null);
  const readinessQuery = useGetWorkflowReadinessQuery(
    { agentId: agent.id, companyId, workflowKey },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: WORKFLOW_READINESS_REMOUNT_REFETCH_SECONDS,
      skip: !companyId,
    },
  );
  const readiness = readinessQuery.currentData;
  const entry = readiness?.apps.find((candidate) => candidate.app === app);
  // Connecting, installing and re-discovering all go through the daemon on
  // the computer the viewer set the Agent up on (ADR 0014), so they need
  // this desktop to be that computer.
  const { local } = useLocalRuntime(agent, isDesktop);
  const provider = readiness?.provider;
  // Empty while the viewer has not set the Agent up: there is no computer
  // of theirs to read or act on, and nothing below asks the daemon.
  const boundRuntime = agent.binding?.runtime;
  const runtimeId = readiness?.runtimeId ?? boundRuntime?.id ?? "";
  // The computer alone ("MacBook Pro"), as the Plugins page names it: the
  // Runtime's own name also carries its provider ("Codex (MacBook Pro)").
  const runtimesQuery = useListAgentRuntimesQuery(companyId, {
    skip: !companyId,
  });
  const computerRuntime =
    runtimesQuery.currentData?.items.find(
      (runtime) => runtime.id === runtimeId,
    ) ?? boundRuntime;
  const computer = computerRuntime
    ? runtimeMachineName(computerRuntime)
    : "your computer";
  // Setting the Agent up is the first step when the viewer has not; the
  // setup re-reads readiness, which moves the panel on.
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  const display = workflowConnectorDisplayFor(app);
  const appLabel = display.label;
  const providerName = provider ? providerLabel(provider) : "the provider";
  const workflowLabel = workflowName ?? "this workflow";

  const [refreshPlugins] = useRefreshPluginsMutation();
  const [reconnectConnector] = useReconnectConnectorMutation();
  // Accepting an app's changed tools is Company Connector policy, which
  // stays with owners and admins; connecting and installing are everyone's.
  const canReview = useCan("connectors.manage");
  // Install {Plugin} starts the one install flow, as the Plugins list's + and
  // a plugin's page do, so an app the plugin must connect first connects
  // before anything installs (D27). The flow takes the plugin as the catalog
  // lists it.
  const installablePlugin =
    local &&
    entry?.state === "needs_connection" &&
    !entry.connector &&
    entry.plugin?.action === "install"
      ? entry.plugin
      : undefined;
  const installableQuery = useGetPluginQuery(
    { companyId, pluginId: installablePlugin?.id ?? "", runtimeId },
    { skip: !installablePlugin },
  );
  const installSession = usePluginInstallSession();
  // Which button's request is running; both are disabled meanwhile.
  const [busy, setBusy] = useState<PanelActionButton | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  // The person opened a connected app's settings in its provider, where it
  // may be disconnected or given another account; coming back checks again.
  const [managedInProvider, setManagedInProvider] = useState(false);
  // The app whose connect page is open in the browser, checked live until
  // it connects.
  const [watch, setWatch] = useState<ConnectionWatch | null>(null);
  // A re-discovery Check again asked for: its button shows it running, and
  // no other button waits on it.
  const [rediscovering, setRediscovering] = useState(false);
  // Check again found the app still unconnected: the line says so briefly.
  const [notConnectedAt, setNotConnectedAt] = useState<number | null>(null);
  // Apps whose wait for readiness ran out: the live check has nothing more
  // to tell about them, so a later check re-discovers.
  const linkedTimedOut = useRef(new Set<string>());
  const arrivalBinding = useRef("");
  const rediscovery = useRef<Promise<unknown> | null>(null);
  const refreshStartedAt = useRef(0);
  const entryState = entry?.state;
  const panelRef = useRef<HTMLElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  // The action button to give focus back to once a request settles.
  const focusAfterAction = useRef<PanelActionButton | null>(null);
  // A Codex app that is not linked yet is checked live through the daemon
  // (`app/installed`, about a second), as the plugin install flow does
  // (D27), not by re-discovering every plugin and app on the computer,
  // which takes a minute or more on a Codex account with many apps. A
  // linked app that isn't usable yet (`installed_not_ready`) has nothing
  // new to tell a live check, so it is re-discovered.
  const liveConnector =
    local &&
    provider === "codex" &&
    entryState === "needs_connection" &&
    entry?.connector?.source === "codex_app" &&
    entry.connector.status === "available"
      ? entry.connector
      : undefined;
  const liveNativeId = useRef<string | undefined>(undefined);
  liveNativeId.current = liveConnector?.nativeId;
  // The live check saw the app connect and readiness has not caught up.
  const linked =
    linkedApp !== null && linkedApp.nativeId === liveConnector?.nativeId;
  // A connection readiness has since shown, or an answer that arrived after
  // the app moved on, stops the quick readiness beat.
  useEffect(() => {
    if (linkedApp && !linked) setLinkedApp(null);
  }, [linked, linkedApp]);

  // A notice or error describes the state it was raised in; once the app
  // moves on (say, connected after the person returns), it no longer holds.
  // Neither does a visit to the provider, nor a wait on the browser: the
  // change they were for has arrived.
  const entryKey = `${entryState ?? ""}:${entry?.connector?.status ?? ""}`;
  useEffect(() => {
    setError("");
    setNotice("");
    setManagedInProvider(false);
    setWatch(null);
    setLinkedApp(null);
    setNotConnectedAt(null);
  }, [entryKey]);

  useEffect(() => {
    if (notConnectedAt === null) return;
    const timer = window.setTimeout(
      () => setNotConnectedAt(null),
      NOT_CONNECTED_FLASH_MS,
    );
    return () => window.clearTimeout(timer);
  }, [notConnectedAt]);

  // Whether the daemon sees the app connected now: undefined when it can't
  // say (an older daemon, or the app is no longer discovered).
  const readLiveConnection = useCallback(
    async (nativeId: string) => {
      const status = await readDesktopConnectorStatus({
        companyId,
        nativeId,
        provider: "codex",
        runtimeId,
        source: "codex_app",
      });
      return status.ok ? status.value.connected : undefined;
    },
    [companyId, runtimeId],
  );

  // What the daemon's live answer means for the app. Connected: the daemon
  // re-reads its linked apps and readiness shows it within seconds, so the
  // panel waits for that ("linked"), unless a wait for it already ran out.
  // Not connected: nothing to do. No answer (an older daemon) or an
  // outlasted wait: only a re-discovery can tell more ("rediscover").
  const settleLiveAnswer = useCallback(
    (nativeId: string, connected: boolean | undefined) => {
      if (connected === false) return "not_connected" as const;
      if (!connected || linkedTimedOut.current.has(nativeId)) {
        return "rediscover" as const;
      }
      if (liveNativeId.current === nativeId) {
        setWatch(null);
        setError("");
        setNotice("");
        setLinkedApp({ nativeId, since: Date.now() });
      }
      return "linked" as const;
    },
    [],
  );

  // Re-discovers the computer's plugins and apps in the background. It can
  // take a minute or more, so no button waits on one it did not start, and
  // calls made meanwhile share the one running.
  const rediscover = useCallback(() => {
    if (!provider) return Promise.resolve();
    if (!rediscovery.current) {
      refreshStartedAt.current = Date.now();
      rediscovery.current = refreshPlugins({ companyId, provider, runtimeId })
        .unwrap()
        .finally(() => {
          rediscovery.current = null;
        });
    }
    return rediscovery.current;
  }, [companyId, provider, refreshPlugins, runtimeId]);

  // A forced re-discovery rewrites the Runtime's whole catalog, so it is
  // asked for only while the person may be finishing setup elsewhere; a
  // connected app has nothing to find unless they left to change it in its
  // provider, and a managed or unoffered one never does.
  const awaitingSetup =
    entryState === "needs_connection" ||
    entryState === "review_required" ||
    entryState === "pending" ||
    entryState === "unknown" ||
    (entryState === "ready" && managedInProvider);

  // What changed in the browser shows without a manual step: checked once
  // on arrival and again when the window regains focus, live for a Codex app
  // awaiting its connection, else by re-discovering in the background.
  const lookForChanges = useCallback(() => {
    const rediscoverQuietly = () => {
      if (Date.now() - refreshStartedAt.current < 5_000) return;
      void rediscover().catch(() => undefined);
    };
    if (!liveConnector) {
      rediscoverQuietly();
      return;
    }
    const { nativeId } = liveConnector;
    void readLiveConnection(nativeId).then((connected) => {
      if (settleLiveAnswer(nativeId, connected) === "rediscover") {
        rediscoverQuietly();
      }
    });
  }, [liveConnector, readLiveConnection, rediscover, settleLiveAnswer]);

  useEffect(() => {
    if (!local || !provider || !companyId || !entryState) return;
    const binding = `${companyId}:${provider}:${runtimeId}`;
    if (arrivalBinding.current === binding) return;
    arrivalBinding.current = binding;
    if (awaitingSetup) lookForChanges();
  }, [
    awaitingSetup,
    companyId,
    entryState,
    local,
    lookForChanges,
    provider,
    runtimeId,
  ]);

  useEffect(() => {
    if (!local || !provider || !companyId || !awaitingSetup) return;
    if (watch || linked) return;
    window.addEventListener("focus", lookForChanges);
    return () => window.removeEventListener("focus", lookForChanges);
  }, [
    awaitingSetup,
    companyId,
    linked,
    local,
    lookForChanges,
    provider,
    watch,
  ]);

  // An app connecting in the browser is checked every few seconds and on
  // focus, for at most ten minutes, as in the plugin install flow.
  useEffect(() => {
    if (!watch) return;
    let checking = false;
    const check = async () => {
      if (checking) return;
      if (Date.now() - watch.since > CONNECTION_WAIT_MS) {
        setWatch(null);
        setNotice(
          `${appLabel} isn't connected yet. Connect it again when you're ready.`,
        );
        return;
      }
      checking = true;
      try {
        const connected = await readLiveConnection(watch.nativeId);
        // Without a live answer the panel falls back to checking when the
        // window regains focus.
        if (settleLiveAnswer(watch.nativeId, connected) === "rediscover") {
          setWatch(null);
          setNotice(focusCheckNotice(appLabel));
        }
      } finally {
        checking = false;
      }
    };
    const onCheck = () => void check();
    const timer = window.setInterval(onCheck, CONNECTION_CHECK_MS);
    window.addEventListener("focus", onCheck);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", onCheck);
    };
  }, [appLabel, readLiveConnection, settleLiveAnswer, watch]);

  // A report that doesn't arrive in time leaves the connection to Check
  // again, which then asks the daemon to re-discover.
  useEffect(() => {
    if (!linked || !linkedApp) return;
    const timer = window.setTimeout(
      () => {
        linkedTimedOut.current.add(linkedApp.nativeId);
        setLinkedApp(null);
        setNotice(
          `${appLabel} is connected, but ${computer} hasn't reported it yet. Check again in a moment.`,
        );
      },
      Math.max(0, LINKED_WAIT_MS - (Date.now() - linkedApp.since)),
    );
    return () => window.clearTimeout(timer);
  }, [appLabel, computer, linked, linkedApp]);

  // An action disables its button while it runs, and a state change can
  // remove the button altogether; either drops keyboard focus on the page
  // body. Focus then returns to the same button when it is still offered,
  // or to the panel's heading, which names the new state.
  useEffect(() => {
    const button = focusAfterAction.current;
    if (!button || busy || rediscovering) return;
    const active = document.activeElement;
    if (active && active !== document.body) {
      if (!panelRef.current?.contains(active)) focusAfterAction.current = null;
      return;
    }
    const target =
      panelRef.current?.querySelector<HTMLElement>(
        `[data-panel-action="${button}"]:not(:disabled)`,
      ) ?? headingRef.current;
    if (target === headingRef.current) focusAfterAction.current = null;
    target?.focus({ preventScroll: true });
  }, [busy, entryState, linked, rediscovering, watch]);

  // Following a Workflow's app chip unmounts the dialog that had focus, so
  // on arrival the panel takes it once, on the heading that names the app's
  // state, unless the person has already moved it somewhere.
  const arrivedRef = useRef(false);
  useEffect(() => {
    const heading = headingRef.current;
    if (arrivedRef.current || !heading) return;
    arrivedRef.current = true;
    const active = document.activeElement;
    if (!active || active === document.body) {
      heading.focus({ preventScroll: true });
    }
  });

  const run = async (
    button: PanelActionButton,
    action: () => Promise<void>,
    fallback: string,
  ) => {
    focusAfterAction.current = button;
    setBusy(button);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (caught) {
      setError(errorMessage(caught, fallback));
    } finally {
      setBusy(null);
    }
  };

  // Check again's re-discovery runs on without holding the other buttons;
  // the button itself shows it until it ends.
  const rediscoverOnRequest = () => {
    setRediscovering(true);
    void rediscover()
      .catch((caught: unknown) =>
        setError(
          errorMessage(caught, `${appLabel} could not be checked again.`),
        ),
      )
      .finally(() => setRediscovering(false));
  };

  const checkAgain = () =>
    run(
      "check",
      async () => {
        if (liveConnector) {
          const { nativeId } = liveConnector;
          const answer = settleLiveAnswer(
            nativeId,
            await readLiveConnection(nativeId),
          );
          if (answer === "linked") return;
          if (answer === "not_connected") {
            if (!watch) setNotConnectedAt(Date.now());
            return;
          }
        }
        if (local && provider) {
          rediscoverOnRequest();
        } else {
          await readinessQuery.refetch();
        }
      },
      `${appLabel} could not be checked again.`,
    );

  const connect = (connector: WorkflowAppConnector) =>
    run(
      "primary",
      async () => {
        if (!provider) return;
        const operation = await connectDesktopConnector({
          companyId,
          nativeId: connector.nativeId,
          provider,
          runtimeId,
          source: connector.source,
        });
        if (!operation.ok) throw new Error(operation.reason);
        if (operation.value.state === "failed") {
          throw new Error(`${appLabel} could not be connected.`);
        }
        const { continueUrl } = operation.value;
        if (continueUrl) {
          const opened = await openDesktopExternal(continueUrl);
          if (!opened.ok) throw new Error(opened.reason);
        }
        const live = liveConnector?.nativeId === connector.nativeId;
        if (operation.value.state === "awaiting_user") {
          if (!live) {
            setNotice(focusCheckNotice(appLabel));
            return;
          }
          // The panel itself shows the wait, in place of the install
          // flow's toast.
          setWatch({
            continueUrl,
            nativeId: connector.nativeId,
            since: Date.now(),
          });
          return;
        }
        if (
          live &&
          operation.value.state === "complete" &&
          settleLiveAnswer(
            connector.nativeId,
            await readLiveConnection(connector.nativeId),
          ) === "linked"
        ) {
          return;
        }
        setNotice(
          operation.value.state === "complete"
            ? `${appLabel} setup finished. Checking its connection…`
            : `${providerName} is still setting up ${appLabel}. Check again in a moment.`,
        );
        void rediscover().catch(() => undefined);
      },
      `${appLabel} could not be connected.`,
    );

  // The flow's dialogs and toasts carry the install from here, and its
  // rediscovery re-reads readiness, which moves the panel on.
  const install = async (plugin: WorkflowAppPlugin) => {
    focusAfterAction.current = "primary";
    setError("");
    setNotice("");
    const listed = installableQuery.currentData?.plugin;
    if (!listed || listed.id !== plugin.id) {
      setError(
        errorMessage(
          installableQuery.error,
          `${plugin.name} could not be installed.`,
        ),
      );
      return;
    }
    const started = startPluginInstall({
      avatar: pluginAvatarProps(listed, { companyId, runtimeId }),
      companyId,
      computer,
      plugin: listed,
      provider: listed.provider,
      runtimeId,
    });
    if (!started) setError("Another plugin change is in progress.");
  };

  const review = (connector: WorkflowAppConnector) =>
    run(
      "primary",
      async () => {
        const operation = await reconnectConnector({
          companyId,
          connectorId: connector.id,
        }).unwrap();
        if (operation.state === "failed") {
          throw new Error(`${appLabel} could not be reconnected.`);
        }
        if (operation.continueUrl) {
          const opened = await openDesktopExternal(operation.continueUrl);
          if (!opened.ok) throw new Error(opened.reason);
        }
        setNotice(
          operation.state === "complete"
            ? `${appLabel} is ready to use again.`
            : `Finish reconnecting ${appLabel} in ${providerName}, then check again.`,
        );
      },
      `${appLabel} could not be reconnected.`,
    );

  // The provider's own settings are where a connected app's account,
  // permissions and connection change: the daemon can't reconnect an app
  // that is already connected. In a browser the link opens by itself.
  const manageInProvider = async (management: ProviderManagement) => {
    setError("");
    setNotice("");
    if (isDesktop) {
      const opened = await openDesktopExternal(management.url);
      if (!opened.ok) {
        setError(opened.reason);
        return;
      }
    }
    setManagedInProvider(true);
    setNotice(
      local
        ? `Make your changes to ${appLabel} in ${management.host}, then come back. Zenith Team checks again when this window is active.`
        : `Changes you make in ${management.host} show here once ${computer} next reports its apps.`,
    );
  };

  if (!readiness) {
    const failed = readinessQuery.isError;
    return (
      <PanelFrame
        actions={
          failed ? (
            <button
              className="dashboard-button"
              type="button"
              onClick={() => void readinessQuery.refetch()}
            >
              Try again
            </button>
          ) : undefined
        }
        app={app}
        back={back}
        line={
          failed
            ? errorMessage(
                readinessQuery.error,
                `Zenith Team could not check ${appLabel} for ${agent.name}.`,
              )
            : `Checking ${appLabel} for ${agent.name} on ${computer}…`
        }
        lineRole={failed ? "alert" : undefined}
        state={failed ? "error" : "loading"}
        title={
          <h3>{failed ? `Connect ${appLabel}` : `Checking ${appLabel}…`}</h3>
        }
        workflowIcon={workflowIcon}
      />
    );
  }

  const unbound = readiness.discovery.state === "unbound";
  const copy = panelCopy({
    agent,
    appLabel,
    canReview,
    computer,
    entry,
    providerName,
    readiness,
    workflowLabel,
  });
  const desktopNote =
    copy.needsLocal && !local
      ? isDesktop
        ? `Connecting ${appLabel} happens on ${computer}. Open Zenith Team Desktop there, with ${computer} online, and return to this page.`
        : `Connecting ${appLabel} needs Zenith Team Desktop on ${computer}. Open this page there to connect it.`
      : undefined;
  const checking = busy === "check" || rediscovering;
  // Install {Plugin} shows the install flow's progress, and holds while the
  // plugin is read or another install runs.
  const installing = installablePlugin
    ? pluginInstallProgress(installSession, installablePlugin.id) !== undefined
    : false;
  const installHeld =
    Boolean(installablePlugin) &&
    ((!installableQuery.currentData && !installableQuery.isError) ||
      pluginInstallBusy(installSession));
  const connected = entry?.state === "ready" ? entry : undefined;
  const management = connected
    ? providerManagement(connected, readiness.provider)
    : undefined;
  // Its capabilities turn an app off for this Agent only when they list
  // it; the Plugins page uninstalls only an installed plugin.
  const turnsOff = connected ? turnsOffForAgent(connected) : false;
  const pinnedPlugin = connected?.plugin?.installed
    ? connected.plugin
    : undefined;
  const primary = linked
    ? undefined
    : primaryAction({
        agent,
        appLabel,
        canReview,
        entry,
        local,
        onConnect: connect,
        onInstall: install,
        onReview: review,
      });
  // Where the app is on its way to connected: its connect page offered,
  // open in the browser, seen connected while the computer's report of it
  // arrives, or connected and being confirmed for the Agent.
  const stage: ConnectStage | undefined = linked
    ? "linked"
    : watch
      ? "waiting"
      : entryState === "pending"
        ? "pending"
        : entryState === "needs_connection" && primary?.kind === "button"
          ? "connect"
          : undefined;
  const title =
    stage === "waiting"
      ? `Finish connecting ${appLabel} in your browser`
      : stage === "linked"
        ? `Getting ${appLabel} ready`
        : stage === "connect"
          ? `Connect ${appLabel} to ${workflowLabel}`
          : connected
            ? `${appLabel} is connected`
            : copy.title;
  const line =
    stage === "waiting"
      ? "Checking automatically"
      : stage === "linked"
        ? "Usually a few seconds"
        : stage === "connect"
          ? notConnectedAt !== null
            ? "Not connected yet"
            : `${agent.name} uses it on ${computer}`
          : connected
            ? `${management?.settings ? `${management.host} account used by ${providerName}` : providerName} · ${computer}`
            : copy.line;
  const tools = connected?.connector
    ? toolSummary(connected.connector)
    : undefined;
  const checkAgainButton = (
    <button
      className="workflow-app-connect__quiet"
      data-panel-action="check"
      disabled={Boolean(busy) || rediscovering}
      type="button"
      onClick={() => void checkAgain()}
    >
      {checking ? <Spinner /> : null}
      {checking ? "Checking…" : "Check again"}
    </button>
  );

  let actions: ReactNode;
  if (stage === "linked") {
    actions = undefined;
  } else if (stage === "waiting") {
    actions = (
      <>
        {checkAgainButton}
        {watch?.continueUrl ? (
          <button
            className="dashboard-button"
            data-panel-action="open"
            type="button"
            onClick={() => {
              // When the connection shows, this button goes and focus
              // moves to the heading.
              focusAfterAction.current = "open";
              if (watch.continueUrl)
                void openDesktopExternal(watch.continueUrl);
            }}
          >
            Open browser
            <ArrowUpRight aria-hidden size={14} />
          </button>
        ) : null}
      </>
    );
  } else if (connected) {
    actions = (
      <>
        {pinnedPlugin && onShowPluginDetails ? (
          <button
            className="workflow-app-connect__quiet"
            type="button"
            onClick={() => onShowPluginDetails(pinnedPlugin)}
          >
            Plugin details
          </button>
        ) : null}
        {management ? (
          isDesktop ? (
            <button
              className="workflow-app-connect__quiet"
              type="button"
              onClick={() => void manageInProvider(management)}
            >
              {management.label}
              <ArrowUpRight aria-hidden size={14} />
            </button>
          ) : (
            <a
              className="workflow-app-connect__quiet"
              href={management.url}
              rel="noopener noreferrer"
              target="_blank"
              onClick={() => void manageInProvider(management)}
            >
              {management.label}
              <ArrowUpRight aria-hidden size={14} />
              <span className="sr-only"> (opens in a new tab)</span>
            </a>
          )
        ) : null}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              aria-label={`More for ${appLabel}`}
              className="workflow-app-connect__more"
              data-panel-action="check"
              title="More"
              type="button"
            >
              {checking ? <Spinner /> : <Ellipsis aria-hidden size={18} />}
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" sideOffset={6}>
            <DropdownMenuItem
              disabled={checking}
              onSelect={() => void checkAgain()}
            >
              {checking ? <Spinner /> : <RefreshCwIcon aria-hidden size={15} />}
              {checking ? "Checking…" : "Check again"}
            </DropdownMenuItem>
            {turnsOff && agent.access.configure ? (
              <DropdownMenuItem asChild>
                <Link
                  to={`/agents/${encodeURIComponent(agent.id)}?tab=capabilities`}
                >
                  <Ban aria-hidden size={15} />
                  Turn off for {agent.name}
                </Link>
              </DropdownMenuItem>
            ) : null}
          </DropdownMenuContent>
        </DropdownMenu>
        <Link
          className="dashboard-button dashboard-button-primary"
          to={workflowPath}
        >
          Back to {workflowLabel}
        </Link>
      </>
    );
  } else if (unbound) {
    actions = (
      <button
        className="dashboard-button dashboard-button-primary"
        data-panel-action="primary"
        type="button"
        onClick={() => {
          focusAfterAction.current = "primary";
          openBindDialog(agent);
        }}
      >
        Set up {agent.name}
      </button>
    );
  } else {
    actions = (
      <>
        {copy.canCheckAgain ? checkAgainButton : null}
        {entry?.state === "provider_managed" ? (
          <button
            className="dashboard-button"
            type="button"
            onClick={() => void openDesktopExternal(CLAUDE_CONNECTORS_URL)}
          >
            Browse Claude connectors
            <ArrowUpRight aria-hidden size={14} />
          </button>
        ) : null}
        {primary && primary.kind === "button" ? (
          <button
            className="dashboard-button dashboard-button-primary"
            data-panel-action="primary"
            disabled={Boolean(busy) || installHeld}
            type="button"
            onClick={() => void primary.onClick()}
          >
            {busy === "primary" || installing ? <Spinner /> : null}
            {primary.label}
          </button>
        ) : null}
        {primary && primary.kind === "link" ? (
          <Link
            className="dashboard-button dashboard-button-primary"
            to={primary.to}
          >
            {primary.label}
          </Link>
        ) : null}
      </>
    );
  }

  const notes = [
    stage ? undefined : copy.detail,
    desktopNote,
    ...(connected
      ? connectedAppNotes({
          agent,
          appLabel,
          computer,
          connector: connected.connector,
          management,
          turnsOff,
        })
      : []),
  ].filter((note): note is string => Boolean(note));
  const footer =
    notes.length || error || notice ? (
      <>
        {notes.map((note) => (
          <p key={note}>{note}</p>
        ))}
        {error ? (
          <p className="workflow-app-connect__error" role="alert">
            {error}
          </p>
        ) : null}
        {notice ? (
          <p className="workflow-app-connect__notice" role="status">
            {notice}
          </p>
        ) : null}
      </>
    ) : undefined;

  return (
    <>
      <PanelFrame
        actions={actions}
        app={app}
        back={connected ? undefined : back}
        footer={footer}
        iconUrl={entry?.iconUrl}
        line={line}
        link={
          connected
            ? "connected"
            : stage === "waiting"
              ? "waiting"
              : stage === "linked" || stage === "pending"
                ? "linked"
                : "idle"
        }
        panelRef={panelRef}
        progress={
          stage ? (
            <ConnectSteps
              computer={computer}
              current={stage === "linked" || stage === "pending" ? 1 : 0}
              spinning={stage !== "connect"}
            />
          ) : tools ? (
            <p className="m-[6px_0_0] text-[var(--workspace-native-text-muted,_rgba(255,_255,_255,_0.64))] text-[length:12px] leading-[16px]">
              {tools}
            </p>
          ) : undefined
        }
        stage={stage}
        state={entry?.state ?? "unknown"}
        title={
          <h3 ref={headingRef} tabIndex={-1}>
            {title}
          </h3>
        }
        workflowIcon={workflowIcon}
        onLeave={() => {
          focusAfterAction.current = null;
        }}
      />
      {bindDialog}
    </>
  );
}

/** How far an app is on its way to connected (the step line's stages). */
type ConnectStage = "connect" | "waiting" | "linked" | "pending";

/** What joins the Workflow's icon and the app's logo. */
type PanelLink = "idle" | "waiting" | "linked" | "connected";

/**
 * The panel around one app: the way back, then one row with the Workflow's
 * icon and the app's logo joined by a link that shows how far the connection
 * is, the title and one short line (with the connect steps or the tools a
 * Run can call under them), and the state's actions at the right; notes,
 * errors and notices follow under the row. The logo is the one readiness
 * observed, else the app's bundled brand mark, else its initial. The title
 * names the app, so the logos are decorative.
 */
function PanelFrame({
  actions,
  app,
  back,
  footer,
  iconUrl,
  line,
  lineRole,
  link = "idle",
  onLeave,
  panelRef,
  progress,
  stage,
  state,
  title,
  workflowIcon,
}: {
  actions?: ReactNode;
  app: WorkflowConnectorType;
  back?: ReactNode;
  footer?: ReactNode;
  iconUrl?: string;
  line: ReactNode;
  /** `alert` for a line that reports a failure. */
  lineRole?: "alert";
  link?: PanelLink;
  /** Keyboard focus moved to another control outside the panel. */
  onLeave?: () => void;
  panelRef?: Ref<HTMLElement>;
  progress?: ReactNode;
  stage?: ConnectStage;
  state: string;
  title: ReactNode;
  workflowIcon?: string;
}) {
  const display = workflowConnectorDisplayFor(app);
  return (
    <section
      ref={panelRef}
      aria-label={`${state === "ready" ? "Manage" : "Connect"} ${display.label}`}
      className="workflow-app-connect [container:workflow-app-connect_/_inline-size] grid gap-[14px] mt-[16px] [border:1px_solid_var(--workspace-divider,_rgba(255,_255,_255,_0.12))] rounded-[12px] [background:var(--workspace-panel-bg,_rgba(255,_255,_255,_0.055))] p-[16px_20px_20px]"
      data-stage={stage}
      data-state={state}
      data-workflow-app-panel={app}
      onBlur={(event) => {
        const next = event.relatedTarget;
        if (next instanceof Node && !event.currentTarget.contains(next)) {
          onLeave?.();
        }
      }}
    >
      {back}
      <div className="workflow-app-connect__main">
        <div
          aria-hidden="true"
          className="flex flex-none items-center gap-[10px]"
        >
          <span className="grid w-[52px] h-[52px] flex-none place-items-center [border:1px_solid_var(--workspace-divider)] rounded-[12px] [background:var(--workspace-control-bg,_rgba(255,_255,_255,_0.06))] text-workspace-accent">
            {workflowIcon ? (
              <Icon name={workflowIcon} size={22} />
            ) : (
              <Workflow size={22} />
            )}
          </span>
          <span className="workflow-app-connect__link" data-link={link}>
            {link === "connected" ? (
              <Check size={11} />
            ) : (
              <>
                <span />
                <span />
                <span />
              </>
            )}
          </span>
          <AppAvatar
            brand={display.brand}
            iconUrl={iconUrl}
            name={display.label}
            size={52}
          />
        </div>
        <div className="workflow-app-connect__text grid gap-[2px] min-w-0">
          {/* A state change reads as its title and line together. */}
          <div aria-atomic="true" aria-live="polite">
            {title}
            <p
              className="m-0 text-[var(--workspace-native-text-muted,_rgba(255,_255,_255,_0.64))] text-workspace-caption leading-workspace-caption wrap-anywhere"
              role={lineRole}
            >
              {line}
            </p>
          </div>
          {progress}
        </div>
        {actions ? (
          <div className="workflow-app-connect__actions">{actions}</div>
        ) : null}
      </div>
      {footer ? (
        <div className="workflow-app-connect__footer">{footer}</div>
      ) : null}
    </section>
  );
}

/**
 * Connect in the browser, the computer's report of the connection, ready:
 * `current` is the step under way, which spins while the panel waits on it.
 */
function ConnectSteps({
  computer,
  current,
  spinning,
}: {
  computer: string;
  current: 0 | 1;
  spinning: boolean;
}) {
  const labels = ["Connect in browser", `Confirm on ${computer}`, "Ready"];
  return (
    <ol aria-label="Connection steps" className="workflow-app-connect__steps">
      {labels.map((label, index) => {
        const step =
          index < current ? "done" : index === current ? "active" : "pending";
        return (
          <li
            key={label}
            aria-current={step === "active" ? "step" : undefined}
            data-step={step}
          >
            <span aria-hidden="true" className="workflow-app-connect__mark">
              {step === "done" ? (
                <Check size={12} />
              ) : step === "active" && spinning ? (
                <Spinner />
              ) : null}
            </span>
            {label}
          </li>
        );
      })}
    </ol>
  );
}

function Spinner() {
  return (
    <svg
      aria-hidden="true"
      className="workflow-app-connect__spinner"
      height="12"
      viewBox="0 0 16 16"
      width="12"
    >
      <circle cx="8" cy="8" r="6.5" />
      <circle cx="8" cy="8" pathLength={100} r="6.5" strokeDasharray="28 72" />
    </svg>
  );
}

type PanelCopy = {
  /** Why the state is what it is, when one short line can't say it. */
  detail?: string;
  /** Whether "Check again" helps in this state. */
  canCheckAgain: boolean;
  line: string;
  /** Whether the state's action runs through the local daemon. */
  needsLocal: boolean;
  title: string;
};

function panelCopy({
  agent,
  appLabel,
  canReview,
  computer,
  entry,
  providerName,
  readiness,
  workflowLabel,
}: {
  agent: AgentDto;
  appLabel: string;
  canReview: boolean;
  computer: string;
  entry: WorkflowAppReadiness | undefined;
  providerName: string;
  readiness: WorkflowReadiness;
  workflowLabel: string;
}): PanelCopy {
  if (!entry) {
    return {
      canCheckAgain: false,
      line: `${appLabel} isn't one of the apps ${workflowLabel} works with`,
      needsLocal: false,
      title: `Connect ${appLabel}`,
    };
  }
  switch (entry.state) {
    case "ready":
      return {
        canCheckAgain: true,
        line: `${providerName} · ${computer}`,
        needsLocal: false,
        title: `${appLabel} is connected`,
      };
    case "pending":
      return {
        canCheckAgain: true,
        line: `Connected on ${computer} · confirming it for ${agent.name}`,
        needsLocal: false,
        title: `Checking ${appLabel}`,
      };
    case "needs_connection":
      return entry.connector?.action === "manage_in_provider"
        ? {
            canCheckAgain: true,
            detail: `Set ${appLabel} up in ${providerName} for the account signed in on ${computer}, then check again.`,
            line: `Managed in ${providerName} · ${computer}`,
            needsLocal: false,
            title: `Set up ${appLabel} in ${providerName}`,
          }
        : {
            canCheckAgain: true,
            line: `${agent.name} uses it on ${computer}`,
            needsLocal: connectableLocally(entry),
            title: `Connect ${appLabel} to ${workflowLabel}`,
          };
    case "review_required":
      return {
        canCheckAgain: true,
        detail: canReview
          ? `Reconnect it so Runs can use the updated tool set.`
          : `An owner or admin reviews its changed tools for the company, then Runs can use it again.`,
        line: `${providerName} changed the tools ${appLabel} offers`,
        needsLocal: false,
        title: `Review ${appLabel}`,
      };
    case "disabled_for_agent":
      return {
        canCheckAgain: false,
        detail: agent.access.configure
          ? undefined
          : `Ask whoever manages ${agent.name} to turn it on.`,
        line: `Connected, but turned off in ${agent.name}'s capabilities`,
        needsLocal: false,
        title: `${appLabel} is off for ${agent.name}`,
      };
    case "provider_managed":
      return {
        canCheckAgain: false,
        detail: `Claude.ai connectors stay in Claude.ai. Run ${workflowLabel} with a Codex Agent, or look in Claude's directory for a plugin that provides ${appLabel}.`,
        line: "Zenith Team Runs can't use Claude.ai connectors",
        needsLocal: false,
        title: `${appLabel} is available only in Claude.ai`,
      };
    case "not_offered":
      return {
        canCheckAgain: false,
        detail: `${agent.name} follows its playbook's rule for a missing app and names it in the final summary.`,
        line: `${workflowLabel} still runs without it`,
        needsLocal: false,
        title: `${appLabel} isn't available on ${providerName}`,
      };
    // A state a newer server added reads as unknown.
    case "unknown":
    default:
      return readiness.discovery.state === "unbound"
        ? {
            canCheckAgain: false,
            detail: `${agent.name} uses the apps connected on the computer you set it up on. Set it up on one of yours, then connect ${appLabel} there.`,
            line: `${agent.name} isn't set up on your computer yet`,
            needsLocal: false,
            title: `Set up ${agent.name} to connect ${appLabel}`,
          }
        : {
            canCheckAgain: true,
            detail: `Open Zenith Team Desktop on ${computer}, then check again.`,
            line: `${computer} hasn't reported its apps yet`,
            needsLocal: false,
            title: `${appLabel} can't be checked yet`,
          };
  }
}

/**
 * Whether the local daemon can start this app's setup: its connector offers
 * sign-in, or — with no connector observed yet — its pinned plugin can be
 * installed, which brings the app with it.
 */
function connectableLocally({ connector, plugin }: WorkflowAppReadiness) {
  if (connector) {
    return (
      connector.action === "install_and_auth" ||
      connector.action === "authenticate"
    );
  }
  return plugin?.action === "install";
}

/**
 * Where a connected app's account and connection change: the place, spelled
 * for a person ("ChatGPT"), the button that opens it, and the page.
 */
type ProviderManagement = {
  host: string;
  label: string;
  settings: boolean;
  url: string;
};

/**
 * Where a connected app is managed, from the source the daemon observed it
 * through, or the Agent's provider when the connector is not disclosed. Only
 * `ready` reaches here, so the source is one a Run uses. A Codex app lives in
 * ChatGPT's connector settings — the daemon's setup link without its
 * add-connector step. A Claude plugin or server signs in through Claude Code
 * on the computer, which has no settings page; the button opens the guide.
 */
function providerManagement(
  { connector }: WorkflowAppReadiness,
  provider: ConnectorProvider,
): ProviderManagement | undefined {
  const source =
    connector?.source ?? (provider === "codex" ? "codex_app" : "claude_plugin");
  switch (source) {
    case "codex_app": {
      const fragment = connector
        ? `settings/Connectors?${new URLSearchParams({ connector: connector.nativeId }).toString()}`
        : "settings/Connectors";
      return {
        host: "ChatGPT",
        label: "Manage in ChatGPT",
        settings: true,
        url: `https://chatgpt.com/apps#${fragment}`,
      };
    }
    case "claude_plugin":
    case "claude_mcp":
      return {
        host: "Claude Code",
        label: "Claude Code guide",
        settings: false,
        url: CLAUDE_CODE_MCP_URL,
      };
    // A Codex MCP server is configured on the computer, with no page to
    // open; a Claude.ai connector is never ready.
    default:
      return undefined;
  }
}

/**
 * Whether the Agent's capabilities list this app, so it can be turned off
 * for this Agent alone: a Claude plugin is a plugin card, an MCP server an
 * MCP card, and a Codex app only through its installed pinned plugin.
 */
function turnsOffForAgent({ connector, plugin }: WorkflowAppReadiness) {
  switch (connector?.source) {
    case "claude_plugin":
    case "claude_mcp":
    case "codex_mcp":
      return true;
    case "codex_app":
      return plugin?.installed === true;
    default:
      return false;
  }
}

/**
 * What a connected app's row can't say in one line: that a read-only
 * Workflow is an instruction, not a switch, when the app has tools that make
 * changes, and whether the app can be turned off for this Agent alone.
 */
function connectedAppNotes({
  agent,
  appLabel,
  computer,
  connector,
  management,
  turnsOff,
}: {
  agent: AgentDto;
  appLabel: string;
  computer: string;
  connector?: WorkflowAppConnector;
  management?: ProviderManagement;
  turnsOff: boolean;
}): string[] {
  const notes: string[] = [];
  if (connector && toolSummary(connector) && connector.writeToolCount > 0) {
    notes.push(
      `${agent.name} can call every one of them. A read-only workflow tells ${agent.name} not to send, delete or edit anything, but doesn't switch those tools off.`,
    );
  }
  if (!turnsOff) {
    notes.push(
      `${appLabel} can't be turned off for ${agent.name} alone. ${
        management?.settings
          ? `Disconnecting it in ${management.host} stops every Agent on ${computer} from using it.`
          : `Disconnecting it stops every Agent on ${computer} from using it.`
      }`,
    );
  } else if (!agent.access.configure) {
    notes.push(
      `An owner or admin, or whoever created ${agent.name}, can turn ${appLabel} off for ${agent.name} in its capabilities.`,
    );
  }
  return notes;
}

/**
 * "21 tools · 9 can make changes": the tools a Run can call, from the app's
 * current observation. Only a Codex app reports them; for anything else, or
 * a server older than the counts, the line is left out.
 */
function toolSummary(connector: WorkflowAppConnector) {
  const { toolCount, writeToolCount } = connector;
  if (
    !Number.isSafeInteger(toolCount) ||
    !Number.isSafeInteger(writeToolCount) ||
    toolCount <= 0
  ) {
    return undefined;
  }
  const tools = `${toolCount} ${toolCount === 1 ? "tool" : "tools"}`;
  return writeToolCount > 0
    ? `${tools} · ${writeToolCount} can make changes`
    : `${tools} · all read-only`;
}

type PrimaryAction =
  | { kind: "button"; label: string; onClick: () => Promise<void> }
  | { kind: "link"; label: string; to: string };

function primaryAction({
  agent,
  appLabel,
  canReview,
  entry,
  local,
  onConnect,
  onInstall,
  onReview,
}: {
  agent: AgentDto;
  appLabel: string;
  canReview: boolean;
  entry: WorkflowAppReadiness | undefined;
  local: boolean;
  onConnect: (connector: WorkflowAppConnector) => Promise<void>;
  onInstall: (plugin: WorkflowAppPlugin) => Promise<void>;
  onReview: (connector: WorkflowAppConnector) => Promise<void>;
}): PrimaryAction | undefined {
  if (!entry) return undefined;
  const { connector, plugin } = entry;
  switch (entry.state) {
    case "needs_connection":
      if (!local || !connectableLocally(entry)) return undefined;
      if (connector) {
        return {
          kind: "button",
          label: `Connect ${appLabel}`,
          onClick: () => onConnect(connector),
        };
      }
      return plugin
        ? {
            kind: "button",
            label: `Install ${plugin.name}`,
            onClick: () => onInstall(plugin),
          }
        : undefined;
    case "review_required":
      return connector && canReview
        ? {
            kind: "button",
            label: `Review and reconnect ${appLabel}`,
            onClick: () => onReview(connector),
          }
        : undefined;
    case "disabled_for_agent":
      return agent.access.configure
        ? {
            kind: "link",
            label: `Open ${agent.name}'s capabilities`,
            to: `/agents/${encodeURIComponent(agent.id)}?tab=capabilities`,
          }
        : undefined;
    default:
      return undefined;
  }
}

function errorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return error.message;
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string" &&
    error.message
  ) {
    return error.message;
  }
  return fallback;
}
