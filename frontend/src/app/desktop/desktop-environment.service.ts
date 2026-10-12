import type {
  DesktopEnvironmentName,
  DesktopPlatform,
} from "./desktop-download.service";

export type DesktopEnvironment = {
  isDesktop: boolean;
  platform: DesktopPlatform;
  appVersion: string | null;
  environment: DesktopEnvironmentName;
};

export type DesktopRunPreviewFrame =
  | { type: "run.preview.reset" }
  | {
      channel: "reasoning" | "text";
      companyId: string;
      content: string;
      conversationId: string;
      itemKey: string;
      operation: "append" | "replace";
      runId: string;
      type: "run.preview.delta";
    };

export type DesktopAgentDiscoveryCapabilities = {
  authStatus?: "authenticated" | "unauthenticated" | "unknown";
  modelCatalog?: import("@/shared/api/types").RuntimeModel[];
};

export type DesktopDiscoveredAgent = {
  capabilities: DesktopAgentDiscoveryCapabilities;
  provider: import("@/shared/api/types").AgentProvider;
};

export type DesktopAgentDiscoverySnapshot = {
  agents: DesktopDiscoveredAgent[];
  schemaVersion: 1;
  warnings?: string[];
};

export type DesktopAgentDiscoveryResult =
  | { ok: true; snapshot: DesktopAgentDiscoverySnapshot }
  | { ok: false; reason: string };

export type DesktopAgentVerificationCode =
  | "authentication_failed"
  | "access_unavailable"
  | "network_unavailable"
  | "provider_timeout"
  | "provider_failed";

export type DesktopAgentVerificationResult =
  | {
      schemaVersion: 1;
      provider: import("@/shared/api/types").AgentProvider;
      ok: true;
    }
  | {
      schemaVersion: 1;
      provider: import("@/shared/api/types").AgentProvider;
      ok: false;
      code: DesktopAgentVerificationCode;
    };

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

export type DesktopNativeTheme = {
  colorScheme: "dark" | "light";
  shouldDifferentiateWithoutColor: boolean;
  shouldUseDarkColors: boolean;
  shouldUseHighContrastColors: boolean;
  shouldUseInvertedColorScheme: boolean;
  inForcedColorsMode: boolean;
  prefersReducedTransparency: boolean;
  themeSource: "dark" | "light" | "system";
};

export type DesktopRouteKey =
  | "analytics"
  | "approvals"
  | "assets"
  | "cockpit"
  | "scheduled"
  | "team"
  | "settings-company"
  | "settings-profile"
  | "work-search";

export type DesktopMenuCommand =
  | { type: "command-palette:open" }
  | {
      type: "notification:activate";
      notificationId: string;
      companyId?: string;
      userId?: string;
      path: string;
    }
  | { type: "route:navigate"; route: DesktopRouteKey }
  | { type: "route:path"; path: string }
  | { type: "route:settings" }
  | { type: "app:reload" };

export type DesktopContextMenuPayload = {
  copyText?: string;
  detailText?: string;
  kind: "agent" | "approval" | "message" | "resource" | "task";
  label: string;
  routePath?: string;
  shareUrl?: string;
  x?: number;
  y?: number;
};

export type DesktopResourceFilePayload = {
  bytes: ArrayBuffer | ArrayBufferView;
  filename?: string;
  mimeType?: string;
  resourceId: string;
  title: string;
};

export type DesktopGeneratedFileReference = {
  path: string;
  runId: string;
};

export type DesktopGeneratedFileLinkReference = {
  href: string;
  runId: string;
};

export type DesktopGeneratedFileLinkResult =
  { ok: true; value: { path: string } } | DesktopGeneratedFileFailure;

export type DesktopGeneratedFilePreview = {
  content?: string;
  contentBase64?: string;
  mimeType?: string;
  filename: string;
  format: "markdown" | "text" | "binary";
  path: string;
  sizeBytes: number;
  truncated: boolean;
};

export type DesktopGeneratedFileFailure = {
  code: string;
  ok: false;
  reason: string;
};

export type DesktopGeneratedFilePreviewResult =
  | { ok: true; value: DesktopGeneratedFilePreview }
  | DesktopGeneratedFileFailure;

export type DesktopGeneratedFileActionResult =
  { ok: true; canceled?: boolean } | DesktopGeneratedFileFailure;

export type DesktopWorkspaceAssetKind =
  "archive" | "document" | "image" | "media" | "other";

export type DesktopWorkspaceAssetSort = "latest" | "name" | "oldest";

export type DesktopWorkspaceAssetReference = {
  companyId: string;
  path: string;
  scope: string;
};

export type DesktopWorkspaceAssetListRequest = {
  companyId: string;
  cursor?: string;
  kinds: DesktopWorkspaceAssetKind[];
  limit: number;
  query: string;
  scope: string;
  sort: DesktopWorkspaceAssetSort;
};

export type DesktopWorkspaceAssetItem = {
  filename: string;
  kind: DesktopWorkspaceAssetKind;
  mimeType: string;
  modifiedAt: string;
  parentPath: string;
  path: string;
  previewKind: "image" | "markdown" | "none" | "text";
  sizeBytes: number;
};

export type DesktopWorkspaceAssetList = {
  companyId: string;
  items: readonly DesktopWorkspaceAssetItem[];
  latestModifiedAt: string | null;
  nextCursor: string | null;
  scope: string;
  totalCount: number;
  truncated: boolean;
};

export type DesktopWorkspaceAssetFailure = {
  code: string;
  ok: false;
  reason: string;
};

export type DesktopWorkspaceAssetListResult =
  { ok: true; value: DesktopWorkspaceAssetList } | DesktopWorkspaceAssetFailure;

export type DesktopWorkspaceAssetPreview = {
  content?: string;
  contentBase64?: string;
  filename: string;
  format: "image" | "markdown" | "text";
  mimeType: string;
  path: string;
  sizeBytes: number;
  truncated: boolean;
};

export type DesktopWorkspaceAssetPreviewResult =
  | { ok: true; value: DesktopWorkspaceAssetPreview }
  | DesktopWorkspaceAssetFailure;

export type DesktopWorkspaceAssetActionResult =
  { ok: true; canceled?: boolean } | DesktopWorkspaceAssetFailure;

export type DesktopOperationFailure = {
  code: string;
  ok: false;
  reason: string;
};

/** When a plugin's apps authenticate: at install, or on first use. */
export type DesktopPluginAuthPolicy = "on_install" | "on_use";

export type DesktopPluginAuthApp = Readonly<{ id: string; name: string }>;

export type DesktopPluginOperation = Readonly<{
  schemaVersion: 1;
  state: "queued" | "installing" | "awaiting_user" | "complete" | "failed";
  continueUrl?: string;
  /** An install's own auth policy, when the provider answers one. */
  authPolicy?: DesktopPluginAuthPolicy;
  /** The apps an install says still need connecting. */
  appsNeedingAuth?: readonly DesktopPluginAuthApp[];
}>;

export type DesktopPluginResult =
  { ok: true; value: DesktopPluginOperation } | DesktopOperationFailure;

export type DesktopPluginActionRequest = {
  schemaVersion: 1;
  companyId: string;
  runtimeId: string;
  provider: import("@/shared/api/types").AgentProvider;
  nativeId: string;
};

export type DesktopConnectorActionRequest = DesktopPluginActionRequest & {
  source: import("@/shared/api/types").ConnectorSource;
};

/** Whether one connector's account is linked in the provider right now. */
export type DesktopConnectorStatus = Readonly<{
  schemaVersion: 1;
  connected: boolean;
}>;

export type DesktopConnectorStatusResult =
  { ok: true; value: DesktopConnectorStatus } | DesktopOperationFailure;

/** `force: false` reuses the daemon's catalog; absent means a forced refetch. */
export type DesktopPluginRefreshRequest = Omit<
  DesktopPluginActionRequest,
  "nativeId"
> & { force?: boolean };

export type DesktopPluginApp = Readonly<{
  id: string;
  name: string;
  description?: string;
  category?: string;
  iconUrl?: string;
}>;

export type DesktopPluginSkill = Readonly<{
  name: string;
  displayName?: string;
  description?: string;
}>;

/**
 * A Plugin as the daemon on this computer reads it live. Presentation fields
 * are Codex-only and omitted when unknown; the lists are always present and
 * may be empty (Claude reports skills and MCP servers but no apps).
 */
export type DesktopPluginDetail = Readonly<{
  schemaVersion: 1;
  nativeId: string;
  name: string;
  longDescription?: string;
  defaultPrompts: readonly string[];
  brandColor?: string;
  developerName?: string;
  websiteUrl?: string;
  privacyPolicyUrl?: string;
  termsOfServiceUrl?: string;
  shareUrl?: string;
  /**
   * Codex only: an on_install plugin connects its app before it installs,
   * through the connect dialog when `installationInterstitial` is set and in
   * the browser at once otherwise.
   */
  authPolicy?: DesktopPluginAuthPolicy;
  installationInterstitial?: boolean;
  apps: readonly DesktopPluginApp[];
  skills: readonly DesktopPluginSkill[];
  mcpServers: readonly string[];
}>;

export type DesktopPluginReadResult =
  { ok: true; value: DesktopPluginDetail } | DesktopOperationFailure;

/**
 * A failed desktop auth call. `reason` is the sentence to show; `kind` says
 * what happened so a lost session routes to sign-in rather than to a
 * connectivity screen: network (the server was unreachable), server (5xx or
 * an unusable answer), rejected (the server refused with its envelope —
 * wrong password, taken email, invalid input), session_missing (nothing
 * saved to refresh with), session_not_saved (the server accepted the
 * sign-in or renewal, but this computer could not save the session: secure
 * storage is unavailable; a new sign-in's server session was ended).
 */
export type DesktopAuthFailureKind =
  "network" | "rejected" | "server" | "session_missing" | "session_not_saved";

export type DesktopAuthFailure = {
  ok: false;
  reason: string;
  kind?: DesktopAuthFailureKind;
  code?: string;
  status?: number;
};

export type DesktopTaskReviewRequest =
  | { action: "prepare"; runId: string }
  | {
      action: "accept";
      companyId: string;
      expectedExecutionGeneration: number;
      expectedRevision: number;
      runId: string;
      runtimeId: string;
      taskId: string;
    };

export type DesktopTaskReview = {
  baseRef: string;
  diff: string;
  headRef: string;
  message: string;
  runId: string;
  status: "ready" | "conflict" | "main_dirty" | "merged";
};

export type DesktopTaskReviewResult =
  { ok: true; value: DesktopTaskReview } | DesktopGeneratedFileFailure;

export type DesktopNotificationKind =
  | "huddle_call"
  | "chat_mention"
  | "approval_request"
  | "question_request"
  | "task_assigned"
  | "blocker"
  | "work_completed"
  | "work_failed"
  | "calendar_reminder"
  | "calendar_update";

export type DesktopNotificationPayload = {
  companyId?: string;
  userId?: string;
  allowNative?: boolean;
  suppressWhenFocused?: boolean;
  body: string;
  id?: string;
  kind: DesktopNotificationKind;
  path?: string;
  route?: DesktopRouteKey;
  silent?: boolean;
  title: string;
};

export type DesktopHuddleCallNotification = {
  allowNative?: boolean;
  body: string;
  invitationId: string;
  title: string;
};

export type DesktopHuddleCallAction = {
  action: "join";
  invitationId: string;
};

export type DesktopNotificationResult =
  | {
      disposition: "shown" | "suppressed-disabled" | "suppressed-focused";
      ok: true;
    }
  | { ok: false; reason: string };

export type DesktopGlobalShortcutState = {
  accelerator: string;
  enabled: boolean;
  reason: string | null;
  registered: boolean;
};

export type DesktopWorkbenchState = {
  blockedWorkCount: number;
  globalShortcut: DesktopGlobalShortcutState;
  isPaused: boolean;
  notificationsSupported: boolean;
  pendingApprovalCount: number;
  readyForReviewCount: number;
  unreadCount: number;
};

export type DesktopWorkbenchStatusPatch = Partial<
  Pick<
    DesktopWorkbenchState,
    | "blockedWorkCount"
    | "isPaused"
    | "pendingApprovalCount"
    | "readyForReviewCount"
    | "unreadCount"
  >
>;

export type DesktopPowerLifecycleBatteryState = "ac" | "battery" | "unknown";

export type DesktopPowerLifecycleIdleState =
  "active" | "idle" | "locked" | "unknown";

export type DesktopPowerLifecycleStage = "active" | "locked" | "suspended";

export type DesktopPowerLifecycleThermalState =
  "critical" | "fair" | "nominal" | "serious" | "unknown";

export type DesktopPowerLifecycleEventType =
  | "lock-screen"
  | "on-ac"
  | "on-battery"
  | "resume"
  | "speed-limit-change"
  | "suspend"
  | "thermal-state-change"
  | "unlock-screen"
  | "user-did-become-active"
  | "user-did-resign-active";

export type DesktopPowerLifecycleEvent = {
  createdAt: string;
  type: DesktopPowerLifecycleEventType;
};

export type DesktopPowerLifecycleState = {
  battery: DesktopPowerLifecycleBatteryState;
  idleState: DesktopPowerLifecycleIdleState;
  idleTimeSeconds: number | null;
  lastEvent: DesktopPowerLifecycleEvent | null;
  lifecycle: DesktopPowerLifecycleStage;
  speedLimitPercent: number | null;
  thermalState: DesktopPowerLifecycleThermalState;
};

export type DesktopCrashReportingState = {
  enabled: boolean;
  metadataKeys: string[];
  provider: string | null;
  reason: string | null;
  submitUrlOrigin: string | null;
  uploadToServer: boolean;
};

export type DesktopSupportDiagnosticsCaptureRequest = {
  durationMs?: number;
};

export type DesktopSupportDiagnosticsCaptureResult =
  | {
      captureMode: "default";
      durationMs: number;
      files: {
        netLogPath: string;
        tracePath: string;
      };
      ok: true;
      traceCategories: string[];
    }
  | { ok: false; reason: string };

export type DesktopStartupSettingsStatus =
  "disabled" | "enabled" | "requires-approval" | "unavailable" | "unsupported";

export type DesktopStartupSettingsState = {
  openAsHidden: boolean;
  openAtLogin: boolean;
  reason: string | null;
  status: DesktopStartupSettingsStatus;
  supported: boolean;
};

export type DesktopStartupSettingsResult =
  | { ok: true; state: DesktopStartupSettingsState }
  | { ok: false; reason: string; state?: DesktopStartupSettingsState };

export type DesktopUpdateStatus =
  | "available"
  | "checking"
  | "downloaded"
  | "downloading"
  | "failed"
  | "idle"
  | "manual"
  | "restart-required"
  | "up-to-date";

export type DesktopUpdatePolicy = "manual" | "native";

export type DesktopUpdateState = {
  availableVersion: string | null;
  automaticUpdatesEnabled: boolean;
  automaticUpdatesSupported: boolean;
  checkedAt: string | null;
  downloadPercent: number | null;
  errorMessage: string | null;
  feedUrl: string | null;
  installedVersion: string | null;
  manualUrl: string | null;
  policy: DesktopUpdatePolicy;
  releaseDate: string | null;
  releaseName: string | null;
  status: DesktopUpdateStatus;
  updateAvailable: boolean;
  updateUrl: string | null;
};

export type DesktopUpdateCheckResult =
  | { mode: "native"; ok: true; status?: DesktopUpdateStatus }
  | { mode: "native"; ok: false; reason: string }
  | {
      manualUrl: string | null;
      mode: "manual";
      ok: false;
      reason: string;
    };

export type DesktopUpdateDownloadResult = DesktopUpdateCheckResult;

export type DesktopUpdateRestartResult =
  { ok: true } | { ok: false; reason: string };

export type DesktopAutomaticUpdatesResult =
  | { ok: true; state: DesktopUpdateState }
  | { ok: false; reason: string; state?: DesktopUpdateState };

export type DesktopDiagnosticPayload = {
  kind: string;
  line?: number | null;
  message: string;
  source?: string;
};

// The bundled daemon as the Electron supervisor sees it: its own state plus
// the parts of the daemon's /health the Daemon settings card renders.
export type DesktopDaemonState =
  | "stopped"
  | "starting"
  | "running"
  | "blocked"
  | "restarting"
  | "stopping"
  | "unavailable";

export type DesktopDaemonStatus = {
  activeRunCount: number;
  agents: { provider: string; version: string | null }[];
  cliVersion: string | null;
  daemonId: string | null;
  pid: number | null;
  ready: boolean;
  reason: string | null;
  reloadPendingReason: string | null;
  skippedAgents: { provider: string; reason: string }[];
  startAttempts: number;
  state: DesktopDaemonState;
};

export type DesktopDaemonActionResult =
  | { ok: true; status: DesktopDaemonStatus }
  | {
      activeRunCount?: number;
      code: string;
      ok: false;
      reason: string;
      status?: DesktopDaemonStatus;
    };

export type DesktopDaemonStopOptions = { confirmActive?: boolean };

export type DesktopDaemonLogLevel = "debug" | "info" | "warn" | "error";

export type DesktopDaemonLogLine = {
  level: DesktopDaemonLogLevel;
  text: string;
  time: string | null;
};

export type DesktopDaemonLogBatch = {
  lines: DesktopDaemonLogLine[];
  reset: boolean;
};

export type DesktopDaemonLogsResult =
  { ok: true; path?: string } | { code: string; ok: false; reason: string };

export type DesktopLinkPreview = {
  title?: string;
  description?: string;
  image?: string;
};

export type DesktopWebsitePreviewBounds = {
  x: number;
  y: number;
  width: number;
  height: number;
};

export type DesktopWebsitePreviewState = {
  id: string;
  url: string;
  title: string;
  loading: boolean;
  error: string | null;
  canGoBack: boolean;
  canGoForward: boolean;
};

export type DesktopWebsitePreviewBridge = {
  open(request: {
    id: string;
    url: string;
  }): Promise<
    | { ok: true; state: DesktopWebsitePreviewState }
    | { ok: false; reason: string }
  >;
  setBounds(id: string, bounds: DesktopWebsitePreviewBounds | null): void;
  command(id: string, command: "back" | "forward" | "reload" | "focus"): void;
  close(id: string): void;
  onEvent(
    listener: (
      event:
        | { type: "state"; state: DesktopWebsitePreviewState }
        | { type: "escape"; id: string },
    ) => void,
  ): () => void;
};

export type DesktopToolRequest = {
  schemaVersion: 1;
  companyId: string;
  runtimeId: string;
  agentId: string;
  provider: "codex" | "claude";
  toolKey: string;
  action: "status" | "repair" | "connect" | "cancel";
};
export type DesktopToolValue = {
  items: NonNullable<import("@/shared/api/types").RuntimeDto["tools"]>;
};
export type DesktopToolResult =
  { ok: true; value: DesktopToolValue } | DesktopOperationFailure;

export async function operateDesktopTool(
  request: Omit<DesktopToolRequest, "schemaVersion">,
): Promise<DesktopToolResult> {
  const bridge = getBoardlyDesktopBridge(globalThis);
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

export type DesktopAppPreviewRequest = {
  viewerId: string;
  companyId: string;
  outputId: string;
  runId: string;
  runtimeId: string;
};

export type DesktopAppPreviewStatus = {
  activationId: string;
  availability: "checking" | "available" | "unavailable";
  companyId: string;
  lastObservedAt: string;
  leaseExpiresAt: string;
  lifecycle: "stopped" | "starting" | "running" | "stopping" | "failed";
  outputId: string;
  ports: readonly number[];
  reason: string;
  renewAfterMs: number;
  runId: string;
  runtimeId: string;
  schemaVersion: 2;
  url: string;
  viewerId: string;
};

export type DesktopAppPreviewResult =
  | { ok: true; value: DesktopAppPreviewStatus }
  | { code: string; ok: false; reason: string; viewerId?: string };

export type DesktopAppPreviewBridge = {
  open(request: DesktopAppPreviewRequest): Promise<DesktopAppPreviewResult>;
  restart(viewerId: string): Promise<DesktopAppPreviewResult>;
  stop(viewerId: string): Promise<DesktopAppPreviewResult>;
  close(viewerId: string): Promise<{ ok: true } | DesktopAppPreviewResult>;
  onEvent(listener: (event: DesktopAppPreviewResult) => void): () => void;
};

export type DesktopArtifactFileReference = {
  companyId: string;
  artifactId: string;
  ordinal: number;
  runId?: string;
};

export type BoardlyDesktopBridge = {
  readArtifactFile?: (
    request: DesktopArtifactFileReference & { requestId: string },
  ) => Promise<
    | { ok: true; bytes: Uint8Array; contentType: string }
    | { ok: false; code: string }
  >;
  cancelArtifactFile?: (requestId: string) => Promise<void>;
  loadLinkPreview?(url: string): Promise<DesktopLinkPreview>;
  operateTool?(request: DesktopToolRequest): Promise<DesktopToolResult>;
  connectConnector?(
    request: DesktopConnectorActionRequest,
  ): Promise<DesktopPluginResult>;
  readConnectorStatus?(
    request: DesktopConnectorActionRequest,
  ): Promise<DesktopConnectorStatusResult>;
  refreshConnectors?(
    request: Omit<DesktopPluginRefreshRequest, "force">,
  ): Promise<DesktopPluginResult>;
  websitePreview?: DesktopWebsitePreviewBridge;
  appPreview?: DesktopAppPreviewBridge;
  discoverAgents?(): Promise<DesktopAgentDiscoveryResult>;
  verifyAgent?(
    provider: import("@/shared/api/types").AgentProvider,
  ): Promise<DesktopAgentVerificationResult>;
  installPlugin?(
    request: DesktopPluginActionRequest,
  ): Promise<DesktopPluginResult>;
  uninstallPlugin?(
    request: DesktopPluginActionRequest,
  ): Promise<DesktopPluginResult>;
  readPlugin?(
    request: DesktopPluginActionRequest,
  ): Promise<DesktopPluginReadResult>;
  refreshPlugins?(
    request: DesktopPluginRefreshRequest,
  ): Promise<DesktopPluginResult>;
  previewGeneratedFile?(
    reference: DesktopGeneratedFileReference,
  ): Promise<DesktopGeneratedFilePreviewResult>;
  resolveGeneratedFileLink?(
    reference: DesktopGeneratedFileLinkReference,
  ): Promise<DesktopGeneratedFileLinkResult>;
  saveGeneratedFile?(
    reference: DesktopGeneratedFileReference,
  ): Promise<DesktopGeneratedFileActionResult>;
  revealGeneratedFile?(
    reference: DesktopGeneratedFileReference,
  ): Promise<DesktopGeneratedFileActionResult>;
  reviewTaskWorkspace?(
    request: DesktopTaskReviewRequest,
  ): Promise<DesktopTaskReviewResult>;
  listWorkspaceAssets?(
    request: DesktopWorkspaceAssetListRequest,
  ): Promise<DesktopWorkspaceAssetListResult>;
  previewWorkspaceAsset?(
    reference: DesktopWorkspaceAssetReference,
  ): Promise<DesktopWorkspaceAssetPreviewResult>;
  saveWorkspaceAsset?(
    reference: DesktopWorkspaceAssetReference,
  ): Promise<DesktopWorkspaceAssetActionResult>;
  revealWorkspaceAsset?(
    reference: DesktopWorkspaceAssetReference,
  ): Promise<DesktopWorkspaceAssetActionResult>;
  getEnvironment(): Promise<DesktopEnvironment>;
  getStoredAccessToken?(): Promise<
    { ok: true; accessToken: string | null } | { ok: false; reason: string }
  >;
  clearStoredAccessToken?(): Promise<
    { ok: true } | { ok: false; reason: string }
  >;
  completeGoogleOAuth?(payload: {
    apiBaseUrl: string;
    code: string;
    error: string;
    flowId: string;
    redirectUri: string;
    state: string;
    verifier: string;
  }): Promise<
    | { ok: true; response: import("@/shared/api/types").DesktopLoginResponse }
    | DesktopAuthFailure
  >;
  loginWithPassword?(payload: {
    apiBaseUrl: string;
    email: string;
    password: string;
    clientMetadata?: { appVersion?: string; platform?: string };
  }): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").DesktopPasswordAuthResponse;
      }
    | DesktopAuthFailure
  >;
  signupWithPassword?(payload: {
    apiBaseUrl: string;
    displayName: string;
    email: string;
    password: string;
    clientMetadata?: { appVersion?: string; platform?: string };
  }): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").DesktopPasswordAuthResponse;
      }
    | DesktopAuthFailure
  >;
  verifyEmail?(payload: {
    apiBaseUrl: string;
    code: string;
    email: string;
    verificationId: string;
  }): Promise<
    | { ok: true; response: import("@/shared/api/types").DesktopLoginResponse }
    | DesktopAuthFailure
  >;
  resendEmailVerification?(payload: {
    apiBaseUrl: string;
    email: string;
    verificationId: string;
  }): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").EmailOtpResendResponse;
      }
    | DesktopAuthFailure
  >;
  getEmailVerificationStatus?(payload: {
    apiBaseUrl: string;
    email: string;
    verificationId: string;
  }): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").PendingVerificationStatusResponse;
      }
    | DesktopAuthFailure
  >;
  getAuthProviders?(apiBaseUrl: string): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").AuthProviderCapabilitiesResponse;
      }
    | DesktopAuthFailure
  >;
  startGoogleOAuth?(
    payload: import("@/shared/api/types").GoogleDesktopStartRequest & {
      apiBaseUrl: string;
    },
  ): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").GoogleDesktopStartResponse;
      }
    | DesktopAuthFailure
  >;
  refreshAuthSession?(apiBaseUrl: string): Promise<
    | {
        ok: true;
        response: import("@/shared/api/types").DesktopSessionRotation;
      }
    | DesktopAuthFailure
  >;
  logoutAuthSession?(
    apiBaseUrl: string,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  getNativeTheme?(): Promise<DesktopNativeTheme>;
  getWorkbenchState?(): Promise<DesktopWorkbenchState>;
  showContextMenu?(
    payload: DesktopContextMenuPayload,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  openResourceFile?(
    payload: DesktopResourceFilePayload,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  revealResourceFile?(
    payload: DesktopResourceFilePayload,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  saveResourceFile?(
    payload: DesktopResourceFilePayload,
  ): Promise<{ ok: true; canceled: boolean } | { ok: false; reason: string }>;
  startResourceDrag?(payload: DesktopResourceFilePayload): void;
  setWorkbenchStatus?(
    status: DesktopWorkbenchStatusPatch,
  ): Promise<
    | { ok: true; state: DesktopWorkbenchState }
    | { ok: false; reason: string; state?: DesktopWorkbenchState }
  >;
  getPowerLifecycleState?(): Promise<DesktopPowerLifecycleState>;
  onPowerLifecycleUpdated?(
    listener: (state: DesktopPowerLifecycleState) => void,
  ): () => void;
  getCrashReportingState?(): Promise<DesktopCrashReportingState>;
  captureSupportDiagnostics?(
    request: DesktopSupportDiagnosticsCaptureRequest,
  ): Promise<DesktopSupportDiagnosticsCaptureResult>;
  getStartupSettings?(): Promise<DesktopStartupSettingsState>;
  setOpenAtLogin?(enabled: boolean): Promise<DesktopStartupSettingsResult>;
  getDaemonStatus?(): Promise<DesktopDaemonStatus>;
  startDaemon?(): Promise<DesktopDaemonActionResult>;
  stopDaemon?(
    options?: DesktopDaemonStopOptions,
  ): Promise<DesktopDaemonActionResult>;
  restartDaemon?(): Promise<DesktopDaemonActionResult>;
  startDaemonLogs?(): Promise<DesktopDaemonLogsResult>;
  stopDaemonLogs?(): Promise<DesktopDaemonLogsResult>;
  revealDaemonLog?(): Promise<DesktopDaemonLogsResult>;
  onDaemonStatusUpdated?(
    listener: (status: DesktopDaemonStatus) => void,
  ): () => void;
  onDaemonLogLines?(
    listener: (batch: DesktopDaemonLogBatch) => void,
  ): () => void;
  getUpdateState?(): Promise<DesktopUpdateState>;
  checkForUpdates?(): Promise<DesktopUpdateCheckResult>;
  downloadUpdate?(): Promise<DesktopUpdateDownloadResult>;
  restartToUpdate?(): Promise<DesktopUpdateRestartResult>;
  setAutomaticUpdates?(
    enabled: boolean,
  ): Promise<DesktopAutomaticUpdatesResult>;
  onUpdateStateUpdated?(
    listener: (state: DesktopUpdateState) => void,
  ): () => void;
  onUpdateDialogRequested?(listener: () => void): () => void;
  setGlobalShortcutEnabled?(
    enabled: boolean,
  ): Promise<
    | { ok: true; state: DesktopWorkbenchState }
    | { ok: false; reason: string; state?: DesktopWorkbenchState }
  >;
  showNotification?(
    payload: DesktopNotificationPayload,
  ): Promise<DesktopNotificationResult>;
  showHuddleCallNotification?(
    payload: DesktopHuddleCallNotification,
  ): Promise<DesktopNotificationResult>;
  closeHuddleCallNotification?(
    invitationId: string,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  onHuddleCallAction?(
    listener: (action: DesktopHuddleCallAction) => void,
  ): () => void;
  startSocialOAuthLoopback?(): Promise<
    | { ok: true; redirectUri: string; state: string }
    | { ok: false; reason: string }
  >;
  cancelSocialOAuthLoopback?(): Promise<{ ok: true }>;
  startGoogleOAuthLoopback(): Promise<
    { ok: true; redirectUri: string } | { ok: false; reason: string }
  >;
  cancelGoogleOAuthLoopback(): Promise<{ ok: true }>;
  openExternal(
    url: string,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  openHuddleWindow?(
    huddleId: string,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  openHuddleStartWindow?(
    conversationId: string,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  onMenuCommand(listener: (command: DesktopMenuCommand) => void): () => void;
  onRunPreview?(listener: (frame: DesktopRunPreviewFrame) => void): () => void;
  onNativeThemeUpdated?(
    listener: (theme: DesktopNativeTheme) => void,
  ): () => void;
  onWorkbenchStatusUpdated?(
    listener: (state: DesktopWorkbenchState) => void,
  ): () => void;
  onGoogleOAuthCallback?(listener: (route: string) => void): () => void;
  reportDiagnostic?(payload: DesktopDiagnosticPayload): void;
};

type BrowserWindowLike = {
  boardlyDesktop?: BoardlyDesktopBridge;
  open?: (
    url?: string | URL,
    target?: string,
    features?: string,
  ) => WindowProxy | null;
};

export type DesktopGlobalLike = {
  boardlyDesktop?: BoardlyDesktopBridge;
  window?: BrowserWindowLike;
};

declare global {
  interface Window {
    boardlyDesktop?: BoardlyDesktopBridge;
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

export function getBoardlyDesktopBridge(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return globalScope.window?.boardlyDesktop ?? globalScope.boardlyDesktop;
}

export function shouldUseDesktopHashRouter(
  globalScope: DesktopGlobalLike = globalThis,
) {
  return Boolean(getBoardlyDesktopBridge(globalScope));
}

export async function getDesktopEnvironment(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopEnvironment> {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
    getBoardlyDesktopBridge(globalScope)?.onMenuCommand(listener) ?? (() => {})
  );
}

export async function getDesktopNativeTheme(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopNativeTheme> {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
    getBoardlyDesktopBridge(globalScope)?.onNativeThemeUpdated?.(listener) ??
    (() => {})
  );
}

export async function showDesktopContextMenu(
  payload: DesktopContextMenuPayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  return Boolean(getBoardlyDesktopBridge(globalScope)?.previewGeneratedFile);
}

export async function resolveDesktopGeneratedFileLink(
  reference: DesktopGeneratedFileLinkReference,
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopGeneratedFileLinkResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  getBoardlyDesktopBridge(globalScope)?.startResourceDrag?.(payload);
}

export async function getDesktopWorkbenchState(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopWorkbenchState> {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
    getBoardlyDesktopBridge(globalScope)?.onPowerLifecycleUpdated?.(listener) ??
    (() => {})
  );
}

export async function captureDesktopSupportDiagnostics(
  request: DesktopSupportDiagnosticsCaptureRequest = {},
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopSupportDiagnosticsCaptureResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
    getBoardlyDesktopBridge(globalScope)?.onUpdateStateUpdated?.(listener) ??
    (() => {})
  );
}

export function subscribeToDesktopUpdateDialogRequests(
  listener: () => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onUpdateDialogRequested?.(listener) ??
    (() => {})
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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);
  return runDesktopDaemonAction(
    bridge?.startDaemon ? () => bridge.startDaemon!() : undefined,
  );
}

export function stopDesktopDaemon(
  options: DesktopDaemonStopOptions = {},
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonActionResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);
  return runDesktopDaemonAction(
    bridge?.stopDaemon ? () => bridge.stopDaemon!(options) : undefined,
  );
}

export function restartDesktopDaemon(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonActionResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);
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
  const bridge = getBoardlyDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.startDaemonLogs ? () => bridge.startDaemonLogs!() : undefined,
  );
}

export function stopDesktopDaemonLogs(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonLogsResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.stopDaemonLogs ? () => bridge.stopDaemonLogs!() : undefined,
  );
}

export function revealDesktopDaemonLog(
  globalScope: DesktopGlobalLike = globalThis,
): Promise<DesktopDaemonLogsResult> {
  const bridge = getBoardlyDesktopBridge(globalScope);
  return runDesktopDaemonLogsAction(
    bridge?.revealDaemonLog ? () => bridge.revealDaemonLog!() : undefined,
  );
}

export function subscribeToDesktopDaemonStatus(
  listener: (status: DesktopDaemonStatus) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onDaemonStatusUpdated?.(listener) ??
    (() => {})
  );
}

export function subscribeToDesktopDaemonLogLines(
  listener: (batch: DesktopDaemonLogBatch) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onDaemonLogLines?.(listener) ??
    (() => {})
  );
}

export async function setDesktopGlobalShortcutEnabled(
  enabled: boolean,
  globalScope: DesktopGlobalLike = globalThis,
) {
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  const bridge = getBoardlyDesktopBridge(globalScope);

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
  void getBoardlyDesktopBridge(globalScope)
    ?.closeHuddleCallNotification?.(invitationId)
    .catch(() => {});
}

export function subscribeToDesktopHuddleCallActions(
  listener: (action: DesktopHuddleCallAction) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onHuddleCallAction?.(listener) ??
    (() => {})
  );
}

export function subscribeToDesktopWorkbenchState(
  listener: (state: DesktopWorkbenchState) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onWorkbenchStatusUpdated?.(
      listener,
    ) ?? (() => {})
  );
}

export function applyDesktopNativeThemeAttributes(
  theme: DesktopNativeTheme,
  documentElement: Pick<HTMLElement, "dataset">,
) {
  documentElement.dataset.boardlyNativeTheme = theme.colorScheme;
  documentElement.dataset.boardlyHighContrast = String(
    theme.shouldUseHighContrastColors || theme.inForcedColorsMode,
  );
  documentElement.dataset.boardlyInvertedColors = String(
    theme.shouldUseInvertedColorScheme,
  );
  documentElement.dataset.boardlyReducedTransparency = String(
    theme.prefersReducedTransparency,
  );
  documentElement.dataset.boardlyDifferentiateWithoutColor = String(
    theme.shouldDifferentiateWithoutColor,
  );
}

export function subscribeToDesktopGoogleOAuthCallback(
  listener: (route: string) => void,
  globalScope: DesktopGlobalLike = globalThis,
) {
  return (
    getBoardlyDesktopBridge(globalScope)?.onGoogleOAuthCallback?.(listener) ??
    (() => {})
  );
}

export function reportDesktopDiagnostic(
  payload: DesktopDiagnosticPayload,
  globalScope: DesktopGlobalLike = globalThis,
) {
  getBoardlyDesktopBridge(globalScope)?.reportDiagnostic?.(payload);
}
