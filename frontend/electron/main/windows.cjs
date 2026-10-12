const path = require("node:path");
const { app, BrowserWindow, dialog } = require("electron");
const { BOARDLY_APP_INDEX_URL } = require("./app-protocol.cjs");
const { attachWindowDiagnostics } = require("./diagnostics.cjs");
const { isAppNavigationUrl, openExternalUrl } = require("./external-links.cjs");
const { isTrustedMainFrame } = require("./trusted-frame.cjs");
const { restoreWindowState, saveWindowState } = require("./window-state.cjs");

const MIN_WINDOW_WIDTH = 1180;
const MIN_WINDOW_HEIGHT = 760;
const APP_ICON_PATH = getRuntimeIconPath();
const huddleWindows = new Map();

function createMainWindow(options = {}) {
  const windowState = restoreWindowState();
  const mainWindow = new BrowserWindow({
    ...windowState,
    ...getNativeTitleBarOptions(process.platform),
    minWidth: MIN_WINDOW_WIDTH,
    minHeight: MIN_WINDOW_HEIGHT,
    backgroundColor: "#e8e5dd",
    icon: APP_ICON_PATH,
    show: false,
    title: "Zenith Team",
    webPreferences: {
      preload: path.join(
        app.getAppPath(),
        "electron",
        "generated",
        "preload.cjs",
      ),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
    },
  });
  const rendererUrl = getRendererUrl();

  attachWindowDiagnostics(mainWindow);

  mainWindow.once("ready-to-show", () => {
    mainWindow.show();
  });

  mainWindow.on("close", (event) => {
    handleMainWindowClose(event, mainWindow, {
      isQuitting: options.isQuitting,
    });
  });

  // A renderer beforeunload guard (for example an unsaved Wiki draft) vetoes
  // the unload silently unless the main process asks the user.
  mainWindow.webContents.on("will-prevent-unload", (event) => {
    handleMainWindowUnloadPrompt(event, mainWindow, {
      canStay: options.canCancelUnload,
      onStay: options.onUnloadCancelled,
    });
  });

  attachAppNavigationGuard(mainWindow.webContents, rendererUrl);

  void mainWindow.loadURL(rendererUrl);

  return mainWindow;
}

function createHuddleWindow(huddleId) {
  return openHuddleWindow(huddleId, `/huddles/${encodeURIComponent(huddleId)}`);
}

// createHuddleStartWindow opens the lobby for a new call in a conversation. The
// Huddle itself is created only when the person presses Start in it.
function createHuddleStartWindow(conversationId) {
  return openHuddleWindow(
    `start:${conversationId}`,
    `/huddles/start/${encodeURIComponent(conversationId)}`,
  );
}

function openHuddleWindow(key, routePath) {
  const existing = huddleWindows.get(key);
  if (existing && !existing.isDestroyed()) {
    existing.show();
    existing.focus();
    return existing;
  }
  const huddleWindow = new BrowserWindow({
    width: 1120,
    height: 780,
    minWidth: 760,
    minHeight: 560,
    backgroundColor: "#f3f2ed",
    title: "Zenith Team Huddle",
    webPreferences: {
      preload: path.join(
        app.getAppPath(),
        "electron",
        "generated",
        "preload.cjs",
      ),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
    },
  });
  huddleWindows.set(key, huddleWindow);
  huddleWindow.on("closed", () => {
    if (huddleWindows.get(key) === huddleWindow) {
      huddleWindows.delete(key);
    }
  });
  const rendererUrl = new URL(getRendererUrl());
  rendererUrl.hash = routePath;
  attachAppNavigationGuard(huddleWindow.webContents, getRendererUrl());
  void huddleWindow.loadURL(rendererUrl.toString());
  return huddleWindow;
}

// attachAppNavigationGuard keeps a window that carries the privileged preload
// on the app's own origin: popups never open in-app, and any top-level
// navigation or redirect away from the app is cancelled and handed to the OS
// browser instead, so a foreign page can never reach the desktop bridge.
function attachAppNavigationGuard(webContents, rendererUrl) {
  webContents.setWindowOpenHandler(({ url }) => {
    if (!isAppNavigationUrl(url, rendererUrl)) void openExternalUrl(url);
    return { action: "deny" };
  });

  const blockForeignNavigation = (event, url) => {
    if (isAppNavigationUrl(url, rendererUrl)) return;
    event.preventDefault();
    void openExternalUrl(url);
  };
  webContents.on("will-navigate", blockForeignNavigation);
  webContents.on("will-redirect", blockForeignNavigation);
}

function isHuddleWebContents(webContents) {
  return [...huddleWindows.values()].some(
    (window) => !window.isDestroyed() && window.webContents === webContents,
  );
}

// isTrustedHuddleFrame is isTrustedMainFrame for Huddle windows: the sender
// must be the main frame of one of our Huddle windows and still be showing the
// app itself, never a page the window was navigated to.
function isTrustedHuddleFrame(event) {
  const window = [...huddleWindows.values()].find(
    (item) => !item.isDestroyed() && item.webContents === event?.sender,
  );
  return Boolean(
    window &&
    isTrustedMainFrame(event, window) &&
    isAppNavigationUrl(event.senderFrame.url, getRendererUrl()),
  );
}

function getRendererUrl() {
  if (!app.isPackaged && process.env.BOARDLY_ELECTRON_DEV_SERVER_URL) {
    return process.env.BOARDLY_ELECTRON_DEV_SERVER_URL;
  }

  return BOARDLY_APP_INDEX_URL;
}

module.exports = {
  createHuddleStartWindow,
  createHuddleWindow,
  isHuddleWebContents,
  isTrustedHuddleFrame,
  createMainWindow,
  getNativeTitleBarOptions,
  getRendererUrl,
  handleMainWindowClose,
  handleMainWindowUnloadPrompt,
};

function getNativeTitleBarOptions(platform = process.platform) {
  if (platform !== "darwin") {
    return {};
  }

  return {
    trafficLightPosition: { x: 18, y: 14 },
    titleBarStyle: "hiddenInset",
  };
}

function handleMainWindowClose(
  event,
  mainWindow,
  { isQuitting = false, saveState = saveWindowState } = {},
) {
  saveState(mainWindow);

  const shouldQuit =
    typeof isQuitting === "function"
      ? Boolean(isQuitting())
      : Boolean(isQuitting);

  if (shouldQuit) {
    return { action: "close" };
  }

  event?.preventDefault?.();
  mainWindow?.hide?.();
  return { action: "hide" };
}

function handleMainWindowUnloadPrompt(
  event,
  mainWindow,
  {
    canStay = () => true,
    onStay = () => undefined,
    showMessageBox = (...args) => dialog.showMessageBoxSync(...args),
  } = {},
) {
  // Once quit teardown has started it cannot be undone, so staying would
  // leave a half-shut-down app; let the unload proceed.
  if (!canStay()) {
    event.preventDefault();
    return { action: "leave" };
  }

  if (!mainWindow.isVisible()) {
    mainWindow.show();
  }

  const choice = showMessageBox(mainWindow, {
    type: "question",
    buttons: ["Leave", "Stay"],
    defaultId: 1,
    cancelId: 1,
    noLink: true,
    title: "Leave this page?",
    message: "Leave this page?",
    detail: "Changes you made may not be saved.",
  });

  if (choice === 0) {
    // Calling preventDefault on will-prevent-unload ignores the veto.
    event.preventDefault();
    return { action: "leave" };
  }

  onStay();
  return { action: "stay" };
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
