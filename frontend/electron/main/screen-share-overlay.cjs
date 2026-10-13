const path = require("node:path");
const { app, BrowserWindow, screen } = require("electron");
const { isTrustedHuddleFrame } = require("./windows.cjs");
const { grantedScreenShareSource } = require("./screen-share.cjs");

const STATE_CHANNEL = "desktop:screen-share-state";
const HUDDLE_ACTION_CHANNEL = "desktop:screen-share-action";
const TOOLBAR_ACTION_CHANNEL = "desktop:screen-share-toolbar-action";
const TOOLBAR_STATE_CHANNEL = "desktop:screen-share-toolbar-state";
const TOOLBAR_ACTIONS = new Set(["mute", "camera", "show-call", "stop"]);
const TOOLBAR_SIZE = { width: 404, height: 50 };
const OVERLAY_DIRECTORY = path.join(app.getAppPath(), "electron", "overlay");

// While a Huddle window shares its screen, a green border marks the shared
// display and a floating toolbar keeps the microphone, camera and Stop
// sharing within reach from any app, like Slack. Both are excluded from the
// capture itself. One share runs at a time.
let overlay = null;

function registerScreenShareOverlay({ ipcMain }) {
  ipcMain.handle(STATE_CHANNEL, (event, state) => {
    if (!isTrustedHuddleFrame(event)) {
      return { ok: false, reason: "Screen sharing is not available here." };
    }
    const huddle = event.sender;
    if (state?.sharing !== true) {
      if (overlay?.huddle === huddle) closeOverlay();
      return { ok: true };
    }
    if (overlay?.huddle !== huddle) {
      closeOverlay();
      openOverlay(huddle);
    }
    overlay.state = {
      muted: state.muted === true,
      camera: state.camera === true,
    };
    sendToolbarState();
    return { ok: true };
  });

  ipcMain.on(TOOLBAR_ACTION_CHANNEL, (event, action) => {
    if (
      !overlay ||
      overlay.toolbar.isDestroyed() ||
      event.sender !== overlay.toolbar.webContents ||
      !TOOLBAR_ACTIONS.has(action)
    ) {
      return;
    }
    if (action === "show-call") {
      const window = BrowserWindow.fromWebContents(overlay.huddle);
      if (window && !window.isDestroyed()) {
        if (window.isMinimized()) window.restore();
        window.show();
        window.focus();
      }
      return;
    }
    if (!overlay.huddle.isDestroyed()) {
      overlay.huddle.send(HUDDLE_ACTION_CHANNEL, action);
    }
  });
}

function openOverlay(huddle) {
  const source = grantedScreenShareSource(huddle);
  const display = displayFor(source, huddle);
  // A shared window moves with the person; only a whole screen is outlined.
  const border = source?.id.startsWith("screen:")
    ? createBorderWindow(display)
    : null;
  const toolbar = createToolbarWindow(display);
  const onDestroyed = () => {
    if (overlay?.huddle === huddle) closeOverlay();
  };
  huddle.once("destroyed", onDestroyed);
  overlay = {
    huddle,
    border,
    toolbar,
    onDestroyed,
    state: { muted: false, camera: false },
  };
  toolbar.webContents.once("did-finish-load", sendToolbarState);
}

function closeOverlay() {
  if (!overlay) return;
  const { huddle, border, toolbar, onDestroyed } = overlay;
  overlay = null;
  if (!huddle.isDestroyed()) huddle.removeListener("destroyed", onDestroyed);
  for (const window of [border, toolbar]) {
    if (window && !window.isDestroyed()) window.destroy();
  }
}

function sendToolbarState() {
  if (!overlay || overlay.toolbar.isDestroyed()) return;
  overlay.toolbar.webContents.send(TOOLBAR_STATE_CHANNEL, overlay.state);
}

function displayFor(source, huddle) {
  const displays = screen.getAllDisplays();
  const shared =
    source?.display_id &&
    displays.find((display) => String(display.id) === source.display_id);
  if (shared) return shared;
  const window = BrowserWindow.fromWebContents(huddle);
  return window && !window.isDestroyed()
    ? screen.getDisplayMatching(window.getBounds())
    : screen.getPrimaryDisplay();
}

function createOverlayWindow(bounds, options) {
  const window = new BrowserWindow({
    ...bounds,
    frame: false,
    transparent: true,
    hasShadow: false,
    resizable: false,
    minimizable: false,
    maximizable: false,
    fullscreenable: false,
    skipTaskbar: true,
    show: false,
    alwaysOnTop: true,
    ...options,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      ...options.webPreferences,
    },
  });
  window.setAlwaysOnTop(true, "screen-saver");
  window.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
  // Keeps the border and toolbar out of the stream everyone else sees.
  window.setContentProtection(true);
  window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  window.webContents.on("will-navigate", (event) => event.preventDefault());
  window.once("ready-to-show", () => window.showInactive());
  return window;
}

function createBorderWindow(display) {
  const window = createOverlayWindow(display.bounds, {
    focusable: false,
    movable: false,
    enableLargerThanScreen: true,
    webPreferences: { javascript: false },
  });
  window.setIgnoreMouseEvents(true);
  void window.loadFile(path.join(OVERLAY_DIRECTORY, "share-border.html"));
  return window;
}

function createToolbarWindow(display) {
  const { workArea } = display;
  const window = createOverlayWindow(
    {
      x: Math.round(workArea.x + (workArea.width - TOOLBAR_SIZE.width) / 2),
      y: workArea.y + 12,
      ...TOOLBAR_SIZE,
    },
    {
      title: "Screen sharing",
      // A click on the toolbar acts at once, even while another app is active.
      acceptFirstMouse: true,
      webPreferences: {
        preload: path.join(
          app.getAppPath(),
          "electron",
          "preload",
          "share-toolbar.cjs",
        ),
      },
    },
  );
  void window.loadFile(path.join(OVERLAY_DIRECTORY, "share-toolbar.html"));
  return window;
}

module.exports = { registerScreenShareOverlay };
