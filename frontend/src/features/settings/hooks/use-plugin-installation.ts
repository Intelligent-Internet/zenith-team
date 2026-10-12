import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import {
  openDesktopExternal,
  shouldUseDesktopHashRouter,
} from "@/app/desktop/desktop-environment.service";
import {
  useChangePluginInstallationMutation,
  useRefreshPluginsMutation,
} from "@/features/settings/api/plugins-api";
import type { PluginItemV1, PluginProvider } from "@/shared/api/types";
import { toast } from "@/shared/ui/sonner";

// `observedAt` is the discovery the change was made against; only a newer
// one can retire the override.
type LifecycleOverride = Pick<
  PluginItemV1,
  "action" | "enabled" | "installed" | "observedAt" | "status"
> & { expiresAt: number };

// One Company's installation state as React reads it. The revision renews
// whenever an override is recorded, so consumers holding plugins re-apply.
type CompanySnapshot = { overridesRevision: number; pendingPluginId?: string };

const EMPTY_SNAPSHOT: CompanySnapshot = { overridesRevision: 0 };

// Module-level so the Plugins list and a plugin's own page show the same
// optimistic state: a successful change holds the plugin's new state for two
// minutes, or until discovery reports it, so the 15 s catalog poll cannot flip
// it back. Keyed by Company, Runtime and plugin id.
const overrides = new Map<string, LifecycleOverride>();
const snapshots = new Map<string, CompanySnapshot>();
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function readSnapshot(companyId: string) {
  return snapshots.get(companyId) ?? EMPTY_SNAPSHOT;
}

function updateSnapshot(
  companyId: string,
  update: (current: CompanySnapshot) => CompanySnapshot,
) {
  const current = readSnapshot(companyId);
  const next = update(current);
  if (next === current) return;
  snapshots.set(companyId, next);
  for (const listener of listeners) listener();
}

function overrideKey(companyId: string, runtimeId: string, pluginId: string) {
  return `${companyId}:${runtimeId}:${pluginId}`;
}

function recordOverride(
  companyId: string,
  runtimeId: string,
  plugin: PluginItemV1,
) {
  const now = Date.now();
  for (const [key, override] of overrides) {
    if (override.expiresAt <= now) overrides.delete(key);
  }
  overrides.set(overrideKey(companyId, runtimeId, plugin.id), {
    action: plugin.action,
    enabled: plugin.enabled,
    expiresAt: now + 120_000,
    installed: plugin.installed,
    observedAt: plugin.observedAt,
    status: plugin.status,
  });
  updateSnapshot(companyId, (current) => ({
    ...current,
    overridesRevision: current.overridesRevision + 1,
  }));
}

// An override that expired, or that a newer discovery agrees with, retires
// without notifying anyone: the plugin its reader holds is already right. A
// plugin that only agrees because the override was applied to it before
// still carries the old observation, so it keeps the override.
function applyStoredOverride(
  companyId: string,
  runtimeId: string | undefined,
  plugin: PluginItemV1,
  now: number,
): PluginItemV1 {
  if (!runtimeId) return plugin;
  const key = overrideKey(companyId, runtimeId, plugin.id);
  const override = overrides.get(key);
  if (!override) return plugin;
  const agrees =
    plugin.action === override.action &&
    plugin.enabled === override.enabled &&
    plugin.installed === override.installed &&
    plugin.status === override.status;
  if (
    override.expiresAt <= now ||
    (agrees && plugin.observedAt !== override.observedAt)
  ) {
    overrides.delete(key);
    return plugin;
  }
  if (agrees) return plugin;
  return {
    ...plugin,
    action: override.action,
    enabled: override.enabled,
    installed: override.installed,
    status: override.status,
  };
}

/**
 * Holds a plugin the install flow just installed as installed, on every page,
 * until discovery reports it (see recordOverride).
 */
export function recordPluginInstalled(
  companyId: string,
  runtimeId: string,
  plugin: PluginItemV1,
) {
  recordOverride(
    companyId,
    runtimeId,
    pluginAfterSuccessfulAction({ ...plugin, action: "install" }),
  );
}

function pluginAfterSuccessfulAction(plugin: PluginItemV1): PluginItemV1 {
  if (plugin.action === "install") {
    return {
      ...plugin,
      action: "uninstall",
      enabled: true,
      installed: true,
      status: "installed",
    };
  }
  return {
    ...plugin,
    action: "install",
    enabled: false,
    installed: false,
    status: "available",
  };
}

/**
 * Installs and uninstalls provider plugins on one Runtime through Zenith Team
 * Desktop. One change runs at a time per Company; `pendingPluginId` names it
 * on every page. After a successful change the plugin reads as changed
 * (`applyOverride`/`applyOverrides`) until discovery catches up, discovery is
 * refreshed in the background, and a sign-in the provider asks for opens in
 * the browser. `canChange` covers the environment only (Desktop, a Runtime,
 * online); callers also disable their controls while a change is pending.
 * `error` and `notice` belong to the page that started the change; an
 * outcome that arrives after that page closed is a toast instead.
 */
export function usePluginInstallation({
  activeProvider,
  companyId,
  runtimeId,
  runtimeOnline,
  targetLabel,
}: {
  companyId: string;
  runtimeId?: string;
  runtimeOnline: boolean;
  activeProvider?: PluginProvider;
  targetLabel: string;
}): {
  pendingPluginId?: string;
  error?: string;
  notice?: string;
  canChange: boolean;
  unavailableReason?: string;
  change(plugin: PluginItemV1): Promise<void>;
  applyOverride(plugin: PluginItemV1): PluginItemV1;
  applyOverrides(plugins: PluginItemV1[]): PluginItemV1[];
  clearMessages(): void;
} {
  const readCompanySnapshot = useCallback(
    () => readSnapshot(companyId),
    [companyId],
  );
  const snapshot = useSyncExternalStore(
    subscribe,
    readCompanySnapshot,
    readCompanySnapshot,
  );
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [changePluginInstallation] = useChangePluginInstallationMutation();
  const [refreshPlugins] = useRefreshPluginsMutation();
  // A change outlives the page that started it: the person can move between
  // the list and a plugin's page while it runs. Its outcome is then a toast,
  // naming the plugin, on whichever page they are on.
  const mounted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const report = useCallback(
    (plugin: PluginItemV1, kind: "error" | "notice", text: string) => {
      if (mounted.current) {
        if (kind === "error") setError(text);
        else setNotice(text);
      } else if (kind === "error") {
        toast.error(`${plugin.name}: ${text}`);
      } else {
        toast(`${plugin.name}: ${text}`);
      }
    },
    [],
  );

  const unavailableReason = !shouldUseDesktopHashRouter()
    ? "Open Zenith Team Desktop to install or remove plugins."
    : !companyId || !runtimeId
      ? "Choose a computer to install or remove plugins."
      : !runtimeOnline
        ? `${targetLabel} is offline. Bring it online to install or remove plugins.`
        : undefined;

  const change = useCallback(
    async (plugin: PluginItemV1) => {
      const action = plugin.action;
      if (action === "none" || readSnapshot(companyId).pendingPluginId) return;
      setError(undefined);
      setNotice(undefined);
      if (activeProvider && plugin.provider !== activeProvider) {
        setError("This plugin belongs to a different provider.");
        return;
      }
      if (unavailableReason || !runtimeId) {
        setError(unavailableReason);
        return;
      }

      updateSnapshot(companyId, (current) => ({
        ...current,
        pendingPluginId: plugin.id,
      }));
      try {
        const operation = await changePluginInstallation({
          action,
          companyId,
          nativeId: plugin.nativeId,
          provider: plugin.provider,
          runtimeId,
        }).unwrap();
        if (operation.state === "failed") {
          report(
            plugin,
            "error",
            action === "install"
              ? "Plugin installation failed."
              : "Plugin removal failed.",
          );
          return;
        }
        if (operation.state === "queued" || operation.state === "installing") {
          report(
            plugin,
            "notice",
            "The provider is still processing this plugin change.",
          );
          return;
        }
        recordOverride(
          companyId,
          runtimeId,
          pluginAfterSuccessfulAction(plugin),
        );
        if (action === "install" && operation.state === "awaiting_user") {
          report(
            plugin,
            "notice",
            operation.continueUrl
              ? "Finish authentication in your browser, then refresh plugins."
              : `${providerDisplayName(plugin.provider)} opened authentication in your browser. Finish it, then refresh plugins.`,
          );
          if (operation.continueUrl) {
            const opened = await openDesktopExternal(operation.continueUrl);
            if (!opened.ok) report(plugin, "error", opened.reason);
          }
        }
        // Rediscovery invalidates the Plugin tag, so every open catalog and
        // plugin page refetches once the provider has reported the change.
        void refreshPlugins({
          companyId,
          provider: plugin.provider,
          runtimeId,
        });
      } catch (caught) {
        report(
          plugin,
          "error",
          errorMessage(
            caught,
            action === "install"
              ? "Plugin installation could not be started."
              : "Plugin removal could not be started.",
          ),
        );
      } finally {
        updateSnapshot(companyId, (current) =>
          current.pendingPluginId === plugin.id
            ? { overridesRevision: current.overridesRevision }
            : current,
        );
      }
    },
    [
      activeProvider,
      changePluginInstallation,
      companyId,
      refreshPlugins,
      report,
      runtimeId,
      unavailableReason,
    ],
  );

  // The revision is a dependency so each recorded override hands consumers
  // new functions to re-apply with; the functions read the live store.
  const { overridesRevision } = snapshot;
  const appliers = useMemo(
    () => ({
      applyOverride: (plugin: PluginItemV1) =>
        applyStoredOverride(companyId, runtimeId, plugin, Date.now()),
      applyOverrides: (plugins: PluginItemV1[]) => {
        const now = Date.now();
        let changed = false;
        const next = plugins.map((plugin) => {
          const applied = applyStoredOverride(
            companyId,
            runtimeId,
            plugin,
            now,
          );
          if (applied !== plugin) changed = true;
          return applied;
        });
        return changed ? next : plugins;
      },
    }),
    [companyId, overridesRevision, runtimeId],
  );

  const clearMessages = useCallback(() => {
    setError(undefined);
    setNotice(undefined);
  }, []);

  return {
    pendingPluginId: snapshot.pendingPluginId,
    error,
    notice,
    canChange: !unavailableReason,
    unavailableReason,
    change,
    applyOverride: appliers.applyOverride,
    applyOverrides: appliers.applyOverrides,
    clearMessages,
  };
}

function providerDisplayName(provider: PluginProvider) {
  return provider === "claude" ? "Claude" : "Codex";
}

function errorMessage(error: unknown, fallback: string) {
  if (!error || typeof error !== "object") return fallback;
  if ("message" in error && typeof error.message === "string") {
    return error.message;
  }
  if (
    "data" in error &&
    error.data &&
    typeof error.data === "object" &&
    "message" in error.data &&
    typeof error.data.message === "string"
  ) {
    return error.data.message;
  }
  return fallback;
}
