const path = require("node:path");
const {
  createDesktopPowerSaveBlockerManager,
} = require("./power-save-blocker.cjs");

function registerDownloadHandling(options = {}) {
  const electron = resolveElectronOptions(options);
  const app = options.app ?? electron.app;
  const session = options.session ?? electron.session;
  const powerSaveBlocker =
    options.powerSaveBlocker ?? electron.powerSaveBlocker;
  const writeMainLog =
    options.writeMainLog ?? require("./diagnostics.cjs").writeMainLog;
  const powerSaveBlockers =
    options.powerSaveBlockerManager ??
    createDesktopPowerSaveBlockerManager({ powerSaveBlocker });

  session.defaultSession.on("will-download", (_event, item) => {
    const filename = sanitizeDownloadFilename(item.getFilename());
    const savePath = path.join(app.getPath("downloads"), filename);
    const blocker = powerSaveBlockers.start({ reason: "download" });

    item.setSavePath(savePath);
    writeMainLog("download-started", {
      filename,
      mimeType: item.getMimeType(),
      powerSaveBlocked: blocker.ok,
      totalBytes: item.getTotalBytes(),
    });

    item.once("done", (_doneEvent, state) => {
      const stopResult = blocker.ok
        ? powerSaveBlockers.stop(blocker.id)
        : { stopped: false };

      writeMainLog("download-finished", {
        filename,
        powerSaveStopped: stopResult.stopped,
        state,
        totalBytes: item.getTotalBytes(),
      });
    });
  });

  app?.on?.("will-quit", () => {
    powerSaveBlockers.dispose();
  });

  return {
    dispose: () => powerSaveBlockers.dispose(),
    powerSaveBlockers,
  };
}

function sanitizeDownloadFilename(filename) {
  const basename = path.basename(filename || "zenith-team-download");
  const sanitized = basename.replace(/[^\w .()[\]-]+/g, "_").trim();

  return sanitized || "zenith-team-download";
}

function resolveElectronOptions(options) {
  if (options.electron) {
    return options.electron;
  }

  if (options.app || options.session || options.powerSaveBlocker) {
    return options;
  }

  return require("electron");
}

module.exports = {
  registerDownloadHandling,
  sanitizeDownloadFilename,
};
