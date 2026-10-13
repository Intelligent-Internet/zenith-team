const path = require("node:path");
const { app, ipcMain, session, webContents } = require("electron");
const { createDesktopRuntimeEnv } = require("./release-env.cjs");
const {
  getDesktopAppUserModelId,
  getDesktopVariant,
} = require("../shared/desktop-variant.cjs");

const DESKTOP_RUNTIME_ENV = createDesktopRuntimeEnv();
const DESKTOP_VARIANT = getDesktopVariant(DESKTOP_RUNTIME_ENV, {
  fallback: app.isPackaged ? "prod" : "local",
});
const APP_NAME = DESKTOP_VARIANT.appName;
const APP_ID = getDesktopAppUserModelId(DESKTOP_VARIANT);
const APP_ICON_PATH = getRuntimeIconPath();

const {
  BOARDLY_APP_INDEX_URL,
  registerCodexGeneratedImageProtocol,
  registerPackagedAppProtocol,
  registerPackagedAppProtocolScheme,
  registerPluginIconProtocol,
} = require("./app-protocol.cjs");
const {
  findDeepLinkUrl,
  registerDeepLinkProtocol,
  routeDeepLinkToWindow,
} = require("./deep-links.cjs");
const {
  createDesktopCrashReportingState,
  registerDesktopCrashReportingIpcHandlers,
  startDesktopCrashReporter,
} = require("./crash-reporter.cjs");
const { registerDiagnostics, writeMainLog } = require("./diagnostics.cjs");
const {
  createDesktopAgentDiscoverer,
  registerDesktopAgentDiscovery,
  resolveBoardlyMCPExecutable,
  resolveBoardlyExecutable,
} = require("./agent-discovery.cjs");
const {
  registerDesktopAgentVerification,
} = require("./agent-verification.cjs");
const { registerDownloadHandling } = require("./downloads.cjs");
const { registerIpcHandlers } = require("./ipc.cjs");
const { createAppMenu } = require("./menu.cjs");
const { registerNativeActions } = require("./native-actions.cjs");
const { registerNativeTheme } = require("./native-theme.cjs");
const { registerDesktopPowerLifecycle } = require("./power-lifecycle.cjs");
const { registerDesktopStartupSettings } = require("./startup-settings.cjs");
const {
  registerDesktopSupportDiagnostics,
} = require("./support-diagnostics.cjs");
const { registerDesktopWorkbench } = require("./workbench.cjs");
const { registerDesktopWebsitePreview } = require("./website-preview.cjs");
const { registerDesktopLinkPreview } = require("./link-preview.cjs");
const {
  registerScreenSharePicker,
  takeSelectedScreenShare,
} = require("./screen-share.cjs");
const { registerScreenShareOverlay } = require("./screen-share-overlay.cjs");
const { createMainWindow, isHuddleWebContents } = require("./windows.cjs");
const { getUserDataPath } = require("./app-data.cjs");
const {
  registerDesktopAutoUpdates,
  SHOW_UPDATE_DIALOG_CHANNEL,
} = require("./auto-updates.cjs");
const {
  createDesktopDaemonRuntime,
  fetchDaemonHealth,
  normalizeServerUrl,
} = require("./daemon-runtime.cjs");
const { registerDesktopDaemonPanel } = require("./daemon-panel.cjs");
const { createDesktopLocalPreview } = require("./local-preview.cjs");
const {
  ensureDaemonSession,
  getCredentialDirectoryPath,
  getSessionStorePath,
  readStoredSession,
  setSharedSessionInUseCheck,
} = require("./desktop-auth-store.cjs");
const {
  registerDesktopRealtimeRequestPolicy,
  resolveDesktopRendererApiUrl,
} = require("./realtime-request-policy.cjs");

let mainWindow = null;
let pendingDeepLinkUrl = findDeepLinkUrl(
  process.argv,
  DESKTOP_VARIANT.deepLinkProtocol,
);
let desktopAutoUpdater = null;
let desktopCrashReporterPolicy = null;
let desktopDaemonPanel = null;
let desktopDaemonRuntime = null;
let desktopDaemonShutdown = null;
let desktopDaemonShutdownComplete = false;
let desktopLocalPreview = null;
let disposeDesktopRealtimeRequestPolicy = null;
let isQuitting = false;
// Set while a quit waits for the main window's unload prompt to resolve.
let quitAfterMainWindowCloses = false;
// Set while a restart-to-update closes the main window before teardown; the
// resolver reports whether it closed (true) or the user chose Stay (false).
let closingMainWindowForUpdate = false;
let resolveMainWindowCloseForUpdate = null;

registerPackagedAppProtocolScheme();

app.setName(APP_NAME);
app.setAppUserModelId(APP_ID);
app.setPath(
  "userData",
  getUserDataPath(app.getPath("appData"), DESKTOP_RUNTIME_ENV),
);
desktopCrashReporterPolicy = startDesktopCrashReporter({
  app,
  env: DESKTOP_RUNTIME_ENV,
});

const hasSingleInstanceLock = app.requestSingleInstanceLock();

if (!hasSingleInstanceLock) {
  app.quit();
} else {
  app.on("second-instance", (_event, argv) => {
    const deepLinkUrl = findDeepLinkUrl(argv, DESKTOP_VARIANT.deepLinkProtocol);

    if (!mainWindow || mainWindow.isDestroyed()) {
      pendingDeepLinkUrl = deepLinkUrl;
      if (app.isReady()) {
        openMainWindow();
      }
      return;
    }

    revealMainWindow(mainWindow);
    handleDeepLinkUrl(deepLinkUrl);
  });

  app.on("open-url", (event, url) => {
    event.preventDefault();
    handleDeepLinkUrl(url);
  });

  app.whenReady().then(() => {
    configureAppIdentity();
    denyUnexpectedPermissions();
    // The first launch after the update (D31): a daemon of the previous
    // version still renews the session this app shared with it, so the app
    // only reads that session until the daemon is replaced.
    setSharedSessionInUseCheck(async () => {
      const health = await fetchDaemonHealth({
        healthPort: DESKTOP_VARIANT.daemonHealthPort,
      });
      return (
        health?.launchedBy === "desktop" &&
        health.cliVersion !== app.getVersion()
      );
    });
    registerDeepLinkProtocol(app, process, DESKTOP_VARIANT.deepLinkProtocol);
    registerPackagedAppProtocol({ app });
    registerCodexGeneratedImageProtocol();
    registerPluginIconProtocol({
      requestPluginIcon: (request) =>
        desktopLocalPreview?.requestPluginIcon(request) ??
        Promise.resolve(null),
    });
    disposeDesktopRealtimeRequestPolicy = registerDesktopRealtimeRequestPolicy(
      session.defaultSession.webRequest,
      resolveDesktopRendererApiUrl(DESKTOP_RUNTIME_ENV, app.isPackaged),
      { readStoredSession },
    );
    registerDownloadHandling();
    registerDesktopCrashReportingIpcHandlers({
      getState: () =>
        createDesktopCrashReportingState(desktopCrashReporterPolicy),
      ipcMain,
    });
    registerDesktopSupportDiagnostics({ ipcMain });
    const daemonResources = resolveDesktopDaemonResources();
    // Registered even without daemon resources so the Daemon card can say
    // "unavailable" instead of hanging on a missing handler.
    desktopDaemonPanel = registerDesktopDaemonPanel({
      boardlyHomeDirectory:
        daemonResources?.boardlyHomeDirectory ?? getCredentialDirectoryPath(),
      getDaemonRuntime: () => desktopDaemonRuntime,
      getMainWindow: () => mainWindow,
      ipcMain,
    });
    if (daemonResources) {
      desktopLocalPreview = createDesktopLocalPreview({
        boardlyHomeDirectory: daemonResources.boardlyHomeDirectory,
        onStateChange(state) {
          writeMainLog("desktop-local-preview-status", state);
        },
      });
      const discoverAgents = createDesktopAgentDiscoverer({
        app,
        boardlyHomeDirectory: daemonResources.boardlyHomeDirectory,
        environment: DESKTOP_RUNTIME_ENV,
        executablePath: daemonResources.binaryPath,
        healthPort: DESKTOP_VARIANT.daemonHealthPort,
        serverUrl: daemonResources.serverUrl,
      });
      registerDesktopAgentDiscovery({ discoverAgents, ipcMain });
      registerDesktopAgentVerification({ discoverAgents, ipcMain });
      startDesktopDaemonRuntime(daemonResources);
    }
    registerIpcHandlers({
      drainLocalRunPreview: () => desktopLocalPreview?.drain() ?? [],
      env: DESKTOP_RUNTIME_ENV,
      getMainWindow: () => mainWindow,
      requestDaemonExport: (pathname, payload, destinationPath) =>
        desktopLocalPreview?.exportFile(pathname, payload, destinationPath) ??
        Promise.resolve(null),
      requestDaemonOperation: (pathname, payload) =>
        desktopLocalPreview?.requestJSON(pathname, payload) ??
        Promise.resolve(null),
      onAuthSessionChanged: async (authenticated) => {
        if (!authenticated) {
          await desktopDaemonRuntime?.signedOut();
          return;
        }
        // The session is already installed. Daemon readiness must not block
        // entering the app; its startup state is shown by the Daemon card.
        void desktopDaemonRuntime?.authenticated().catch((error) => {
          writeMainLog("desktop-daemon-authentication-failed", {
            message: error?.message,
          });
        });
      },
    });
    registerDesktopWebsitePreview({ ipcMain, getMainWindow: () => mainWindow });
    registerDesktopLinkPreview({ ipcMain, getMainWindow: () => mainWindow });
    registerScreenSharePicker({ ipcMain });
    registerScreenShareOverlay({ ipcMain });
    registerNativeActions({
      getMainWindow: () => mainWindow,
      iconPath: APP_ICON_PATH,
    });
    registerNativeTheme({ getMainWindow: () => mainWindow });
    registerDesktopPowerLifecycle({ getMainWindow: () => mainWindow });
    registerDesktopStartupSettings();
    registerDesktopWorkbench({
      getMainWindow: () => mainWindow,
      iconPath: APP_ICON_PATH,
      openMainWindow,
    });
    desktopAutoUpdater = registerDesktopAutoUpdates({
      env: DESKTOP_RUNTIME_ENV,
      getMainWindow: () => mainWindow,
      onManualCheckResult: showDesktopUpdateResult,
      onBeforeQuitForUpdate: prepareForUpdateQuit,
      onUpdateDownloaded: showDesktopUpdateResult,
      onUpdateDownloadFailed: showDesktopUpdateResult,
    });
    registerDiagnostics({ env: DESKTOP_RUNTIME_ENV });
    openMainWindow();

    app.on("activate", () => {
      if (revealMainWindow(mainWindow)) {
        return;
      }

      openMainWindow();
    });
  });

  app.on("before-quit", (event) => {
    if (!isQuitting && mainWindow && !mainWindow.isDestroyed()) {
      // Close the window before tearing anything down, so a renderer
      // unsaved-changes prompt can still cancel the quit with Stay.
      event.preventDefault();
      quitAfterMainWindowCloses = true;
      mainWindow.close();
      return;
    }

    prepareForAppQuit();
    desktopDaemonPanel?.unregister();
    desktopDaemonPanel = null;
    const shutdown = disposeDesktopDaemonRuntime();

    if (!shutdown) {
      return;
    }

    event.preventDefault();
    void shutdown.finally(() => {
      // Let Electron finish cancelling this quit before starting another.
      setImmediate(() => app.quit());
    });
  });

  app.on("window-all-closed", () => {
    if (isQuitting || quitAfterMainWindowCloses || closingMainWindowForUpdate) {
      return;
    }

    if (process.platform !== "darwin") {
      app.quit();
    }
  });

  // Keep authenticating reconnects while before-quit defers daemon shutdown.
  app.on("will-quit", () => {
    disposeDesktopRealtimeRequestPolicy?.();
    disposeDesktopRealtimeRequestPolicy = null;
  });
}

function prepareForAppQuit() {
  isQuitting = true;
  desktopLocalPreview?.dispose();
  desktopLocalPreview = null;
  desktopAutoUpdater?.stopAutomaticChecks?.();
}

// Returns false when the user keeps unsaved changes with Stay: nothing has
// been torn down and the downloaded update stays ready to install.
async function prepareForUpdateQuit() {
  if (!isQuitting && !(await closeMainWindowForUpdate())) {
    return false;
  }

  prepareForAppQuit();
  closingMainWindowForUpdate = false;
  const shutdown = disposeDesktopDaemonRuntime();

  if (shutdown) {
    await shutdown;
  }
  return true;
}

// Electron's updater must own an uninterrupted quit, so the renderer's
// unsaved-changes prompt has to run before it starts.
function closeMainWindowForUpdate() {
  if (!mainWindow || mainWindow.isDestroyed()) {
    return Promise.resolve(true);
  }
  if (resolveMainWindowCloseForUpdate) {
    // A restart is already waiting on this window's prompt.
    return Promise.resolve(false);
  }

  return new Promise((resolve) => {
    closingMainWindowForUpdate = true;
    resolveMainWindowCloseForUpdate = resolve;
    mainWindow.close();
  });
}

function settleMainWindowCloseForUpdate(closed) {
  const resolve = resolveMainWindowCloseForUpdate;
  resolveMainWindowCloseForUpdate = null;
  if (!closed) {
    closingMainWindowForUpdate = false;
  }
  resolve?.(closed);
}

function disposeDesktopDaemonRuntime() {
  if (!desktopDaemonRuntime || desktopDaemonShutdownComplete) {
    return null;
  }

  if (!desktopDaemonShutdown) {
    desktopDaemonShutdown = desktopDaemonRuntime
      .dispose()
      .catch((error) => {
        writeMainLog("desktop-daemon-dispose-failed", {
          message: error?.message,
        });
      })
      .finally(() => {
        desktopDaemonRuntime = null;
        desktopDaemonShutdownComplete = true;
      });
  }

  return desktopDaemonShutdown;
}

function resolveDesktopDaemonResources() {
  const binaryPath = resolveBoardlyExecutable({
    app,
    env: DESKTOP_RUNTIME_ENV,
  });
  if (!binaryPath) {
    writeMainLog("desktop-daemon-unavailable", {
      reason: "binary_unavailable",
    });
    return null;
  }
  const boardlyMCPExecutable = resolveBoardlyMCPExecutable({
    app,
    env: DESKTOP_RUNTIME_ENV,
  });
  if (!boardlyMCPExecutable) {
    writeMainLog("desktop-daemon-unavailable", {
      reason: "boardly_mcp_unavailable",
    });
    return null;
  }
  return {
    binaryPath,
    boardlyHomeDirectory: getCredentialDirectoryPath(),
    boardlyMCPExecutable,
    serverUrl: resolveDesktopDaemonServerUrl(),
  };
}

function startDesktopDaemonRuntime(resources) {
  try {
    desktopDaemonRuntime = createDesktopDaemonRuntime({
      autoStart: true,
      autoStop: false,
      binaryPath: resources.binaryPath,
      boardlyHomeDirectory: resources.boardlyHomeDirectory,
      boardlyMCPExecutable: resources.boardlyMCPExecutable,
      desktopSessionPath: getSessionStorePath(),
      environment: DESKTOP_RUNTIME_ENV,
      expectedVersion: app.getVersion(),
      healthPort: DESKTOP_VARIANT.daemonHealthPort,
      onStatusChange(status) {
        writeMainLog("desktop-daemon-status", status);
        desktopDaemonPanel?.broadcastStatus(status);
        if (status.ready) {
          desktopLocalPreview?.start();
        } else {
          desktopLocalPreview?.stop();
        }
      },
      // The daemon's own session (D31), started from the app's sign-in.
      prepareLaunch: ensureDaemonSession,
      readStoredSession,
      serverUrl: resources.serverUrl,
    });
    void desktopDaemonRuntime.start().catch((error) => {
      writeMainLog("desktop-daemon-start-failed", {
        message: error?.message,
      });
    });
  } catch (error) {
    writeMainLog("desktop-daemon-unavailable", {
      message: error?.message,
      reason: "initialization_failed",
    });
  }
}

function resolveDesktopDaemonServerUrl() {
  const configured =
    DESKTOP_RUNTIME_ENV.BOARDLY_DESKTOP_API_BASE_URL ||
    DESKTOP_RUNTIME_ENV.VITE_API_URL ||
    DESKTOP_VARIANT.apiOrigin;
  return normalizeServerUrl(configured);
}

function handleDeepLinkUrl(url) {
  if (!url) {
    return;
  }

  if (
    routeDeepLinkToWindow(mainWindow, url, DESKTOP_VARIANT.deepLinkProtocol)
  ) {
    revealMainWindow(mainWindow);
    return;
  }

  if (!mainWindow || mainWindow.isDestroyed()) {
    pendingDeepLinkUrl = url;
    if (app.isReady()) {
      openMainWindow();
    }
  }
}

function flushPendingDeepLink() {
  const deepLinkUrl = pendingDeepLinkUrl;
  pendingDeepLinkUrl = null;
  handleDeepLinkUrl(deepLinkUrl);
}

function createTrackedMainWindow() {
  const nextWindow = createMainWindow({
    isQuitting: () =>
      isQuitting || quitAfterMainWindowCloses || closingMainWindowForUpdate,
    canCancelUnload: () => !isQuitting,
    onUnloadCancelled: () => {
      quitAfterMainWindowCloses = false;
      settleMainWindowCloseForUpdate(false);
    },
  });

  nextWindow.on("closed", () => {
    if (mainWindow === nextWindow) {
      mainWindow = null;
    }
    settleMainWindowCloseForUpdate(true);
    if (quitAfterMainWindowCloses) {
      setImmediate(() => app.quit());
    }
  });

  return nextWindow;
}

function openMainWindow() {
  if (revealMainWindow(mainWindow)) {
    return mainWindow;
  }

  mainWindow = createTrackedMainWindow();
  flushPendingDeepLink();
  createDesktopMenu();
  return mainWindow;
}

function revealMainWindow(window) {
  if (!window || window.isDestroyed?.()) {
    return false;
  }

  if (window.isMinimized?.()) {
    window.restore?.();
  }

  if (!window.isVisible?.()) {
    window.show?.();
  }

  window.focus?.();
  return true;
}

function createDesktopMenu() {
  createAppMenu(
    () => mainWindow,
    desktopAutoUpdater?.checkForUpdates
      ? {
          appName: APP_NAME,
          checkForUpdates: () => desktopAutoUpdater.checkForUpdates(),
          env: DESKTOP_RUNTIME_ENV,
          onNativeUpdateResult: showDesktopUpdateResult,
        }
      : {
          appName: APP_NAME,
          env: DESKTOP_RUNTIME_ENV,
        },
  );
}

function showDesktopUpdateResult() {
  const window = openMainWindow();
  const showDialog = () => {
    if (!window.isDestroyed()) {
      window.webContents.send(SHOW_UPDATE_DIALOG_CHANNEL);
    }
  };

  if (window.webContents.isLoadingMainFrame()) {
    window.webContents.once("did-finish-load", showDialog);
    return;
  }

  showDialog();
}

function denyUnexpectedPermissions() {
  session.defaultSession.setPermissionRequestHandler(
    (contents, permission, callback, details) => {
      const trustedFrame =
        isTrustedRenderer(contents) &&
        details?.isMainFrame === true &&
        details.requestingUrl === contents.getURL();
      callback(
        trustedFrame &&
          (permission === "media" ||
            (permission === "display-capture" &&
              isHuddleWebContents(contents))),
      );
    },
  );
  session.defaultSession.setPermissionCheckHandler(
    (contents, permission, _origin, details) =>
      isTrustedRenderer(contents) &&
      details?.isMainFrame === true &&
      details.requestingUrl === contents.getURL() &&
      (permission === "media" ||
        (permission === "display-capture" && isHuddleWebContents(contents))),
  );
  session.defaultSession.setDisplayMediaRequestHandler((request, callback) => {
    const frame = request.frame;
    const contents = webContents
      .getAllWebContents()
      .find((item) => item.mainFrame === frame);
    if (
      !frame ||
      !contents ||
      !isHuddleWebContents(contents) ||
      !isTrustedRenderer(contents) ||
      frame.parent ||
      frame.url !== contents.getURL() ||
      !isSameOrigin(request.securityOrigin, frame.origin) ||
      !request.videoRequested
    ) {
      callback(null);
      return;
    }
    // The Huddle window shows its own picker first and names the chosen
    // source; nothing is captured without that choice.
    const choice = takeSelectedScreenShare(contents);
    if (!choice) {
      callback(null);
      return;
    }
    callback(
      choice.audio && request.audioRequested
        ? { video: choice.source, audio: "loopback" }
        : { video: choice.source },
    );
  });
}

// Electron reports the request's security origin with a trailing slash
// ("http://host/"), while frame.origin has none.
function isSameOrigin(left, right) {
  const trim = (origin) =>
    typeof origin === "string" ? origin.replace(/\/$/, "") : "";
  return Boolean(trim(left)) && trim(left) === trim(right);
}

function isTrustedRenderer(webContents) {
  const url = webContents?.getURL?.();
  if (typeof url !== "string") return false;
  if (url.startsWith(BOARDLY_APP_INDEX_URL)) return true;
  const devUrl = process.env.BOARDLY_ELECTRON_DEV_SERVER_URL;
  return typeof devUrl === "string" && url.startsWith(devUrl);
}

function configureAppIdentity() {
  app.setName(APP_NAME);
  app.setAboutPanelOptions({
    applicationName: APP_NAME,
    applicationVersion: app.getVersion(),
    iconPath: APP_ICON_PATH,
  });
}

function getRuntimeIconPath(platform = process.platform) {
  if (platform === "darwin") {
    return path.join(__dirname, "..", "assets", "icons", "macos", "icon.icns");
  }

  if (platform === "win32") {
    return path.join(__dirname, "..", "assets", "icons", "windows", "icon.ico");
  }

  return path.join(
    __dirname,
    "..",
    "assets",
    "icons",
    "linux",
    "icons",
    "512x512.png",
  );
}
