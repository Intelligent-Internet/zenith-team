"use strict";

const {
  isToolRequest,
  toolFailure,
  toolOperationResponse,
} = require("../shared/tool-contract.cjs");

const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { fileURLToPath } = require("node:url");
const { app, dialog, ipcMain, shell } = require("electron");

const {
  clearStoredAccessToken,
  readStoredAccessToken,
} = require("./desktop-auth-store.cjs");
const {
  completeGoogleOAuth,
  describeAuthFailure,
  getEmailVerificationStatus,
  getProviders,
  loginWithPassword,
  logoutSession,
  refreshSession,
  resendEmailVerification,
  setBeforeDesktopSessionReplace,
  signupWithPassword,
  startGoogleOAuth,
  verifyEmail,
} = require("./desktop-auth-api.cjs");
const { isAppNavigationUrl, openExternalUrl } = require("./external-links.cjs");
const { createDesktopAppPreview } = require("./app-preview.cjs");
const {
  createArtifactFileCache,
  registerArtifactFileCacheHandlers,
} = require("./artifact-file-cache.cjs");
const {
  createHuddleStartWindow,
  createHuddleWindow,
  getRendererUrl,
  isTrustedHuddleFrame,
} = require("./windows.cjs");
const { isTrustedMainFrame } = require("./trusted-frame.cjs");
const { createToolAuthReturn } = require("./tool-auth-return.cjs");
const { isSafePluginContinueUrl } = require("../shared/url-policy.cjs");
const {
  sanitizeConnectorStatus,
  sanitizePluginDetail,
  sanitizePluginInstallAnswer,
} = require("../shared/plugin-detail.cjs");
const {
  startSocialOAuthLoopback,
  stopSocialOAuthLoopback,
} = require("./social-oauth-loopback.cjs");
const {
  startGoogleOAuthLoopback,
  stopGoogleOAuthLoopback,
} = require("./oauth-loopback.cjs");
const {
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");

const DRAIN_LOCAL_RUN_PREVIEW_CHANNEL = "desktop:drain-run-preview";
const CONNECTOR_CONNECT_PATH = "/v1/connectors/connect";
const CONNECTOR_REFRESH_PATH = "/v1/connectors/refresh";
const CONNECTOR_STATUS_PATH = "/v1/connectors/status";
const PLUGIN_INSTALL_PATH = "/v1/plugins/install";
const PLUGIN_READ_PATH = "/v1/plugins/read";
const PLUGIN_REFRESH_PATH = "/v1/plugins/refresh";
const PLUGIN_UNINSTALL_PATH = "/v1/plugins/uninstall";
const TASK_REVIEW_ACCEPT_PATH = "/v1/task-review/accept";
const TASK_REVIEW_PREPARE_PATH = "/v1/task-review/prepare";
const WORKSPACE_ASSET_EXPORT_PATH = "/v1/workspace-assets/export";
const WORKSPACE_ASSET_LIST_PATH = "/v1/workspace-assets/list";
const WORKSPACE_ASSET_PREVIEW_PATH = "/v1/workspace-assets/preview";
const WORKSPACE_FILE_EXPORT_PATH = "/v1/workspace-files/export";
const WORKSPACE_FILE_PREVIEW_PATH = "/v1/workspace-files/preview";
const WORKSPACE_FILE_RESOLVE_PATH = "/v1/workspace-files/resolve";
const REVEAL_SNAPSHOT_LIMIT = 32;
const REVEAL_SNAPSHOT_TTL_MS = 15 * 60 * 1000;

let registered = false;
let desktopEnv = process.env;
let drainLocalRunPreviewFrames = () => [];
let getMainWindow = () => null;
let onAuthSessionChanged = async () => {};
let requestDaemonExport = () => Promise.resolve(null);
let requestDaemonOperation = () => Promise.resolve(null);
let desktopAppPreview = null;
let artifactFileCache = null;
const revealSnapshots = [];
const toolAuthReturn = createToolAuthReturn({
  getWindow: () => getMainWindow(),
  activateApp: () => {
    if (process.platform === "darwin") app.focus({ steal: true });
  },
  readStatus: async (request) =>
    toolOperationResponse(
      await requestLocalOperation("/v1/tools/operate", request),
    ),
});

function registerIpcHandlers(options = {}) {
  if (typeof options.drainLocalRunPreview === "function") {
    drainLocalRunPreviewFrames = options.drainLocalRunPreview;
  }
  if (options.env && typeof options.env === "object") {
    desktopEnv = options.env;
  }
  if (typeof options.getMainWindow === "function") {
    getMainWindow = options.getMainWindow;
  }
  if (typeof options.requestDaemonOperation === "function") {
    requestDaemonOperation = options.requestDaemonOperation;
  }
  if (typeof options.requestDaemonExport === "function") {
    requestDaemonExport = options.requestDaemonExport;
  }
  if (typeof options.onAuthSessionChanged === "function") {
    onAuthSessionChanged = options.onAuthSessionChanged;
  }
  setBeforeDesktopSessionReplace(stopAndClearDesktopSession);

  if (registered) return;
  registered = true;
  artifactFileCache = createArtifactFileCache({ app, env: desktopEnv });
  registerArtifactFileCacheHandlers({
    cache: artifactFileCache,
    getMainWindow: () => getMainWindow(),
  });
  desktopAppPreview = createDesktopAppPreview({
    getMainWindow: () => getMainWindow(),
    requestDaemonOperation: (pathname, payload) =>
      requestDaemonOperation(pathname, payload),
  });

  ipcMain.handle("desktop:get-environment", () => ({
    isDesktop: true,
    platform: getDesktopPlatform(),
    appName: app.getName(),
    appVersion: app.getVersion(),
    environment: getDesktopEnvironmentName(),
  }));

  ipcMain.handle(DRAIN_LOCAL_RUN_PREVIEW_CHANNEL, (event) => {
    const window = getMainWindow();
    if (
      !window ||
      window.isDestroyed?.() ||
      !window.webContents ||
      window.webContents.isDestroyed?.() ||
      event.sender !== window.webContents
    ) {
      return [];
    }
    return drainLocalRunPreviewFrames();
  });

  ipcMain.handle("desktop:operate-tool", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isToolRequest(request)
    ) {
      return toolFailure("invalid_request");
    }
    const result = await requestLocalOperation("/v1/tools/operate", request);
    const response = toolOperationResponse(result);
    if (response.ok && request.action === "connect")
      toolAuthReturn.start(request);
    if (response.ok && request.action === "cancel")
      toolAuthReturn.cancel(request);
    return response;
  });

  ipcMain.handle("desktop:connect-connector", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isConnectorActionRequest(request)
    ) {
      return pluginFailure("Connector request is invalid.", "invalid_request");
    }
    return requestPluginOperation(CONNECTOR_CONNECT_PATH, request);
  });

  ipcMain.handle("desktop:read-connector-status", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isConnectorActionRequest(request)
    ) {
      return pluginFailure("Connector request is invalid.", "invalid_request");
    }
    return requestConnectorStatus(request);
  });

  ipcMain.handle("desktop:refresh-connectors", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isPluginRefreshRequest(request)
    ) {
      return pluginFailure("Connector request is invalid.", "invalid_request");
    }
    return requestPluginOperation(CONNECTOR_REFRESH_PATH, request);
  });

  ipcMain.handle("desktop:open-app-preview", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isAppPreviewRequest(request)
    ) {
      return appPreviewFailure(
        "App preview request is invalid.",
        "invalid_request",
      );
    }
    return desktopAppPreview.open(event.sender, request);
  });

  ipcMain.handle("desktop:restart-app-preview", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isAppPreviewViewerRequest(request)
    ) {
      return appPreviewFailure(
        "App preview request is invalid.",
        "invalid_request",
      );
    }
    return desktopAppPreview.restart(request.viewerId);
  });

  ipcMain.handle("desktop:stop-app-preview", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isAppPreviewViewerRequest(request)
    ) {
      return appPreviewFailure(
        "App preview request is invalid.",
        "invalid_request",
      );
    }
    return desktopAppPreview.stop(request.viewerId);
  });

  ipcMain.handle("desktop:close-app-preview", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isAppPreviewViewerRequest(request)
    ) {
      return appPreviewFailure(
        "App preview request is invalid.",
        "invalid_request",
      );
    }
    return desktopAppPreview.close(request.viewerId);
  });

  ipcMain.handle("desktop:install-plugin", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isPluginActionRequest(request)
    ) {
      return pluginFailure("Plugin request is invalid.", "invalid_request");
    }
    return requestPluginOperation(PLUGIN_INSTALL_PATH, request);
  });

  ipcMain.handle("desktop:uninstall-plugin", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isPluginActionRequest(request)
    ) {
      return pluginFailure("Plugin request is invalid.", "invalid_request");
    }
    return requestPluginOperation(PLUGIN_UNINSTALL_PATH, request);
  });

  ipcMain.handle("desktop:read-plugin", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isPluginActionRequest(request)
    ) {
      return pluginFailure("Plugin request is invalid.", "invalid_request");
    }
    return requestPluginRead(request);
  });

  ipcMain.handle("desktop:refresh-plugins", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isPluginRefreshRequest(request)
    ) {
      return pluginFailure("Plugin request is invalid.", "invalid_request");
    }
    return refreshPluginSources(request);
  });

  ipcMain.handle("desktop:preview-generated-file", async (event, reference) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isGeneratedFileReference(reference)
    ) {
      return generatedFileFailure(
        "Generated file reference is invalid.",
        "invalid_request",
      );
    }
    const mediaType = generatedFileMediaType(reference.path);
    if (mediaType) return previewGeneratedFileBytes(reference, mediaType);
    const result = await requestWorkspaceFile(
      WORKSPACE_FILE_PREVIEW_PATH,
      reference,
    );
    if (!result?.ok) {
      return generatedFileFailure(
        safeWorkspaceReason(
          result?.payload,
          "Generated file preview is unavailable.",
        ),
        safeFailureCode(result?.payload),
      );
    }
    const value = sanitizeGeneratedFilePreview(result.payload, reference.path);
    return value
      ? { ok: true, value }
      : generatedFileFailure(
          "Generated file preview response is invalid.",
          "invalid_response",
        );
  });

  ipcMain.handle(
    "desktop:resolve-generated-file-link",
    async (event, reference) => {
      if (
        !isTrustedMainFrame(event, getMainWindow()) ||
        !isGeneratedFileLinkReference(reference)
      ) {
        return generatedFileFailure(
          "Generated file link is invalid.",
          "invalid_request",
        );
      }
      let absolutePath;
      try {
        absolutePath = reference.href.startsWith("file:")
          ? fileURLToPath(new URL(reference.href))
          : decodeURIComponent(reference.href);
      } catch {
        return generatedFileFailure(
          "Generated file link is invalid.",
          "invalid_request",
        );
      }
      if (
        !path.isAbsolute(absolutePath) ||
        Buffer.byteLength(absolutePath, "utf8") > 4096
      ) {
        return generatedFileFailure(
          "Generated file link is invalid.",
          "invalid_request",
        );
      }
      const result = await requestWorkspaceFile(WORKSPACE_FILE_RESOLVE_PATH, {
        path: absolutePath,
        runId: reference.runId,
      });
      if (!result?.ok) {
        return generatedFileFailure(
          safeWorkspaceReason(
            result?.payload,
            "Generated file preview is unavailable.",
          ),
          safeFailureCode(result?.payload),
        );
      }
      const resolved = result.payload;
      return resolved?.schemaVersion === 1 &&
        canonicalWorkspacePath(resolved.path)
        ? { ok: true, value: { path: resolved.path } }
        : generatedFileFailure(
            "Generated file link response is invalid.",
            "invalid_response",
          );
    },
  );

  ipcMain.handle("desktop:save-generated-file", async (event, reference) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isGeneratedFileReference(reference)
    ) {
      return generatedFileFailure(
        "Generated file reference is invalid.",
        "invalid_request",
      );
    }
    return saveWorkspaceFile(
      WORKSPACE_FILE_EXPORT_PATH,
      workspaceFilePayload(reference),
      path.posix.basename(reference.path),
      "Export Zenith Team file",
      "Unable to export this generated file.",
      generatedFileFailure,
    );
  });

  ipcMain.handle("desktop:reveal-generated-file", async (event, reference) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isGeneratedFileReference(reference)
    ) {
      return generatedFileFailure(
        "Generated file reference is invalid.",
        "invalid_request",
      );
    }
    return revealWorkspaceFile(
      WORKSPACE_FILE_EXPORT_PATH,
      workspaceFilePayload(reference),
      path.posix.basename(reference.path),
      "Unable to reveal this generated file.",
      generatedFileFailure,
    );
  });

  ipcMain.handle("desktop:list-workspace-assets", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isWorkspaceAssetListRequest(request)
    ) {
      return workspaceAssetFailure(
        "Workspace assets request is invalid.",
        "invalid_request",
      );
    }
    const result = await requestLocalOperation(WORKSPACE_ASSET_LIST_PATH, {
      ...request,
      schemaVersion: 1,
    });
    if (!result?.ok) {
      return workspaceAssetFailure(
        safeWorkspaceReason(
          result?.payload,
          "Workspace assets are unavailable.",
        ),
        safeFailureCode(result?.payload),
      );
    }
    const value = sanitizeWorkspaceAssetList(result.payload, request);
    return value
      ? { ok: true, value }
      : workspaceAssetFailure(
          "Workspace assets response is invalid.",
          "invalid_response",
        );
  });

  ipcMain.handle(
    "desktop:preview-workspace-asset",
    async (event, reference) => {
      if (
        !isTrustedMainFrame(event, getMainWindow()) ||
        !isWorkspaceAssetReference(reference)
      ) {
        return workspaceAssetFailure(
          "Workspace asset reference is invalid.",
          "invalid_request",
        );
      }
      const result = await requestWorkspaceAsset(
        WORKSPACE_ASSET_PREVIEW_PATH,
        reference,
      );
      if (!result?.ok) {
        return workspaceAssetFailure(
          safeWorkspaceReason(
            result?.payload,
            "Workspace asset preview is unavailable.",
          ),
          safeFailureCode(result?.payload),
        );
      }
      const value = sanitizeWorkspaceAssetPreview(
        result.payload,
        reference.path,
      );
      return value
        ? { ok: true, value }
        : workspaceAssetFailure(
            "Workspace asset preview response is invalid.",
            "invalid_response",
          );
    },
  );

  ipcMain.handle("desktop:save-workspace-asset", async (event, reference) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isWorkspaceAssetReference(reference)
    ) {
      return workspaceAssetFailure(
        "Workspace asset reference is invalid.",
        "invalid_request",
      );
    }
    return saveWorkspaceFile(
      WORKSPACE_ASSET_EXPORT_PATH,
      workspaceAssetPayload(reference),
      path.posix.basename(reference.path),
      "Export workspace asset",
      "Unable to export this workspace asset.",
      workspaceAssetFailure,
    );
  });

  ipcMain.handle("desktop:reveal-workspace-asset", async (event, reference) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isWorkspaceAssetReference(reference)
    ) {
      return workspaceAssetFailure(
        "Workspace asset reference is invalid.",
        "invalid_request",
      );
    }
    return revealWorkspaceFile(
      WORKSPACE_ASSET_EXPORT_PATH,
      workspaceAssetPayload(reference),
      path.posix.basename(reference.path),
      "Unable to reveal this workspace asset.",
      workspaceAssetFailure,
    );
  });

  ipcMain.handle("desktop:review-task-workspace", async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isTaskReviewRequest(request)
    ) {
      return taskReviewFailure(
        "Task review request is invalid.",
        "invalid_request",
      );
    }
    const result = await requestLocalOperation(
      request.action === "accept"
        ? TASK_REVIEW_ACCEPT_PATH
        : TASK_REVIEW_PREPARE_PATH,
      {
        schemaVersion: 1,
        runId: request.runId,
        ...(request.action === "accept"
          ? {
              companyId: request.companyId,
              taskId: request.taskId,
              runtimeId: request.runtimeId,
              expectedRevision: request.expectedRevision,
              expectedExecutionGeneration: request.expectedExecutionGeneration,
            }
          : {}),
      },
    );
    if (!result) {
      return taskReviewFailure(
        "The desktop connection to the local daemon is unavailable. Restart the daemon, then try again.",
        "daemon_unavailable",
      );
    }
    if (!result.ok) {
      return taskReviewFailure(
        safeWorkspaceReason(result.payload, "Task review is unavailable."),
        safeFailureCode(result.payload),
      );
    }
    const value = sanitizeTaskReviewResponse(result.payload, request.runId);
    return value
      ? { ok: true, value }
      : taskReviewFailure(
          "Task review response is invalid.",
          "invalid_response",
        );
  });

  ipcMain.handle("desktop:open-external", (_event, url) => {
    if (typeof url !== "string") {
      return { ok: false, reason: "External URL must be a string." };
    }
    return openExternalUrl(url);
  });

  ipcMain.handle("desktop:open-huddle-window", (event, huddleId) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isHuddleWindowId(huddleId)
    ) {
      return { ok: false, reason: "Huddle window request is invalid." };
    }
    createHuddleWindow(huddleId);
    return { ok: true };
  });

  ipcMain.handle(
    "desktop:open-huddle-start-window",
    (event, conversationId) => {
      if (
        !isTrustedMainFrame(event, getMainWindow()) ||
        !isHuddleWindowId(conversationId)
      ) {
        return { ok: false, reason: "Huddle window request is invalid." };
      }
      createHuddleStartWindow(conversationId);
      return { ok: true };
    },
  );

  ipcMain.handle("desktop:start-social-oauth-loopback", () =>
    startSocialOAuthLoopback({ getMainWindow }),
  );
  ipcMain.handle("desktop:cancel-social-oauth-loopback", async () => {
    await stopSocialOAuthLoopback();
    return { ok: true };
  });
  ipcMain.handle("desktop:start-google-oauth-loopback", () =>
    startGoogleOAuthLoopback({ env: desktopEnv, getMainWindow }),
  );
  ipcMain.handle("desktop:cancel-google-oauth-loopback", async () => {
    await stopGoogleOAuthLoopback();
    return { ok: true };
  });

  // The session token is the most sensitive thing the bridge hands out, so
  // only the app itself — the main window or a Huddle window, main frame,
  // still on the app origin — may read or clear it.
  ipcMain.handle("desktop:get-stored-access-token", (event) =>
    isTrustedSessionFrame(event)
      ? readStoredAccessToken()
      : { ok: false, reason: "Desktop session request is invalid." },
  );
  ipcMain.handle("desktop:clear-stored-access-token", async (event) => {
    if (!isTrustedSessionFrame(event)) {
      return { ok: false, reason: "Unable to clear the desktop session." };
    }
    try {
      await stopAndClearDesktopSession();
      return { ok: true };
    } catch {
      return { ok: false, reason: "Unable to clear the desktop session." };
    }
  });

  // Every sign-in that ends in a session may fail only because this
  // computer cannot save it (session_not_saved). That failure is passed on
  // as it is, never as a wrong password or a wrong code.
  ipcMain.handle("desktop:complete-google-oauth", async (_event, payload) => {
    try {
      const response = await completeGoogleOAuth(payload);
      await notifyAuthSessionChanged(true);
      return { ok: true, response };
    } catch (error) {
      return sessionNotSavedFailure(
        error,
        "Unable to complete Google sign-in.",
      );
    }
  });
  ipcMain.handle("desktop:login-password", async (_event, payload) => {
    try {
      const response = await loginWithPassword(payload);
      if (response.result === "session") {
        await notifyAuthSessionChanged(true);
      }
      return { ok: true, response };
    } catch (error) {
      // A wrong password is the server's refusal (kind rejected, with its
      // own words); anything else is not, so the fallback never says so.
      return describeAuthFailure(error, "Unable to sign in.");
    }
  });
  ipcMain.handle("desktop:signup-password", async (_event, payload) => {
    try {
      const response = await signupWithPassword(payload);
      if (response.result === "session") {
        await notifyAuthSessionChanged(true);
      }
      return { ok: true, response };
    } catch (error) {
      return describeAuthFailure(error, "Account could not be created.");
    }
  });
  ipcMain.handle("desktop:verify-email", async (_event, payload) => {
    try {
      const response = await verifyEmail(payload);
      await notifyAuthSessionChanged(true);
      return { ok: true, response };
    } catch (error) {
      return sessionNotSavedFailure(error, "Unable to verify this code.");
    }
  });
  ipcMain.handle(
    "desktop:resend-email-verification",
    async (_event, payload) => {
      try {
        return { ok: true, response: await resendEmailVerification(payload) };
      } catch {
        return { ok: false, reason: "Unable to resend the verification code." };
      }
    },
  );
  ipcMain.handle(
    "desktop:get-email-verification-status",
    async (_event, payload) => {
      try {
        return {
          ok: true,
          response: await getEmailVerificationStatus(payload),
        };
      } catch {
        return {
          ok: false,
          reason: "Unable to load email verification status.",
        };
      }
    },
  );
  ipcMain.handle("desktop:get-auth-providers", async (_event, apiBaseUrl) => {
    try {
      return { ok: true, response: await getProviders({ apiBaseUrl }) };
    } catch {
      return { ok: false, reason: "Unable to load authentication providers." };
    }
  });
  ipcMain.handle("desktop:start-google-oauth", async (_event, payload) => {
    try {
      return { ok: true, response: await startGoogleOAuth(payload) };
    } catch {
      return { ok: false, reason: "Unable to start Google sign-in." };
    }
  });
  ipcMain.handle("desktop:refresh-auth-session", async (_event, apiBaseUrl) => {
    try {
      const response = await refreshSession({ apiBaseUrl });
      await notifyAuthSessionChanged(true);
      return { ok: true, response };
    } catch (error) {
      return describeAuthFailure(
        error,
        "Unable to refresh the desktop session.",
      );
    }
  });
  ipcMain.handle("desktop:logout-auth-session", async (_event, apiBaseUrl) => {
    const failures = [];
    try {
      await logoutSession({ apiBaseUrl });
    } catch (error) {
      failures.push(error);
    }
    try {
      await stopAndClearDesktopSession();
    } catch (error) {
      failures.push(error);
    }
    if (failures.length > 0) {
      return { ok: false, reason: "Unable to sign out of Zenith Team." };
    }
    return { ok: true };
  });
}

// sessionNotSavedFailure keeps a flow's one sentence for every failure but
// a session this computer could not save, which keeps its own kind.
function sessionNotSavedFailure(error, reason) {
  const failure = describeAuthFailure(error, reason);
  return failure.kind === "session_not_saved" ? failure : { ok: false, reason };
}

function isTrustedSessionFrame(event) {
  if (isTrustedHuddleFrame(event)) return true;
  return (
    isTrustedMainFrame(event, getMainWindow()) &&
    isAppNavigationUrl(event.senderFrame.url, getRendererUrl())
  );
}

function isHuddleWindowId(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/iu.test(
      value,
    )
  );
}

async function requestPluginOperation(requestPath, payload) {
  const result = await requestLocalOperation(requestPath, payload);
  if (isPlainObject(result?.payload) && result.payload.ok === false) {
    return pluginFailure(
      safeFailureReason(result.payload, "Plugin operation failed."),
      safeFailureCode(result.payload, "operation_failed"),
    );
  }
  if (!result?.ok || result.payload?.ok !== true) {
    return pluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
  const value = sanitizePluginOperation(result.payload.value);
  return value
    ? { ok: true, value }
    : pluginFailure(
        "Plugin operation response is invalid.",
        "invalid_response",
      );
}

async function requestPluginRead(payload) {
  const result = await requestLocalOperation(PLUGIN_READ_PATH, payload);
  if (result?.status === 404 && !result.payload) {
    // A daemon that predates the read answers a plain-text 404.
    return pluginFailure(
      "Update the local daemon to read Plugin details.",
      "daemon_update_required",
    );
  }
  if (isPlainObject(result?.payload) && result.payload.ok === false) {
    return pluginFailure(
      safeFailureReason(result.payload, "Plugin details are unavailable."),
      safeFailureCode(result.payload, "operation_failed"),
    );
  }
  if (!result?.ok || result.payload?.ok !== true) {
    return pluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
  const value = sanitizePluginDetail(result.payload.value, payload.nativeId);
  return value
    ? { ok: true, value }
    : pluginFailure("Plugin details response is invalid.", "invalid_response");
}

async function requestConnectorStatus(payload) {
  const result = await requestLocalOperation(CONNECTOR_STATUS_PATH, payload);
  if (result?.status === 404 && !result.payload) {
    // A daemon that predates the check answers a plain-text 404.
    return pluginFailure(
      "Update the local daemon to check connections.",
      "daemon_update_required",
    );
  }
  if (isPlainObject(result?.payload) && result.payload.ok === false) {
    return pluginFailure(
      safeFailureReason(result.payload, "The connection could not be checked."),
      safeFailureCode(result.payload, "operation_failed"),
    );
  }
  if (!result?.ok || result.payload?.ok !== true) {
    return pluginFailure(
      "The local daemon is unavailable.",
      "daemon_unavailable",
    );
  }
  const value = sanitizeConnectorStatus(result.payload.value);
  return value
    ? { ok: true, value }
    : pluginFailure(
        "Connector status response is invalid.",
        "invalid_response",
      );
}

// Plugin and connector discovery are independent reads, so both run at once;
// the plugin result still decides the answer when it did not complete.
async function refreshPluginSources(payload) {
  const [pluginResult, connectorResult] = await Promise.all([
    requestRefreshOperation(PLUGIN_REFRESH_PATH, payload),
    requestRefreshOperation(CONNECTOR_REFRESH_PATH, payload),
  ]);
  if (!pluginResult.ok || pluginResult.value.state !== "complete") {
    return pluginResult;
  }
  return connectorResult;
}

// A daemon that predates the optional force flag rejects it as an unknown
// field, so an unforced refresh retries as the forced refresh it knows.
async function requestRefreshOperation(requestPath, payload) {
  const result = await requestPluginOperation(requestPath, payload);
  if (
    payload.force === false &&
    !result.ok &&
    result.code === "invalid_request"
  ) {
    const { force: _force, ...forced } = payload;
    return requestPluginOperation(requestPath, forced);
  }
  return result;
}

async function requestLocalOperation(requestPath, payload) {
  try {
    return await requestDaemonOperation(requestPath, payload);
  } catch {
    return null;
  }
}

function workspaceFilePayload(reference) {
  return {
    schemaVersion: 1,
    runId: reference.runId,
    path: reference.path,
  };
}

function requestWorkspaceFile(requestPath, reference) {
  return requestLocalOperation(requestPath, workspaceFilePayload(reference));
}

function workspaceAssetPayload(reference) {
  return {
    schemaVersion: 1,
    companyId: reference.companyId,
    scope: reference.scope,
    path: reference.path,
  };
}

function requestWorkspaceAsset(requestPath, reference) {
  return requestLocalOperation(requestPath, workspaceAssetPayload(reference));
}

async function requestWorkspaceExport(requestPath, payload, destinationPath) {
  try {
    return await requestDaemonExport(requestPath, payload, destinationPath);
  } catch {
    return null;
  }
}

async function saveWorkspaceFile(
  requestPath,
  payload,
  filename,
  title,
  fallback,
  failure,
) {
  try {
    const result = await dialog.showSaveDialog(getMainWindow(), {
      buttonLabel: "Export",
      defaultPath: filename,
      title,
    });
    if (result.canceled || !result.filePath) {
      return { canceled: true, ok: true };
    }
    const exported = await requestWorkspaceExport(
      requestPath,
      payload,
      result.filePath,
    );
    if (!exported?.ok) {
      return failure(
        safeWorkspaceReason(exported?.payload, fallback),
        safeFailureCode(exported?.payload, "operation_failed"),
      );
    }
    addRecentDocument(result.filePath);
    return { canceled: false, ok: true };
  } catch {
    return failure(fallback, "operation_failed");
  }
}

async function revealWorkspaceFile(
  requestPath,
  payload,
  filename,
  fallback,
  failure,
) {
  let snapshot;
  try {
    snapshot = await createRevealSnapshot(filename);
    const exported = await requestWorkspaceExport(
      requestPath,
      payload,
      snapshot.filePath,
    );
    if (!exported?.ok) {
      await removeRevealSnapshot(snapshot.directory);
      return failure(
        safeWorkspaceReason(exported?.payload, fallback),
        safeFailureCode(exported?.payload, "operation_failed"),
      );
    }
    retainRevealSnapshot(snapshot.directory);
    shell.showItemInFolder(snapshot.filePath);
    return { ok: true };
  } catch {
    if (snapshot) await removeRevealSnapshot(snapshot.directory);
    return failure(fallback, "operation_failed");
  }
}

async function createRevealSnapshot(filename) {
  const directory = await fs.mkdtemp(
    path.join(os.tmpdir(), "zenith-team-reveal-"),
  );
  await fs.chmod(directory, 0o700);
  return {
    directory,
    filePath: path.join(directory, safeRevealFilename(filename)),
  };
}

function safeRevealFilename(filename) {
  const sanitized = filename.replace(/[<>:"/\\|?*\u0000-\u001f]/gu, "_");
  return sanitized !== "." &&
    sanitized !== ".." &&
    sanitized.length > 0 &&
    Buffer.byteLength(sanitized, "utf8") <= 240
    ? sanitized
    : "zenith-team-export";
}

function retainRevealSnapshot(directory) {
  const entry = { directory, timer: null };
  entry.timer = setTimeout(() => {
    void removeRevealSnapshot(directory);
  }, REVEAL_SNAPSHOT_TTL_MS);
  entry.timer.unref?.();
  revealSnapshots.push(entry);
  while (revealSnapshots.length > REVEAL_SNAPSHOT_LIMIT) {
    const oldest = revealSnapshots.shift();
    clearTimeout(oldest?.timer);
    if (oldest) void fs.rm(oldest.directory, { force: true, recursive: true });
  }
}

async function removeRevealSnapshot(directory) {
  const index = revealSnapshots.findIndex(
    (entry) => entry.directory === directory,
  );
  if (index >= 0) {
    const [entry] = revealSnapshots.splice(index, 1);
    clearTimeout(entry.timer);
  }
  await fs.rm(directory, { force: true, recursive: true });
}

function generatedFileMediaType(filename) {
  return {
    ".pdf": "application/pdf",
    ".xlsx":
      "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    ".pptx":
      "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".gif": "image/gif",
    ".webp": "image/webp",
    ".avif": "image/avif",
    ".bmp": "image/bmp",
    ".svg": "image/svg+xml",
    ".mp4": "video/mp4",
    ".webm": "video/webm",
    ".mov": "video/quicktime",
    ".mp3": "audio/mpeg",
    ".wav": "audio/wav",
    ".m4a": "audio/mp4",
    ".ogg": "audio/ogg",
  }[path.extname(filename).toLowerCase()];
}

async function previewGeneratedFileBytes(reference, mimeType) {
  let snapshot;
  try {
    snapshot = await createRevealSnapshot(path.posix.basename(reference.path));
    const exported = await requestWorkspaceExport(
      WORKSPACE_FILE_EXPORT_PATH,
      workspaceFilePayload(reference),
      snapshot.filePath,
    );
    if (!exported?.ok) {
      return generatedFileFailure(
        safeWorkspaceReason(exported?.payload, "File preview is unavailable."),
        safeFailureCode(exported?.payload),
      );
    }
    const info = await fs.stat(snapshot.filePath);
    if (info.size > 100 * 1024 * 1024) {
      return generatedFileFailure(
        "This file exceeds the 100 MB preview limit. Export it to open it.",
        "too_large",
      );
    }
    const bytes = await fs.readFile(snapshot.filePath);
    return {
      ok: true,
      value: {
        filename: path.posix.basename(reference.path),
        path: reference.path,
        format: "binary",
        mimeType,
        contentBase64: bytes.toString("base64"),
        sizeBytes: bytes.length,
        truncated: false,
      },
    };
  } catch {
    return generatedFileFailure("Unable to load this file preview.");
  } finally {
    if (snapshot)
      await removeRevealSnapshot(snapshot.directory).catch(() => {});
  }
}

function sanitizeGeneratedFilePreview(value, expectedPath) {
  if (
    !isPlainObject(value) ||
    value.schemaVersion !== 1 ||
    value.path !== expectedPath ||
    value.filename !== path.posix.basename(expectedPath) ||
    !["markdown", "text"].includes(value.format) ||
    typeof value.content !== "string" ||
    Buffer.byteLength(value.content, "utf8") > 1 << 20 ||
    !Number.isSafeInteger(value.sizeBytes) ||
    value.sizeBytes < 0 ||
    typeof value.truncated !== "boolean"
  ) {
    return null;
  }
  return Object.freeze({
    content: value.content,
    filename: value.filename,
    format: value.format,
    path: value.path,
    sizeBytes: value.sizeBytes,
    truncated: value.truncated,
  });
}

function sanitizeWorkspaceAssetList(value, expectedScope) {
  if (
    !isPlainObject(value) ||
    value.schemaVersion !== 1 ||
    value.companyId !== expectedScope.companyId ||
    value.scope !== expectedScope.scope ||
    !Array.isArray(value.items) ||
    value.items.length > 100 ||
    !Number.isSafeInteger(value.totalCount) ||
    value.totalCount < 0 ||
    value.totalCount > 20_000 ||
    typeof value.truncated !== "boolean" ||
    !validWorkspaceAssetCursorResponse(value.nextCursor) ||
    !validWorkspaceAssetDate(value.latestModifiedAt, true)
  ) {
    return null;
  }
  const items = value.items.map(sanitizeWorkspaceAssetItem);
  if (items.some((item) => item === null)) return null;
  return Object.freeze({
    companyId: value.companyId,
    items: Object.freeze(items),
    latestModifiedAt: value.latestModifiedAt,
    nextCursor: value.nextCursor,
    scope: value.scope,
    totalCount: value.totalCount,
    truncated: value.truncated,
  });
}

function sanitizeWorkspaceAssetItem(value) {
  if (
    !isPlainObject(value) ||
    !canonicalWorkspacePath(value.path) ||
    value.filename !== path.posix.basename(value.path) ||
    typeof value.parentPath !== "string" ||
    value.parentPath !==
      (path.posix.dirname(value.path) === "."
        ? ""
        : path.posix.dirname(value.path)) ||
    !["archive", "document", "image", "media", "other"].includes(value.kind) ||
    typeof value.mimeType !== "string" ||
    value.mimeType.length < 1 ||
    value.mimeType.length > 160 ||
    !/^[\x20-\x7e]+$/u.test(value.mimeType) ||
    !Number.isSafeInteger(value.sizeBytes) ||
    value.sizeBytes < 0 ||
    !validWorkspaceAssetDate(value.modifiedAt, false) ||
    !["image", "markdown", "none", "text"].includes(value.previewKind)
  ) {
    return null;
  }
  return Object.freeze({
    filename: value.filename,
    kind: value.kind,
    mimeType: value.mimeType,
    modifiedAt: value.modifiedAt,
    parentPath: value.parentPath,
    path: value.path,
    previewKind: value.previewKind,
    sizeBytes: value.sizeBytes,
  });
}

function sanitizeWorkspaceAssetPreview(value, expectedPath) {
  if (
    !isPlainObject(value) ||
    value.schemaVersion !== 1 ||
    value.path !== expectedPath ||
    value.filename !== path.posix.basename(expectedPath) ||
    !["image", "markdown", "text"].includes(value.format) ||
    typeof value.mimeType !== "string" ||
    value.mimeType.length < 1 ||
    value.mimeType.length > 160 ||
    !Number.isSafeInteger(value.sizeBytes) ||
    value.sizeBytes < 0 ||
    typeof value.truncated !== "boolean"
  ) {
    return null;
  }
  if (value.format === "image") {
    if (
      !["image/gif", "image/jpeg", "image/png"].includes(value.mimeType) ||
      typeof value.contentBase64 !== "string" ||
      value.contentBase64.length > 5_592_408 ||
      !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(
        value.contentBase64,
      ) ||
      Buffer.from(value.contentBase64, "base64").byteLength > 4 << 20 ||
      value.truncated
    ) {
      return null;
    }
    return Object.freeze({
      contentBase64: value.contentBase64,
      filename: value.filename,
      format: value.format,
      mimeType: value.mimeType,
      path: value.path,
      sizeBytes: value.sizeBytes,
      truncated: false,
    });
  }
  if (
    typeof value.content !== "string" ||
    Buffer.byteLength(value.content, "utf8") > 1 << 20 ||
    (value.format === "markdown" && value.mimeType !== "text/markdown")
  ) {
    return null;
  }
  return Object.freeze({
    content: value.content,
    filename: value.filename,
    format: value.format,
    mimeType: value.mimeType,
    path: value.path,
    sizeBytes: value.sizeBytes,
    truncated: value.truncated,
  });
}

function sanitizeTaskReviewResponse(value, expectedRunID) {
  if (
    !isPlainObject(value) ||
    value.schemaVersion !== 1 ||
    value.runId !== expectedRunID ||
    !["ready", "conflict", "main_dirty", "merged"].includes(value.status) ||
    typeof value.baseRef !== "string" ||
    typeof value.headRef !== "string" ||
    typeof value.diff !== "string" ||
    Buffer.byteLength(value.diff, "utf8") > 4 << 20 ||
    typeof value.message !== "string" ||
    Buffer.byteLength(value.message, "utf8") > 1024
  ) {
    return null;
  }
  return Object.freeze({
    baseRef: value.baseRef,
    diff: value.diff,
    headRef: value.headRef,
    message: value.message,
    runId: value.runId,
    status: value.status,
  });
}

function safeWorkspaceReason(payload, fallback) {
  return isPlainObject(payload) &&
    typeof payload.reason === "string" &&
    payload.reason.length > 0 &&
    payload.reason.length <= 240
    ? payload.reason
    : fallback;
}

function generatedFileFailure(reason, code = "unavailable") {
  return { code, ok: false, reason };
}

function workspaceAssetFailure(reason, code = "unavailable") {
  return { code, ok: false, reason };
}

function taskReviewFailure(reason, code = "unavailable") {
  return { code, ok: false, reason };
}

function appPreviewFailure(reason, code = "unavailable") {
  return { code, ok: false, reason };
}

function isAppPreviewRequest(value) {
  return Boolean(
    hasOnlyKeys(value, [
      "companyId",
      "outputId",
      "runId",
      "runtimeId",
      "viewerId",
    ]) &&
    isCanonicalUuid(value.viewerId) &&
    isCanonicalUuid(value.companyId) &&
    isCanonicalUuid(value.outputId) &&
    isCanonicalUuid(value.runId) &&
    isCanonicalUuid(value.runtimeId),
  );
}

function isAppPreviewViewerRequest(value) {
  return Boolean(
    hasOnlyKeys(value, ["viewerId"]) && isCanonicalUuid(value.viewerId),
  );
}

function isGeneratedFileReference(value) {
  return Boolean(
    hasOnlyKeys(value, ["path", "runId"]) &&
    isCanonicalUuid(value.runId) &&
    canonicalWorkspacePath(value.path),
  );
}

function isGeneratedFileLinkReference(value) {
  return Boolean(
    hasOnlyKeys(value, ["href", "runId"]) &&
    isCanonicalUuid(value.runId) &&
    typeof value.href === "string" &&
    Buffer.byteLength(value.href, "utf8") <= 4096 &&
    (value.href.startsWith("/") || value.href.startsWith("file:")) &&
    !/[\u0000-\u001f\u007f]/u.test(value.href),
  );
}

function isWorkspaceAssetReference(value) {
  return Boolean(
    hasOnlyKeys(value, ["companyId", "path", "scope"]) &&
    isCanonicalUuid(value.companyId) &&
    typeof value.scope === "string" &&
    /^(?:company|projects\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|[a-z][a-z0-9-]{0,63})$/u.test(
      value.scope,
    ) &&
    canonicalWorkspacePath(value.path),
  );
}

function isWorkspaceAssetListRequest(value) {
  if (
    !isPlainObject(value) ||
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
    !isCanonicalUuid(value.companyId) ||
    !validWorkspaceAssetScope(value.scope) ||
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

function validWorkspaceAssetScope(value) {
  return (
    typeof value === "string" &&
    /^(?:company|projects\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|[a-z][a-z0-9-]{0,63})$/u.test(
      value,
    )
  );
}

function validWorkspaceAssetCursorResponse(value) {
  return (
    value === null ||
    (typeof value === "string" &&
      value.length > 0 &&
      value.length <= 4096 &&
      /^[A-Za-z0-9_-]+$/u.test(value))
  );
}

function validWorkspaceAssetDate(value, nullable) {
  return (
    (nullable && value === null) ||
    (typeof value === "string" &&
      value.length <= 64 &&
      Number.isFinite(Date.parse(value)))
  );
}

function isTaskReviewRequest(value) {
  if (
    hasOnlyKeys(value, ["action", "runId"]) &&
    value.action === "prepare" &&
    isCanonicalUuid(value.runId)
  ) {
    return true;
  }
  return Boolean(
    hasOnlyKeys(value, [
      "action",
      "companyId",
      "expectedExecutionGeneration",
      "expectedRevision",
      "runId",
      "runtimeId",
      "taskId",
    ]) &&
    value.action === "accept" &&
    isCanonicalUuid(value.companyId) &&
    isCanonicalUuid(value.taskId) &&
    isCanonicalUuid(value.runtimeId) &&
    isCanonicalUuid(value.runId) &&
    isRevision(value.expectedRevision) &&
    isRevision(value.expectedExecutionGeneration),
  );
}

function isRevision(value) {
  return Number.isSafeInteger(value) && value >= 1;
}

function canonicalWorkspacePath(value) {
  if (
    typeof value !== "string" ||
    value.length < 1 ||
    Buffer.byteLength(value, "utf8") > 4096 ||
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

function addRecentDocument(filePath) {
  if (typeof app.addRecentDocument !== "function") return;
  try {
    app.addRecentDocument(filePath);
  } catch {
    // Native file actions remain successful when recent documents are unavailable.
  }
}

function isPluginActionRequest(value) {
  return Boolean(
    hasOnlyKeys(value, [
      "schemaVersion",
      "companyId",
      "runtimeId",
      "provider",
      "nativeId",
    ]) &&
    value.schemaVersion === 1 &&
    isCanonicalUuid(value.companyId) &&
    isCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider) &&
    typeof value.nativeId === "string" &&
    Buffer.byteLength(value.nativeId, "utf8") <= 240 &&
    /^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(value.nativeId),
  );
}

function isConnectorActionRequest(value) {
  return Boolean(
    hasOnlyKeys(value, [
      "schemaVersion",
      "companyId",
      "runtimeId",
      "provider",
      "source",
      "nativeId",
    ]) &&
    value.schemaVersion === 1 &&
    isCanonicalUuid(value.companyId) &&
    isCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider) &&
    [
      "codex_app",
      "codex_mcp",
      "claude_plugin",
      "claude_mcp",
      "claude_cloud",
    ].includes(value.source) &&
    typeof value.nativeId === "string" &&
    Buffer.byteLength(value.nativeId, "utf8") <= 240 &&
    /^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(value.nativeId),
  );
}

function isPluginRefreshRequest(value) {
  const keys = ["schemaVersion", "companyId", "runtimeId", "provider"];
  return Boolean(
    (hasOnlyKeys(value, keys) ||
      (hasOnlyKeys(value, [...keys, "force"]) &&
        typeof value.force === "boolean")) &&
    value.schemaVersion === 1 &&
    isCanonicalUuid(value.companyId) &&
    isCanonicalUuid(value.runtimeId) &&
    ["codex", "claude"].includes(value.provider),
  );
}

// An install may also answer the provider's auth policy and the apps it
// still needs connected (sanitizePluginInstallAnswer).
const PLUGIN_OPERATION_KEYS = [
  "schemaVersion",
  "state",
  "continueUrl",
  "authPolicy",
  "appsNeedingAuth",
];

function sanitizePluginOperation(value) {
  const hasContinueUrl =
    isPlainObject(value) &&
    Object.prototype.hasOwnProperty.call(value, "continueUrl");
  const answer = sanitizePluginInstallAnswer(value);
  if (
    !answer ||
    Object.keys(value).some((key) => !PLUGIN_OPERATION_KEYS.includes(key)) ||
    value.schemaVersion !== 1 ||
    !["queued", "installing", "awaiting_user", "complete", "failed"].includes(
      value.state,
    ) ||
    (hasContinueUrl && value.state !== "awaiting_user")
  ) {
    return null;
  }
  const continueUrl = hasContinueUrl
    ? sanitizePluginContinueUrl(value.continueUrl)
    : undefined;
  if (hasContinueUrl && !continueUrl) return null;
  return Object.freeze({
    schemaVersion: 1,
    state: value.state,
    ...(continueUrl ? { continueUrl } : {}),
    ...answer,
  });
}

function sanitizePluginContinueUrl(value) {
  if (
    typeof value !== "string" ||
    value !== value.trim() ||
    Buffer.byteLength(value, "utf8") > 4096 ||
    !isSafePluginContinueUrl(value)
  ) {
    return null;
  }
  return new URL(value).href;
}

function safeFailureReason(payload, fallback) {
  return typeof payload?.reason === "string" && payload.reason.length <= 256
    ? payload.reason
    : fallback;
}

function safeFailureCode(payload, fallback = "unavailable") {
  return typeof payload?.code === "string" &&
    /^[a-z][a-z0-9_]{0,63}$/u.test(payload.code)
    ? payload.code
    : fallback;
}

function pluginFailure(reason, code) {
  return { code, ok: false, reason };
}

function hasOnlyKeys(value, keys) {
  return Boolean(
    isPlainObject(value) &&
    Object.keys(value).length === keys.length &&
    Object.keys(value).every((key) => keys.includes(key)),
  );
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isCanonicalUuid(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value,
    )
  );
}

async function stopAndClearDesktopSession() {
  toolAuthReturn.clear();
  const failures = [];
  try {
    await artifactFileCache?.clear();
  } catch (error) {
    failures.push(error);
  }
  try {
    await notifyAuthSessionChanged(false);
  } catch (error) {
    failures.push(error);
  }
  try {
    const cleared = await clearStoredAccessToken();
    if (!cleared?.ok) {
      failures.push(new Error("Desktop auth session could not be cleared"));
    }
  } catch (error) {
    failures.push(error);
  }
  if (failures.length === 1) {
    throw failures[0];
  }
  if (failures.length > 1) {
    throw new AggregateError(failures, "Desktop session cleanup failed");
  }
}

async function notifyAuthSessionChanged(authenticated) {
  await onAuthSessionChanged(authenticated);
  if (authenticated) artifactFileCache?.resume();
}

function getDesktopPlatform() {
  switch (process.platform) {
    case "darwin":
      return "macos";
    case "win32":
      return "windows";
    case "linux":
      return "linux";
    default:
      return "unknown";
  }
}

function getDesktopEnvironmentName() {
  return normalizeDesktopEnvironment(
    desktopEnv?.ZENITH_TEAM_DESKTOP_ENV,
    app.isPackaged ? "prod" : "local",
  );
}

module.exports = { registerIpcHandlers };
