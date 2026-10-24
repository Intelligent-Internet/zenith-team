export type DesktopPlatform = "macos" | "windows" | "linux" | "unknown";

export type DesktopEnvironmentName = "dev" | "local" | "prod" | "unknown";

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

export type DesktopConnectorStatus = Readonly<{
  schemaVersion: 1;
  connected: boolean;
}>;

export type DesktopConnectorStatusResult =
  { ok: true; value: DesktopConnectorStatus } | DesktopOperationFailure;

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

export type DesktopScreenShareSource = {
  id: string;
  name: string;
  kind: "screen" | "window";
  thumbnail: string;
  appIcon: string;
};

export type DesktopScreenShareState = {
  sharing: boolean;
  muted: boolean;
  camera: boolean;
};

export type DesktopScreenShareAction = "mute" | "camera" | "stop";

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

export type ZenithTeamDesktopBridge = {
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
    | { ok: true; accessToken: string | null; accessExpiresAt?: string | null }
    | { ok: false; reason: string }
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
  listScreenShareSources?(): Promise<
    | { ok: true; sources: DesktopScreenShareSource[]; audio?: boolean }
    | { ok: false; reason: string; blocked?: boolean }
  >;
  reportScreenShareState?(
    state: DesktopScreenShareState,
  ): Promise<{ ok: true } | { ok: false; reason: string }>;
  onScreenShareAction?(
    listener: (action: DesktopScreenShareAction) => void,
  ): () => void;
  openScreenShareSettings?(): Promise<
    { ok: true } | { ok: false; reason: string }
  >;
  selectScreenShareSource?(
    sourceId: string,
    shareAudio?: boolean,
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
  zenithTeamDesktop?: ZenithTeamDesktopBridge;
  open?: (
    url?: string | URL,
    target?: string,
    features?: string,
  ) => WindowProxy | null;
};

export type DesktopGlobalLike = {
  zenithTeamDesktop?: ZenithTeamDesktopBridge;
  window?: BrowserWindowLike;
};
