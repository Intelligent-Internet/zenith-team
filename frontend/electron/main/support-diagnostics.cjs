const fs = require("node:fs");
const path = require("node:path");
const { writeMainLog: defaultWriteMainLog } = require("./diagnostics.cjs");

const CAPTURE_SUPPORT_DIAGNOSTICS_CHANNEL =
  "desktop:capture-support-diagnostics";
const DEFAULT_SUPPORT_DIAGNOSTICS_DURATION_MS = 10_000;
const MIN_SUPPORT_DIAGNOSTICS_DURATION_MS = 1_000;
const MAX_SUPPORT_DIAGNOSTICS_DURATION_MS = 30_000;
const NET_LOG_MAX_FILE_SIZE_BYTES = 5 * 1024 * 1024;
const NET_LOG_CAPTURE_MODE = "default";
const TRACE_CATEGORIES = ["electron", "browser"];
const TRACE_OPTIONS = {
  excluded_categories: ["disabled-by-default-*"],
  included_categories: TRACE_CATEGORIES,
};

let activeCapture = null;
let registeredSupportDiagnosticsIpc = false;

async function captureDesktopSupportDiagnostics(options = {}) {
  if (activeCapture) {
    return {
      ok: false,
      reason: "Support diagnostics capture is already running.",
    };
  }

  const capturePromise = runDesktopSupportDiagnosticsCapture(options);
  activeCapture = capturePromise;

  try {
    return await capturePromise;
  } finally {
    if (activeCapture === capturePromise) {
      activeCapture = null;
    }
  }
}

async function runDesktopSupportDiagnosticsCapture(options) {
  const electron = options.electron ?? require("electron");
  const app = options.app ?? electron.app;
  const contentTracing = options.contentTracing ?? electron.contentTracing;
  const netLog = options.netLog ?? electron.netLog;
  const writeMainLog = options.writeMainLog ?? defaultWriteMainLog;
  const sleep = options.sleep ?? delay;
  const durationMs = normalizeSupportDiagnosticsDurationMs(options.durationMs);
  const timestamp = createSafeTimestamp(options.now?.() ?? new Date());
  const diagnosticsDirectory =
    options.diagnosticsDirectory ?? getSupportDiagnosticsDirectory(app);
  const netLogPath = path.join(
    diagnosticsDirectory,
    `boardly-support-netlog-${timestamp}.json`,
  );
  const tracePath = path.join(
    diagnosticsDirectory,
    `boardly-support-trace-${timestamp}.json`,
  );

  if (netLog.currentlyLogging) {
    return {
      ok: false,
      reason: "Network logging is already running.",
    };
  }

  fs.mkdirSync(diagnosticsDirectory, { recursive: true });

  let netLogStarted = false;
  let traceStarted = false;

  try {
    await netLog.startLogging(netLogPath, {
      captureMode: NET_LOG_CAPTURE_MODE,
      maxFileSize: NET_LOG_MAX_FILE_SIZE_BYTES,
    });
    netLogStarted = true;

    await contentTracing.startRecording(TRACE_OPTIONS);
    traceStarted = true;

    await sleep(durationMs);

    const recordedTracePath = await contentTracing.stopRecording(tracePath);
    traceStarted = false;
    await netLog.stopLogging();
    netLogStarted = false;

    const result = {
      captureMode: NET_LOG_CAPTURE_MODE,
      durationMs,
      files: {
        netLogPath,
        tracePath: recordedTracePath || tracePath,
      },
      ok: true,
      traceCategories: TRACE_CATEGORIES,
    };

    writeMainLog("support-diagnostics-captured", {
      captureMode: result.captureMode,
      durationMs: result.durationMs,
      netLogPath: result.files.netLogPath,
      tracePath: result.files.tracePath,
      traceCategories: result.traceCategories,
    });

    return result;
  } catch (error) {
    if (traceStarted) {
      await stopTracingQuietly(contentTracing, tracePath);
    }

    if (netLogStarted) {
      await stopNetLogQuietly(netLog);
    }

    const reason = getErrorMessage(error);
    writeMainLog("support-diagnostics-capture-failed", { reason });
    return {
      ok: false,
      reason,
    };
  }
}

function registerDesktopSupportDiagnostics(options = {}) {
  const electron = options.electron ?? require("electron");
  const ipcMain = options.ipcMain ?? electron.ipcMain;

  if (!ipcMain || typeof ipcMain.handle !== "function") {
    return;
  }

  if (registeredSupportDiagnosticsIpc) {
    return;
  }

  registeredSupportDiagnosticsIpc = true;
  ipcMain.handle(CAPTURE_SUPPORT_DIAGNOSTICS_CHANNEL, (_event, payload) =>
    captureDesktopSupportDiagnostics({
      ...options,
      durationMs: readDurationMs(payload),
      ipcMain: undefined,
    }),
  );
}

function normalizeSupportDiagnosticsDurationMs(value) {
  if (!Number.isFinite(value)) {
    return DEFAULT_SUPPORT_DIAGNOSTICS_DURATION_MS;
  }

  return Math.min(
    MAX_SUPPORT_DIAGNOSTICS_DURATION_MS,
    Math.max(MIN_SUPPORT_DIAGNOSTICS_DURATION_MS, Math.round(value)),
  );
}

function readDurationMs(payload) {
  if (!payload || typeof payload !== "object") {
    return undefined;
  }

  return payload.durationMs;
}

function getSupportDiagnosticsDirectory(app) {
  return path.join(app.getPath("userData"), "support-diagnostics");
}

function createSafeTimestamp(date) {
  return date.toISOString().replace(/[:.]/g, "-");
}

function delay(durationMs) {
  return new Promise((resolve) => {
    setTimeout(resolve, durationMs);
  });
}

async function stopTracingQuietly(contentTracing, tracePath) {
  try {
    await contentTracing.stopRecording(tracePath);
  } catch {
    // Best-effort cleanup after a failed support capture.
  }
}

async function stopNetLogQuietly(netLog) {
  try {
    await netLog.stopLogging();
  } catch {
    // Best-effort cleanup after a failed support capture.
  }
}

function getErrorMessage(error) {
  return error instanceof Error
    ? error.message
    : "Support diagnostics capture failed.";
}

module.exports = {
  CAPTURE_SUPPORT_DIAGNOSTICS_CHANNEL,
  captureDesktopSupportDiagnostics,
  normalizeSupportDiagnosticsDurationMs,
  registerDesktopSupportDiagnostics,
};
