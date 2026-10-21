"use strict";

const path = require("node:path");

const { isTrustedMainFrame } = require("./trusted-frame.cjs");

// The Daemon card in Settings → Updates & Support talks to the supervisor
// through these channels. Status is pushed on every material change; the log
// follower streams parsed lines while the card shows them.
const DAEMON_STATUS_UPDATED_CHANNEL = "desktop:daemon-status-updated";
const DAEMON_LOG_LINES_CHANNEL = "desktop:daemon-log-lines";
// The daemon writes its structured slog to stdout, which `zen daemon start`
// maps to this file under ZENITH_TEAM_HOME (cmd/zen/cli_daemon.go
// daemonLogDestination); daemon.err.log only carries a crash's stderr. Both
// rotate to `.old` at 10 MiB on start.
const DAEMON_LOG_FILE_NAME = "daemon.log";
const DAEMON_LOG_MAX_LINES = 500;
const DAEMON_LOG_LINE_MAX_CHARS = 16 << 10;
const DAEMON_LOG_POLL_MS = 500;
// One poll reads at most this much; a burst larger than that is drained over
// consecutive polls, and the initial tail only needs the end of the file.
const DAEMON_LOG_READ_MAX_BYTES = 1 << 20;
const DAEMON_LOG_LINE_PATTERN =
  /^time=(\S+)\s+level=(DEBUG|INFO|WARN|ERROR)\b/u;

function registerDesktopDaemonPanel({
  zenithTeamHomeDirectory,
  fs = require("node:fs"),
  getDaemonRuntime,
  getMainWindow,
  ipcMain,
  pollIntervalMs = DAEMON_LOG_POLL_MS,
  shell = require("electron").shell,
}) {
  const logPath = path.join(zenithTeamHomeDirectory, DAEMON_LOG_FILE_NAME);
  let follower = null;

  function liveWindow() {
    const window = getMainWindow();
    return window && !window.isDestroyed?.() ? window : null;
  }

  function send(channel, payload) {
    liveWindow()?.webContents.send(channel, payload);
  }

  function stopFollowing() {
    if (!follower) return;
    clearInterval(follower.timer);
    follower = null;
  }

  // The follower is offset-based: it reads what was appended since the last
  // poll and treats a shrink (rotation or truncation) as a fresh file.
  function pollLog() {
    if (!follower) return;
    if (!liveWindow()) {
      stopFollowing();
      return;
    }
    let size;
    try {
      size = fs.statSync(logPath).size;
    } catch {
      // A rotated file can disappear briefly before its replacement is
      // created. Keep the reset pending for the replacement's first lines.
      if (!follower.resetPending) {
        send(DAEMON_LOG_LINES_CHANNEL, { lines: [], reset: true });
      }
      follower.offset = 0;
      follower.resetPending = true;
      return;
    }
    if (size < follower.offset) {
      const tail = readLogTail(fs, logPath);
      follower.offset = tail.offset;
      follower.resetPending = tail.lines.length === 0;
      send(DAEMON_LOG_LINES_CHANNEL, { lines: tail.lines, reset: true });
      return;
    }
    if (size === follower.offset) return;
    const chunk = readLogRange(
      fs,
      logPath,
      follower.offset,
      Math.min(size, follower.offset + DAEMON_LOG_READ_MAX_BYTES),
    );
    if (chunk === null) return;
    const text = chunk.toString("utf8");
    const lastBreak = text.lastIndexOf("\n");
    if (lastBreak < 0) return;
    const complete = text.slice(0, lastBreak + 1);
    follower.offset += Buffer.byteLength(complete, "utf8");
    const lines = tailLines(complete).map(parseDaemonLogLine);
    if (lines.length > 0) {
      send(DAEMON_LOG_LINES_CHANNEL, {
        lines,
        reset: follower.resetPending,
      });
      follower.resetPending = false;
    }
  }

  function startFollowing() {
    stopFollowing();
    const tail = readLogTail(fs, logPath);
    follower = {
      offset: tail.offset,
      resetPending: tail.lines.length === 0,
      timer: null,
    };
    send(DAEMON_LOG_LINES_CHANNEL, { lines: tail.lines, reset: true });
    follower.timer = setInterval(pollLog, pollIntervalMs);
    follower.timer.unref?.();
    return { ok: true, path: logPath };
  }

  function failure(code, reason) {
    return { code, ok: false, reason };
  }

  function untrusted() {
    return failure(
      "untrusted_sender",
      "The request did not come from Zenith Team.",
    );
  }

  function withRuntime(event, operation) {
    if (!isTrustedMainFrame(event, getMainWindow())) return untrusted();
    const runtime = getDaemonRuntime();
    if (!runtime) {
      return failure(
        "daemon_unavailable",
        "The agent service is not available on this computer.",
      );
    }
    return operation(runtime);
  }

  const handlers = {
    "desktop:get-daemon-status": (event) => {
      if (!isTrustedMainFrame(event, getMainWindow())) {
        return unavailableDaemonStatus();
      }
      return getDaemonRuntime()?.status() ?? unavailableDaemonStatus();
    },
    "desktop:start-daemon": (event) =>
      withRuntime(event, (runtime) => runtime.startDaemon()),
    "desktop:stop-daemon": (event, options = {}) =>
      withRuntime(event, (runtime) => {
        if (!isDaemonStopOptions(options)) {
          return failure(
            "invalid_request",
            "Daemon stop options are not valid.",
          );
        }
        return runtime.stopDaemon({
          confirmActive: Boolean(options.confirmActive),
        });
      }),
    "desktop:restart-daemon": (event) =>
      withRuntime(event, (runtime) => runtime.restartDaemon()),
    "desktop:start-daemon-logs": (event) => {
      if (!isTrustedMainFrame(event, getMainWindow())) return untrusted();
      return startFollowing();
    },
    "desktop:stop-daemon-logs": (event) => {
      if (!isTrustedMainFrame(event, getMainWindow())) return untrusted();
      stopFollowing();
      return { ok: true };
    },
    "desktop:reveal-daemon-log": (event) => {
      if (!isTrustedMainFrame(event, getMainWindow())) return untrusted();
      if (!fs.existsSync(logPath)) {
        return failure("not_found", "The daemon has not written a log yet.");
      }
      shell.showItemInFolder(logPath);
      return { ok: true, path: logPath };
    },
  };

  for (const [channel, handler] of Object.entries(handlers)) {
    ipcMain.handle(channel, handler);
  }

  return {
    broadcastStatus(status) {
      send(DAEMON_STATUS_UPDATED_CHANNEL, status);
    },
    unregister() {
      stopFollowing();
      for (const channel of Object.keys(handlers)) {
        ipcMain.removeHandler(channel);
      }
    },
  };
}

// A status for an app whose bundled daemon binary is missing or whose
// supervisor is gone; the renderer shows "Unavailable" and disables actions.
function unavailableDaemonStatus() {
  return {
    state: "unavailable",
    reason: "daemon_binary_unavailable",
    ready: false,
    activeRunCount: 0,
    pid: null,
    startAttempts: 0,
    cliVersion: null,
    daemonId: null,
    agents: [],
    skippedAgents: [],
    reloadPendingReason: null,
  };
}

function isDaemonStopOptions(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const keys = Object.keys(value);
  if (keys.length === 0) return true;
  return (
    keys.length === 1 &&
    keys[0] === "confirmActive" &&
    typeof value.confirmActive === "boolean"
  );
}

function parseDaemonLogLine(line) {
  const text = String(line).slice(0, DAEMON_LOG_LINE_MAX_CHARS);
  const match = DAEMON_LOG_LINE_PATTERN.exec(text);
  return {
    level: match ? match[2].toLowerCase() : "info",
    text,
    time: match ? match[1] : null,
  };
}

function tailLines(text, max = DAEMON_LOG_MAX_LINES) {
  const lines = String(text).split(/\r?\n/u);
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines.length > max ? lines.slice(lines.length - max) : lines;
}

// readLogTail returns the last DAEMON_LOG_MAX_LINES complete lines of the
// file and the offset the follower continues from (the end of the last
// complete line). A missing file is an empty tail at offset 0.
function readLogTail(fs, logPath) {
  let size;
  try {
    size = fs.statSync(logPath).size;
  } catch {
    return { lines: [], offset: 0 };
  }
  const start = Math.max(0, size - DAEMON_LOG_READ_MAX_BYTES);
  const chunk = readLogRange(fs, logPath, start, size);
  if (chunk === null) return { lines: [], offset: 0 };
  let text = chunk.toString("utf8");
  if (start > 0) {
    // Drop the partial line the window cut into.
    text = text.slice(text.indexOf("\n") + 1);
  }
  const lastBreak = text.lastIndexOf("\n");
  const complete = lastBreak < 0 ? "" : text.slice(0, lastBreak + 1);
  const offset = size - Buffer.byteLength(text.slice(complete.length), "utf8");
  return { lines: tailLines(complete).map(parseDaemonLogLine), offset };
}

function readLogRange(fs, logPath, start, end) {
  if (end <= start) return Buffer.alloc(0);
  let descriptor = null;
  try {
    descriptor = fs.openSync(logPath, "r");
    const buffer = Buffer.alloc(end - start);
    let filled = 0;
    while (filled < buffer.length) {
      const read = fs.readSync(
        descriptor,
        buffer,
        filled,
        buffer.length - filled,
        start + filled,
      );
      if (read <= 0) break;
      filled += read;
    }
    return buffer.subarray(0, filled);
  } catch {
    return null;
  } finally {
    if (descriptor !== null) {
      try {
        fs.closeSync(descriptor);
      } catch {
        // Nothing to do with a descriptor that is already gone.
      }
    }
  }
}

module.exports = {
  DAEMON_LOG_LINES_CHANNEL,
  DAEMON_STATUS_UPDATED_CHANNEL,
  parseDaemonLogLine,
  registerDesktopDaemonPanel,
  tailLines,
  unavailableDaemonStatus,
};
