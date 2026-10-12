const fs = require("node:fs");
const path = require("node:path");
const { app, clipboard, ipcMain } = require("electron");
const {
  redactDiagnosticsValue,
} = require("../shared/diagnostics-redaction.cjs");
const {
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");

const RENDERER_DIAGNOSTIC_CHANNEL = "desktop:renderer-diagnostic";
const LOG_FILENAME = "boardly-main.log";
let desktopEnv = process.env;

function registerDiagnostics(options = {}) {
  if (options.env && typeof options.env === "object") {
    desktopEnv = options.env;
  }

  ipcMain.on(RENDERER_DIAGNOSTIC_CHANNEL, (_event, payload) => {
    writeMainLog("renderer-diagnostic", normalizeRendererDiagnostic(payload));
  });

  writeMainLog("app-ready", createDiagnosticsSummary());
}

function attachWindowDiagnostics(mainWindow) {
  mainWindow.on("unresponsive", () => {
    writeMainLog("window-unresponsive");
  });

  mainWindow.webContents.on(
    "did-fail-load",
    (_event, errorCode, errorDescription, validatedURL, isMainFrame) => {
      writeMainLog("renderer-did-fail-load", {
        errorCode,
        errorDescription,
        isMainFrame,
        url: validatedURL,
      });
    },
  );

  mainWindow.webContents.on("render-process-gone", (_event, details) => {
    writeMainLog("renderer-process-gone", details);
  });

  mainWindow.webContents.on("console-message", (details) => {
    if (details.level !== "warning" && details.level !== "error") {
      return;
    }

    writeMainLog("renderer-console", {
      level: details.level === "error" ? 3 : 2,
      line: details.lineNumber,
      message: details.message,
      sourceId: details.sourceId,
    });
  });
}

function copyDiagnosticsSummaryToClipboard() {
  clipboard.writeText(createDiagnosticsReport());
}

function createDiagnosticsReport() {
  const summary = createDiagnosticsSummary();

  return [
    "Zenith Team Desktop Diagnostics",
    `App version: ${summary.appVersion}`,
    `Environment: ${summary.environment}`,
    `Platform: ${summary.platform}`,
    `Arch: ${summary.arch}`,
    `Electron: ${summary.electronVersion}`,
    `Chrome: ${summary.chromeVersion}`,
    `Node: ${summary.nodeVersion}`,
    `User data: ${summary.userDataPath}`,
    `Log file: ${summary.logFilePath}`,
    `Crash dumps: ${summary.crashDumpsPath}`,
  ].join("\n");
}

function createDiagnosticsSummary() {
  return {
    appVersion: app.getVersion(),
    arch: process.arch,
    chromeVersion: process.versions.chrome,
    crashDumpsPath: safeGetPath("crashDumps"),
    electronVersion: process.versions.electron,
    environment: getDesktopEnvironmentName(),
    logFilePath: getLogFilePath(),
    nodeVersion: process.versions.node,
    platform: process.platform,
    userDataPath: app.getPath("userData"),
  };
}

function writeMainLog(event, details = {}) {
  const logEntry = {
    details: redactDiagnosticsValue(details),
    event,
    timestamp: new Date().toISOString(),
  };

  try {
    fs.mkdirSync(getLogDirectory(), { recursive: true });
    fs.appendFileSync(
      getLogFilePath(),
      `${JSON.stringify(logEntry)}\n`,
      "utf8",
    );
  } catch {
    // Diagnostics must never break app startup or user workflows.
  }
}

function normalizeRendererDiagnostic(payload) {
  if (!payload || typeof payload !== "object") {
    return {
      kind: "unknown",
      message: "Invalid renderer diagnostic payload.",
    };
  }

  return {
    kind: readString(payload.kind, "unknown"),
    line: readNumber(payload.line),
    message: readString(payload.message, "Renderer diagnostic."),
    source: readString(payload.source, "unknown"),
  };
}

function readString(value, fallback) {
  return typeof value === "string" && value.trim() ? value : fallback;
}

function readNumber(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function getDesktopEnvironmentName(env = desktopEnv) {
  return normalizeDesktopEnvironment(
    env?.BOARDLY_DESKTOP_ENV,
    app.isPackaged ? "prod" : "local",
  );
}

function safeGetPath(name) {
  try {
    return app.getPath(name);
  } catch {
    return "Unavailable";
  }
}

function getLogDirectory() {
  return path.join(app.getPath("userData"), "logs");
}

function getLogFilePath() {
  return path.join(getLogDirectory(), LOG_FILENAME);
}

module.exports = {
  RENDERER_DIAGNOSTIC_CHANNEL,
  attachWindowDiagnostics,
  copyDiagnosticsSummaryToClipboard,
  createDiagnosticsReport,
  createDiagnosticsSummary,
  getLogFilePath,
  registerDiagnostics,
  writeMainLog,
};
