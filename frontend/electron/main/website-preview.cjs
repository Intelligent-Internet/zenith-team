"use strict";

const { isTrustedMainFrame } = require("./trusted-frame.cjs");
const { createIsolatedPreviewView } = require("./webcontents-view-policy.cjs");
const { sanitizeDownloadFilename } = require("./downloads.cjs");
const {
  websitePreviewChannels,
  isWebsitePreviewId,
  websitePreviewUrl,
  isWebsitePreviewBounds,
  isWebsitePreviewCommand,
} = require("../shared/website-preview.cjs");

function registerDesktopWebsitePreview({ ipcMain, getMainWindow }) {
  let preview = null;

  function closePreview() {
    const previous = preview;
    preview = null;
    previous?.dispose();
  }

  function ownsPreview(event, id) {
    return (
      isTrustedMainFrame(event, getMainWindow()) &&
      isWebsitePreviewId(id) &&
      preview?.id === id
    );
  }

  ipcMain.handle(websitePreviewChannels.open, (event, request) => {
    const window = getMainWindow();
    const url = websitePreviewUrl(request?.url);
    if (
      !isTrustedMainFrame(event, window) ||
      !isWebsitePreviewId(request?.id) ||
      !url
    ) {
      return { ok: false, reason: "Website preview request is invalid." };
    }
    closePreview();
    try {
      preview = createWebsitePreview({
        window,
        id: request.id,
        url,
        onDispose: closePreview,
      });
      return { ok: true, state: preview.getState() };
    } catch {
      return {
        ok: false,
        reason: "The website preview could not be opened. Try again.",
      };
    }
  });
  ipcMain.on(websitePreviewChannels.bounds, (event, request) => {
    if (
      ownsPreview(event, request?.id) &&
      (request.bounds === null || isWebsitePreviewBounds(request.bounds))
    ) {
      preview.setBounds(request.bounds);
    }
  });
  ipcMain.on(websitePreviewChannels.command, (event, request) => {
    if (
      ownsPreview(event, request?.id) &&
      isWebsitePreviewCommand(request.command)
    )
      preview.command(request.command);
  });
  ipcMain.on(websitePreviewChannels.close, (event, id) => {
    if (ownsPreview(event, id)) closePreview();
  });
  return { dispose: closePreview };
}

function createWebsitePreview({ window, id, url, onDispose }) {
  const view = createIsolatedPreviewView(
    `website-preview:${window.webContents.id}:${id}`,
  );
  const contents = view.webContents;
  const session = contents.session;
  let disposed = false;
  let bounds = null;
  let failure = null;
  let currentUrl = url;
  let loading = true;

  session.setPermissionCheckHandler(() => false);
  session.setPermissionRequestHandler((_contents, _permission, callback) =>
    callback(false),
  );
  session.setDevicePermissionHandler(() => false);
  const onDownload = (_event, item) => {
    item.setSaveDialogOptions({
      title: "Save website download",
      defaultPath: sanitizeDownloadFilename(item.getFilename()),
    });
  };
  session.on("will-download", onDownload);
  view.setVisible(false);
  view.setBackgroundColor("#ffffff");
  window.contentView.addChildView(view);

  function getState() {
    return {
      id,
      url: currentUrl,
      title: contents.isDestroyed() ? "" : contents.getTitle().slice(0, 512),
      loading,
      error: failure,
      canGoBack:
        !contents.isDestroyed() && contents.navigationHistory.canGoBack(),
      canGoForward:
        !contents.isDestroyed() && contents.navigationHistory.canGoForward(),
    };
  }

  function emitState() {
    if (!disposed && !window.webContents.isDestroyed()) {
      window.webContents.send(websitePreviewChannels.event, {
        type: "state",
        state: getState(),
      });
    }
  }

  function fail(message) {
    if (disposed) return;
    failure = message;
    loading = false;
    view.setVisible(false);
    emitState();
  }

  function load(nextUrl) {
    if (disposed) return;
    currentUrl = nextUrl;
    failure = null;
    loading = true;
    emitState();
    void contents.loadURL(nextUrl).catch((error) => {
      if (error?.code !== "ERR_ABORTED")
        fail(
          "This website could not be loaded. Reload or open it in your browser.",
        );
    });
  }

  function applyBounds() {
    if (disposed || window.isDestroyed()) return;
    if (!bounds || failure) {
      view.setVisible(false);
      return;
    }
    const zoom = window.webContents.getZoomFactor();
    const [width, height] = window.getContentSize();
    const x = Math.max(0, Math.min(width, Math.round(bounds.x * zoom)));
    const y = Math.max(0, Math.min(height, Math.round(bounds.y * zoom)));
    const right = Math.max(
      x,
      Math.min(width, Math.round((bounds.x + bounds.width) * zoom)),
    );
    const bottom = Math.max(
      y,
      Math.min(height, Math.round((bounds.y + bounds.height) * zoom)),
    );
    view.setBounds({ x, y, width: right - x, height: bottom - y });
    view.setVisible(right > x && bottom > y);
  }

  const guardNavigation = (event, targetUrl) => {
    if (!websitePreviewUrl(targetUrl)) event.preventDefault();
  };
  contents.on("will-navigate", guardNavigation);
  contents.on("will-redirect", guardNavigation);
  contents.setWindowOpenHandler(({ url: targetUrl }) => {
    const destination = websitePreviewUrl(targetUrl);
    if (destination) queueMicrotask(() => load(destination));
    return { action: "deny" };
  });
  contents.on(
    "did-start-navigation",
    (_event, targetUrl, isInPlace, isMainFrame) => {
      if (!isMainFrame || isInPlace) return;
      const destination = websitePreviewUrl(targetUrl);
      if (destination) currentUrl = destination;
      failure = null;
      loading = true;
      applyBounds();
      emitState();
    },
  );
  contents.on("did-navigate", (_event, targetUrl) => {
    currentUrl = websitePreviewUrl(targetUrl) ?? currentUrl;
    emitState();
  });
  contents.on("did-navigate-in-page", (_event, targetUrl, isMainFrame) => {
    if (isMainFrame) {
      currentUrl = websitePreviewUrl(targetUrl) ?? currentUrl;
      emitState();
    }
  });
  contents.on("did-stop-loading", () => {
    loading = false;
    emitState();
  });
  contents.on("page-title-updated", emitState);
  contents.on(
    "did-fail-load",
    (_event, code, _description, _url, isMainFrame) => {
      if (isMainFrame && code !== -3)
        fail(
          "This website could not be loaded. Reload or open it in your browser.",
        );
    },
  );
  contents.on("render-process-gone", () =>
    fail("The website stopped responding. Reload to try again."),
  );
  contents.on("before-input-event", (event, input) => {
    if (
      input.type === "keyDown" &&
      input.key === "Escape" &&
      !input.isAutoRepeat
    ) {
      event.preventDefault();
      window.webContents.focus();
      window.webContents.send(websitePreviewChannels.event, {
        type: "escape",
        id,
      });
    }
  });

  const onWindowNavigation = (_event, _url, isInPlace, isMainFrame) => {
    if (isMainFrame && !isInPlace) onDispose();
  };
  window.on("closed", onDispose);
  window.on("resize", applyBounds);
  window.webContents.on("did-start-navigation", onWindowNavigation);
  window.webContents.on("render-process-gone", onDispose);
  load(url);

  return {
    id,
    getState,
    setBounds(nextBounds) {
      bounds = nextBounds;
      applyBounds();
    },
    command(command) {
      if (disposed || contents.isDestroyed()) return;
      const history = contents.navigationHistory;
      if (command === "back" && history.canGoBack()) history.goBack();
      if (command === "forward" && history.canGoForward()) history.goForward();
      if (command === "reload") load(currentUrl);
      if (command === "focus") contents.focus();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      window.removeListener("closed", onDispose);
      window.removeListener("resize", applyBounds);
      window.webContents.removeListener(
        "did-start-navigation",
        onWindowNavigation,
      );
      window.webContents.removeListener("render-process-gone", onDispose);
      session.removeListener("will-download", onDownload);
      if (!window.isDestroyed()) window.contentView.removeChildView(view);
      if (!contents.isDestroyed())
        contents.close({ waitForBeforeUnload: false });
      void session.clearStorageData().catch(() => {});
    },
  };
}

module.exports = { registerDesktopWebsitePreview };
