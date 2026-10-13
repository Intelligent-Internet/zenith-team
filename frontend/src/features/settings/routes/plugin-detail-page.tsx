import type { DesktopPluginDetail } from "../../../../electron/shared/desktop-bridge.types";
import {
  type CSSProperties,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";

import {
  connectDesktopConnector,
  openDesktopExternal,
  readDesktopConnectorStatus,
  readDesktopPlugin,
  shouldUseDesktopHashRouter,
} from "@/shared/desktop/desktop-environment.service";
import { readSessionSnapshot } from "@/shared/session/session";
import {
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import {
  providerLabel,
  runtimeMachineName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import {
  createChatWorkspaceHref,
  createComposerDraftNavigationState,
} from "@/features/chat-workspace/model/chat-workspace-route";
import {
  reconnectableConnector,
  useListConnectorReviewsQuery,
  useReconnectConnectorMutation,
} from "@/features/settings/api/connector-reviews-api";
import {
  useGetPluginQuery,
  useRefreshPluginsMutation,
} from "@/features/settings/api/plugins-api";
import {
  CONNECTION_CHECK_MS,
  CONNECTION_WAIT_MS,
  PluginInstallProgressLabel,
} from "@/features/settings/components/plugin-install-flow";
import { PluginUninstallDialog } from "@/features/settings/components/plugin-uninstall-dialog";
import { usePluginInstallation } from "@/features/settings/hooks/use-plugin-installation";
import {
  pluginInstallBusy,
  pluginInstallProgress,
  startPluginInstall,
  usePluginInstallSession,
} from "@/features/settings/model/plugin-install-session";
import {
  syncedAgo,
  useMinuteClock,
} from "@/features/settings/model/plugin-sync";
import { readPluginCatalogHref } from "@/features/settings/model/settings-navigation";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type {
  PluginAgentUse,
  PluginDetailApp,
  PluginDetailResponseV1,
  PluginItemV1,
} from "@/shared/api/types";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import type { LocalPluginIconContext } from "@/shared/lib/plugin-icons";
import { useCan } from "@/shared/permissions";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { AppAvatar, pluginAvatarProps } from "@/shared/ui/app-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  ArrowRight,
  ArrowUpRight,
  BookOpen,
  ChevronRight,
  Ellipsis,
  FileText,
  Globe,
  Link2,
  MessageSquareText,
  Plug,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
} from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import "@/features/settings/styles/plugin-detail.css";

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const BRAND_COLOR_PATTERN = /^#[0-9A-Fa-f]{6}$/;
// Where the daemon itself sends people to manage a Claude plugin or server.
const CLAUDE_CODE_MCP_URL = "https://code.claude.com/docs/en/mcp";
const CLAUDE_CONNECTORS_URL = "https://claude.com/connectors";

/**
 * Settings › Plugins › one plugin, as the selected computer observed it: its
 * logo, what it does, prompts to try with the CEO Agent, the apps it brings
 * and whether each is connected, its skills, and the Agents that can use it.
 * `?runtimeId` picks the computer, as on the catalog; the breadcrumb keeps it,
 * and returns to the exact catalog view (and Workflow app panel) the plugin
 * was opened from.
 */
export function PluginDetailPage({ pluginId }: { pluginId: string }) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [params] = useSearchParams();
  const location = useLocation();
  const requestedRuntimeId = params.get("runtimeId") ?? "";
  // A malformed id would be refused by the server; the catalog's default
  // computer is the better answer to an old or mistyped link.
  const runtimeId = UUID_PATTERN.test(requestedRuntimeId)
    ? requestedRuntimeId
    : undefined;
  const pluginsHref =
    readPluginCatalogHref(location.state) ??
    (requestedRuntimeId
      ? `/settings/plugins?${new URLSearchParams({ runtimeId: requestedRuntimeId }).toString()}`
      : "/settings/plugins");
  const topRef = useRef<HTMLDivElement>(null);

  // The settings frame keeps its scroll position across routes, so a plugin
  // opened from far down the catalog would otherwise open scrolled down.
  useLayoutEffect(() => {
    topRef.current
      ?.closest(".workspace-page-frame__content")
      ?.scrollTo({ top: 0 });
  }, [pluginId]);

  return (
    <div className="plugin-page" ref={topRef}>
      {companyId ? (
        <PluginDetailContent
          key={`${companyId}:${pluginId}:${runtimeId ?? ""}`}
          companyId={companyId}
          pluginId={pluginId}
          pluginsHref={pluginsHref}
          runtimeId={runtimeId}
        />
      ) : (
        <PluginPageState
          pluginsHref={pluginsHref}
          title="Company context is unavailable"
        />
      )}
    </div>
  );
}

type AppNotice = { kind: "error" | "notice"; text: string };

/** An app whose connect page is open in the browser. */
type AppWatch = {
  key: string;
  name: string;
  nativeId: string;
  /** A Codex app, checked live through the daemon; else re-discovered. */
  live: boolean;
  since: number;
  /** The live check saw it connect; the computer's report is on its way. */
  connected?: boolean;
};

function PluginDetailContent({
  companyId,
  pluginId,
  pluginsHref,
  runtimeId,
}: {
  companyId: string;
  pluginId: string;
  pluginsHref: string;
  runtimeId?: string;
}) {
  const navigate = useNavigate();
  const isDesktop = shouldUseDesktopHashRouter();
  const canManageConnectors = useCan("connectors.manage");
  const query = useGetPluginQuery(
    { companyId, pluginId, runtimeId },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
    },
  );
  const runtimesQuery = useListAgentRuntimesQuery(companyId);
  const agentsQuery = useListCompanyAgentsQuery(companyId);
  const [refreshPlugins] = useRefreshPluginsMutation();
  const [reconnectConnector] = useReconnectConnectorMutation();
  const detail = query.currentData;
  const runtime = detail?.runtime ?? null;
  const runtimeOnline = runtime?.online === true;
  const provider = detail?.plugin.provider;
  const nativeId = detail?.plugin.nativeId;
  const runtimeRow = runtimesQuery.currentData?.items.find(
    (candidate) => candidate.id === runtime?.id,
  );
  const computer = runtimeRow
    ? runtimeMachineName(runtimeRow)
    : (runtime?.name ?? "No computer");
  const providerName = provider ? providerLabel(provider) : "";
  // Every member reads which of their computer's apps wait for review (a
  // member read), so an app Runs can't use never reads as fine; accepting
  // the changed tools stays with connectors.manage (D28).
  const reviewsQuery = useListConnectorReviewsQuery(
    { companyId, provider, runtimeId: runtime?.id },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
      skip: !provider || !runtime,
    },
  );
  const installation = usePluginInstallation({
    activeProvider: provider,
    companyId,
    runtimeId: runtime?.id,
    runtimeOnline,
    targetLabel: `${computer}${providerName ? ` · ${providerName}` : ""}`,
  });
  const installSession = usePluginInstallSession();
  const [live, setLive] = useState<{
    key: string;
    detail?: DesktopPluginDetail;
    failed?: boolean;
  }>();
  const [liveRevision, setLiveRevision] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [busyApp, setBusyApp] = useState<string>();
  const [appNotice, setAppNotice] = useState<AppNotice>();
  // A connect page has no callback into Zenith Team. As in the Workflow
  // connect panel, a Codex app is checked live every few seconds and when
  // the window regains focus; any other app is re-discovered when the window
  // regains focus. Either stops after CONNECTION_WAIT_MS.
  const [appWatch, setAppWatch] = useState<AppWatch>();
  const now = useMinuteClock();
  const [uninstallTarget, setUninstallTarget] = useState<PluginItemV1 | null>(
    null,
  );
  // Uninstall is chosen from the "…" menu, which stays after it, so focus
  // returns there when the confirmation closes.
  const moreButtonRef = useRef<HTMLButtonElement>(null);

  // On the Agent's own computer the daemon reads the plugin live, which can
  // be newer than the last discovery. A failed read keeps the server's copy
  // (and a refresh keeps the last live one); nothing is shown for it.
  const liveRuntimeId = isDesktop && runtimeOnline ? runtime?.id : undefined;
  const liveKey =
    liveRuntimeId && provider && nativeId
      ? `${liveRuntimeId}:${provider}:${nativeId}`
      : undefined;
  useEffect(() => {
    if (!liveKey || !liveRuntimeId || !provider || !nativeId) return;
    let active = true;
    readDesktopPlugin({
      companyId,
      nativeId,
      provider,
      runtimeId: liveRuntimeId,
    })
      .then((value) => {
        if (active) setLive({ key: liveKey, detail: value });
      })
      .catch(() => {
        if (!active) return;
        setLive((current) =>
          current?.key === liveKey && current.detail
            ? current
            : { key: liveKey, failed: true },
        );
      });
    return () => {
      active = false;
    };
  }, [companyId, liveKey, liveRevision, liveRuntimeId, nativeId, provider]);

  const liveRead = live?.key === liveKey ? live : undefined;
  const liveDetail = liveRead?.detail;
  // Why skills and MCP servers may be counted but not named: the names
  // come only from the live read.
  const liveHint = liveKey
    ? liveRead?.failed
      ? "failed"
      : liveRead
        ? undefined
        : "reading"
    : isDesktop
      ? "offline"
      : "desktop";
  const view = useMemo(
    () => (detail ? pluginView(detail, liveDetail) : undefined),
    [detail, liveDetail],
  );

  const watchRuntimeId = runtime?.id;
  const refetchPlugin = query.refetch;
  useEffect(() => {
    if (!appWatch || appWatch.connected || !watchRuntimeId || !provider) {
      return;
    }
    let checking = false;
    const check = async () => {
      if (checking) return;
      if (Date.now() - appWatch.since > CONNECTION_WAIT_MS) {
        setAppWatch(undefined);
        setAppNotice({
          kind: "notice",
          text: `${appWatch.name} isn't connected yet. Connect it again when you're ready.`,
        });
        return;
      }
      checking = true;
      try {
        if (!appWatch.live) {
          // Rediscovery refetches this page once the computer reports.
          await refreshPlugins({
            companyId,
            provider,
            runtimeId: watchRuntimeId,
          })
            .unwrap()
            .catch(() => undefined);
          return;
        }
        const status = await readDesktopConnectorStatus({
          companyId,
          nativeId: appWatch.nativeId,
          provider: "codex",
          runtimeId: watchRuntimeId,
          source: "codex_app",
        });
        if (!status.ok) {
          // A desktop too old to check live re-discovers on focus instead.
          if (status.code === "daemon_update_required") {
            setAppWatch({ ...appWatch, live: false });
            setAppNotice({
              kind: "notice",
              text: focusCheckNotice(appWatch.name),
            });
          }
          return;
        }
        if (!status.value.connected) return;
        // The check makes the daemon re-read its linked apps, and the page
        // shows the connection once that report arrives.
        setAppWatch({ ...appWatch, connected: true });
        setAppNotice({
          kind: "notice",
          text: `${appWatch.name} is connected. It shows here once ${computer} reports it.`,
        });
        void refetchPlugin();
      } finally {
        checking = false;
      }
    };
    const onCheck = () => void check();
    const timer = appWatch.live
      ? window.setInterval(onCheck, CONNECTION_CHECK_MS)
      : undefined;
    window.addEventListener("focus", onCheck);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", onCheck);
    };
  }, [
    appWatch,
    companyId,
    computer,
    provider,
    refetchPlugin,
    refreshPlugins,
    watchRuntimeId,
  ]);

  // Once the computer reports the watched app connected, its row says so.
  const watchedAppConnected = Boolean(
    appWatch &&
    detail?.apps.some(
      (app) => appKey(app) === appWatch.key && app.status === "connected",
    ),
  );
  useEffect(() => {
    if (!watchedAppConnected) return;
    setAppWatch(undefined);
    setAppNotice(undefined);
  }, [watchedAppConnected]);

  const error = query.error as BoardlyApiError | undefined;
  if (error?.code === "plugin_not_found" || error?.status === 404) {
    return (
      <PluginPageState
        pluginsHref={pluginsHref}
        title="Plugin not found"
        body="The selected computer has not discovered this plugin. It may belong to another provider, or the computer has not reported its plugins yet."
      />
    );
  }
  if (!detail || !view) {
    return query.isError ? (
      <PluginPageState
        pluginsHref={pluginsHref}
        role="alert"
        title="The plugin could not be loaded"
        body={error?.message}
        action={
          <button
            className="plugin-page__button"
            type="button"
            onClick={() => void query.refetch()}
          >
            Try again
          </button>
        }
      />
    ) : (
      <>
        <Breadcrumb pluginsHref={pluginsHref} />
        <p
          className="m-0 text-workspace-muted text-workspace-caption leading-workspace-caption"
          role="status"
        >
          Loading plugin…
        </p>
      </>
    );
  }

  const plugin = installation.applyOverride(detail.plugin);
  const name = view.name;
  const pending = installation.pendingPluginId === plugin.id;
  const busy =
    Boolean(installation.pendingPluginId) || pluginInstallBusy(installSession);
  const installProgress = pluginInstallProgress(installSession, plugin.id);
  const localContext: LocalPluginIconContext | undefined =
    isDesktop && runtimeOnline && runtime
      ? { companyId, runtimeId: runtime.id }
      : undefined;
  const avatar = pluginAvatarProps({ ...plugin, name }, localContext);
  const ceo = agentsQuery.currentData?.items.find(
    (agent) => !agent.archivedAt && agent.reportsTo === null,
  );
  // Only a connected app can accept its changed tools; one that lost its
  // connection offers Connect instead.
  const reviewConnectorIds = new Set(
    (reviewsQuery.currentData?.connectors ?? [])
      .filter(reconnectableConnector)
      .map((connector) => connector.id),
  );
  const canActLocally = isDesktop && runtimeOnline && Boolean(runtime);
  // When the computer last reported its plugins, as the list says it. Plugin
  // discovery has no keepalive, so an old time alone is no cause for alarm.
  const synced = syncedAgo(detail.plugin.observedAt, now);

  const tryNow = (prompt?: string) => {
    if (!ceo) return;
    const draft = prompt ? `${name}: ${prompt}` : `Use ${name} to `;
    navigate(
      `${createChatWorkspaceHref({ kind: "direct", agentId: ceo.id })}&newSession=1`,
      { state: createComposerDraftNavigationState(draft) },
    );
  };

  const copyLink = async () => {
    if (!view.shareUrl) return;
    try {
      await copyTextToClipboard(view.shareUrl);
      toast.success("Link copied");
    } catch {
      toast.error("Couldn’t copy the link. Please try again.");
    }
  };

  // Desktop only: in a browser the links are plain new-tab links, since a
  // window opened without an opener cannot report that it opened.
  const openLink = async (url: string) => {
    const opened = await openDesktopExternal(url);
    if (!opened.ok) setAppNotice({ kind: "error", text: opened.reason });
  };

  const refresh = async () => {
    if (refreshing) return;
    setRefreshing(true);
    setAppNotice(undefined);
    installation.clearMessages();
    try {
      if (canActLocally && runtime && provider) {
        // Rediscovery invalidates the Plugin tag, which refetches this page.
        await refreshPlugins({
          companyId,
          provider,
          runtimeId: runtime.id,
        }).unwrap();
      } else {
        await query.refetch();
      }
      setLiveRevision((current) => current + 1);
    } catch (caught) {
      setAppNotice({
        kind: "error",
        text: errorMessage(caught, "The plugin could not be refreshed."),
      });
    } finally {
      setRefreshing(false);
    }
  };

  // Outside Desktop, or with the computer offline, the control stays and
  // says why it cannot change the plugin here.
  const changeInstallation = () => {
    if (busy) return;
    setAppNotice(undefined);
    if (!installation.canChange) {
      setAppNotice({
        kind: "notice",
        text:
          installation.unavailableReason ??
          "This plugin cannot be changed here.",
      });
      return;
    }
    if (plugin.action === "uninstall") {
      setUninstallTarget(plugin);
      return;
    }
    if (!runtime || !provider) return;
    // The install flow connects the plugin's app first when Codex would.
    startPluginInstall({
      avatar,
      companyId,
      computer,
      plugin: { ...plugin, name },
      provider,
      runtimeId: runtime.id,
    });
  };

  const confirmUninstall = async () => {
    if (!uninstallTarget) return;
    await installation.change(uninstallTarget);
    setUninstallTarget(null);
  };

  const runAppAction = async (
    app: PluginDetailApp,
    action: () => Promise<string>,
    fallback: string,
  ) => {
    const key = appKey(app);
    setBusyApp(key);
    setAppNotice(undefined);
    installation.clearMessages();
    try {
      setAppNotice({ kind: "notice", text: await action() });
    } catch (caught) {
      setAppNotice({ kind: "error", text: errorMessage(caught, fallback) });
    } finally {
      setBusyApp((current) => (current === key ? undefined : current));
    }
  };

  const connectApp = (app: PluginDetailApp) =>
    runAppAction(
      app,
      async () => {
        if (!runtime || !provider) throw new Error();
        const operation = await connectDesktopConnector({
          companyId,
          nativeId: app.nativeId,
          provider,
          runtimeId: runtime.id,
          source: app.source,
        });
        if (!operation.ok) throw new Error(operation.reason);
        if (operation.value.state === "failed") throw new Error();
        if (operation.value.continueUrl) {
          const opened = await openDesktopExternal(operation.value.continueUrl);
          if (!opened.ok) throw new Error(opened.reason);
        }
        if (operation.value.state === "awaiting_user") {
          const live = app.source === "codex_app";
          setAppWatch({
            key: appKey(app),
            name: app.name,
            nativeId: app.nativeId,
            live,
            since: Date.now(),
          });
          return live
            ? `Finish connecting ${app.name} in your browser. Zenith Team checks the connection automatically.`
            : focusCheckNotice(app.name);
        }
        void refreshPlugins({ companyId, provider, runtimeId: runtime.id })
          .unwrap()
          .catch(() => undefined);
        return operation.value.state === "complete"
          ? `${app.name} setup finished. Checking its connection…`
          : `${providerName} is still setting up ${app.name}. Check again in a moment.`;
      },
      `${app.name} could not be connected.`,
    );

  const reviewApp = (app: PluginDetailApp, connectorId: string) =>
    runAppAction(
      app,
      async () => {
        const operation = await reconnectConnector({
          companyId,
          connectorId,
        }).unwrap();
        if (operation.state === "failed") throw new Error();
        if (operation.continueUrl) {
          const opened = await openDesktopExternal(operation.continueUrl);
          // In a browser the tab opens without reporting that it did.
          if (!opened.ok && isDesktop) throw new Error(opened.reason);
        }
        void query.refetch();
        return operation.state === "complete"
          ? `${app.name} is ready to use again.`
          : `Finish reconnecting ${app.name} in ${providerName}, then refresh.`;
      },
      `${app.name} could not be reconnected.`,
    );

  const statusMessage: AppNotice | undefined = installation.error
    ? { kind: "error", text: installation.error }
    : installation.notice
      ? { kind: "notice", text: installation.notice }
      : appNotice;
  const installLabel =
    plugin.action === "install"
      ? pending
        ? "Installing…"
        : "Install"
      : pending
        ? "Uninstalling…"
        : "Uninstall";
  const installTitle = installation.canChange
    ? undefined
    : installation.unavailableReason;
  const providerListingLabel =
    provider === "codex" ? "Open in ChatGPT" : `Open in ${providerName}`;

  return (
    <>
      <Breadcrumb pluginsHref={pluginsHref} name={name} />

      <header className="plugin-page__header">
        <AppAvatar {...avatar} size={72} className="plugin-page__avatar" />
        <div className="plugin-page__heading min-w-0 pt-[4px]">
          <h1 title={name}>{name}</h1>
          {plugin.description ? (
            <p className="m-[6px_0_0] text-workspace-muted text-workspace-body leading-workspace-body wrap-anywhere">
              {plugin.description}
            </p>
          ) : null}
          <p className="plugin-page__meta flex flex-wrap items-center gap-y-[4px] m-[8px_0_0] text-workspace-muted text-workspace-caption leading-workspace-caption">
            <span className="inline-flex items-center gap-[8px]">
              <span
                className="plugin-page__dot"
                data-tone={installTone(plugin)}
                aria-hidden="true"
              />
              {installStateLabel(plugin, computer)}
            </span>
            {providerName ? <span>{providerName}</span> : null}
            {view.developerName ? <span>{view.developerName}</span> : null}
            {synced ? (
              <span
                title={`Plugins synced ${new Date(detail.plugin.observedAt).toLocaleString()}`}
              >
                Synced {synced}
              </span>
            ) : null}
          </p>
        </div>
        <div className="plugin-page__actions">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                aria-label={`More actions for ${name}`}
                className="plugin-page__button plugin-page__button--icon"
                ref={moreButtonRef}
                title="More actions"
                type="button"
              >
                <Ellipsis aria-hidden="true" size={18} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" sideOffset={6}>
              {plugin.action !== "none" ? (
                <DropdownMenuItem
                  disabled={busy}
                  title={installTitle}
                  variant={
                    plugin.action === "uninstall" ? "destructive" : undefined
                  }
                  onSelect={changeInstallation}
                >
                  {plugin.action === "install" ? (
                    <Plus aria-hidden="true" size={15} />
                  ) : (
                    <Trash2 aria-hidden="true" size={15} />
                  )}
                  {installLabel}
                </DropdownMenuItem>
              ) : null}
              {view.shareUrl ? (
                <ExternalMenuItem
                  href={view.shareUrl}
                  isDesktop={isDesktop}
                  onOpen={openLink}
                >
                  <ArrowUpRight aria-hidden="true" size={15} />
                  {providerListingLabel}
                </ExternalMenuItem>
              ) : null}
              {view.websiteUrl ? (
                <ExternalMenuItem
                  href={view.websiteUrl}
                  isDesktop={isDesktop}
                  onOpen={openLink}
                >
                  <Globe aria-hidden="true" size={15} />
                  Website
                </ExternalMenuItem>
              ) : null}
              {view.privacyPolicyUrl ? (
                <ExternalMenuItem
                  href={view.privacyPolicyUrl}
                  isDesktop={isDesktop}
                  onOpen={openLink}
                >
                  <ShieldCheck aria-hidden="true" size={15} />
                  Privacy policy
                </ExternalMenuItem>
              ) : null}
              {view.termsOfServiceUrl ? (
                <ExternalMenuItem
                  href={view.termsOfServiceUrl}
                  isDesktop={isDesktop}
                  onOpen={openLink}
                >
                  <FileText aria-hidden="true" size={15} />
                  Terms of service
                </ExternalMenuItem>
              ) : null}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                disabled={refreshing}
                onSelect={() => void refresh()}
              >
                <RefreshCw aria-hidden="true" size={15} />
                {refreshing ? "Refreshing…" : "Refresh"}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {view.shareUrl ? (
            <button
              className="plugin-page__button"
              type="button"
              onClick={() => void copyLink()}
            >
              <Link2 aria-hidden="true" size={16} />
              Copy link
            </button>
          ) : null}
          {installProgress ? (
            <PluginInstallProgressLabel
              className="plugin-install-progress plugin-install-progress--page"
              name={name}
              progress={installProgress}
            />
          ) : plugin.action === "install" ? (
            <button
              aria-disabled={!installation.canChange || busy}
              className="plugin-page__button"
              title={installTitle}
              type="button"
              onClick={changeInstallation}
            >
              <Plus aria-hidden="true" size={16} />
              {installLabel}
            </button>
          ) : null}
          <button
            className="plugin-page__button plugin-page__button--primary"
            disabled={!ceo}
            title={
              ceo
                ? `Start a chat with ${ceo.name} about ${name}`
                : "Your CEO Agent is not available yet."
            }
            type="button"
            onClick={() => tryNow(view.prompts[0])}
          >
            <MessageSquareText aria-hidden="true" size={16} />
            Try now
          </button>
        </div>
      </header>

      {statusMessage ? (
        <p
          className="plugin-page__notice"
          data-kind={statusMessage.kind}
          role={statusMessage.kind === "error" ? "alert" : "status"}
        >
          {statusMessage.text}
        </p>
      ) : null}
      {/* The page shows the computer's last report instead of reading it
          live only when it is offline or the live read failed. */}
      {runtime && (!runtimeOnline || liveRead?.failed) ? (
        <p className="plugin-page__notice" role="note">
          {runtimeOnline
            ? `${computer} could not be read just now. This page shows what it last reported.`
            : `${computer} is offline. This page shows what it last reported.`}
        </p>
      ) : null}

      {view.prompts.length > 0 ? (
        <section
          aria-label={`Try ${name}`}
          className="plugin-hero"
          data-brand={view.brandColor ? "" : undefined}
          style={
            view.brandColor
              ? ({ "--plugin-brand": view.brandColor } as CSSProperties)
              : undefined
          }
        >
          <ul className="flex w-[min(100%,_640px)] flex-col gap-[10px] m-0 p-0 [list-style:none]">
            {view.prompts.map((prompt) => (
              <li key={prompt}>
                <button
                  className="plugin-hero__prompt"
                  disabled={!ceo}
                  title={
                    ceo
                      ? `Start a chat with ${ceo.name} with this prompt`
                      : undefined
                  }
                  type="button"
                  onClick={() => tryNow(prompt)}
                >
                  <span className="plugin-hero__app">
                    <AppAvatar {...avatar} size={20} />
                    {name}
                  </span>
                  <span className="min-w-0 text-workspace-body leading-workspace-body wrap-anywhere">
                    {prompt}
                  </span>
                  <span className="plugin-hero__go" aria-hidden="true">
                    <ArrowRight size={16} />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {view.longDescription ? (
        <p className="m-0 text-workspace-muted text-workspace-body leading-[1.6] whitespace-pre-line wrap-anywhere">
          {view.longDescription}
        </p>
      ) : null}

      {view.apps.length > 0 ? (
        <PluginSection title="Apps" count={view.apps.length}>
          <ul className="plugin-apps">
            {view.apps.map((app) => (
              <PluginAppRow
                key={appKey(app)}
                app={app}
                busy={busyApp === appKey(app)}
                disabled={busyApp !== undefined}
                canActLocally={canActLocally}
                computer={computer}
                isDesktop={isDesktop}
                pluginInstalled={plugin.installed}
                providerName={providerName}
                canReview={canManageConnectors}
                reviewRequired={Boolean(
                  app.connectorId && reviewConnectorIds.has(app.connectorId),
                )}
                onConnect={() => void connectApp(app)}
                onManage={(url) => void openLink(url)}
                onReview={(connectorId) => void reviewApp(app, connectorId)}
              />
            ))}
          </ul>
        </PluginSection>
      ) : null}

      {view.skills.length > 0 || view.skillCount > 0 ? (
        <PluginSection
          title="Skills"
          count={view.skills.length || view.skillCount}
        >
          {view.skills.length > 0 ? (
            <ul className="plugin-list">
              {view.skills.map((skill, index) => (
                <li
                  key={`${skill.name}:${index}`}
                  className="plugin-list__row flex items-start gap-[14px] p-[12px_0] text-workspace-muted"
                >
                  <BookOpen aria-hidden="true" size={18} />
                  <p>
                    <strong>{skill.displayName || skill.name}</strong>
                    {skill.description ? (
                      <span> — {skill.description}</span>
                    ) : null}
                  </p>
                </li>
              ))}
            </ul>
          ) : (
            <p className="plugin-page__hint">
              {namesHint("skills", computer, liveHint)}
            </p>
          )}
        </PluginSection>
      ) : null}

      {view.mcpServers.length > 0 || view.mcpServerCount > 0 ? (
        <PluginSection
          title="MCP servers"
          count={view.mcpServers.length || view.mcpServerCount}
        >
          {view.mcpServers.length > 0 ? (
            <ul className="plugin-list">
              {view.mcpServers.map((server, index) => (
                <li
                  key={`${server}:${index}`}
                  className="plugin-list__row flex items-start gap-[14px] p-[12px_0] text-workspace-muted"
                >
                  <Plug aria-hidden="true" size={18} />
                  <p>
                    <strong>{server}</strong>
                  </p>
                </li>
              ))}
            </ul>
          ) : (
            <p className="plugin-page__hint">
              {namesHint("MCP servers", computer, liveHint)}
            </p>
          )}
        </PluginSection>
      ) : null}

      <PluginSection title="Used by">
        <UsedBy
          agents={detail.usedBy}
          computer={computer}
          installed={plugin.installed}
          name={name}
        />
      </PluginSection>

      <PluginUninstallDialog
        pending={Boolean(uninstallTarget) && pending}
        plugin={uninstallTarget}
        returnFocus={() => moreButtonRef.current}
        targetLabel={computer}
        onCancel={() => setUninstallTarget(null)}
        onConfirm={() => void confirmUninstall()}
      />
    </>
  );
}

// Opens in the desktop app on Desktop; in a browser it is a plain link to a
// new tab.
function ExternalMenuItem({
  children,
  href,
  isDesktop,
  onOpen,
}: {
  children: ReactNode;
  href: string;
  isDesktop: boolean;
  onOpen(url: string): Promise<void>;
}) {
  if (isDesktop) {
    return (
      <DropdownMenuItem onSelect={() => void onOpen(href)}>
        {children}
      </DropdownMenuItem>
    );
  }
  return (
    <DropdownMenuItem asChild>
      <a href={href} rel="noopener noreferrer" target="_blank">
        {children}
      </a>
    </DropdownMenuItem>
  );
}

function Breadcrumb({
  name,
  pluginsHref,
}: {
  name?: string;
  pluginsHref: string;
}) {
  return (
    <nav
      aria-label="Breadcrumb"
      className="plugin-page__breadcrumb flex items-center gap-[8px] min-w-0 text-workspace-muted text-workspace-label leading-workspace-label"
    >
      <Link to={pluginsHref}>Plugins</Link>
      {name ? (
        <>
          <ChevronRight aria-hidden="true" size={14} />
          <span aria-current="page">{name}</span>
        </>
      ) : null}
    </nav>
  );
}

function PluginPageState({
  action,
  body,
  pluginsHref,
  role = "status",
  title,
}: {
  action?: ReactNode;
  body?: string;
  pluginsHref: string;
  role?: "alert" | "status";
  title: string;
}) {
  return (
    <>
      <Breadcrumb pluginsHref={pluginsHref} />
      <section className="plugin-page__state" role={role}>
        <h1>{title}</h1>
        {body ? <p>{body}</p> : null}
        <div className="flex flex-wrap gap-[8px] mt-[6px]">
          {action}
          <Link className="plugin-page__button" to={pluginsHref}>
            Back to Plugins
          </Link>
        </div>
      </section>
    </>
  );
}

function PluginSection({
  children,
  count,
  title,
}: {
  children: ReactNode;
  count?: number;
  title: string;
}) {
  return (
    <section className="plugin-section flex min-w-0 flex-col">
      <h2>
        {title}
        {count !== undefined ? (
          <span className="text-workspace-muted text-workspace-body font-[500]">
            {count}
          </span>
        ) : null}
      </h2>
      {children}
    </section>
  );
}

type AppState = {
  label: string;
  tone: "success" | "warning" | "danger" | "muted";
  /** Why, when the label alone doesn't say who can change it. */
  hint?: string;
};

type AppAction =
  | { kind: "connect"; label: string }
  | { kind: "review"; connectorId: string; label: string }
  | { kind: "manage"; label: string; url: string };

function PluginAppRow({
  app,
  busy,
  canActLocally,
  canReview,
  disabled,
  computer,
  isDesktop,
  onConnect,
  onManage,
  onReview,
  pluginInstalled,
  providerName,
  reviewRequired,
}: {
  app: PluginDetailApp;
  busy: boolean;
  canActLocally: boolean;
  /** Whether the viewer may accept an app's changed tools. */
  canReview: boolean;
  /** Another app's action is running. */
  disabled: boolean;
  computer: string;
  isDesktop: boolean;
  pluginInstalled: boolean;
  providerName: string;
  reviewRequired: boolean;
  onConnect(): void;
  onManage(url: string): void;
  onReview(connectorId: string): void;
}) {
  const state = appState(
    app,
    reviewRequired,
    canReview,
    pluginInstalled,
    providerName,
  );
  const action = appAction(app, reviewRequired && canReview, pluginInstalled);
  const localOnly = action?.kind === "connect" && !canActLocally;

  return (
    <li className="plugin-app">
      <AppAvatar iconUrl={app.iconUrl} name={app.name} size={40} />
      <div className="plugin-app__text min-w-0">
        <strong>{app.name}</strong>
        {app.description ? <p>{app.description}</p> : null}
      </div>
      <div className="plugin-app__status">
        <span
          className="plugin-app__state"
          data-tone={state.tone}
          title={state.hint}
        >
          <span
            className="plugin-page__dot"
            data-tone={state.tone}
            aria-hidden="true"
          />
          {state.label}
        </span>
        {action?.kind === "manage" && !isDesktop ? (
          <a
            className="plugin-app__action"
            data-kind={action.kind}
            href={action.url}
            rel="noopener noreferrer"
            target="_blank"
          >
            {action.label}
            <ArrowUpRight aria-hidden="true" size={14} />
          </a>
        ) : action ? (
          <button
            aria-disabled={disabled || localOnly}
            className="plugin-app__action"
            data-kind={action.kind}
            title={
              localOnly
                ? `Open Zenith Team Desktop on ${computer}, with it online, to connect ${app.name}.`
                : undefined
            }
            type="button"
            onClick={() => {
              if (disabled || localOnly) return;
              if (action.kind === "connect") onConnect();
              else if (action.kind === "review") onReview(action.connectorId);
              else onManage(action.url);
            }}
          >
            {busy ? "Working…" : action.label}
            {action.kind === "manage" ? (
              <ArrowUpRight aria-hidden="true" size={14} />
            ) : null}
          </button>
        ) : null}
      </div>
    </li>
  );
}

function UsedBy({
  agents,
  computer,
  installed,
  name,
}: {
  agents: PluginAgentUse[];
  computer: string;
  installed: boolean;
  name: string;
}) {
  if (agents.length === 0) {
    return (
      <p className="plugin-page__hint">
        {installed
          ? `No Agent on ${computer} uses ${name} yet.`
          : `Install ${name} so Agents on ${computer} can use it.`}
      </p>
    );
  }
  return (
    <div className="plugin-agents flex flex-wrap items-center gap-[12px_24px] pt-[14px]">
      <ul>
        {agents.map((agent) => (
          <li key={agent.agentId}>
            <Link
              className="plugin-agents__agent"
              title={`Open ${agent.name}'s capabilities`}
              to={`/agents/${encodeURIComponent(agent.agentId)}?tab=capabilities`}
            >
              <FoldAvatar agentId={agent.agentId} size={32} paused />
              <span>{agent.name}</span>
            </Link>
          </li>
        ))}
      </ul>
      <p className="plugin-page__hint">
        Turn {name} off for one Agent in that Agent’s capabilities.
      </p>
    </div>
  );
}

type PluginSkillView = {
  name: string;
  displayName?: string;
  description?: string;
};

type PluginView = {
  name: string;
  longDescription?: string;
  prompts: string[];
  brandColor?: string;
  developerName?: string;
  shareUrl?: string;
  websiteUrl?: string;
  privacyPolicyUrl?: string;
  termsOfServiceUrl?: string;
  apps: PluginDetailApp[];
  skills: PluginSkillView[];
  skillCount: number;
  mcpServers: string[];
  mcpServerCount: number;
};

/**
 * The page's content: the server's observation, with the daemon's live read
 * winning wherever it has a value. Links are shown only when https.
 */
function pluginView(
  detail: PluginDetailResponseV1,
  live: DesktopPluginDetail | undefined,
): PluginView {
  const prompts = (
    live?.defaultPrompts.length ? live.defaultPrompts : detail.defaultPrompts
  )
    .map((prompt) => prompt.trim())
    .filter(
      (prompt, index, all) =>
        prompt.length > 0 && all.indexOf(prompt) === index,
    )
    .slice(0, 3);
  const brandColor = live?.brandColor || detail.brandColor;
  const skills = (live?.skills ?? []).filter((skill) => skill.name);
  const mcpServers = (live?.mcpServers ?? []).filter(Boolean);
  return {
    name: live?.name.trim() || detail.plugin.name,
    longDescription:
      (live?.longDescription || detail.longDescription)?.trim() || undefined,
    prompts,
    brandColor:
      brandColor && BRAND_COLOR_PATTERN.test(brandColor)
        ? brandColor
        : undefined,
    developerName: live?.developerName || detail.developerName,
    shareUrl: httpsUrl(live?.shareUrl) ?? httpsUrl(detail.plugin.shareUrl),
    websiteUrl: httpsUrl(live?.websiteUrl) ?? httpsUrl(detail.websiteUrl),
    privacyPolicyUrl:
      httpsUrl(live?.privacyPolicyUrl) ?? httpsUrl(detail.privacyPolicyUrl),
    termsOfServiceUrl:
      httpsUrl(live?.termsOfServiceUrl) ?? httpsUrl(detail.termsOfServiceUrl),
    apps: mergeApps(detail, live),
    // The live read names them; without one only the counts are known.
    skills,
    skillCount: live ? skills.length : detail.plugin.componentCounts.skills,
    mcpServers,
    mcpServerCount: live
      ? mcpServers.length
      : detail.plugin.componentCounts.mcpServers,
  };
}

// A live Codex app is the same app as the server's `codex_app` with its id;
// the daemon's name, description and logo are the newer ones. An app only
// the daemon reports is listed too, without a connection state.
function mergeApps(
  detail: PluginDetailResponseV1,
  live: DesktopPluginDetail | undefined,
): PluginDetailApp[] {
  if (!live || live.apps.length === 0) return detail.apps;
  const liveApps = new Map(
    live.apps.filter((app) => app.id && app.name).map((app) => [app.id, app]),
  );
  const apps = detail.apps.map((app) => {
    const current =
      app.source === "codex_app" ? liveApps.get(app.nativeId) : undefined;
    if (!current) return app;
    liveApps.delete(app.nativeId);
    return {
      ...app,
      name: current.name,
      description: current.description || app.description,
      iconUrl: current.iconUrl || app.iconUrl,
    };
  });
  if (detail.plugin.provider === "codex") {
    for (const app of liveApps.values()) {
      apps.push({
        source: "codex_app",
        nativeId: app.id,
        name: app.name,
        ...(app.description ? { description: app.description } : {}),
        ...(app.iconUrl ? { iconUrl: app.iconUrl } : {}),
      });
    }
  }
  return apps;
}

function appKey(app: Pick<PluginDetailApp, "nativeId" | "source">) {
  return `${app.source}:${app.nativeId}`;
}

function appState(
  app: PluginDetailApp,
  reviewRequired: boolean,
  canReview: boolean,
  pluginInstalled: boolean,
  providerName: string,
): AppState {
  if (reviewRequired) {
    return canReview
      ? { label: "Tools changed", tone: "warning" }
      : {
          label: "Needs review",
          tone: "warning",
          hint: `An owner or admin reviews ${app.name}'s changed tools for the company, then Runs can use it again.`,
        };
  }
  switch (app.status) {
    case "connected":
      return { label: "Connected", tone: "success" };
    case "needs_auth":
      return { label: "Needs sign-in", tone: "warning" };
    case "installed_not_ready":
      return { label: "Setting up", tone: "warning" };
    case "managed_in_provider":
      return {
        label: `Managed in ${providerName || "provider"}`,
        tone: "muted",
      };
    case "unavailable":
      return { label: "Unavailable", tone: "danger" };
    case "error":
      return { label: "Error", tone: "danger" };
    case "available":
      return { label: "Not connected", tone: "muted" };
    default:
      return {
        label: pluginInstalled ? "Not connected" : "Not installed",
        tone: "muted",
      };
  }
}

/**
 * The next step for one app, as the Workflows connect panel offers it:
 * connect it through the daemon, review its changed tools, or manage a
 * connected app where its account lives (the provider's own settings).
 */
function appAction(
  app: PluginDetailApp,
  reviewRequired: boolean,
  pluginInstalled: boolean,
): AppAction | undefined {
  if (reviewRequired && app.connectorId) {
    return {
      kind: "review",
      connectorId: app.connectorId,
      label: "Review and reconnect",
    };
  }
  if (app.status === "connected" || app.status === "managed_in_provider") {
    return providerManagement(app);
  }
  if (
    app.action === "install_and_auth" ||
    app.action === "authenticate" ||
    (!app.status && pluginInstalled && app.source === "codex_app")
  ) {
    return { kind: "connect", label: "Connect" };
  }
  return undefined;
}

function providerManagement(app: PluginDetailApp): AppAction | undefined {
  switch (app.source) {
    case "codex_app":
      return {
        kind: "manage",
        label: "Manage in ChatGPT",
        url: `https://chatgpt.com/apps#settings/Connectors?${new URLSearchParams({ connector: app.nativeId }).toString()}`,
      };
    case "claude_plugin":
    case "claude_mcp":
      return {
        kind: "manage",
        label: "Claude Code guide",
        url: CLAUDE_CODE_MCP_URL,
      };
    case "claude_cloud":
      return {
        kind: "manage",
        label: "Manage in Claude",
        url: CLAUDE_CONNECTORS_URL,
      };
    // A Codex MCP server is configured on the computer, with no page.
    default:
      return undefined;
  }
}

function installTone(plugin: PluginItemV1): AppState["tone"] {
  if (plugin.status === "error") return "danger";
  if (plugin.installed && plugin.enabled) return "success";
  if (plugin.installed) return "warning";
  return "muted";
}

function installStateLabel(plugin: PluginItemV1, computer: string) {
  if (plugin.status === "error") return `Unavailable on ${computer}`;
  if (plugin.installed && !plugin.enabled) return `Disabled on ${computer}`;
  if (plugin.installed) return `Installed on ${computer}`;
  return `Not installed on ${computer}`;
}

// An app re-discovered, not checked live, is looked at again on focus.
function focusCheckNotice(name: string) {
  return `Finish connecting ${name} in your browser, then come back. Zenith Team checks again when this window is active.`;
}

function namesHint(
  kind: string,
  computer: string,
  reason: "desktop" | "failed" | "offline" | "reading" | undefined,
) {
  switch (reason) {
    case "offline":
      return `The ${kind} are listed here while ${computer} is online.`;
    case "reading":
      return `Reading the ${kind} from ${computer}…`;
    case "failed":
      return `${computer} did not list the ${kind}. Refresh to try again.`;
    default:
      return `Open this page in Zenith Team Desktop, with ${computer} online, to see the ${kind}.`;
  }
}

function httpsUrl(value: string | undefined) {
  if (!value || value.length > 2048) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password
      ? url.href
      : undefined;
  } catch {
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
