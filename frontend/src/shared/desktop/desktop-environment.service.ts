import type {
  DesktopEnvironment,
  DesktopAgentDiscoveryResult,
  DesktopAgentVerificationResult,
  DesktopNativeTheme,
  DesktopMenuCommand,
  DesktopContextMenuPayload,
  DesktopResourceFilePayload,
  DesktopGeneratedFileReference,
  DesktopGeneratedFileLinkReference,
  DesktopGeneratedFileLinkResult,
  DesktopGeneratedFilePreviewResult,
  DesktopGeneratedFileActionResult,
  DesktopWorkspaceAssetReference,
  DesktopWorkspaceAssetListRequest,
  DesktopWorkspaceAssetListResult,
  DesktopWorkspaceAssetPreviewResult,
  DesktopWorkspaceAssetActionResult,
  DesktopPluginResult,
  DesktopPluginActionRequest,
  DesktopConnectorActionRequest,
  DesktopConnectorStatusResult,
  DesktopPluginRefreshRequest,
  DesktopPluginDetail,
  DesktopPluginReadResult,
  DesktopTaskReviewRequest,
  DesktopTaskReviewResult,
  DesktopNotificationPayload,
  DesktopHuddleCallNotification,
  DesktopScreenShareSource,
  DesktopScreenShareState,
  DesktopScreenShareAction,
  DesktopHuddleCallAction,
  DesktopNotificationResult,
  DesktopWorkbenchState,
  DesktopWorkbenchStatusPatch,
  DesktopPowerLifecycleState,
  DesktopSupportDiagnosticsCaptureRequest,
  DesktopSupportDiagnosticsCaptureResult,
  DesktopStartupSettingsState,
  DesktopStartupSettingsResult,
  DesktopUpdateState,
  DesktopUpdateCheckResult,
  DesktopUpdateDownloadResult,
  DesktopUpdateRestartResult,
  DesktopAutomaticUpdatesResult,
  DesktopDiagnosticPayload,
  DesktopDaemonStatus,
  DesktopDaemonActionResult,
  DesktopDaemonStopOptions,
  DesktopDaemonLogBatch,
  DesktopDaemonLogsResult,
  DesktopToolRequest,
  DesktopToolResult,
  ZenithTeamDesktopBridge,
  DesktopGlobalLike,
} from "../../../electron/shared/desktop-bridge.types";
type DesktopAgentVerificationCacheEntry = {
  completedAt: number;
  result: DesktopAgentVerificationResult;
};

const desktopAgentVerificationCache = new Map<
  import("@/shared/api/types").AgentProvider,
  DesktopAgentVerificationCacheEntry
>();
const desktopAgentVerificationInFlight = new Map<
  import("@/shared/api/types").AgentProvider,
  Promise<DesktopAgentVerificationResult>
>();

export async function operateDesktopTool(
  request: Omit<DesktopToolRequest, "schemaVersion">,
): Promise<DesktopToolResult> {
  const bridge = getZenithTeamDesktopBridge(globalThis);
  if (!bridge?.operateTool)
    return desktopOperationFailure(
      "desktop_required",
      "Tool setup requires Zenith Team Desktop.",
    );
  try {
    return await bridge.operateTool({ ...request, schemaVersion: 1 });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Connect this computer and try again.",
    );
  }
}

declare global {
  interface Window {
    zenithTeamDesktop?: ZenithTeamDesktopBridge;
  }
}

export const WEB_DESKTOP_ENVIRONMENT_FALLBACK: DesktopEnvironment = {
  isDesktop: false,
  platform: "unknown",
  appVersion: null,
  environment: "unknown",
};

export const WEB_DESKTOP_NATIVE_THEME_FALLBACK: DesktopNativeTheme = {
  colorScheme: "light",
  shouldDifferentiateWithoutColor: false,
  shouldUseDarkColors: false,
  shouldUseHighContrastColors: false,
  shouldUseInvertedColorScheme: false,
  inForcedColorsMode: false,
  prefersReducedTransparency: false,
  themeSource: "system",
};

export const WEB_DESKTOP_WORKBENCH_STATE_FALLBACK: DesktopWorkbenchState = {
  blockedWorkCount: 0,
  globalShortcut: {
    accelerator: "CommandOrControl+Shift+Space",
    enabled: false,
    reason: null,
    registered: false,
  },
  isPaused: false,
  notificationsSupported: false,
  pendingApprovalCount: 0,
  readyForReviewCount: 0,
  unreadCount: 0,
};

export const WEB_DESKTOP_POWER_LIFECYCLE_STATE_FALLBACK: DesktopPowerLifecycleState =
  {
    battery: "unknown",
    idleState: "unknown",
    idleTimeSeconds: null,
    lastEvent: null,
    lifecycle: "active",
    speedLimitPercent: null,
    thermalState: "unknown",
  };

export const WEB_DESKTOP_STARTUP_SETTINGS_FALLBACK: DesktopStartupSettingsState =
  {
    openAsHidden: false,
    openAtLogin: false,
    reason: "Desktop startup settings are not available here.",
    status: "unsupported",
    supported: false,
  };

export const WEB_DESKTOP_UPDATE_STATE_FALLBACK: DesktopUpdateState = {
  availableVersion: null,
  automaticUpdatesEnabled: false,
  automaticUpdatesSupported: false,
  checkedAt: null,
  downloadPercent: null,
  errorMessage: "Desktop native update checks are not available here.",
  feedUrl: null,
  installedVersion: null,
  manualUrl: null,
  policy: "manual",
  releaseDate: null,
  releaseName: null,
  status: "manual",
  updateAvailable: false,
  updateUrl: null,
};

export const WEB_DESKTOP_DAEMON_STATUS_FALLBACK: DesktopDaemonStatus = {
  activeRunCount: 0,
  agents: [],
  cliVersion: null,
  daemonId: null,
  pid: null,
  ready: false,
  reason: "desktop_daemon_unavailable",
  reloadPendingReason: null,
  skippedAgents: [],
  startAttempts: 0,
  state: "unavailable",
};

export function getZenithTeamDesktopBridge(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return globalScope.window?.zenithTeamDesktop ?? globalScope.zenithTeamDesktop;
}

export function shouldUseDesktopHashRouter(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return Boolean(getZenithTeamDesktopBridge(globalScope));
}

export async function getDesktopEnvironment(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopEnvironment> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge) {
    return WEB_DESKTOP_ENVIRONMENT_FALLBACK;
  }

  try {
    return await bridge.getEnvironment();
  } catch {
    return WEB_DESKTOP_ENVIRONMENT_FALLBACK;
  }
}

export async function discoverDesktopAgents(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopAgentDiscoveryResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.discoverAgents) {
    return {
      ok: false,
      reason: "Local agent discovery requires Zenith Team Desktop.",
    };
  }
  try {
    return await bridge.discoverAgents();
  } catch {
    return {
      ok: false,
      reason: "Zenith Team could not inspect local agent providers.",
    };
  }
}

export async function installDesktopPlugin(
  request: Omit<DesktopPluginActionRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginResult> {
  return requestDesktopPlugin("installPlugin", request, globalScope);
}

export async function connectDesktopConnector(
  request: Omit<DesktopConnectorActionRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.connectConnector) {
    return desktopOperationFailure(
      "desktop_required",
      "Connector setup must be completed in Zenith Team Desktop on the selected runtime.",
    );
  }
  try {
    return await bridge.connectConnector({ schemaVersion: 1, ...request });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Connector setup requires an online local daemon.",
    );
  }
}

/**
 * Asks the daemon, live, whether one connector is connected. A connect page
 * in the browser has no callback into Zenith Team, so a flow waiting on one
 * checks this instead.
 */
export async function readDesktopConnectorStatus(
  request: Omit<DesktopConnectorActionRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopConnectorStatusResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.readConnectorStatus) {
    return desktopOperationFailure(
      "daemon_update_required",
      "Update Zenith Team Desktop to check connections.",
    );
  }
  try {
    return await bridge.readConnectorStatus({ schemaVersion: 1, ...request });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Checking a connection requires an online local daemon.",
    );
  }
}

export async function refreshDesktopConnectors(
  request: Omit<DesktopPluginRefreshRequest, "schemaVersion" | "force">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.refreshConnectors) {
    return desktopOperationFailure(
      "desktop_required",
      "Connection discovery must run in Zenith Team Desktop on the selected runtime.",
    );
  }
  try {
    return await bridge.refreshConnectors({ schemaVersion: 1, ...request });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Connection discovery requires an online local daemon.",
    );
  }
}

export async function uninstallDesktopPlugin(
  request: Omit<DesktopPluginActionRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginResult> {
  return requestDesktopPlugin("uninstallPlugin", request, globalScope);
}

export async function refreshDesktopPlugins(
  request: Omit<DesktopPluginRefreshRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.refreshPlugins) {
    return desktopOperationFailure(
      "desktop_required",
      "Plugin discovery requires Zenith Team Desktop.",
    );
  }
  try {
    return await bridge.refreshPlugins({ schemaVersion: 1, ...request });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Plugin discovery requires an online local daemon.",
    );
  }
}

/** Why a live Plugin read failed; `code` is the daemon's or the bridge's. */
export class DesktopPluginReadError extends Error {
  readonly code: string;

  constructor(code: string, reason: string) {
    super(reason);
    this.name = "DesktopPluginReadError";
    this.code = code;
  }
}

/**
 * Reads a Plugin live from the daemon on this computer. Rejects with a
 * DesktopPluginReadError outside Zenith Team Desktop, when the daemon is
 * offline or predates the read, or when the Plugin is not in that Runtime's
 * catalog; the caller keeps the server's observation then.
 */
export async function readDesktopPlugin(
  input: {
    companyId: string;
    runtimeId: string;
    provider: import("@/shared/api/types").PluginProvider;
    nativeId: string;
  },
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPluginDetail> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.readPlugin) {
    throw new DesktopPluginReadError(
      "desktop_required",
      "Live Plugin details require Zenith Team Desktop.",
    );
  }
  let result: DesktopPluginReadResult;
  try {
    result = await bridge.readPlugin({
      schemaVersion: 1,
      companyId: input.companyId,
      runtimeId: input.runtimeId,
      provider: input.provider,
      nativeId: input.nativeId,
    });
  } catch {
    throw new DesktopPluginReadError(
      "daemon_unavailable",
      "Live Plugin details require an online local daemon.",
    );
  }
  if (!result.ok) {
    throw new DesktopPluginReadError(result.code, result.reason);
  }
  if (!isDesktopPluginDetail(result.value, input.nativeId)) {
    throw new DesktopPluginReadError(
      "invalid_response",
      "Plugin details response is invalid.",
    );
  }
  return result.value;
}

function isDesktopPluginDetail(
  value: unknown,
  nativeId: string,
): value is DesktopPluginDetail {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const detail = value as Record<string, unknown>;
  return (
    detail.schemaVersion === 1 &&
    detail.nativeId === nativeId &&
    typeof detail.name === "string" &&
    detail.name.length > 0 &&
    Array.isArray(detail.defaultPrompts) &&
    detail.defaultPrompts.every((prompt) => typeof prompt === "string") &&
    Array.isArray(detail.apps) &&
    Array.isArray(detail.skills) &&
    Array.isArray(detail.mcpServers)
  );
}

async function requestDesktopPlugin(
  method: "installPlugin" | "uninstallPlugin",
  request: Omit<DesktopPluginActionRequest, "schemaVersion">,
  globalScope: DesktopGlobalLike,
): Promise<DesktopPluginResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  const operation = bridge?.[method];
  if (!operation) {
    return desktopOperationFailure(
      "desktop_required",
      "Plugin management requires Zenith Team Desktop.",
    );
  }
  try {
    return await operation({ schemaVersion: 1, ...request });
  } catch {
    return desktopOperationFailure(
      "daemon_unavailable",
      "Plugin management requires an online local daemon.",
    );
  }
}

function desktopOperationFailure(code: string, reason: string) {
  return { code, ok: false as const, reason };
}

export async function verifyDesktopAgent(
  provider: import("@/shared/api/types").AgentProvider,
  {
    cacheMaxAgeMs = 0,
    globalScope = globalThis,
  }: {
    cacheMaxAgeMs?: number;
    globalScope?: DesktopGlobalLike;
  } = {},
): Promise<DesktopAgentVerificationResult> {
  const cached = desktopAgentVerificationCache.get(provider);
  if (
    cached &&
    cacheMaxAgeMs > 0 &&
    Date.now() - cached.completedAt <= cacheMaxAgeMs
  ) {
    return cached.result;
  }
  const existing = desktopAgentVerificationInFlight.get(provider);
  if (existing) {
    return existing;
  }

  const request = requestDesktopAgentVerification(provider, globalScope);
  desktopAgentVerificationInFlight.set(provider, request);
  try {
    const result = await request;
    desktopAgentVerificationCache.set(provider, {
      completedAt: Date.now(),
      result,
    });
    return result;
  } finally {
    desktopAgentVerificationInFlight.delete(provider);
  }
}

export function clearDesktopAgentVerificationCache(
  provider?: import("@/shared/api/types").AgentProvider,
) {
  if (provider) {
    desktopAgentVerificationCache.delete(provider);
    return;
  }
  desktopAgentVerificationCache.clear();
}

async function requestDesktopAgentVerification(
  provider: import("@/shared/api/types").AgentProvider,
  globalScope: DesktopGlobalLike,
): Promise<DesktopAgentVerificationResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.verifyAgent) {
    return {
      schemaVersion: 1,
      provider,
      ok: false,
      code: "provider_failed",
    };
  }
  try {
    const result = await bridge.verifyAgent(provider);
    if (result.schemaVersion === 1 && result.provider === provider) {
      return result;
    }
  } catch {
    // Renderer-facing failures remain in the closed safe code set.
  }
  return {
    schemaVersion: 1,
    provider,
    ok: false,
    code: "provider_failed",
  };
}

export async function openDesktopExternal(
  url: string,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  // Never rejects: callers wait on the answer to leave a launching state.
  if (bridge) {
    try {
      return await bridge.openExternal(url);
    } catch {
      return { ok: false as const, reason: "The link could not be opened." };
    }
  }

  const browserWindow = globalScope.window;

  if (typeof browserWindow?.open !== "function") {
    return { ok: false as const, reason: "No browser window is available." };
  }

  const openedWindow = browserWindow.open(url, "_blank", "noopener,noreferrer");

  if (!openedWindow) {
    return { ok: false as const, reason: "The browser blocked the link." };
  }

  return { ok: true as const };
}

export function subscribeToDesktopMenuCommands(
  listener: (command: DesktopMenuCommand) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onMenuCommand(listener) ??
    (() => {})
  );
}

export async function getDesktopNativeTheme(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopNativeTheme> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getNativeTheme) {
    return WEB_DESKTOP_NATIVE_THEME_FALLBACK;
  }

  try {
    return await bridge.getNativeTheme();
  } catch {
    return WEB_DESKTOP_NATIVE_THEME_FALLBACK;
  }
}

export function subscribeToDesktopNativeTheme(
  listener: (theme: DesktopNativeTheme) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onNativeThemeUpdated?.(listener) ??
    (() => {})
  );
}

export async function showDesktopContextMenu(
  payload: DesktopContextMenuPayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.showContextMenu) {
    return {
      ok: false as const,
      reason: "Desktop context menus are not available here.",
    };
  }

  return bridge.showContextMenu(payload);
}

export function canPreviewDesktopGeneratedFiles(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return Boolean(getZenithTeamDesktopBridge(globalScope)?.previewGeneratedFile);
}

export async function resolveDesktopGeneratedFileLink(
  reference: DesktopGeneratedFileLinkReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopGeneratedFileLinkResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.resolveGeneratedFileLink) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "File preview is available in Zenith Team desktop.",
    };
  }
  try {
    return await bridge.resolveGeneratedFileLink(reference);
  } catch {
    return {
      code: "unavailable",
      ok: false,
      reason: "Generated file preview is unavailable.",
    };
  }
}

export async function previewDesktopGeneratedFile(
  reference: DesktopGeneratedFileReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopGeneratedFilePreviewResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.previewGeneratedFile) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "File preview is available in Zenith Team desktop.",
    };
  }
  return bridge.previewGeneratedFile(reference);
}

export async function saveDesktopGeneratedFile(
  reference: DesktopGeneratedFileReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopGeneratedFileActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.saveGeneratedFile) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "File export is available in Zenith Team desktop.",
    };
  }
  return bridge.saveGeneratedFile(reference);
}

export async function revealDesktopGeneratedFile(
  reference: DesktopGeneratedFileReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopGeneratedFileActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.revealGeneratedFile) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "File reveal is available in Zenith Team desktop.",
    };
  }
  return bridge.revealGeneratedFile(reference);
}

export function canBrowseDesktopWorkspaceAssets(
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return Boolean(
    bridge?.listWorkspaceAssets &&
    bridge.previewWorkspaceAsset &&
    bridge.saveWorkspaceAsset &&
    bridge.revealWorkspaceAsset,
  );
}

export async function listDesktopWorkspaceAssets(
  request: DesktopWorkspaceAssetListRequest,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkspaceAssetListResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.listWorkspaceAssets) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "Workspace Assets are available in Zenith Team Desktop.",
    };
  }
  return bridge.listWorkspaceAssets(request);
}

export async function previewDesktopWorkspaceAsset(
  reference: DesktopWorkspaceAssetReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkspaceAssetPreviewResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.previewWorkspaceAsset) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "Workspace asset preview is available in Zenith Team Desktop.",
    };
  }
  return bridge.previewWorkspaceAsset(reference);
}

export async function saveDesktopWorkspaceAsset(
  reference: DesktopWorkspaceAssetReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkspaceAssetActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.saveWorkspaceAsset) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "Workspace asset export is available in Zenith Team Desktop.",
    };
  }
  return bridge.saveWorkspaceAsset(reference);
}

export async function revealDesktopWorkspaceAsset(
  reference: DesktopWorkspaceAssetReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkspaceAssetActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.revealWorkspaceAsset) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "Workspace asset reveal is available in Zenith Team Desktop.",
    };
  }
  return bridge.revealWorkspaceAsset(reference);
}

export async function reviewDesktopTaskWorkspace(
  request: DesktopTaskReviewRequest,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopTaskReviewResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  if (!bridge?.reviewTaskWorkspace) {
    return {
      code: "desktop_required",
      ok: false,
      reason: "Task review is available in Zenith Team desktop.",
    };
  }
  return bridge.reviewTaskWorkspace(request);
}

export async function openDesktopResourceFile(
  payload: DesktopResourceFilePayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.openResourceFile) {
    return {
      ok: false as const,
      reason: "Desktop resource open is not available here.",
    };
  }

  return bridge.openResourceFile(payload);
}

export async function revealDesktopResourceFile(
  payload: DesktopResourceFilePayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.revealResourceFile) {
    return {
      ok: false as const,
      reason: "Desktop resource reveal is not available here.",
    };
  }

  return bridge.revealResourceFile(payload);
}

export async function saveDesktopResourceFile(
  payload: DesktopResourceFilePayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.saveResourceFile) {
    return {
      ok: false as const,
      reason: "Desktop resource save is not available here.",
    };
  }

  return bridge.saveResourceFile(payload);
}

export function startDesktopResourceDrag(
  payload: DesktopResourceFilePayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  getZenithTeamDesktopBridge(globalScope)?.startResourceDrag?.(payload);
}

export async function getDesktopWorkbenchState(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkbenchState> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getWorkbenchState) {
    return WEB_DESKTOP_WORKBENCH_STATE_FALLBACK;
  }

  try {
    return await bridge.getWorkbenchState();
  } catch {
    return WEB_DESKTOP_WORKBENCH_STATE_FALLBACK;
  }
}

export async function setDesktopWorkbenchStatus(
  status: DesktopWorkbenchStatusPatch,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.setWorkbenchStatus) {
    return {
      ok: false as const,
      reason: "Desktop workbench status is not available here.",
      state: WEB_DESKTOP_WORKBENCH_STATE_FALLBACK,
    };
  }

  return bridge.setWorkbenchStatus(status);
}

export async function getDesktopPowerLifecycleState(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopPowerLifecycleState> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getPowerLifecycleState) {
    return WEB_DESKTOP_POWER_LIFECYCLE_STATE_FALLBACK;
  }

  try {
    return await bridge.getPowerLifecycleState();
  } catch {
    return WEB_DESKTOP_POWER_LIFECYCLE_STATE_FALLBACK;
  }
}

export function subscribeToDesktopPowerLifecycle(
  listener: (state: DesktopPowerLifecycleState) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onPowerLifecycleUpdated?.(
      listener,
    ) ?? (() => {})
  );
}

export async function captureDesktopSupportDiagnostics(
  request: DesktopSupportDiagnosticsCaptureRequest = {},
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopSupportDiagnosticsCaptureResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.captureSupportDiagnostics) {
    return {
      ok: false,
      reason: "Desktop support diagnostics capture is not available here.",
    };
  }

  return bridge.captureSupportDiagnostics({
    durationMs: request.durationMs,
  });
}

export async function getDesktopStartupSettings(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopStartupSettingsState> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getStartupSettings) {
    return WEB_DESKTOP_STARTUP_SETTINGS_FALLBACK;
  }

  try {
    return await bridge.getStartupSettings();
  } catch {
    return WEB_DESKTOP_STARTUP_SETTINGS_FALLBACK;
  }
}

export async function setDesktopOpenAtLogin(
  enabled: boolean,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopStartupSettingsResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.setOpenAtLogin) {
    return {
      ok: false,
      reason: "Desktop startup settings are not available here.",
      state: WEB_DESKTOP_STARTUP_SETTINGS_FALLBACK,
    };
  }

  return bridge.setOpenAtLogin(enabled);
}

export async function getDesktopUpdateState(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopUpdateState> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getUpdateState) {
    return WEB_DESKTOP_UPDATE_STATE_FALLBACK;
  }

  try {
    return await bridge.getUpdateState();
  } catch {
    return WEB_DESKTOP_UPDATE_STATE_FALLBACK;
  }
}

export async function checkDesktopUpdates(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopUpdateCheckResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.checkForUpdates) {
    return {
      manualUrl: null,
      mode: "manual",
      ok: false,
      reason: "Desktop update checks are not available here.",
    };
  }

  return bridge.checkForUpdates();
}

export async function downloadDesktopUpdate(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopUpdateDownloadResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.downloadUpdate) {
    return {
      manualUrl: null,
      mode: "manual",
      ok: false,
      reason: "Desktop update downloads are not available here.",
    };
  }

  return bridge.downloadUpdate();
}

export async function restartDesktopUpdate(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopUpdateRestartResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.restartToUpdate) {
    return {
      ok: false,
      reason: "Desktop update restart is not available here.",
    };
  }

  return bridge.restartToUpdate();
}

export async function setDesktopAutomaticUpdates(
  enabled: boolean,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopAutomaticUpdatesResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.setAutomaticUpdates) {
    return {
      ok: false,
      reason: "Automatic updates are only available in Zenith Team Desktop.",
      state: WEB_DESKTOP_UPDATE_STATE_FALLBACK,
    };
  }

  return bridge.setAutomaticUpdates(enabled);
}

export function subscribeToDesktopUpdateState(
  listener: (state: DesktopUpdateState) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onUpdateStateUpdated?.(listener) ??
    (() => {})
  );
}

export function subscribeToDesktopUpdateDialogRequests(
  listener: () => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onUpdateDialogRequested?.(
      listener,
    ) ?? (() => {})
  );
}

const DESKTOP_DAEMON_ACTION_UNAVAILABLE: DesktopDaemonActionResult = {
  code: "desktop_unavailable",
  ok: false,
  reason: "Available in Zenith Team Desktop.",
};

const DESKTOP_DAEMON_LOGS_UNAVAILABLE: DesktopDaemonLogsResult = {
  code: "desktop_unavailable",
  ok: false,
  reason: "Available in Zenith Team Desktop.",
};

export async function getDesktopDaemonStatus(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonStatus> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.getDaemonStatus) {
    return WEB_DESKTOP_DAEMON_STATUS_FALLBACK;
  }

  try {
    return await bridge.getDaemonStatus();
  } catch {
    return WEB_DESKTOP_DAEMON_STATUS_FALLBACK;
  }
}

async function runDesktopDaemonAction(
  action: (() => Promise<DesktopDaemonActionResult>) | undefined,
): Promise<DesktopDaemonActionResult> {
  if (!action) {
    return DESKTOP_DAEMON_ACTION_UNAVAILABLE;
  }

  try {
    return await action();
  } catch (error) {
    return {
      code: "desktop_error",
      ok: false,
      reason:
        error instanceof Error && error.message
          ? error.message
          : "The daemon action failed.",
    };
  }
}

export function startDesktopDaemon(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonAction(
    bridge?.startDaemon ? () => bridge.startDaemon!() : undefined,
  );
}

export function stopDesktopDaemon(
  options: DesktopDaemonStopOptions = {},
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonAction(
    bridge?.stopDaemon ? () => bridge.stopDaemon!(options) : undefined,
  );
}

export function restartDesktopDaemon(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonActionResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonAction(
    bridge?.restartDaemon ? () => bridge.restartDaemon!() : undefined,
  );
}

async function runDesktopDaemonLogsAction(
  action: (() => Promise<DesktopDaemonLogsResult>) | undefined,
): Promise<DesktopDaemonLogsResult> {
  if (!action) {
    return DESKTOP_DAEMON_LOGS_UNAVAILABLE;
  }

  try {
    return await action();
  } catch {
    return {
      code: "desktop_error",
      ok: false,
      reason: "The daemon log is not available.",
    };
  }
}

export function startDesktopDaemonLogs(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonLogsResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.startDaemonLogs ? () => bridge.startDaemonLogs!() : undefined,
  );
}

export function stopDesktopDaemonLogs(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonLogsResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.stopDaemonLogs ? () => bridge.stopDaemonLogs!() : undefined,
  );
}

export function revealDesktopDaemonLog(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonLogsResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.revealDaemonLog ? () => bridge.revealDaemonLog!() : undefined,
  );
}

export function subscribeToDesktopDaemonStatus(
  listener: (status: DesktopDaemonStatus) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onDaemonStatusUpdated?.(
      listener,
    ) ?? (() => {})
  );
}

export function subscribeToDesktopDaemonLogLines(
  listener: (batch: DesktopDaemonLogBatch) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onDaemonLogLines?.(listener) ??
    (() => {})
  );
}

export async function setDesktopGlobalShortcutEnabled(
  enabled: boolean,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.setGlobalShortcutEnabled) {
    return {
      ok: false as const,
      reason: "Desktop global shortcut settings are not available here.",
      state: WEB_DESKTOP_WORKBENCH_STATE_FALLBACK,
    };
  }

  return bridge.setGlobalShortcutEnabled(enabled);
}

export async function showDesktopNotification(
  payload: DesktopNotificationPayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.showNotification) {
    return {
      ok: false as const,
      reason: "Desktop notifications are not available here.",
    };
  }

  return bridge.showNotification(payload);
}

export async function showDesktopHuddleCallNotification(
  payload: DesktopHuddleCallNotification,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopNotificationResult> {
  const bridge = getZenithTeamDesktopBridge(globalScope);

  if (!bridge?.showHuddleCallNotification) {
    return {
      ok: false,
      reason: "Desktop call notifications are not available here.",
    };
  }

  return bridge.showHuddleCallNotification(payload);
}

export function closeDesktopHuddleCallNotification(
  invitationId: string,
  globalScope: DesktopGlobalLike = globalThis,
) {
  void getZenithTeamDesktopBridge(globalScope)
    ?.closeHuddleCallNotification?.(invitationId)
    .catch(() => {});
}

export function subscribeToDesktopHuddleCallActions(
  listener: (action: DesktopHuddleCallAction) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onHuddleCallAction?.(listener) ??
    (() => {})
  );
}

// The desktop app picks a screen or window in the Huddle window itself; the
// web relies on the browser's own picker.
export function canPickDesktopScreenShareSource(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return Boolean(
    getZenithTeamDesktopBridge(globalScope)?.listScreenShareSources,
  );
}

// DesktopScreenSharePermissionError means the OS, not Zenith, is refusing
// screen capture, and the person can fix it in system settings.
export class DesktopScreenSharePermissionError extends Error {}

export async function listDesktopScreenShareSources(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<{ sources: DesktopScreenShareSource[]; audio: boolean }> {
  const result =
    await getZenithTeamDesktopBridge(globalScope)?.listScreenShareSources?.();
  if (!result) throw new Error("Screen sharing is not available here.");
  if (!result.ok) {
    throw result.blocked
      ? new DesktopScreenSharePermissionError(result.reason)
      : new Error(result.reason);
  }
  return { sources: result.sources, audio: result.audio === true };
}

// The desktop app marks the shared screen and floats sharing controls over
// other apps while a Huddle window shares.
export function reportDesktopScreenShareState(
  state: DesktopScreenShareState,
  globalScope: DesktopGlobalLike = globalThis,
) {
  void getZenithTeamDesktopBridge(globalScope)
    ?.reportScreenShareState?.(state)
    .catch(() => {});
}

export function subscribeToDesktopScreenShareActions(
  listener: (action: DesktopScreenShareAction) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onScreenShareAction?.(listener) ??
    (() => {})
  );
}

export function openDesktopScreenShareSettings(
  globalScope: DesktopGlobalLike = globalThis,
) {
  void getZenithTeamDesktopBridge(globalScope)
    ?.openScreenShareSettings?.()
    .catch(() => {});
}

export async function selectDesktopScreenShareSource(
  sourceId: string,
  shareAudio: boolean,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const result = await getZenithTeamDesktopBridge(
    globalScope,
  )?.selectScreenShareSource?.(sourceId, shareAudio);
  if (!result) throw new Error("Screen sharing is not available here.");
  if (!result.ok) throw new Error(result.reason);
}

export function subscribeToDesktopWorkbenchState(
  listener: (state: DesktopWorkbenchState) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onWorkbenchStatusUpdated?.(
      listener,
    ) ?? (() => {})
  );
}

export function applyDesktopNativeThemeAttributes(
  theme: DesktopNativeTheme,
  documentElement: Pick<HTMLElement, "dataset">,
) {
  documentElement.dataset.zenithTeamNativeTheme = theme.colorScheme;
  documentElement.dataset.zenithTeamHighContrast = String(
    theme.shouldUseHighContrastColors || theme.inForcedColorsMode,
  );
  documentElement.dataset.zenithTeamInvertedColors = String(
    theme.shouldUseInvertedColorScheme,
  );
  documentElement.dataset.zenithTeamReducedTransparency = String(
    theme.prefersReducedTransparency,
  );
  documentElement.dataset.zenithTeamDifferentiateWithoutColor = String(
    theme.shouldDifferentiateWithoutColor,
  );
}

export function subscribeToDesktopGoogleOAuthCallback(
  listener: (route: string) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onGoogleOAuthCallback?.(
      listener,
    ) ?? (() => {})
  );
}

export function reportDesktopDiagnostic(
  payload: DesktopDiagnosticPayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  getZenithTeamDesktopBridge(globalScope)?.reportDiagnostic?.(payload);
}
