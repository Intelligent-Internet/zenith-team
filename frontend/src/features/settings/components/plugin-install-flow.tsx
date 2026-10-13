import type {
  DesktopPluginAuthApp,
  DesktopPluginDetail,
} from "../../../../electron/shared/desktop-bridge.types";
import { type ReactNode, useEffect, useRef } from "react";
import { useNavigate } from "react-router";

import {
  connectDesktopConnector,
  DesktopPluginReadError,
  openDesktopExternal,
  readDesktopConnectorStatus,
  readDesktopPlugin,
} from "@/shared/desktop/desktop-environment.service";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  createChatWorkspaceHref,
  createComposerDraftNavigationState,
} from "@/features/chat-workspace/model/chat-workspace-route";
import {
  useChangePluginInstallationMutation,
  useRefreshPluginsMutation,
} from "@/features/settings/api/plugins-api";
import { recordPluginInstalled } from "@/features/settings/hooks/use-plugin-installation";
import {
  closePluginInstallSession,
  type PluginInstallApp,
  type PluginInstallProgress,
  type PluginInstallSession,
  type PluginInstallSessionOf,
  type PluginInstallTarget,
  readPluginInstallSession,
  updatePluginInstallSession,
  usePluginInstallSession,
} from "@/features/settings/model/plugin-install-session";
import { pluginDetailPath } from "@/features/settings/model/settings-navigation";
import { AppAvatar, type AppAvatarProps } from "@/shared/ui/app-avatar";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import {
  ArrowUpRight,
  Check,
  LockKeyhole,
  MessageSquare,
  Settings,
  Users,
  X,
} from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import "@/features/settings/styles/plugin-install-flow.css";

// Codex's toast lengths: the installed toast stays 8 s, the rest 5 s.
const INSTALLED_TOAST_MS = 8_000;
const TOAST_MS = 5_000;
// Lets the flow's own toast card replace the toaster's.
const TOAST_HOST = "plugin-install-toast-host";
// A connect page in the browser has no callback into Zenith Team, so a
// waiting app is checked on this beat and whenever the window regains focus,
// for at most ten minutes (Codex's own limit on a pending connection). The
// Workflow connect panel waits on its app the same way.
export const CONNECTION_CHECK_MS = 5_000;
export const CONNECTION_WAIT_MS = 10 * 60_000;

type OpenSession = Exclude<PluginInstallSession, { kind: "closed" }>;

/**
 * Runs plugin installs the way Codex does, from any page: the Plugins list's
 * "+" and a plugin page's Install only start one (startPluginInstall). An
 * on_install plugin whose app is not connected installs nothing until that
 * app connects, so a stray click leaves no plugin behind; the rest install at
 * once and say so in a toast. Mounted once in the workspace shell.
 */
export function PluginInstallFlow({ companyId }: { companyId: string }) {
  const current = usePluginInstallSession();
  const navigate = useNavigate();
  const [changeInstallation] = useChangePluginInstallationMutation();
  const [refreshPlugins] = useRefreshPluginsMutation();
  // Read for as long as the flow is mounted, not only while an install is
  // open: the installed toast's Try now runs after the session closed.
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  // Each step of an install runs once, however often the effects re-run.
  const started = useRef(new Set<string>());
  const actions = useRef({
    changeInstallation,
    refreshPlugins,
    navigate,
    ceoId: undefined as string | undefined,
  });
  actions.current = {
    changeInstallation,
    refreshPlugins,
    navigate,
    ceoId: agentsQuery.currentData?.items.find(
      (agent) => !agent.archivedAt && agent.reportsTo === null,
    )?.id,
  };

  // Another Company's install does not follow the switch.
  useEffect(() => {
    if (current.kind !== "closed" && current.target.companyId !== companyId) {
      closePluginInstallSession(current.request);
    }
  }, [companyId, current]);

  const once = (step: string, request: number) => {
    const key = `${request}:${step}`;
    if (started.current.has(key)) return false;
    started.current.add(key);
    return true;
  };

  const tryNow = (
    target: PluginInstallTarget,
    detail?: DesktopPluginDetail,
  ) => {
    const { ceoId, navigate: go } = actions.current;
    const name = target.plugin.name;
    if (!ceoId) {
      void go(pluginDetailPath(target.plugin.id, target.runtimeId));
      return;
    }
    const prompt = detail?.defaultPrompts[0];
    const draft = prompt ? `${name}: ${prompt}` : `Use ${name} to `;
    void go(
      `${createChatWorkspaceHref({ kind: "direct", agentId: ceoId })}&newSession=1`,
      { state: createComposerDraftNavigationState(draft) },
    );
  };

  const finish = (session: OpenSession) => {
    closePluginInstallSession(session.request);
    showInstalledToast(session.target, () =>
      tryNow(session.target, session.detail),
    );
  };

  const install = async (session: PluginInstallSessionOf<"installing">) => {
    const { request, target } = session;
    const setProgress = (progress: number) =>
      updatePluginInstallSession(request, (open) =>
        open.kind === "installing" ? { ...open, progress } : undefined,
      );
    let operation;
    try {
      operation = await actions.current
        .changeInstallation({
          action: "install",
          companyId: target.companyId,
          nativeId: target.plugin.nativeId,
          provider: target.provider,
          runtimeId: target.runtimeId,
        })
        .unwrap();
    } catch {
      operation = undefined;
    }
    if (!operation || operation.state === "failed") {
      toast.error("Failed to install plugin");
      closePluginInstallSession(request);
      return;
    }
    if (operation.state === "queued" || operation.state === "installing") {
      toast(`${target.plugin.name}: the provider is still installing it.`);
      closePluginInstallSession(request);
      return;
    }
    setProgress(55);
    recordPluginInstalled(target.companyId, target.runtimeId, target.plugin);
    // Rediscovery refetches every open catalog and plugin page.
    void actions.current
      .refreshPlugins({
        companyId: target.companyId,
        provider: target.provider,
        runtimeId: target.runtimeId,
      })
      .unwrap()
      .catch(() => undefined);
    setProgress(90);
    setProgress(100);

    const latest = readPluginInstallSession();
    if (latest.kind !== "installing" || latest.request !== request) return;
    const policy = operation.authPolicy ?? latest.detail?.authPolicy;
    const apps = (operation.appsNeedingAuth ?? [])
      .filter((app) => app.id !== latest.connectedAppId)
      .map((app) => installApp(app, latest.detail));
    if (policy === "on_use" || apps.length === 0) {
      finish(latest);
      return;
    }
    updatePluginInstallSession(request, (open) =>
      apps.length === 1 && policy === "on_install"
        ? { ...open, kind: "connectApp", app: apps[0], apps }
        : { ...open, kind: "needsApps", apps },
    );
  };

  const beginInstall = (
    request: number,
    detail?: DesktopPluginDetail,
    connectedAppId?: string,
  ) =>
    updatePluginInstallSession(request, (open) => ({
      kind: "installing",
      request,
      target: open.target,
      detail: detail ?? open.detail,
      progress: 0,
      connectedAppId,
    }));

  // Installs nothing. The daemon's reason, when it gave one, says why.
  const failPreparation = (
    request: number,
    target: PluginInstallTarget,
    reason?: string,
  ) => {
    toast.error(
      reason
        ? `${target.plugin.name}: ${reason}`
        : "Failed to load plugin connection",
    );
    closePluginInstallSession(request);
  };

  const prepare = async (session: PluginInstallSessionOf<"preparing">) => {
    const { request, target } = session;
    if (target.provider !== "codex") {
      beginInstall(request);
      return;
    }
    let detail: DesktopPluginDetail;
    try {
      detail = await readDesktopPlugin({
        companyId: target.companyId,
        nativeId: target.plugin.nativeId,
        provider: target.provider,
        runtimeId: target.runtimeId,
      });
    } catch (caught) {
      // A daemon from before the flow installs as it always did.
      if (
        caught instanceof DesktopPluginReadError &&
        caught.code === "daemon_update_required"
      ) {
        beginInstall(request);
      } else {
        failPreparation(
          request,
          target,
          caught instanceof DesktopPluginReadError ? caught.message : undefined,
        );
      }
      return;
    }
    // Codex connects first only the one app of an on_install plugin.
    const app =
      detail.authPolicy === "on_install" && detail.apps.length === 1
        ? detail.apps[0]
        : undefined;
    if (!app) {
      beginInstall(request, detail);
      return;
    }
    const readStatus = () =>
      readDesktopConnectorStatus({
        companyId: target.companyId,
        nativeId: app.id,
        provider: "codex",
        runtimeId: target.runtimeId,
        source: "codex_app",
      });
    let status = await readStatus();
    // The daemon knows an app only once its connector discovery has run,
    // which a daemon that just started, or whose discovery keeps failing,
    // has not: discover once, then ask again.
    if (!status.ok && status.code === "capability_not_discovered") {
      const refreshed = await actions.current
        .refreshPlugins({
          companyId: target.companyId,
          provider: target.provider,
          runtimeId: target.runtimeId,
        })
        .unwrap()
        .then(
          () => true,
          () => false,
        );
      const latest = readPluginInstallSession();
      if (latest.kind !== "preparing" || latest.request !== request) return;
      if (refreshed) status = await readStatus();
    }
    if (!status.ok) {
      if (status.code === "daemon_update_required") {
        beginInstall(request, detail);
      } else {
        failPreparation(request, target, status.reason);
      }
      return;
    }
    if (status.value.connected) {
      beginInstall(request, detail, app.id);
      return;
    }
    const mode = detail.installationInterstitial ? "interactive" : "automatic";
    updatePluginInstallSession(request, (open) => ({
      kind: "connectFirst",
      request,
      target: open.target,
      detail,
      mode,
      app: installApp(
        app,
        detail,
        mode === "automatic" ? "launching" : "pending",
      ),
    }));
    if (mode === "automatic") void connect(request, app.id);
  };

  const setApp = (
    request: number,
    appId: string,
    change: Partial<PluginInstallApp>,
  ) =>
    updatePluginInstallSession(request, (open) => {
      const apply = (app: PluginInstallApp) =>
        app.id === appId ? { ...app, ...change } : app;
      switch (open.kind) {
        case "connectFirst":
          return open.app.id === appId
            ? { ...open, app: apply(open.app) }
            : undefined;
        case "needsApps":
          return { ...open, apps: open.apps.map(apply) };
        case "connectApp":
          return { ...open, app: apply(open.app), apps: open.apps.map(apply) };
        default:
          return undefined;
      }
    });

  // Opens the app's connect page in the browser and waits for it.
  const connect = async (request: number, appId: string) => {
    const session = readPluginInstallSession();
    if (session.kind === "closed" || session.request !== request) return;
    const { target } = session;
    const automatic =
      session.kind === "connectFirst" && session.mode === "automatic";
    setApp(request, appId, { status: "launching" });
    const result = await connectDesktopConnector({
      companyId: target.companyId,
      nativeId: appId,
      provider: "codex",
      runtimeId: target.runtimeId,
      source: "codex_app",
    });
    const failed = () => {
      // An automatic connect that cannot open falls back to the dialog.
      if (automatic) {
        updatePluginInstallSession(request, (open) =>
          open.kind === "connectFirst"
            ? {
                ...open,
                mode: "interactive",
                app: { ...open.app, status: "pending" },
              }
            : undefined,
        );
        return;
      }
      setApp(request, appId, { status: "pending" });
    };
    if (!result.ok || result.value.state === "failed") {
      if (!automatic) toast.error("Failed to connect app");
      failed();
      return;
    }
    if (result.value.state === "complete") {
      appConnected(request, appId);
      return;
    }
    // A flow closed meanwhile opens nothing.
    const latest = readPluginInstallSession();
    if (latest.kind === "closed" || latest.request !== request) return;
    const continueUrl = result.value.continueUrl;
    if (!continueUrl) {
      if (!automatic) {
        toast.error("This app does not provide a browser setup URL right now");
      }
      failed();
      return;
    }
    const opened = await openDesktopExternal(continueUrl);
    if (!opened.ok) {
      toast.error(opened.reason);
      failed();
      return;
    }
    setApp(request, appId, {
      status: "waiting",
      continueUrl,
      waitingSince: Date.now(),
    });
    if (automatic) {
      showConnectingToast(target, continueUrl);
    } else {
      toast("Finish connecting in your browser", { duration: TOAST_MS });
    }
    // Codex closes the post-install connect dialog once the browser opens,
    // back to the list of apps to connect.
    updatePluginInstallSession(request, (open) =>
      open.kind === "connectApp" ? { ...open, kind: "needsApps" } : undefined,
    );
  };

  const appConnected = (request: number, appId: string) => {
    const session = readPluginInstallSession();
    if (session.kind === "closed" || session.request !== request) return;
    if (session.kind === "connectFirst") {
      if (session.app.id === appId)
        beginInstall(request, session.detail, appId);
      return;
    }
    if (session.kind !== "needsApps" && session.kind !== "connectApp") return;
    const apps = session.apps.map((app) =>
      app.id === appId ? { ...app, status: "connected" as const } : app,
    );
    if (apps.every((app) => app.status === "connected")) {
      finish(session);
      return;
    }
    setApp(request, appId, { status: "connected" });
  };

  // Steps that start by themselves: reading the plugin, then installing.
  useEffect(() => {
    if (current.kind === "preparing" && once("prepare", current.request)) {
      void prepare(current);
    }
    if (
      current.kind === "installing" &&
      current.progress === 0 &&
      once("install", current.request)
    ) {
      void install(current);
    }
  });

  // Checks the apps waiting on the browser until they connect.
  const waiting =
    current.kind === "connectFirst"
      ? [current.app]
      : current.kind === "needsApps" || current.kind === "connectApp"
        ? current.apps
        : [];
  const waitingIds = waiting
    .filter((app) => app.status === "waiting")
    .map((app) => app.id)
    .join(" ");
  const waitingRequest = current.kind === "closed" ? 0 : current.request;
  useEffect(() => {
    if (!waitingIds || !waitingRequest) return;
    let checking = false;
    const check = async () => {
      if (checking) return;
      checking = true;
      try {
        const session = readPluginInstallSession();
        if (session.kind === "closed" || session.request !== waitingRequest) {
          return;
        }
        const apps =
          session.kind === "connectFirst"
            ? [session.app]
            : session.kind === "needsApps" || session.kind === "connectApp"
              ? session.apps
              : [];
        for (const app of apps) {
          if (app.status !== "waiting") continue;
          if (Date.now() - (app.waitingSince ?? 0) > CONNECTION_WAIT_MS) {
            timedOut(session, app);
            continue;
          }
          const status = await readDesktopConnectorStatus({
            companyId: session.target.companyId,
            nativeId: app.id,
            provider: "codex",
            runtimeId: session.target.runtimeId,
            source: "codex_app",
          });
          if (status.ok && status.value.connected) {
            appConnected(waitingRequest, app.id);
          }
        }
      } finally {
        checking = false;
      }
    };
    const onFocus = () => void check();
    const timer = window.setInterval(onFocus, CONNECTION_CHECK_MS);
    window.addEventListener("focus", onFocus);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", onFocus);
    };
  }, [waitingIds, waitingRequest]);

  // A connect nobody finished stops being checked: an automatic one, which
  // installed nothing, just ends; otherwise the app offers its connect again
  // and the dialog stays until it is closed, as in Codex.
  const timedOut = (session: OpenSession, app: PluginInstallApp) => {
    if (session.kind === "connectFirst" && session.mode === "automatic") {
      closePluginInstallSession(session.request);
      return;
    }
    setApp(session.request, app.id, {
      status: "pending",
      waitingSince: undefined,
    });
  };

  if (current.kind === "connectFirst" && current.mode === "interactive") {
    const busy = current.app.status !== "pending";
    return (
      <ConnectAppDialog
        app={current.app}
        target={current.target}
        onClose={() => {
          // Nothing is installed yet; the connect page may still be open in
          // the browser, which Zenith Team cannot close.
          if (current.app.status !== "launching") {
            closePluginInstallSession(current.request);
          }
        }}
        onContinue={() => {
          if (!busy) void connect(current.request, current.app.id);
        }}
      />
    );
  }
  if (current.kind === "connectApp") {
    return (
      <ConnectAppDialog
        app={current.app}
        installed
        target={current.target}
        onClose={() => {
          if (current.app.status === "launching") return;
          // Codex: the one app still unconnected ends the flow; otherwise
          // back to the list, where a started connect keeps its spinner.
          updatePluginInstallSession(current.request, (open) =>
            open.kind !== "connectApp"
              ? undefined
              : open.apps.length === 1 && open.app.status === "pending"
                ? { kind: "closed" }
                : { ...open, kind: "needsApps" },
          );
        }}
        onContinue={() => {
          if (current.app.status === "pending") {
            void connect(current.request, current.app.id);
          }
        }}
      />
    );
  }
  if (current.kind === "needsApps") {
    return (
      <ConnectAppsDialog
        session={current}
        onClose={() => closePluginInstallSession(current.request)}
        onConnect={(app) => {
          if (app.status === "pending") void connect(current.request, app.id);
        }}
        onTryNow={() => {
          closePluginInstallSession(current.request);
          tryNow(current.target, current.detail);
        }}
        onViewDetails={() => {
          closePluginInstallSession(current.request);
          void navigate(
            pluginDetailPath(
              current.target.plugin.id,
              current.target.runtimeId,
            ),
          );
        }}
      />
    );
  }
  return null;
}

function installApp(
  app: DesktopPluginAuthApp,
  detail: DesktopPluginDetail | undefined,
  status: PluginInstallApp["status"] = "pending",
): PluginInstallApp {
  const known = detail?.apps.find((candidate) => candidate.id === app.id);
  return {
    id: app.id,
    name: known?.name ?? app.name,
    description: known?.description,
    iconUrl: known?.iconUrl,
    status,
  };
}

/** The two tiles Codex puts atop its install dialogs: the product, then the app. */
function LogoPair({ children, size }: { children: ReactNode; size: 56 | 60 }) {
  return (
    <div className="plugin-install-logos" data-size={size} aria-hidden="true">
      <span className="plugin-install-logos__brand">
        {/* The wordmark's own symbol, cropped from it. */}
        <span className="plugin-install-logos__mark">
          <Icon name="logo" />
        </span>
      </span>
      <span className="plugin-install-logos__dots">
        <i />
        <i />
        <i />
      </span>
      {children}
    </div>
  );
}

// Codex's "Connect {app}" dialog, before an install (nothing is installed
// until the app connects) or after it (`installed`). It has no Cancel: the
// close button, Escape or a click outside dismiss it.
function ConnectAppDialog({
  app,
  installed = false,
  target,
  onClose,
  onContinue,
}: {
  app: PluginInstallApp;
  installed?: boolean;
  target: PluginInstallTarget;
  onClose(): void;
  onContinue(): void;
}) {
  const waiting = app.status === "waiting";
  const launching = app.status === "launching";
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        className="plugin-install-dialog plugin-connect-dialog"
        overlayClassName="plugin-install-dialog__overlay"
      >
        <LogoPair size={56}>
          <AppAvatar iconUrl={app.iconUrl} name={app.name} size={56} />
        </LogoPair>
        <div className="plugin-connect-dialog__heading">
          <DialogTitle>Connect {app.name}</DialogTitle>
          <DialogDescription>
            For the {target.plugin.name} plugin on {target.computer}
          </DialogDescription>
        </div>
        <ul className="plugin-connect-dialog__facts">
          <li>
            <LockKeyhole aria-hidden="true" size={18} />
            <div>
              <strong>Connect in ChatGPT</strong>
              <p>
                You sign in to {app.name} on chatgpt.com. Zenith Team never sees
                your password.
              </p>
            </div>
          </li>
          <li>
            <Users aria-hidden="true" size={18} />
            <div>
              <strong>Used by your Agents</strong>
              <p>
                {installed
                  ? `Agents on ${target.computer} use ${app.name} through Codex.`
                  : `Agents on ${target.computer} use ${app.name} through Codex once the plugin is installed.`}
              </p>
            </div>
          </li>
          <li>
            <Settings aria-hidden="true" size={18} />
            <div>
              <strong>Disconnect anytime</strong>
              <p>Remove access in ChatGPT’s Connectors settings.</p>
            </div>
          </li>
        </ul>
        <button
          aria-busy={launching || undefined}
          className="plugin-install-dialog__primary"
          disabled={waiting || launching}
          type="button"
          onClick={onContinue}
        >
          {waiting ? (
            "Finishing connection…"
          ) : launching ? (
            <Spinner />
          ) : (
            <>
              Continue to {app.name}
              <ArrowUpRight aria-hidden="true" size={16} />
            </>
          )}
        </button>
      </DialogContent>
    </Dialog>
  );
}

// Codex's "Connect your apps to {plugin}", after an install whose apps still
// need connecting. Closing it keeps the plugin installed.
function ConnectAppsDialog({
  session,
  onClose,
  onConnect,
  onTryNow,
  onViewDetails,
}: {
  session: PluginInstallSessionOf<"needsApps">;
  onClose(): void;
  onConnect(app: PluginInstallApp): void;
  onTryNow(): void;
  onViewDetails(): void;
}) {
  const name = session.target.plugin.name;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        className="plugin-install-dialog plugin-apps-dialog"
        overlayClassName="plugin-install-dialog__overlay"
      >
        <header className="plugin-apps-dialog__header">
          <LogoPair size={60}>
            <AppAvatar {...session.target.avatar} size={60} />
          </LogoPair>
          <DialogTitle>Connect your apps to {name}</DialogTitle>
          <DialogDescription>
            Get more relevant help from the apps you already use. You can always
            do this later.
          </DialogDescription>
        </header>
        <ul className="plugin-apps-dialog__apps">
          {session.apps.map((app) => (
            <li key={app.id}>
              <AppAvatar iconUrl={app.iconUrl} name={app.name} size={40} />
              <div className="plugin-apps-dialog__app">
                <strong>{app.name}</strong>
                <span>{app.description || "No description"}</span>
              </div>
              <button
                aria-label={
                  app.status === "connected"
                    ? `${app.name} connected`
                    : app.status === "pending"
                      ? `Connect ${app.name}`
                      : "Continue connecting app"
                }
                className="plugin-apps-dialog__connect"
                data-status={app.status}
                disabled={app.status !== "pending"}
                title={
                  app.status === "waiting"
                    ? "Continue connecting app"
                    : undefined
                }
                type="button"
                onClick={() => onConnect(app)}
              >
                {app.status === "connected" ? (
                  <>
                    <Check aria-hidden="true" size={14} />
                    Connected
                  </>
                ) : app.status === "pending" ? (
                  "Connect"
                ) : (
                  <Spinner />
                )}
              </button>
            </li>
          ))}
        </ul>
        <footer className="plugin-apps-dialog__footer">
          <button
            className="plugin-install-dialog__secondary"
            type="button"
            onClick={onViewDetails}
          >
            View details
          </button>
          <button
            className="plugin-install-dialog__primary plugin-install-dialog__primary--compact"
            type="button"
            onClick={onTryNow}
          >
            <MessageSquare aria-hidden="true" size={16} />
            Try {name} now
          </button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}

function Spinner() {
  return (
    <svg
      aria-hidden="true"
      className="plugin-install-spinner"
      height="16"
      viewBox="0 0 16 16"
      width="16"
    >
      <circle cx="8" cy="8" r="6.5" />
      <circle cx="8" cy="8" pathLength={100} r="6.5" strokeDasharray="28 72" />
    </svg>
  );
}

function InstallToast({
  action,
  avatar,
  title,
  onDismiss,
}: {
  action?: { label: string; tone: "primary" | "outline"; onClick(): void };
  avatar: AppAvatarProps;
  title: string;
  onDismiss(): void;
}) {
  return (
    <div className="plugin-install-toast" role="status">
      <AppAvatar {...avatar} size={28} />
      <div className="plugin-install-toast__body">
        <p>{title}</p>
        {action ? (
          <button
            className="plugin-install-toast__action"
            data-tone={action.tone}
            type="button"
            onClick={action.onClick}
          >
            {action.label}
          </button>
        ) : null}
      </div>
      <button
        aria-label="Dismiss"
        className="plugin-install-toast__close"
        type="button"
        onClick={onDismiss}
      >
        <X aria-hidden="true" size={14} />
      </button>
    </div>
  );
}

function showInstalledToast(target: PluginInstallTarget, onTryNow: () => void) {
  const id = `plugin-added:${target.plugin.id}`;
  toast.custom(
    () => (
      <InstallToast
        action={{
          label: "Try now",
          tone: "primary",
          onClick: () => {
            toast.dismiss(id);
            onTryNow();
          },
        }}
        avatar={target.avatar}
        title={`${target.plugin.name} plugin installed`}
        onDismiss={() => toast.dismiss(id)}
      />
    ),
    { className: TOAST_HOST, id, duration: INSTALLED_TOAST_MS },
  );
}

function showConnectingToast(target: PluginInstallTarget, continueUrl: string) {
  const id = `plugin-connecting:${target.plugin.id}`;
  toast.custom(
    () => (
      <InstallToast
        action={{
          label: "Open browser",
          tone: "outline",
          onClick: () => void openDesktopExternal(continueUrl),
        }}
        avatar={target.avatar}
        title={`Finish connecting ${target.plugin.name} in your browser`}
        onDismiss={() => toast.dismiss(id)}
      />
    ),
    { className: TOAST_HOST, id, duration: TOAST_MS },
  );
}

/**
 * Codex's install progress, in place of a plugin's install control: a 16px
 * ring filling 0 → 55 → 90 → 100 and "Loading" or "Installing".
 */
export function PluginInstallProgressLabel({
  className,
  name,
  progress,
}: {
  className?: string;
  name: string;
  progress: PluginInstallProgress;
}) {
  const percent = Math.max(0, Math.min(100, progress.percent));
  const label = progress.phase === "installing" ? "Installing" : "Loading";
  return (
    <span
      aria-label={`${label} ${name}`}
      className={className ?? "plugin-install-progress"}
      role="status"
    >
      <svg
        aria-hidden="true"
        className="plugin-install-progress__ring"
        height="16"
        viewBox="0 0 16 16"
        width="16"
      >
        <circle cx="8" cy="8" r="6.5" />
        <circle
          cx="8"
          cy="8"
          data-empty={percent === 0 || undefined}
          pathLength={100}
          r="6.5"
          strokeDasharray="100"
          strokeDashoffset={100 - percent}
          transform="rotate(-90 8 8)"
        />
      </svg>
      {label}
    </span>
  );
}
