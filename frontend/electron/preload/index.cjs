const {
  isToolRequest,
  sanitizeToolResult,
  toolFailure,
} = require("../shared/tool-contract.cjs");
const { contextBridge, ipcRenderer } = require("electron");
const {
  artifactCacheChannels,
  isArtifactFileRequest,
  isArtifactRequestId,
} = require("../shared/artifact-cache.cjs");
const { isSafePluginContinueUrl } = require("../shared/url-policy.cjs");
const {
  sanitizeConnectorStatus,
  sanitizePluginDetail,
  sanitizePluginInstallAnswer,
} = require("../shared/plugin-detail.cjs");
const {
  websitePreviewChannels,
  isWebsitePreviewId,
  websitePreviewUrl,
  isWebsitePreviewBounds,
  isWebsitePreviewCommand,
  isWebsitePreviewEvent,
} = require("../shared/website-preview.cjs");
const {
  LINK_PREVIEW_CHANNEL,
  linkPreviewUrl,
  sanitizeLinkPreview,
} = require("../shared/link-preview.cjs");

const daemonLogLinesListeners = new Set();
const appPreviewListeners = new Set();
const daemonStatusListeners = new Set();
const googleOAuthCallbackListeners = new Set();
const localRunPreviewListeners = new Set();
const menuCommandListeners = new Set();
const nativeThemeListeners = new Set();
const powerLifecycleListeners = new Set();
const updateDialogListeners = new Set();
const updateStateListeners = new Set();
const workbenchStatusListeners = new Set();
const huddleCallActionListeners = new Set();
const screenShareActionListeners = new Set();
const websitePreviewListeners = new Set();
let localRunPreviewEmptyDrains = 0;
let localRunPreviewPollInFlight = false;
let localRunPreviewPollTimer = null;
let pendingUpdateDialogRequest = false;
const DAEMON_LOG_LINES_CHANNEL = "desktop:daemon-log-lines";
const DAEMON_LOG_MAX_LINES = 500;
const DAEMON_STATUS_UPDATED_CHANNEL = "desktop:daemon-status-updated";
const APP_PREVIEW_EVENT_CHANNEL = "desktop:app-preview-status";
const DRAIN_LOCAL_RUN_PREVIEW_CHANNEL = "desktop:drain-run-preview";
const GOOGLE_OAUTH_CALLBACK_ROUTE_CHANNEL = "desktop:google-oauth-callback";
const LOCAL_RUN_PREVIEW_MAX_DRAIN_FRAMES = 257;
const LOCAL_RUN_PREVIEW_POLL_INTERVAL_MS = 32;
// An idle open chat must not cost ~31 empty IPC round-trips a second: after
// this many consecutive empty drains the poll interval doubles per drain up to
// the idle cap, and any non-empty batch snaps it back to the fast interval.
const LOCAL_RUN_PREVIEW_IDLE_DRAIN_THRESHOLD = 8;
const LOCAL_RUN_PREVIEW_IDLE_POLL_INTERVAL_MS = 500;
const NATIVE_THEME_UPDATED_CHANNEL = "desktop:native-theme-updated";
const POWER_LIFECYCLE_UPDATED_CHANNEL = "desktop:power-lifecycle-updated";
const RENDERER_DIAGNOSTIC_CHANNEL = "desktop:renderer-diagnostic";
const SHOW_UPDATE_DIALOG_CHANNEL = "desktop:show-update-dialog";
const UPDATE_STATE_UPDATED_CHANNEL = "desktop:update-state-updated";
const WORKBENCH_STATUS_UPDATED_CHANNEL = "desktop:workbench-status-updated";
const HUDDLE_CALL_ACTION_CHANNEL = "desktop:huddle-call-action";
const SCREEN_SHARE_ACTION_CHANNEL = "desktop:screen-share-action";
const SCREEN_SHARE_ACTIONS = new Set(["mute", "camera", "stop"]);
window.addEventListener("error", (event) => {
  sendRendererDiagnostic({
    kind: "window-error",
    line: event.lineno,
    message: event.message,
    source: event.filename,
  });
});

window.addEventListener("unhandledrejection", (event) => {
  sendRendererDiagnostic({
    kind: "unhandled-rejection",
    message: getRejectionMessage(event.reason),
    source: "promise",
  });
});

ipcRenderer.on("desktop:menu-command", (_event, command) => {
  if (!isDesktopMenuCommand(command)) {
    return;
  }

  for (const listener of menuCommandListeners) {
    listener(command);
  }
});

ipcRenderer.on(websitePreviewChannels.event, (_event, event) => {
  if (!isWebsitePreviewEvent(event)) return;
  for (const listener of websitePreviewListeners) listener(event);
});

ipcRenderer.on(APP_PREVIEW_EVENT_CHANNEL, (_event, event) => {
  const value = sanitizeDesktopAppPreviewEvent(event);
  if (!value) return;
  for (const listener of appPreviewListeners) listener(value);
});

ipcRenderer.on(NATIVE_THEME_UPDATED_CHANNEL, (_event, theme) => {
  if (!isDesktopNativeTheme(theme)) {
    return;
  }

  for (const listener of nativeThemeListeners) {
    listener(theme);
  }
});

ipcRenderer.on(SCREEN_SHARE_ACTION_CHANNEL, (_event, action) => {
  if (!SCREEN_SHARE_ACTIONS.has(action)) return;
  for (const listener of screenShareActionListeners) {
    listener(action);
  }
});

ipcRenderer.on(HUDDLE_CALL_ACTION_CHANNEL, (_event, action) => {
  if (
    !action ||
    typeof action !== "object" ||
    action.action !== "join" ||
    !isDesktopHuddleInvitationId(action.invitationId)
  ) {
    return;
  }

  for (const listener of huddleCallActionListeners) {
    listener({ action: action.action, invitationId: action.invitationId });
  }
});

ipcRenderer.on(WORKBENCH_STATUS_UPDATED_CHANNEL, (_event, status) => {
  if (!isDesktopWorkbenchStatus(status)) {
    return;
  }

  for (const listener of workbenchStatusListeners) {
    listener(status);
  }
});

ipcRenderer.on(POWER_LIFECYCLE_UPDATED_CHANNEL, (_event, state) => {
  if (!isDesktopPowerLifecycleState(state)) {
    return;
  }

  for (const listener of powerLifecycleListeners) {
    listener(state);
  }
});

ipcRenderer.on(UPDATE_STATE_UPDATED_CHANNEL, (_event, state) => {
  if (!isDesktopUpdateState(state)) {
    return;
  }

  for (const listener of updateStateListeners) {
    listener(state);
  }
});

ipcRenderer.on(SHOW_UPDATE_DIALOG_CHANNEL, () => {
  if (updateDialogListeners.size === 0) {
    pendingUpdateDialogRequest = true;
    return;
  }

  for (const listener of updateDialogListeners) {
    listener();
  }
});

ipcRenderer.on(DAEMON_STATUS_UPDATED_CHANNEL, (_event, status) => {
  if (!isDesktopDaemonStatus(status)) {
    return;
  }

  for (const listener of daemonStatusListeners) {
    listener(status);
  }
});

ipcRenderer.on(DAEMON_LOG_LINES_CHANNEL, (_event, batch) => {
  if (!isDesktopDaemonLogBatch(batch)) {
    return;
  }

  for (const listener of daemonLogLinesListeners) {
    listener(batch);
  }
});

ipcRenderer.on(GOOGLE_OAUTH_CALLBACK_ROUTE_CHANNEL, (_event, route) => {
  if (typeof route !== "string") {
    return;
  }

  if (
    route !== "/auth/google/callback" &&
    !route.startsWith("/auth/google/callback?")
  ) {
    return;
  }

  sendRendererDiagnostic({
    kind: "google-oauth-callback-route",
    message: "Received Google OAuth callback route.",
    source: "preload",
  });

  if (googleOAuthCallbackListeners.size === 0) {
    window.location.hash = route;
    return;
  }

  for (const listener of googleOAuthCallbackListeners) {
    listener(route);
  }
});

ipcRenderer.on("desktop:social-oauth-callback", (_event, route) => {
  if (typeof route === "string" && route.startsWith("/social?oauth=")) {
    window.location.hash = route;
  }
});

contextBridge.exposeInMainWorld("boardlyDesktop", {
  readArtifactFile: async (request) => {
    if (!isArtifactFileRequest(request))
      return { ok: false, code: "invalid_request" };
    const result = await ipcRenderer.invoke(
      artifactCacheChannels.read,
      request,
    );
    if (
      result?.ok === true &&
      result.bytes instanceof Uint8Array &&
      result.bytes.byteLength <= 100 * 1024 * 1024 &&
      typeof result.contentType === "string" &&
      result.contentType.length <= 256
    ) {
      return { ok: true, bytes: result.bytes, contentType: result.contentType };
    }
    return {
      ok: false,
      code: typeof result?.code === "string" ? result.code : "unavailable",
    };
  },
  cancelArtifactFile: async (requestId) => {
    if (isArtifactRequestId(requestId))
      await ipcRenderer.invoke(artifactCacheChannels.cancel, requestId);
  },
  loadLinkPreview: async (value) => {
    const url = linkPreviewUrl(value);
    if (!url) return {};
    try {
      return sanitizeLinkPreview(
        await ipcRenderer.invoke(LINK_PREVIEW_CHANNEL, url),
      );
    } catch {
      return {};
    }
  },
  websitePreview: {
    open: (request) => {
      if (
        !isWebsitePreviewId(request?.id) ||
        !websitePreviewUrl(request?.url)
      ) {
        return Promise.resolve({
          ok: false,
          reason: "Website preview request is invalid.",
        });
      }
      return ipcRenderer.invoke(websitePreviewChannels.open, {
        id: request.id,
        url: request.url,
      });
    },
    setBounds: (id, bounds) => {
      if (
        isWebsitePreviewId(id) &&
        (bounds === null || isWebsitePreviewBounds(bounds))
      ) {
        ipcRenderer.send(websitePreviewChannels.bounds, { id, bounds });
      }
    },
    command: (id, command) => {
      if (isWebsitePreviewId(id) && isWebsitePreviewCommand(command)) {
        ipcRenderer.send(websitePreviewChannels.command, { id, command });
      }
    },
    close: (id) => {
      if (isWebsitePreviewId(id))
        ipcRenderer.send(websitePreviewChannels.close, id);
    },
    onEvent: (listener) => {
      if (typeof listener !== "function") return () => {};
      websitePreviewListeners.add(listener);
      return () => websitePreviewListeners.delete(listener);
    },
  },
  appPreview: {
    open: (request) => invokeDesktopAppPreviewOpen(request),
    restart: (viewerId) =>
      invokeDesktopAppPreviewAction("desktop:restart-app-preview", viewerId),
    stop: (viewerId) =>
      invokeDesktopAppPreviewAction("desktop:stop-app-preview", viewerId),
    close: (viewerId) =>
      invokeDesktopAppPreviewAction("desktop:close-app-preview", viewerId),
    onEvent: (listener) => {
      if (typeof listener !== "function") return () => {};
      appPreviewListeners.add(listener);
      return () => appPreviewListeners.delete(listener);
    },
  },
  discoverAgents: () => ipcRenderer.invoke("desktop:discover-agents"),
  verifyAgent: (provider) => {
    if (provider !== "codex" && provider !== "claude") {
      return Promise.resolve({
        schemaVersion: 1,
        provider: "codex",
        ok: false,
        code: "provider_failed",
      });
    }
    return ipcRenderer.invoke("desktop:verify-agent", provider);
  },
  operateTool: async (request) => {
    if (!isToolRequest(request)) return toolFailure("invalid_request");
    const result = await ipcRenderer.invoke("desktop:operate-tool", request);
    if (result?.ok !== true) return toolFailure(result?.code);
    const value = sanitizeToolResult(result.value);
    return value ? { ok: true, value } : toolFailure("invalid_response");
  },
  connectConnector: (request) => invokeDesktopConnectorOperation(request),
  readConnectorStatus: (request) => invokeDesktopConnectorStatus(request),
  refreshConnectors: (request) =>
    invokeDesktopPluginOperation("desktop:refresh-connectors", request, true),
  installPlugin: (request) =>
    invokeDesktopPluginOperation("desktop:install-plugin", request, false),
  uninstallPlugin: (request) =>
    invokeDesktopPluginOperation("desktop:uninstall-plugin", request, false),
  readPlugin: (request) => invokeDesktopPluginRead(request),
  refreshPlugins: (request) =>
    invokeDesktopPluginOperation("desktop:refresh-plugins", request, true),
  previewGeneratedFile: (reference) => {
    if (!isDesktopGeneratedFileReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Generated file reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:preview-generated-file", reference);
  },
  resolveGeneratedFileLink: (reference) => {
    if (!isDesktopGeneratedFileLinkReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Generated file link is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:resolve-generated-file-link", reference);
  },
  saveGeneratedFile: (reference) => {
    if (!isDesktopGeneratedFileReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Generated file reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:save-generated-file", reference);
  },
  revealGeneratedFile: (reference) => {
    if (!isDesktopGeneratedFileReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Generated file reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:reveal-generated-file", reference);
  },
  listWorkspaceAssets: (request) => {
    if (!isDesktopWorkspaceAssetListRequest(request)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Workspace assets request is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:list-workspace-assets", request);
  },
  previewWorkspaceAsset: (reference) => {
    if (!isDesktopWorkspaceAssetReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Workspace asset reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:preview-workspace-asset", reference);
  },
  saveWorkspaceAsset: (reference) => {
    if (!isDesktopWorkspaceAssetReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Workspace asset reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:save-workspace-asset", reference);
  },
  revealWorkspaceAsset: (reference) => {
    if (!isDesktopWorkspaceAssetReference(reference)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Workspace asset reference is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:reveal-workspace-asset", reference);
  },
  reviewTaskWorkspace: (request) => {
    if (!isDesktopTaskReviewRequest(request)) {
      return Promise.resolve(
        desktopWorkspaceFailure(
          "Task review request is invalid.",
          "invalid_request",
        ),
      );
    }
    return ipcRenderer.invoke("desktop:review-task-workspace", request);
  },
  getEnvironment: () => ipcRenderer.invoke("desktop:get-environment"),
  getStoredAccessToken: () =>
    ipcRenderer.invoke("desktop:get-stored-access-token"),
  clearStoredAccessToken: () =>
    ipcRenderer.invoke("desktop:clear-stored-access-token"),
  loginWithPassword: (payload) =>
    ipcRenderer.invoke("desktop:login-password", payload),
  signupWithPassword: (payload) =>
    ipcRenderer.invoke("desktop:signup-password", payload),
  verifyEmail: (payload) => ipcRenderer.invoke("desktop:verify-email", payload),
  resendEmailVerification: (payload) =>
    ipcRenderer.invoke("desktop:resend-email-verification", payload),
  getEmailVerificationStatus: (payload) =>
    ipcRenderer.invoke("desktop:get-email-verification-status", payload),
  completeGoogleOAuth: (payload) => {
    if (!isDesktopGoogleOAuthCompletion(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Google sign-in completion is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:complete-google-oauth", payload);
  },
  getAuthProviders: (apiBaseUrl) => {
    if (!isDesktopApiBaseUrl(apiBaseUrl)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop API URL is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:get-auth-providers", apiBaseUrl);
  },
  startGoogleOAuth: (payload) => {
    if (!isDesktopGoogleOAuthStart(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Google sign-in request is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:start-google-oauth", payload);
  },
  refreshAuthSession: (apiBaseUrl) => {
    if (!isDesktopApiBaseUrl(apiBaseUrl)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop API URL is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:refresh-auth-session", apiBaseUrl);
  },
  logoutAuthSession: (apiBaseUrl) => {
    if (!isDesktopApiBaseUrl(apiBaseUrl)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop API URL is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:logout-auth-session", apiBaseUrl);
  },
  getNativeTheme: () => ipcRenderer.invoke("desktop:get-native-theme"),
  getWorkbenchState: () => ipcRenderer.invoke("desktop:get-workbench-state"),
  getCrashReportingState: () =>
    ipcRenderer.invoke("desktop:get-crash-reporting-state"),
  captureSupportDiagnostics: (payload = {}) => {
    if (!isDesktopSupportDiagnosticsPayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop support diagnostics payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:capture-support-diagnostics", payload);
  },
  getPowerLifecycleState: () =>
    ipcRenderer.invoke("desktop:get-power-lifecycle-state"),
  getStartupSettings: () => ipcRenderer.invoke("desktop:get-startup-settings"),
  setOpenAtLogin: (enabled) => {
    if (typeof enabled !== "boolean") {
      return Promise.resolve({
        ok: false,
        reason: "Open at login setting must be enabled or disabled.",
      });
    }

    return ipcRenderer.invoke("desktop:set-open-at-login", enabled);
  },
  getDaemonStatus: () => ipcRenderer.invoke("desktop:get-daemon-status"),
  startDaemon: () => ipcRenderer.invoke("desktop:start-daemon"),
  stopDaemon: (options = {}) => {
    if (!isDesktopDaemonStopOptions(options)) {
      return Promise.resolve({
        code: "invalid_request",
        ok: false,
        reason: "Daemon stop options are not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:stop-daemon", {
      confirmActive: Boolean(options.confirmActive),
    });
  },
  restartDaemon: () => ipcRenderer.invoke("desktop:restart-daemon"),
  startDaemonLogs: () => ipcRenderer.invoke("desktop:start-daemon-logs"),
  stopDaemonLogs: () => ipcRenderer.invoke("desktop:stop-daemon-logs"),
  revealDaemonLog: () => ipcRenderer.invoke("desktop:reveal-daemon-log"),
  getUpdateState: () => ipcRenderer.invoke("desktop:get-update-state"),
  checkForUpdates: () => ipcRenderer.invoke("desktop:check-for-updates"),
  downloadUpdate: () => ipcRenderer.invoke("desktop:download-update"),
  restartToUpdate: () => ipcRenderer.invoke("desktop:restart-to-update"),
  setAutomaticUpdates: (enabled) => {
    if (typeof enabled !== "boolean") {
      return Promise.resolve({
        ok: false,
        reason: "Automatic updates setting must be enabled or disabled.",
      });
    }

    return ipcRenderer.invoke("desktop:set-automatic-updates", enabled);
  },
  showContextMenu: (payload) => {
    if (!isDesktopContextMenuPayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop context menu payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:show-context-menu", payload);
  },
  openResourceFile: (payload) => {
    if (!isDesktopResourceFilePayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop resource file payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:open-resource-file", payload);
  },
  revealResourceFile: (payload) => {
    if (!isDesktopResourceFilePayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop resource file payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:reveal-resource-file", payload);
  },
  saveResourceFile: (payload) => {
    if (!isDesktopResourceFilePayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop resource file payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:save-resource-file", payload);
  },
  startResourceDrag: (payload) => {
    if (!isDesktopResourceFilePayload(payload)) {
      return;
    }

    ipcRenderer.send("desktop:start-resource-drag", payload);
  },
  setWorkbenchStatus: (status) => {
    if (!isDesktopWorkbenchStatusPatch(status)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop workbench status is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:set-workbench-status", status);
  },
  setGlobalShortcutEnabled: (enabled) => {
    if (typeof enabled !== "boolean") {
      return Promise.resolve({
        ok: false,
        reason: "Global shortcut setting must be enabled or disabled.",
      });
    }

    return ipcRenderer.invoke("desktop:set-global-shortcut-enabled", enabled);
  },
  showNotification: (payload) => {
    if (!isDesktopNotificationPayload(payload)) {
      return Promise.resolve({
        ok: false,
        reason: "Desktop notification payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:show-notification", payload);
  },
  showHuddleCallNotification: (payload) => {
    if (
      !payload ||
      typeof payload !== "object" ||
      !isDesktopHuddleInvitationId(payload.invitationId) ||
      typeof payload.title !== "string" ||
      typeof payload.body !== "string" ||
      (payload.allowNative !== undefined &&
        typeof payload.allowNative !== "boolean")
    ) {
      return Promise.resolve({
        ok: false,
        reason: "Huddle call notification payload is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:show-huddle-call", {
      allowNative: payload.allowNative,
      body: payload.body,
      invitationId: payload.invitationId,
      title: payload.title,
    });
  },
  closeHuddleCallNotification: (invitationId) => {
    if (!isDesktopHuddleInvitationId(invitationId)) {
      return Promise.resolve({
        ok: false,
        reason: "Huddle invitation id is not valid.",
      });
    }

    return ipcRenderer.invoke("desktop:close-huddle-call", invitationId);
  },
  onHuddleCallAction: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    huddleCallActionListeners.add(listener);

    return () => {
      huddleCallActionListeners.delete(listener);
    };
  },
  startSocialOAuthLoopback: () =>
    ipcRenderer.invoke("desktop:start-social-oauth-loopback"),
  cancelSocialOAuthLoopback: () =>
    ipcRenderer.invoke("desktop:cancel-social-oauth-loopback"),
  startGoogleOAuthLoopback: () =>
    ipcRenderer.invoke("desktop:start-google-oauth-loopback"),
  cancelGoogleOAuthLoopback: () =>
    ipcRenderer.invoke("desktop:cancel-google-oauth-loopback"),
  openExternal: (url) => ipcRenderer.invoke("desktop:open-external", url),
  openHuddleWindow: (huddleId) => {
    if (typeof huddleId !== "string" || huddleId.length > 64) {
      return Promise.resolve({
        ok: false,
        reason: "Huddle window request is invalid.",
      });
    }
    return ipcRenderer.invoke("desktop:open-huddle-window", huddleId);
  },
  openHuddleStartWindow: (conversationId) => {
    if (typeof conversationId !== "string" || conversationId.length > 64) {
      return Promise.resolve({
        ok: false,
        reason: "Huddle window request is invalid.",
      });
    }
    return ipcRenderer.invoke(
      "desktop:open-huddle-start-window",
      conversationId,
    );
  },
  listScreenShareSources: () =>
    ipcRenderer.invoke("desktop:list-screen-share-sources"),
  openScreenShareSettings: () =>
    ipcRenderer.invoke("desktop:open-screen-share-settings"),
  reportScreenShareState: (state) =>
    ipcRenderer.invoke("desktop:screen-share-state", {
      sharing: state?.sharing === true,
      muted: state?.muted === true,
      camera: state?.camera === true,
    }),
  onScreenShareAction: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }
    screenShareActionListeners.add(listener);
    return () => {
      screenShareActionListeners.delete(listener);
    };
  },
  selectScreenShareSource: (sourceId, shareAudio) => {
    if (typeof sourceId !== "string" || sourceId.length > 256) {
      return Promise.resolve({
        ok: false,
        reason: "Choose a screen or window to share.",
      });
    }
    return ipcRenderer.invoke(
      "desktop:select-screen-share-source",
      sourceId,
      shareAudio === true,
    );
  },
  onMenuCommand: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    menuCommandListeners.add(listener);

    return () => {
      menuCommandListeners.delete(listener);
    };
  },
  onRunPreview: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    localRunPreviewListeners.add(listener);
    // A new subscriber (a chat just opened) deserves fast-path latency even
    // if the poller had backed off while idle.
    localRunPreviewEmptyDrains = 0;
    if (localRunPreviewPollTimer) {
      clearTimeout(localRunPreviewPollTimer);
      localRunPreviewPollTimer = null;
    }
    startLocalRunPreviewPolling();

    return () => {
      localRunPreviewListeners.delete(listener);
      if (localRunPreviewListeners.size === 0 && localRunPreviewPollTimer) {
        clearTimeout(localRunPreviewPollTimer);
        localRunPreviewPollTimer = null;
      }
    };
  },
  onNativeThemeUpdated: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    nativeThemeListeners.add(listener);

    return () => {
      nativeThemeListeners.delete(listener);
    };
  },
  onWorkbenchStatusUpdated: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    workbenchStatusListeners.add(listener);

    return () => {
      workbenchStatusListeners.delete(listener);
    };
  },
  onPowerLifecycleUpdated: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    powerLifecycleListeners.add(listener);

    return () => {
      powerLifecycleListeners.delete(listener);
    };
  },
  onUpdateStateUpdated: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    updateStateListeners.add(listener);

    return () => {
      updateStateListeners.delete(listener);
    };
  },
  onUpdateDialogRequested: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    updateDialogListeners.add(listener);
    if (pendingUpdateDialogRequest) {
      pendingUpdateDialogRequest = false;
      listener();
    }

    return () => {
      updateDialogListeners.delete(listener);
    };
  },
  onDaemonStatusUpdated: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    daemonStatusListeners.add(listener);

    return () => {
      daemonStatusListeners.delete(listener);
    };
  },
  onDaemonLogLines: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    daemonLogLinesListeners.add(listener);

    return () => {
      daemonLogLinesListeners.delete(listener);
    };
  },
  onGoogleOAuthCallback: (listener) => {
    if (typeof listener !== "function") {
      return () => {};
    }

    googleOAuthCallbackListeners.add(listener);

    return () => {
      googleOAuthCallbackListeners.delete(listener);
    };
  },
  reportDiagnostic: (payload) => {
    sendRendererDiagnostic(payload);
  },
});

function startLocalRunPreviewPolling() {
  if (
    localRunPreviewListeners.size === 0 ||
    localRunPreviewPollInFlight ||
    localRunPreviewPollTimer
  ) {
    return;
  }
  void drainLocalRunPreviewFrames();
}

async function drainLocalRunPreviewFrames() {
  if (localRunPreviewListeners.size === 0 || localRunPreviewPollInFlight) {
    return;
  }
  localRunPreviewPollInFlight = true;
  let drainedFrames = false;
  try {
    let frames;
    try {
      frames = await ipcRenderer.invoke(DRAIN_LOCAL_RUN_PREVIEW_CHANNEL);
    } catch {
      return;
    }
    if (!isDesktopRunPreviewBatch(frames)) {
      return;
    }
    drainedFrames = frames.length > 0;
    for (const frame of frames) {
      for (const listener of localRunPreviewListeners) {
        listener(frame);
      }
    }
  } finally {
    localRunPreviewEmptyDrains = drainedFrames
      ? 0
      : localRunPreviewEmptyDrains + 1;
    localRunPreviewPollInFlight = false;
    scheduleLocalRunPreviewPoll();
  }
}

function scheduleLocalRunPreviewPoll() {
  if (localRunPreviewListeners.size === 0 || localRunPreviewPollTimer) {
    return;
  }
  localRunPreviewPollTimer = setTimeout(() => {
    localRunPreviewPollTimer = null;
    void drainLocalRunPreviewFrames();
  }, localRunPreviewPollDelay());
}

function localRunPreviewPollDelay() {
  const idleDrains =
    localRunPreviewEmptyDrains - LOCAL_RUN_PREVIEW_IDLE_DRAIN_THRESHOLD;
  if (idleDrains < 0) {
    return LOCAL_RUN_PREVIEW_POLL_INTERVAL_MS;
  }
  return Math.min(
    LOCAL_RUN_PREVIEW_POLL_INTERVAL_MS * 2 ** (idleDrains + 1),
    LOCAL_RUN_PREVIEW_IDLE_POLL_INTERVAL_MS,
  );
}

function sendRendererDiagnostic(payload) {
  ipcRenderer.send(RENDERER_DIAGNOSTIC_CHANNEL, payload);
}

function getRejectionMessage(reason) {
  if (reason instanceof Error) {
    return reason.message;
  }

  if (typeof reason === "string") {
    return reason;
  }

  return "Unhandled renderer promise rejection.";
}

function isDesktopApiBaseUrl(value) {
  if (typeof value !== "string" || !value.trim()) {
    return false;
  }
  try {
    const parsed = new URL(value);
    return (
      (parsed.protocol === "https:" || parsed.protocol === "http:") &&
      parsed.pathname.replace(/\/+$/, "") === "/api/v1" &&
      !parsed.username &&
      !parsed.password &&
      !parsed.search &&
      !parsed.hash
    );
  } catch {
    return false;
  }
}

function isDesktopGoogleOAuthCompletion(payload) {
  if (!payload || typeof payload !== "object") {
    return false;
  }
  const keys = Object.keys(payload).sort();
  const expectedKeys = [
    "apiBaseUrl",
    "code",
    "error",
    "flowId",
    "redirectUri",
    "state",
    "verifier",
  ];
  if (
    keys.length !== expectedKeys.length ||
    keys.some((key, index) => key !== expectedKeys[index]) ||
    !isDesktopApiBaseUrl(payload.apiBaseUrl)
  ) {
    return false;
  }
  for (const key of ["flowId", "redirectUri", "state", "verifier"]) {
    if (typeof payload[key] !== "string" || !payload[key].trim()) {
      return false;
    }
  }
  const code = typeof payload.code === "string" ? payload.code.trim() : "";
  const error = typeof payload.error === "string" ? payload.error.trim() : "";
  return (
    Boolean(code) !== Boolean(error) && (!error || error === "access_denied")
  );
}

function isDesktopGoogleOAuthStart(payload) {
  if (!payload || typeof payload !== "object") {
    return false;
  }
  const keys = Object.keys(payload).sort();
  if (
    keys.length !== 4 ||
    keys[0] !== "apiBaseUrl" ||
    keys[1] !== "clientMetadata" ||
    keys[2] !== "codeChallenge" ||
    keys[3] !== "redirectUri" ||
    !isDesktopApiBaseUrl(payload.apiBaseUrl) ||
    typeof payload.codeChallenge !== "string" ||
    payload.codeChallenge.length !== 43 ||
    typeof payload.redirectUri !== "string" ||
    !payload.redirectUri.trim() ||
    !payload.clientMetadata ||
    typeof payload.clientMetadata !== "object"
  ) {
    return false;
  }
  const metadataKeys = Object.keys(payload.clientMetadata);
  if (metadataKeys.some((key) => key !== "appVersion" && key !== "platform")) {
    return false;
  }
  return metadataKeys.every(
    (key) =>
      typeof payload.clientMetadata[key] === "string" &&
      payload.clientMetadata[key].trim(),
  );
}

async function invokeDesktopPluginOperation(channel, request, refresh) {
  if (
    !(refresh
      ? isDesktopPluginRefreshRequest(request)
      : isDesktopPluginActionRequest(request))
  ) {
    return desktopPluginFailure(
      "Plugin request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke(channel, request);
    if (isDesktopOperationFailure(result)) {
      return desktopPluginFailure(result.reason, result.code);
    }
    const value = sanitizeDesktopPluginOperation(result?.value);
    return result?.ok === true && value
      ? { ok: true, value }
      : desktopPluginFailure(
          "Plugin operation response is invalid.",
          "invalid_response",
        );
  } catch {
    return desktopPluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
}

async function invokeDesktopPluginRead(request) {
  if (!isDesktopPluginActionRequest(request)) {
    return desktopPluginFailure(
      "Plugin request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke("desktop:read-plugin", request);
    if (isDesktopOperationFailure(result)) {
      return desktopPluginFailure(result.reason, result.code);
    }
    const value = sanitizePluginDetail(result?.value, request.nativeId);
    return result?.ok === true && value
      ? { ok: true, value }
      : desktopPluginFailure(
          "Plugin details response is invalid.",
          "invalid_response",
        );
  } catch {
    return desktopPluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
}

async function invokeDesktopAppPreviewOpen(request) {
  if (!isDesktopAppPreviewRequest(request)) {
    return desktopAppPreviewFailure(
      "App preview request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke(
      "desktop:open-app-preview",
      request,
    );
    return sanitizeDesktopAppPreviewResult(result, request);
  } catch {
    return desktopAppPreviewFailure(
      "The app preview is unavailable on this device.",
      "daemon_unavailable",
    );
  }
}

async function invokeDesktopAppPreviewAction(channel, viewerId) {
  if (!isDesktopCanonicalUuid(viewerId)) {
    return desktopAppPreviewFailure(
      "App preview request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke(channel, { viewerId });
    if (channel === "desktop:close-app-preview" && result?.ok === true) {
      return { ok: true };
    }
    return sanitizeDesktopAppPreviewResult(result);
  } catch {
    return desktopAppPreviewFailure(
      "The app preview is unavailable on this device.",
      "daemon_unavailable",
    );
  }
}

function isDesktopConnectorActionRequest(value) {
  return Boolean(
    desktopHasOnlyKeys(value, [
      "schemaVersion",
      "companyId",
      "runtimeId",
      "provider",
      "source",
      "nativeId",
    ]) &&
    value.schemaVersion === 1 &&
    isDesktopCanonicalUuid(value.companyId) &&
    isDesktopCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider) &&
    [
      "codex_app",
      "codex_mcp",
      "claude_plugin",
      "claude_mcp",
      "claude_cloud",
    ].includes(value.source) &&
    typeof value.nativeId === "string" &&
    value.nativeId.length <= 240 &&
    /^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(value.nativeId),
  );
}

async function invokeDesktopConnectorOperation(request) {
  if (!isDesktopConnectorActionRequest(request)) {
    return desktopPluginFailure(
      "Connector request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke(
      "desktop:connect-connector",
      request,
    );
    if (isDesktopOperationFailure(result)) {
      return desktopPluginFailure(result.reason, result.code);
    }
    const value = sanitizeDesktopPluginOperation(result?.value);
    return result?.ok === true && value
      ? { ok: true, value }
      : desktopPluginFailure(
          "Connector operation response is invalid.",
          "invalid_response",
        );
  } catch {
    return desktopPluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
}

async function invokeDesktopConnectorStatus(request) {
  if (!isDesktopConnectorActionRequest(request)) {
    return desktopPluginFailure(
      "Connector request is invalid.",
      "invalid_request",
    );
  }
  try {
    const result = await ipcRenderer.invoke(
      "desktop:read-connector-status",
      request,
    );
    if (isDesktopOperationFailure(result)) {
      return desktopPluginFailure(result.reason, result.code);
    }
    const value = sanitizeConnectorStatus(result?.value);
    return result?.ok === true && value
      ? { ok: true, value }
      : desktopPluginFailure(
          "Connector status response is invalid.",
          "invalid_response",
        );
  } catch {
    return desktopPluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
}

function isDesktopAppPreviewRequest(value) {
  return Boolean(
    desktopHasOnlyKeys(value, [
      "companyId",
      "outputId",
      "runId",
      "runtimeId",
      "viewerId",
    ]) &&
    isDesktopCanonicalUuid(value.viewerId) &&
    isDesktopCanonicalUuid(value.companyId) &&
    isDesktopCanonicalUuid(value.outputId) &&
    isDesktopCanonicalUuid(value.runId) &&
    isDesktopCanonicalUuid(value.runtimeId),
  );
}

function sanitizeDesktopAppPreviewResult(result, request) {
  if (isDesktopOperationFailure(result)) {
    return desktopAppPreviewFailure(result.reason, result.code);
  }
  const value = sanitizeDesktopAppPreview(result?.value, request);
  return result?.ok === true && value
    ? { ok: true, value }
    : desktopAppPreviewFailure(
        "App preview response is invalid.",
        "invalid_response",
      );
}

function sanitizeDesktopAppPreviewEvent(event) {
  if (isDesktopOperationFailure(event)) {
    return {
      code: event.code,
      ok: false,
      reason: event.reason,
      viewerId: isDesktopCanonicalUuid(event.viewerId) ? event.viewerId : "",
    };
  }
  const value = sanitizeDesktopAppPreview(event?.value);
  return event?.ok === true && value ? { ok: true, value } : null;
}

function sanitizeDesktopAppPreview(value, request) {
  if (
    !desktopHasOnlyKeys(value, [
      "activationId",
      "availability",
      "companyId",
      "lastObservedAt",
      "leaseExpiresAt",
      "lifecycle",
      "schemaVersion",
      "outputId",
      "ports",
      "reason",
      "renewAfterMs",
      "runId",
      "runtimeId",
      "url",
      "viewerId",
    ]) ||
    value.schemaVersion !== 2 ||
    !isDesktopCanonicalUuid(value.companyId) ||
    !isDesktopCanonicalUuid(value.outputId) ||
    !isDesktopCanonicalUuid(value.runId) ||
    !isDesktopCanonicalUuid(value.runtimeId) ||
    !isDesktopCanonicalUuid(value.viewerId) ||
    (request &&
      (value.companyId !== request.companyId ||
        value.viewerId !== request.viewerId ||
        value.outputId !== request.outputId ||
        value.runId !== request.runId ||
        value.runtimeId !== request.runtimeId)) ||
    !["stopped", "starting", "running", "stopping", "failed"].includes(
      value.lifecycle,
    ) ||
    !["checking", "available", "unavailable"].includes(value.availability) ||
    (value.activationId !== "" &&
      !isDesktopCanonicalUuid(value.activationId)) ||
    typeof value.reason !== "string" ||
    value.reason.length > 80 ||
    typeof value.lastObservedAt !== "string" ||
    value.lastObservedAt.length > 80 ||
    typeof value.leaseExpiresAt !== "string" ||
    value.leaseExpiresAt.length > 80 ||
    !Number.isSafeInteger(value.renewAfterMs) ||
    value.renewAfterMs < 1 ||
    !Array.isArray(value.ports) ||
    value.ports.length > 8 ||
    new Set(value.ports).size !== value.ports.length ||
    !value.ports.every(
      (port) => Number.isSafeInteger(port) && port >= 1024 && port <= 65535,
    )
  ) {
    return null;
  }
  if (value.url) {
    try {
      const url = new URL(value.url);
      if (
        url.protocol !== "http:" ||
        url.hostname !== "127.0.0.1" ||
        url.port !== String(value.ports[0]) ||
        url.pathname !== "/" ||
        url.search ||
        url.hash ||
        url.username ||
        url.password
      ) {
        return null;
      }
    } catch {
      return null;
    }
  }
  return Object.freeze({
    activationId: value.activationId,
    availability: value.availability,
    companyId: value.companyId,
    lastObservedAt: value.lastObservedAt,
    leaseExpiresAt: value.leaseExpiresAt,
    lifecycle: value.lifecycle,
    outputId: value.outputId,
    ports: Object.freeze([...value.ports]),
    reason: value.reason,
    renewAfterMs: value.renewAfterMs,
    runId: value.runId,
    runtimeId: value.runtimeId,
    schemaVersion: 2,
    url: value.url,
    viewerId: value.viewerId,
  });
}

function desktopAppPreviewFailure(reason, code) {
  return { code, ok: false, reason };
}

function isDesktopPluginActionRequest(value) {
  return Boolean(
    desktopHasOnlyKeys(value, [
      "schemaVersion",
      "companyId",
      "runtimeId",
      "provider",
      "nativeId",
    ]) &&
    value.schemaVersion === 1 &&
    isDesktopCanonicalUuid(value.companyId) &&
    isDesktopCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider) &&
    typeof value.nativeId === "string" &&
    value.nativeId.length <= 240 &&
    /^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(value.nativeId),
  );
}

function isDesktopPluginRefreshRequest(value) {
  const keys = ["schemaVersion", "companyId", "runtimeId", "provider"];
  return Boolean(
    (desktopHasOnlyKeys(value, keys) ||
      (desktopHasOnlyKeys(value, [...keys, "force"]) &&
        typeof value.force === "boolean")) &&
    value.schemaVersion === 1 &&
    isDesktopCanonicalUuid(value.companyId) &&
    isDesktopCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider),
  );
}

// An install may also answer the provider's auth policy and the apps it
// still needs connected (sanitizePluginInstallAnswer).
const DESKTOP_PLUGIN_OPERATION_KEYS = [
  "schemaVersion",
  "state",
  "continueUrl",
  "authPolicy",
  "appsNeedingAuth",
];

function sanitizeDesktopPluginOperation(value) {
  const hasContinueUrl =
    desktopIsPlainObject(value) &&
    Object.prototype.hasOwnProperty.call(value, "continueUrl");
  const answer = sanitizePluginInstallAnswer(value);
  if (
    !answer ||
    Object.keys(value).some(
      (key) => !DESKTOP_PLUGIN_OPERATION_KEYS.includes(key),
    ) ||
    value.schemaVersion !== 1 ||
    !["queued", "installing", "awaiting_user", "complete", "failed"].includes(
      value.state,
    ) ||
    (hasContinueUrl && value.state !== "awaiting_user")
  ) {
    return null;
  }
  if (
    hasContinueUrl &&
    (typeof value.continueUrl !== "string" ||
      value.continueUrl.length > 4096 ||
      value.continueUrl !== value.continueUrl.trim() ||
      !isSafePluginContinueUrl(value.continueUrl))
  ) {
    return null;
  }
  return Object.freeze({
    schemaVersion: 1,
    state: value.state,
    ...(hasContinueUrl ? { continueUrl: value.continueUrl } : {}),
    ...answer,
  });
}

function isDesktopOperationFailure(value) {
  return Boolean(
    value?.ok === false &&
    typeof value.code === "string" &&
    /^[a-z][a-z0-9_]{0,63}$/u.test(value.code) &&
    typeof value.reason === "string" &&
    value.reason.length > 0 &&
    value.reason.length <= 256,
  );
}

function desktopPluginFailure(reason, code) {
  return { code, ok: false, reason };
}

function desktopHasOnlyKeys(value, keys) {
  return Boolean(
    desktopIsPlainObject(value) &&
    Object.keys(value).length === keys.length &&
    Object.keys(value).every((key) => keys.includes(key)),
  );
}

function desktopIsPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isDesktopCanonicalUuid(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value,
    )
  );
}

function isDesktopGeneratedFileReference(value) {
  return Boolean(
    desktopHasOnlyKeys(value, ["path", "runId"]) &&
    isDesktopCanonicalUuid(value.runId) &&
    isDesktopWorkspacePath(value.path),
  );
}

function isDesktopGeneratedFileLinkReference(value) {
  return Boolean(
    desktopHasOnlyKeys(value, ["href", "runId"]) &&
    isDesktopCanonicalUuid(value.runId) &&
    typeof value.href === "string" &&
    Buffer.byteLength(value.href, "utf8") <= 4096 &&
    (value.href.startsWith("/") || value.href.startsWith("file:")) &&
    !/[\u0000-\u001f\u007f]/u.test(value.href),
  );
}

function isDesktopWorkspaceAssetReference(value) {
  return Boolean(
    desktopHasOnlyKeys(value, ["companyId", "path", "scope"]) &&
    isDesktopCanonicalUuid(value.companyId) &&
    typeof value.scope === "string" &&
    /^(?:company|projects\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|[a-z][a-z0-9-]{0,63})$/u.test(
      value.scope,
    ) &&
    isDesktopWorkspacePath(value.path),
  );
}

function isDesktopWorkspaceAssetListRequest(value) {
  if (
    !desktopIsPlainObject(value) ||
    !Object.keys(value).every((key) =>
      [
        "companyId",
        "cursor",
        "kinds",
        "limit",
        "query",
        "scope",
        "sort",
      ].includes(key),
    ) ||
    !isDesktopCanonicalUuid(value.companyId) ||
    !isDesktopWorkspaceScope(value.scope) ||
    typeof value.query !== "string" ||
    Buffer.byteLength(value.query, "utf8") > 256 ||
    !["latest", "name", "oldest"].includes(value.sort) ||
    !Array.isArray(value.kinds) ||
    value.kinds.length > 5 ||
    new Set(value.kinds).size !== value.kinds.length ||
    !value.kinds.every((kind) =>
      ["archive", "document", "image", "media", "other"].includes(kind),
    ) ||
    !Number.isSafeInteger(value.limit) ||
    value.limit < 1 ||
    value.limit > 100
  ) {
    return false;
  }
  return (
    value.cursor === undefined ||
    (typeof value.cursor === "string" &&
      value.cursor.length > 0 &&
      value.cursor.length <= 4096 &&
      /^[A-Za-z0-9_-]+$/u.test(value.cursor))
  );
}

function isDesktopWorkspaceScope(value) {
  return (
    typeof value === "string" &&
    /^(?:company|projects\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|[a-z][a-z0-9-]{0,63})$/u.test(
      value,
    )
  );
}

function isDesktopTaskReviewRequest(value) {
  if (
    desktopHasOnlyKeys(value, ["action", "runId"]) &&
    value.action === "prepare" &&
    isDesktopCanonicalUuid(value.runId)
  ) {
    return true;
  }
  return Boolean(
    desktopHasOnlyKeys(value, [
      "action",
      "companyId",
      "expectedExecutionGeneration",
      "expectedRevision",
      "runId",
      "runtimeId",
      "taskId",
    ]) &&
    value.action === "accept" &&
    isDesktopCanonicalUuid(value.companyId) &&
    isDesktopCanonicalUuid(value.taskId) &&
    isDesktopCanonicalUuid(value.runtimeId) &&
    isDesktopCanonicalUuid(value.runId) &&
    isDesktopRevision(value.expectedRevision) &&
    isDesktopRevision(value.expectedExecutionGeneration),
  );
}

function isDesktopRevision(value) {
  return Number.isSafeInteger(value) && value >= 1;
}

function isDesktopWorkspacePath(value) {
  if (
    typeof value !== "string" ||
    value.length < 1 ||
    new TextEncoder().encode(value).byteLength > 4096 ||
    value.startsWith("/") ||
    value.includes("\\") ||
    /[\u0000-\u001f\u007f]/u.test(value)
  ) {
    return false;
  }
  return value
    .split("/")
    .every(
      (segment) => segment.length > 0 && segment !== "." && segment !== "..",
    );
}

function desktopWorkspaceFailure(reason, code) {
  return { code, ok: false, reason };
}

function isDesktopMenuCommand(command) {
  if (!command || typeof command !== "object") {
    return false;
  }

  if (command.type === "app:reload" || command.type === "route:settings") {
    return true;
  }

  if (command.type === "command-palette:open") {
    return true;
  }

  if (command.type === "route:path") {
    return isSafeDesktopRoutePath(command.path);
  }

  if (command.type === "notification:activate") {
    return (
      typeof command.notificationId === "string" &&
      command.notificationId.length > 0 &&
      command.notificationId.length <= 120 &&
      (command.companyId === undefined ||
        (typeof command.companyId === "string" &&
          command.companyId.length > 0 &&
          command.companyId.length <= 120)) &&
      (command.userId === undefined ||
        (typeof command.userId === "string" &&
          command.userId.length > 0 &&
          command.userId.length <= 120)) &&
      isSafeDesktopNotificationPath(command.path)
    );
  }

  return (
    command.type === "route:navigate" &&
    [
      "approvals",
      "assets",
      "cockpit",
      "scheduled",
      "settings-company",
      "settings-profile",
      "team",
      "work-search",
    ].includes(command.route)
  );
}

function isDesktopRunPreviewFrame(frame) {
  if (
    frame &&
    typeof frame === "object" &&
    frame.type === "run.preview.reset"
  ) {
    return Object.keys(frame).length === 1;
  }
  return (
    frame &&
    typeof frame === "object" &&
    frame.type === "run.preview.delta" &&
    isBoundedDesktopText(frame.companyId, 512) &&
    isBoundedDesktopText(frame.runId, 512) &&
    isBoundedDesktopText(frame.conversationId, 512) &&
    isBoundedDesktopText(frame.itemKey, 512) &&
    (frame.channel === "text" || frame.channel === "reasoning") &&
    (frame.operation === "append" || frame.operation === "replace") &&
    isBoundedDesktopText(frame.content, 64 << 10)
  );
}

function isDesktopRunPreviewBatch(frames) {
  return (
    Array.isArray(frames) &&
    frames.length <= LOCAL_RUN_PREVIEW_MAX_DRAIN_FRAMES &&
    frames.every(isDesktopRunPreviewFrame)
  );
}

function isBoundedDesktopText(value, maxLength) {
  return (
    typeof value === "string" && value.length > 0 && value.length <= maxLength
  );
}

function isDesktopContextMenuPayload(payload) {
  return (
    payload &&
    typeof payload === "object" &&
    ["agent", "approval", "message", "resource", "task"].includes(
      payload.kind,
    ) &&
    typeof payload.label === "string" &&
    (payload.copyText === undefined || typeof payload.copyText === "string") &&
    (payload.detailText === undefined ||
      typeof payload.detailText === "string") &&
    (payload.routePath === undefined ||
      isSafeDesktopRoutePath(payload.routePath)) &&
    (payload.shareUrl === undefined ||
      isSafeDesktopShareUrl(payload.shareUrl)) &&
    (payload.x === undefined || Number.isFinite(payload.x)) &&
    (payload.y === undefined || Number.isFinite(payload.y))
  );
}

function isDesktopResourceFilePayload(payload) {
  return (
    payload &&
    typeof payload === "object" &&
    typeof payload.resourceId === "string" &&
    typeof payload.title === "string" &&
    (payload.filename === undefined || typeof payload.filename === "string") &&
    (payload.mimeType === undefined || typeof payload.mimeType === "string") &&
    isArrayBufferLike(payload.bytes)
  );
}

function isArrayBufferLike(value) {
  return (
    value instanceof ArrayBuffer ||
    ArrayBuffer.isView(value) ||
    Boolean(
      value &&
      typeof value === "object" &&
      typeof value.byteLength === "number" &&
      typeof value.slice === "function",
    )
  );
}

function isDesktopHuddleInvitationId(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 64 &&
    /^[0-9a-f-]+$/i.test(value)
  );
}

function isDesktopNotificationPayload(payload) {
  return (
    payload &&
    typeof payload === "object" &&
    [
      "huddle_call",
      "chat_mention",
      "approval_request",
      "blocker",
      "question_request",
      "task_assigned",
      "work_completed",
      "work_failed",
      "calendar_reminder",
      "calendar_update",
    ].includes(payload.kind) &&
    typeof payload.title === "string" &&
    typeof payload.body === "string" &&
    (payload.companyId === undefined ||
      (typeof payload.companyId === "string" &&
        payload.companyId.length > 0 &&
        payload.companyId.length <= 120)) &&
    (payload.userId === undefined ||
      (typeof payload.userId === "string" &&
        payload.userId.length > 0 &&
        payload.userId.length <= 120)) &&
    (payload.suppressWhenFocused === undefined ||
      typeof payload.suppressWhenFocused === "boolean") &&
    (payload.allowNative === undefined ||
      typeof payload.allowNative === "boolean") &&
    (payload.id === undefined || typeof payload.id === "string") &&
    (payload.path === undefined ||
      isSafeDesktopNotificationPath(payload.path)) &&
    (payload.silent === undefined || typeof payload.silent === "boolean") &&
    (payload.route === undefined || isDesktopRouteKey(payload.route))
  );
}

function isDesktopWorkbenchStatusPatch(status) {
  return (
    status &&
    typeof status === "object" &&
    (status.unreadCount === undefined ||
      isSafeDesktopCount(status.unreadCount)) &&
    (status.blockedWorkCount === undefined ||
      isSafeDesktopCount(status.blockedWorkCount)) &&
    (status.pendingApprovalCount === undefined ||
      isSafeDesktopCount(status.pendingApprovalCount)) &&
    (status.readyForReviewCount === undefined ||
      isSafeDesktopCount(status.readyForReviewCount)) &&
    (status.isPaused === undefined || typeof status.isPaused === "boolean")
  );
}

function isDesktopWorkbenchStatus(status) {
  return (
    isDesktopWorkbenchStatusPatch(status) &&
    typeof status.unreadCount === "number" &&
    typeof status.blockedWorkCount === "number" &&
    typeof status.isPaused === "boolean" &&
    typeof status.notificationsSupported === "boolean" &&
    typeof status.pendingApprovalCount === "number" &&
    typeof status.readyForReviewCount === "number" &&
    status.globalShortcut &&
    typeof status.globalShortcut === "object" &&
    typeof status.globalShortcut.accelerator === "string" &&
    typeof status.globalShortcut.enabled === "boolean" &&
    typeof status.globalShortcut.registered === "boolean" &&
    (status.globalShortcut.reason === null ||
      typeof status.globalShortcut.reason === "string")
  );
}

function isDesktopPowerLifecycleState(state) {
  return (
    state &&
    typeof state === "object" &&
    ["ac", "battery", "unknown"].includes(state.battery) &&
    ["active", "idle", "locked", "unknown"].includes(state.idleState) &&
    (state.idleTimeSeconds === null ||
      isSafeNonNegativeInteger(state.idleTimeSeconds)) &&
    ["active", "locked", "suspended"].includes(state.lifecycle) &&
    (state.speedLimitPercent === null ||
      isSafePowerLimitPercent(state.speedLimitPercent)) &&
    ["critical", "fair", "nominal", "serious", "unknown"].includes(
      state.thermalState,
    ) &&
    (state.lastEvent === null || isDesktopPowerLifecycleEvent(state.lastEvent))
  );
}

function isDesktopPowerLifecycleEvent(event) {
  return (
    event &&
    typeof event === "object" &&
    [
      "lock-screen",
      "on-ac",
      "on-battery",
      "resume",
      "speed-limit-change",
      "suspend",
      "thermal-state-change",
      "unlock-screen",
      "user-did-become-active",
      "user-did-resign-active",
    ].includes(event.type) &&
    typeof event.createdAt === "string" &&
    Number.isFinite(Date.parse(event.createdAt))
  );
}

function isDesktopDaemonStopOptions(options) {
  return (
    desktopIsPlainObject(options) &&
    (Object.keys(options).length === 0 ||
      (desktopHasOnlyKeys(options, ["confirmActive"]) &&
        typeof options.confirmActive === "boolean"))
  );
}

function isDesktopDaemonStatus(status) {
  return (
    desktopIsPlainObject(status) &&
    [
      "stopped",
      "starting",
      "running",
      "blocked",
      "restarting",
      "stopping",
      "unavailable",
    ].includes(status.state) &&
    (status.reason === null || isBoundedDesktopText(status.reason, 200)) &&
    typeof status.ready === "boolean" &&
    Number.isSafeInteger(status.activeRunCount) &&
    status.activeRunCount >= 0 &&
    Number.isSafeInteger(status.startAttempts) &&
    status.startAttempts >= 0 &&
    (status.pid === null ||
      (Number.isSafeInteger(status.pid) && status.pid > 0)) &&
    (status.cliVersion === null ||
      isBoundedDesktopText(status.cliVersion, 120)) &&
    (status.daemonId === null || isBoundedDesktopText(status.daemonId, 200)) &&
    (status.reloadPendingReason === null ||
      isBoundedDesktopText(status.reloadPendingReason, 2_000)) &&
    Array.isArray(status.agents) &&
    status.agents.length <= 16 &&
    status.agents.every(
      (agent) =>
        desktopHasOnlyKeys(agent, ["provider", "version"]) &&
        isBoundedDesktopText(agent.provider, 64) &&
        (agent.version === null || isBoundedDesktopText(agent.version, 120)),
    ) &&
    Array.isArray(status.skippedAgents) &&
    status.skippedAgents.length <= 16 &&
    status.skippedAgents.every(
      (agent) =>
        desktopHasOnlyKeys(agent, ["provider", "reason"]) &&
        isBoundedDesktopText(agent.provider, 64) &&
        isBoundedDesktopText(agent.reason, 2_000),
    )
  );
}

function isDesktopDaemonLogBatch(batch) {
  return (
    desktopHasOnlyKeys(batch, ["lines", "reset"]) &&
    typeof batch.reset === "boolean" &&
    Array.isArray(batch.lines) &&
    batch.lines.length <= DAEMON_LOG_MAX_LINES &&
    batch.lines.every(
      (line) =>
        desktopHasOnlyKeys(line, ["level", "text", "time"]) &&
        ["debug", "info", "warn", "error"].includes(line.level) &&
        typeof line.text === "string" &&
        line.text.length <= 16 << 10 &&
        (line.time === null || isBoundedDesktopText(line.time, 64)),
    )
  );
}

function isDesktopUpdateState(state) {
  return (
    state &&
    typeof state === "object" &&
    (state.availableVersion === null ||
      isDesktopReleaseVersion(state.availableVersion)) &&
    typeof state.automaticUpdatesEnabled === "boolean" &&
    typeof state.automaticUpdatesSupported === "boolean" &&
    [
      "available",
      "checking",
      "downloaded",
      "downloading",
      "failed",
      "idle",
      "manual",
      "restart-required",
      "up-to-date",
    ].includes(state.status) &&
    (state.checkedAt === null ||
      (typeof state.checkedAt === "string" &&
        Number.isFinite(Date.parse(state.checkedAt)))) &&
    (state.downloadPercent === null ||
      (Number.isInteger(state.downloadPercent) &&
        state.downloadPercent >= 0 &&
        state.downloadPercent <= 100)) &&
    (state.errorMessage === null || typeof state.errorMessage === "string") &&
    (state.feedUrl === null || isSafeDesktopShareUrl(state.feedUrl)) &&
    (state.installedVersion === null ||
      isDesktopReleaseVersion(state.installedVersion)) &&
    (state.manualUrl === null || isSafeDesktopShareUrl(state.manualUrl)) &&
    (state.policy === "manual" || state.policy === "native") &&
    (state.releaseDate === null ||
      (typeof state.releaseDate === "string" &&
        Number.isFinite(Date.parse(state.releaseDate)))) &&
    (state.releaseName === null || typeof state.releaseName === "string") &&
    typeof state.updateAvailable === "boolean" &&
    (state.updateUrl === null || isSafeDesktopShareUrl(state.updateUrl))
  );
}

function isDesktopReleaseVersion(value) {
  return (
    typeof value === "string" &&
    value.length <= 120 &&
    /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/u.test(value)
  );
}

function isDesktopSupportDiagnosticsPayload(payload) {
  return (
    payload &&
    typeof payload === "object" &&
    (payload.durationMs === undefined ||
      (Number.isFinite(payload.durationMs) &&
        payload.durationMs >= 1000 &&
        payload.durationMs <= 30000))
  );
}

function isSafeDesktopCount(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

function isSafeNonNegativeInteger(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

function isSafePowerLimitPercent(value) {
  return Number.isSafeInteger(value) && value >= 0 && value <= 100;
}

function isDesktopRouteKey(route) {
  return [
    "approvals",
    "assets",
    "cockpit",
    "scheduled",
    "settings-company",
    "settings-profile",
    "team",
    "work-search",
  ].includes(route);
}

function isSafeDesktopRoutePath(pathname) {
  return (
    typeof pathname === "string" &&
    pathname.startsWith("/") &&
    pathname.length <= 320 &&
    !pathname.startsWith("//") &&
    !pathname.includes("\\") &&
    !/^[a-z][a-z0-9+.-]*:/i.test(pathname)
  );
}

function isSafeDesktopNotificationPath(pathname) {
  return (
    typeof pathname === "string" &&
    pathname.startsWith("/") &&
    pathname.length <= 1024 &&
    !pathname.startsWith("//") &&
    !pathname.includes("\\") &&
    !/^[a-z][a-z0-9+.-]*:/i.test(pathname)
  );
}

function isSafeDesktopShareUrl(value) {
  if (typeof value !== "string" || value.length > 1024) {
    return false;
  }

  const normalized = value.trim();
  if (!normalized || /\s/.test(normalized)) {
    return false;
  }

  try {
    const url = new URL(normalized);
    return (
      (url.protocol === "https:" || url.protocol === "http:") &&
      !url.username &&
      !url.password
    );
  } catch (_error) {
    return false;
  }
}

function isDesktopNativeTheme(theme) {
  return (
    theme &&
    typeof theme === "object" &&
    (theme.colorScheme === "dark" || theme.colorScheme === "light") &&
    typeof theme.shouldUseDarkColors === "boolean" &&
    typeof theme.shouldUseHighContrastColors === "boolean" &&
    typeof theme.shouldUseInvertedColorScheme === "boolean" &&
    typeof theme.inForcedColorsMode === "boolean" &&
    typeof theme.prefersReducedTransparency === "boolean" &&
    typeof theme.shouldDifferentiateWithoutColor === "boolean" &&
    ["dark", "light", "system"].includes(theme.themeSource)
  );
}
