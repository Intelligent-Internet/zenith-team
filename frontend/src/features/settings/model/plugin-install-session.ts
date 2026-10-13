import type { DesktopPluginDetail } from "../../../../electron/shared/desktop-bridge.types";
import { useSyncExternalStore } from "react";

import type { PluginItemV1, PluginProvider } from "@/shared/api/types";
import type { AppAvatarProps } from "@/shared/ui/app-avatar";

/** Where a plugin installs, and how the flow shows it. */
export type PluginInstallTarget = {
  companyId: string;
  runtimeId: string;
  provider: PluginProvider;
  /** The computer's name, for the connect dialog. */
  computer: string;
  plugin: PluginItemV1;
  /** The plugin's logo, for the dialogs and toasts. */
  avatar: AppAvatarProps;
};

/**
 * One app the flow connects. `launching` is while its connect page is being
 * opened; `waiting` is while that page is open in the browser, which has no
 * callback into Zenith Team, so the flow checks the connection until it holds.
 */
export type PluginInstallApp = {
  id: string;
  name: string;
  description?: string;
  iconUrl?: string;
  status: "pending" | "launching" | "waiting" | "connected";
  continueUrl?: string;
  /** When `waiting` began, which bounds how long the flow keeps checking. */
  waitingSince?: number;
};

type SessionBase = {
  /** Renews with every new install, so a late answer of an older one is dropped. */
  request: number;
  target: PluginInstallTarget;
  /** The live plugin read, once the flow has one. */
  detail?: DesktopPluginDetail;
};

/**
 * One plugin install, as Codex runs it. `preparing` reads the plugin and its
 * app. An on_install plugin whose one app is not connected connects it first
 * (`connectFirst`): in the connect dialog (`interactive`) or straight in the
 * browser (`automatic`); closing it before the app connects installs nothing.
 * `installing` runs the install. Apps the install still needs connected are
 * then offered in the list (`needsApps`) or, for one on_install app, in the
 * connect dialog (`connectApp`); the plugin stays installed if they are closed.
 */
export type PluginInstallSession =
  | { kind: "closed" }
  | (SessionBase & { kind: "preparing" })
  | (SessionBase & {
      kind: "connectFirst";
      mode: "interactive" | "automatic";
      app: PluginInstallApp;
    })
  | (SessionBase & {
      kind: "installing";
      /** 0 → 55 once the install answered → 90 → 100, as Codex's ring. */
      progress: number;
      connectedAppId?: string;
    })
  | (SessionBase & { kind: "needsApps"; apps: PluginInstallApp[] })
  | (SessionBase & {
      kind: "connectApp";
      app: PluginInstallApp;
      apps: PluginInstallApp[];
    });

export type PluginInstallSessionOf<Kind extends PluginInstallSession["kind"]> =
  Extract<PluginInstallSession, { kind: Kind }>;

export type PluginInstallProgress = {
  phase: "loading" | "installing";
  percent: number;
};

const CLOSED: PluginInstallSession = { kind: "closed" };

let session: PluginInstallSession = CLOSED;
let nextRequest = 0;
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function setSession(next: PluginInstallSession) {
  if (next === session) return;
  session = next;
  for (const listener of listeners) listener();
}

export function readPluginInstallSession(): PluginInstallSession {
  return session;
}

export function usePluginInstallSession(): PluginInstallSession {
  return useSyncExternalStore(
    subscribe,
    readPluginInstallSession,
    readPluginInstallSession,
  );
}

/**
 * Whether a new install may start now. Like Codex, one runs at a time: only a
 * closed session, a post-install dialog, or an automatic connect already
 * waiting on the browser gives way to it.
 */
export function canStartPluginInstall(current = session) {
  switch (current.kind) {
    case "closed":
    case "needsApps":
    case "connectApp":
      return true;
    case "connectFirst":
      return current.mode === "automatic" && current.app.status === "waiting";
    default:
      return false;
  }
}

/** Starts installing a plugin; false while another install holds the flow. */
export function startPluginInstall(target: PluginInstallTarget) {
  if (!canStartPluginInstall()) return false;
  nextRequest += 1;
  setSession({ kind: "preparing", request: nextRequest, target });
  return true;
}

/** Applies an update to the install `request`, if it is still the current one. */
export function updatePluginInstallSession(
  request: number,
  update: (
    current: Exclude<PluginInstallSession, { kind: "closed" }>,
  ) => PluginInstallSession | undefined,
) {
  if (session.kind === "closed" || session.request !== request) return;
  const next = update(session);
  if (next) setSession(next);
}

/** Closes the install `request`, or whatever is open when none is given. */
export function closePluginInstallSession(request?: number) {
  if (session.kind === "closed") return;
  if (request !== undefined && session.request !== request) return;
  setSession(CLOSED);
}

/**
 * The inline progress a plugin's install control shows, as Codex's ring:
 * "Loading" while the plugin and its app are read or an automatic connect is
 * opening, "Installing" while it installs. A dialog, or a connect waiting on
 * the browser, shows the control as usual.
 */
export function pluginInstallProgress(
  current: PluginInstallSession,
  pluginId: string,
): PluginInstallProgress | undefined {
  if (current.kind === "closed" || current.target.plugin.id !== pluginId) {
    return undefined;
  }
  switch (current.kind) {
    case "preparing":
      return { phase: "loading", percent: 0 };
    case "connectFirst":
      return current.mode === "automatic" && current.app.status !== "waiting"
        ? { phase: "loading", percent: 0 }
        : undefined;
    case "installing":
      return { phase: "installing", percent: current.progress };
    default:
      return undefined;
  }
}

/** Whether an install is running, so other install controls hold still. */
export function pluginInstallBusy(current: PluginInstallSession) {
  return !canStartPluginInstall(current);
}
