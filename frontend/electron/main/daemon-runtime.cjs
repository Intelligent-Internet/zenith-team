"use strict";

const { execFile } = require("node:child_process");
const { createHash, randomUUID } = require("node:crypto");
const { createReadStream } = require("node:fs");
const {
  mkdir,
  readFile,
  rename,
  rm,
  stat,
  writeFile,
} = require("node:fs/promises");
const path = require("node:path");

const { daemonEnvironment } = require("./daemon-environment.cjs");

const HEALTH_RESPONSE_MAX_BYTES = 64 << 10;
const HEALTH_REQUEST_TIMEOUT_MS = 2_000;
const DAEMON_START_TIMEOUT_MS = 60_000;
// `zen daemon stop` itself waits up to 15 s for the exit before it
// returns; the command timeout only guards a wedged CLI.
const DAEMON_STOP_TIMEOUT_MS = 30_000;
const DAEMON_EXIT_ATTEMPTS = 100;
const DAEMON_EXIT_POLL_MS = 100;
// A binary that fails fast must not be respawned on every ~1s poll forever:
// consecutive failed launches back off exponentially up to the cap. A launch
// that yields an owned daemon, or an explicit authenticated()/checkNow()
// trigger, resets the backoff.
const DAEMON_LAUNCH_BACKOFF_BASE_MS = 2_000;
const DAEMON_LAUNCH_BACKOFF_MAX_MS = 60_000;
// One computer can serve several people, one at a time. This file in the
// daemon's home names the person the agent service last ran for (the
// holder): it never starts by itself for anybody else, who is asked first.
// A missing or unreadable file means nobody holds the computer.
const COMPUTER_HOLDER_FILE_NAME = "desktop-computer-holder.json";
const COMPUTER_HOLDER_MAX_BYTES = 4 << 10;
const COMPUTER_HANDOVER_PENDING = "computer_handover_pending";

function createDesktopDaemonRuntime({
  autoStart = true,
  autoStop = false,
  binaryPath,
  boardlyHomeDirectory,
  boardlyMCPExecutable,
  desktopSessionPath,
  environment = process.env,
  expectedVersion,
  healthPort,
  onStatusChange = () => {},
  pollIntervalMs = 1_000,
  prepareLaunch = async () => ({ ok: true }),
  probeHealth,
  readComputerHolder = () => readComputerHolderFile(boardlyHomeDirectory),
  readStoredSession,
  runDaemonCommand = executeDaemonCommand,
  serverUrl,
  stopDaemon: requestStopOverride,
  wait = delay,
  writeComputerHolder = (userId) =>
    writeComputerHolderFile(boardlyHomeDirectory, userId),
}) {
  const normalizedServerUrl = normalizeServerUrl(serverUrl);
  // BOARDLY_DESKTOP_SESSION_PATH names the app's own session store, so the
  // daemon keeps every Run away from it (D31). The daemon never reads it.
  const daemonEnv = stringEnvironment(
    daemonEnvironment(environment, {
      BOARDLY_DESKTOP_SESSION_PATH: desktopSessionPath,
      BOARDLY_HEALTH_PORT: String(healthPort),
      BOARDLY_HOME: boardlyHomeDirectory,
      BOARDLY_LAUNCHED_BY: "desktop",
      BOARDLY_MCP_EXECUTABLE: boardlyMCPExecutable,
      BOARDLY_SERVER_URL: normalizedServerUrl,
    }),
  );
  const readHealth = probeHealth ?? (() => fetchDaemonHealth({ healthPort }));
  const readBinaryDigest = createBinaryDigestReader(binaryPath);
  const requestStop =
    requestStopOverride ??
    ((health) =>
      requestDaemonStop({
        binaryPath,
        env: daemonEnv,
        health,
        runDaemonCommand,
      }));

  let authenticatedOwner = null;
  let disposed = false;
  // The person who said, in this app session, to use this computer for
  // their agents (Start or Restart). It covers a holder record that could
  // not be written, and goes with a sign-out or a change of account.
  let handoverAcceptedBy = null;
  let launchBackoffUntil = 0;
  let launchFailureCount = 0;
  // A Stop from the daemon panel must hold across polls for the person who
  // pressed it: the latch keeps reconcile() from relaunching until Start or
  // a sign-in by somebody else clears it. It is not persisted — an app
  // restart starts the daemon again.
  let manuallyStoppedFor = null;
  // The pid of the daemon we have already asked to stop. It serves /health
  // for its whole shutdown, so it keeps answering while it drains its last
  // runs: without this, every poll would read an owned, version-matching,
  // answering daemon and call it `running`, and every poll through a
  // stop-shaped branch (sign-out, account switch, version restart) would
  // spawn another `zen daemon stop` child that blocks for up to 15 s.
  // One stop request per shutdown; the latch goes when that process does.
  let stopRequestedPid = null;
  let pollTimer = null;
  let operationPromise = null;
  let reconcilePromise = null;
  let snapshot = createStatus("stopped", "desktop_authentication_unavailable");
  let notifiedStatus = null;

  // Polling reconciles every second, so observers hear material changes
  // only — a signed-out app must not append an identical status line to the
  // main log once per tick. status() always serves the latest snapshot.
  // startAttempts is part of a status's identity, so sustained launch failure
  // surfaces one line per (backed-off) attempt instead of being deduplicated.
  function update(state, reason = null, health = null) {
    snapshot = createStatus(state, reason, health, launchFailureCount);
    if (statusEquals(notifiedStatus, snapshot)) {
      return;
    }
    notifiedStatus = snapshot;
    try {
      onStatusChange({ ...snapshot });
    } catch {
      // Observers do not own the daemon lifecycle.
    }
  }

  function registerLaunchFailure() {
    launchFailureCount += 1;
    launchBackoffUntil =
      Date.now() +
      Math.min(
        DAEMON_LAUNCH_BACKOFF_BASE_MS * 2 ** (launchFailureCount - 1),
        DAEMON_LAUNCH_BACKOFF_MAX_MS,
      );
  }

  function clearLaunchBackoff() {
    launchBackoffUntil = 0;
    launchFailureCount = 0;
  }

  function isOwned(health) {
    return (
      health?.launchedBy === "desktop" &&
      normalizeServerUrl(health.serverUrl) === normalizedServerUrl
    );
  }

  // True while the daemon answering /health is the very process we asked to
  // stop. Any other pid is a different daemon and the latch is stale.
  function draining(health) {
    return stopRequestedPid !== null && stopRequestedPid === health?.pid;
  }

  function forgetStopRequest() {
    stopRequestedPid = null;
  }

  function applyHealth(health, reason = null) {
    if (!isOwned(health)) {
      update("blocked", "foreign_daemon", health);
      return false;
    }
    clearLaunchBackoff();
    update(health.status, reason, health);
    return true;
  }

  function manuallyStopped() {
    return (
      manuallyStoppedFor !== null && manuallyStoppedFor === authenticatedOwner
    );
  }

  async function readHolder() {
    try {
      return (await readComputerHolder()) || null;
    } catch {
      return null;
    }
  }

  // A holder record that cannot be written costs at most one question
  // later; it never keeps the agent service from starting.
  async function recordHolder(owner) {
    if (!owner) return;
    try {
      await writeComputerHolder(owner);
    } catch {
      // The record is a courtesy, not a lock.
    }
  }

  // Somebody else used this computer last, and the person signed in now has
  // not yet said to use it for their agents.
  async function handoverPending() {
    if (!authenticatedOwner || handoverAcceptedBy === authenticatedOwner) {
      return false;
    }
    const holder = await readHolder();
    return holder !== null && holder !== authenticatedOwner;
  }

  // An explicit Start or Restart is the signed-in person's word to use this
  // computer for their agents.
  async function acceptHandover() {
    handoverAcceptedBy = authenticatedOwner;
    await recordHolder(authenticatedOwner);
  }

  // A launch that yields an owned daemon makes its person the holder, so the
  // first launch on a computer (a fresh install or an upgrade) asks nobody.
  async function applyLaunchedHealth(health, owner) {
    if (applyHealth(health) && owner && owner === authenticatedOwner) {
      await recordHolder(owner);
    }
  }

  async function launch() {
    const owner = authenticatedOwner;
    update("starting");
    // The daemon runs on its own session (D31): the app gives it one before
    // every launch that finds none, and a launch without one cannot sign in.
    let prepared;
    try {
      prepared = await prepareLaunch();
    } catch {
      prepared = { ok: false };
    }
    if (!prepared?.ok) {
      registerLaunchFailure();
      update("unavailable", "daemon_session_unavailable");
      return;
    }
    try {
      await runDaemonCommand(binaryPath, ["daemon", "start"], {
        encoding: "utf8",
        env: daemonEnv,
        maxBuffer: 64 << 10,
        shell: false,
        timeout: DAEMON_START_TIMEOUT_MS,
        windowsHide: true,
      });
    } catch {
      const health = await readHealth();
      if (health) {
        await applyLaunchedHealth(health, owner);
        return;
      }
      registerLaunchFailure();
      update("unavailable", "daemon_start_failed");
      return;
    }
    const health = await readHealth();
    if (!health) {
      registerLaunchFailure();
      update("unavailable", "daemon_health_unavailable");
      return;
    }
    await applyLaunchedHealth(health, owner);
  }

  async function stopOwned(stoppedReason = null) {
    const health = await readHealth();
    if (!health) {
      forgetStopRequest();
      update("stopped", stoppedReason);
      return true;
    }
    if (!isOwned(health)) {
      update("blocked", "foreign_daemon", health);
      return false;
    }
    if (draining(health)) {
      // This process was already asked to stop and is still finishing its
      // runs. Asking again buys nothing and costs another blocking child.
      update("stopping", "stop_draining_runs", health);
      return false;
    }
    update("stopping", null, health);
    stopRequestedPid = health.pid;
    try {
      await requestStop(health);
    } catch {
      forgetStopRequest();
      update("unavailable", "daemon_stop_failed", health);
      return false;
    }
    try {
      await waitForDaemonExit(readHealth, wait);
    } catch {
      // A daemon that still answers is draining its in-flight runs, not
      // wedged: it holds the health port until the last one finishes and
      // exits on its own. Say so and relaunch nothing — a replacement
      // started now could not bind the port the old process still owns.
      const stillAnswering = await readHealth();
      if (isOwned(stillAnswering)) {
        update("stopping", "stop_draining_runs", stillAnswering);
        return false;
      }
      forgetStopRequest();
      update("unavailable", "daemon_stop_failed", health);
      return false;
    }
    forgetStopRequest();
    update("stopped", stoppedReason);
    return true;
  }

  async function restartForBuildMismatch(health, reason) {
    update("restarting", reason, health);
    if (!(await stopOwned())) return;
    if (disposed || !authenticatedOwner) return;
    await launch();
  }

  async function reconcile() {
    if (disposed) return;
    const health = await readHealth();
    if (health) {
      if (!isOwned(health)) {
        applyHealth(health);
        return;
      }
      if (draining(health)) {
        // The shutdown we asked for is still under way. It is not running,
        // it does not need a second stop, and nothing may be launched into
        // the port it still holds — whatever wanted it stopped still does.
        update("stopping", "stop_draining_runs", health);
        return;
      }
      // Any other owned daemon on the port is a different process.
      forgetStopRequest();
      if (!authenticatedOwner) {
        await stopOwned();
        if (snapshot.state === "stopped") {
          update("stopped", "desktop_authentication_unavailable");
        }
        return;
      }
      const binaryDigest = await readBinaryDigest();
      const versionMismatch = health.cliVersion !== expectedVersion;
      if (
        versionMismatch ||
        (binaryDigest && health.cliDigest !== binaryDigest)
      ) {
        const reason = versionMismatch ? "version_mismatch" : "binary_mismatch";
        if (health.activeRunCount > 0) {
          applyHealth(health, `${reason}_active_runs`);
          return;
        }
        await restartForBuildMismatch(health, reason);
        return;
      }
      applyHealth(health);
      return;
    }
    // The port is free: the daemon we asked to stop has exited.
    forgetStopRequest();
    if (!authenticatedOwner) {
      update("stopped", "desktop_authentication_unavailable");
      return;
    }
    if (manuallyStopped()) {
      update("stopped", "stopped_by_user");
      return;
    }
    if (autoStart) {
      if (await handoverPending()) {
        // Somebody else used this computer last: the agent service starts
        // for this person on their word (Start) and never by itself.
        update("stopped", COMPUTER_HANDOVER_PENDING);
        return;
      }
      if (Date.now() < launchBackoffUntil) {
        // Still backing off after a failed launch; the failure status stands.
        return;
      }
      await launch();
      return;
    }
    update("stopped");
  }

  // Manual operations and the poll must never interleave: a poll landing
  // between a restart's stop and its launch would launch a second time.
  // Every lifecycle operation queues behind the one in flight.
  function runExclusive(operation) {
    const chained = (operationPromise ?? Promise.resolve())
      .catch(() => {})
      .then(operation);
    const tracked = chained.then(
      () => {},
      () => {},
    );
    void tracked.then(() => {
      if (operationPromise === tracked) operationPromise = null;
    });
    operationPromise = tracked;
    return chained;
  }

  // Polls coalesce: a tick that lands while a reconcile is queued or running
  // shares its promise instead of queueing another.
  function runReconcile() {
    if (reconcilePromise) return reconcilePromise;
    reconcilePromise = runExclusive(reconcile).finally(() => {
      reconcilePromise = null;
    });
    return reconcilePromise;
  }

  // Explicit triggers (start/authenticated/IPC checkNow) retry immediately;
  // only the background poll honours the launch backoff.
  function checkNow() {
    clearLaunchBackoff();
    return runReconcile();
  }

  function schedulePoll() {
    if (disposed || pollTimer || pollIntervalMs <= 0) return;
    pollTimer = setTimeout(() => {
      pollTimer = null;
      void runReconcile().finally(schedulePoll);
    }, pollIntervalMs);
    pollTimer.unref?.();
  }

  async function readOwner() {
    const stored = await readStoredSession();
    if (!stored?.ok || !stored.accessToken) return null;
    return ownerFromAccessToken(stored.accessToken);
  }

  function actionSucceeded() {
    return { ok: true, status: { ...snapshot } };
  }

  function actionFailed(code, reason, extra = {}) {
    return { ok: false, code, reason, status: { ...snapshot }, ...extra };
  }

  function actionResult(failureCode, failureReason) {
    return snapshot.state === "unavailable"
      ? actionFailed(failureCode, failureReason)
      : actionSucceeded();
  }

  // The daemon panel's operations. Each resolves to
  // { ok: true, status } | { ok: false, code, reason, status, activeRunCount? }
  // and never throws; the status is the snapshot after the operation.
  async function startDaemon() {
    if (disposed) return actionFailed(...DAEMON_UNAVAILABLE);
    if (!authenticatedOwner) return actionFailed(...SIGNED_OUT);
    manuallyStoppedFor = null;
    await acceptHandover();
    await checkNow();
    if (snapshot.state === "blocked") return actionFailed(...FOREIGN_DAEMON);
    return actionResult(...START_FAILED);
  }

  function stopDaemon({ confirmActive = false } = {}) {
    return runExclusive(async () => {
      if (disposed) return actionFailed(...DAEMON_UNAVAILABLE);
      const health = await readHealth();
      if (!health) {
        manuallyStoppedFor = authenticatedOwner;
        update("stopped", "stopped_by_user");
        return actionSucceeded();
      }
      if (!isOwned(health)) {
        update("blocked", "foreign_daemon", health);
        return actionFailed(...FOREIGN_DAEMON);
      }
      if (draining(health)) {
        // Already stopping. Re-asking would confirm a stop that is happening.
        update("stopping", "stop_draining_runs", health);
        return actionFailed(...STOP_DRAINING);
      }
      if (health.activeRunCount > 0 && !confirmActive) {
        return actionFailed("active_runs", "Runs are in flight.", {
          activeRunCount: health.activeRunCount,
        });
      }
      manuallyStoppedFor = authenticatedOwner;
      if (await stopOwned("stopped_by_user")) return actionSucceeded();
      return stopFailure();
    });
  }

  // A stop that has not finished is either a drain — the daemon is still
  // running its last runs and exits by itself — or a real failure. They read
  // very differently to the person watching the panel.
  function stopFailure() {
    return snapshot.reason === "stop_draining_runs"
      ? actionFailed(...STOP_DRAINING)
      : actionFailed(...STOP_FAILED);
  }

  // Restart is stop + launch under the supervisor's own timeouts rather than
  // `zen daemon restart`, which drains for minutes and re-verifies the
  // session; observers see restarting → stopping → stopped → starting →
  // running.
  function restartDaemon() {
    return runExclusive(async () => {
      if (disposed) return actionFailed(...DAEMON_UNAVAILABLE);
      if (!authenticatedOwner) return actionFailed(...SIGNED_OUT);
      const health = await readHealth();
      if (health && !isOwned(health)) {
        update("blocked", "foreign_daemon", health);
        return actionFailed(...FOREIGN_DAEMON);
      }
      manuallyStoppedFor = null;
      clearLaunchBackoff();
      await acceptHandover();
      if (health) {
        update("restarting", "user_requested", health);
        if (!(await stopOwned())) return stopFailure();
      }
      await launch();
      return actionResult(...START_FAILED);
    });
  }

  return {
    async start() {
      authenticatedOwner = await readOwner();
      await checkNow();
      schedulePoll();
    },
    async authenticated() {
      const nextOwner = await readOwner();
      if (!nextOwner) return;
      const changedAccount = Boolean(
        authenticatedOwner && authenticatedOwner !== nextOwner,
      );
      authenticatedOwner = nextOwner;
      // Only the person who pressed Stop (or Not now) keeps that answer;
      // anybody else's sign-in clears it.
      if (manuallyStoppedFor !== nextOwner) manuallyStoppedFor = null;
      if (changedAccount) {
        handoverAcceptedBy = null;
        if (!(await runExclusive(stopOwned))) return;
      }
      await checkNow();
      schedulePoll();
    },
    async signedOut() {
      authenticatedOwner = null;
      handoverAcceptedBy = null;
      await runExclusive(stopOwned);
      if (snapshot.reason === "daemon_stop_failed") {
        throw new Error("Desktop daemon could not be stopped");
      }
    },
    status() {
      return { ...snapshot };
    },
    checkNow,
    startDaemon,
    stopDaemon,
    restartDaemon,
    async dispose() {
      if (disposed) return;
      disposed = true;
      if (pollTimer) clearTimeout(pollTimer);
      pollTimer = null;
      if (autoStop && authenticatedOwner) {
        await stopOwned();
      }
    },
  };
}

// Action results name their failure as [code, reason]; the panel shows the
// reason and branches on the code.
const DAEMON_UNAVAILABLE = [
  "daemon_unavailable",
  "The agent service is not available on this computer.",
];
const SIGNED_OUT = ["signed_out", "Sign in to manage the agent service."];
const FOREIGN_DAEMON = [
  "foreign_daemon",
  "Another agent service already runs on this computer; stop it from where it was started.",
];
const STOP_FAILED = ["stop_failed", "The agent service could not be stopped."];
const STOP_DRAINING = [
  "stop_draining",
  "The agent service is finishing its current runs and stops when they are done.",
];
const START_FAILED = [
  "start_failed",
  "The agent service could not be started.",
];

// A status carries the supervisor's own state plus the parts of /health the
// daemon panel renders. Uptime is deliberately left out: it changes every
// second and would defeat statusEquals.
function createStatus(state, reason = null, health = null, startAttempts = 0) {
  return {
    state,
    reason,
    ready: state === "running",
    activeRunCount: health?.activeRunCount ?? 0,
    pid: health?.pid ?? null,
    startAttempts,
    cliVersion: health?.cliVersion ?? null,
    daemonId: health?.daemonId || null,
    agents: providerEntries(health),
    skippedAgents: skippedProviderEntries(health),
    reloadPendingReason: health?.reloadPendingReason || null,
  };
}

function providerEntries(health) {
  const versions = stringMap(health?.agentVersions);
  return (Array.isArray(health?.agents) ? health.agents : [])
    .filter((provider) => typeof provider === "string")
    .sort()
    .map((provider) => ({ provider, version: versions[provider] ?? null }));
}

function skippedProviderEntries(health) {
  const skipped = stringMap(health?.skippedAgents);
  return Object.keys(skipped)
    .sort()
    .map((provider) => ({ provider, reason: skipped[provider] }));
}

function statusEquals(previous, next) {
  return (
    previous !== null &&
    previous.state === next.state &&
    previous.reason === next.reason &&
    previous.activeRunCount === next.activeRunCount &&
    previous.pid === next.pid &&
    previous.startAttempts === next.startAttempts &&
    previous.cliVersion === next.cliVersion &&
    previous.daemonId === next.daemonId &&
    previous.reloadPendingReason === next.reloadPendingReason &&
    JSON.stringify(previous.agents) === JSON.stringify(next.agents) &&
    JSON.stringify(previous.skippedAgents) ===
      JSON.stringify(next.skippedAgents)
  );
}

async function fetchDaemonHealth({
  fetch: fetchImplementation = globalThis.fetch,
  healthPort,
}) {
  const controller = new AbortController();
  const timeout = setTimeout(
    () => controller.abort(),
    HEALTH_REQUEST_TIMEOUT_MS,
  );
  timeout.unref?.();
  try {
    const response = await fetchImplementation(
      `http://127.0.0.1:${healthPort}/health`,
      {
        headers: { Accept: "application/json" },
        method: "GET",
        redirect: "error",
        signal: controller.signal,
      },
    );
    if (!response?.ok) return null;
    const raw = await response.text();
    if (Buffer.byteLength(raw, "utf8") > HEALTH_RESPONSE_MAX_BYTES) return null;
    return parseDaemonHealth(raw);
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
  }
}

// requestDaemonStop stops the owned daemon through the CLI's own stop path:
// `zen daemon stop` under the desktop BOARDLY_HOME and health port
// verifies the health-reported pid against the process record and signals
// exactly that process, then waits a short while for that process to exit.
// It returns before the exit when the daemon is still draining runs, so the
// caller must confirm the exit itself rather than assume one. The daemon
// serves no HTTP shutdown route on purpose — a run's agent process shares
// the loopback network and must never be able to stop it.
async function requestDaemonStop({
  binaryPath,
  env,
  health,
  runDaemonCommand = executeDaemonCommand,
}) {
  if (!health || health.launchedBy !== "desktop") {
    throw new Error("Refusing to stop a foreign daemon");
  }
  await runDaemonCommand(binaryPath, ["daemon", "stop"], {
    encoding: "utf8",
    env,
    maxBuffer: 64 << 10,
    shell: false,
    timeout: DAEMON_STOP_TIMEOUT_MS,
    windowsHide: true,
  });
}

function executeDaemonCommand(binaryPath, args, options) {
  return new Promise((resolve, reject) => {
    execFile(binaryPath, args, options, (error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

// waitForDaemonExit watches the health port until it goes: the daemon serves
// it for the whole shutdown drain, so it disappearing is the exit. A daemon
// still answering when the attempts run out is still draining.
async function waitForDaemonExit(readHealth, wait) {
  for (let attempt = 0; attempt < DAEMON_EXIT_ATTEMPTS; attempt += 1) {
    if (!(await readHealth())) return;
    await wait(DAEMON_EXIT_POLL_MS);
  }
  throw new Error("Desktop daemon did not stop");
}

function computerHolderPath(boardlyHomeDirectory) {
  return path.join(boardlyHomeDirectory, COMPUTER_HOLDER_FILE_NAME);
}

// readComputerHolderFile returns the user id the agent service last ran
// for, or null when the record is missing, unreadable or malformed.
async function readComputerHolderFile(boardlyHomeDirectory) {
  if (!boardlyHomeDirectory) return null;
  try {
    const raw = await readFile(
      computerHolderPath(boardlyHomeDirectory),
      "utf8",
    );
    if (Buffer.byteLength(raw, "utf8") > COMPUTER_HOLDER_MAX_BYTES) {
      return null;
    }
    const value = JSON.parse(raw);
    return isPlainObject(value) && typeof value.userId === "string"
      ? value.userId.trim() || null
      : null;
  } catch {
    return null;
  }
}

// writeComputerHolderFile replaces the record with one rename, so a reader
// sees the previous holder or the new one and never half a file.
async function writeComputerHolderFile(boardlyHomeDirectory, userId) {
  if (!boardlyHomeDirectory) return;
  const target = computerHolderPath(boardlyHomeDirectory);
  const temporary = `${target}.${randomUUID()}.tmp`;
  await mkdir(boardlyHomeDirectory, { mode: 0o700, recursive: true });
  try {
    await writeFile(temporary, `${JSON.stringify({ userId })}\n`, {
      encoding: "utf8",
      flag: "wx",
      mode: 0o600,
    });
    await rename(temporary, target);
  } catch (error) {
    await rm(temporary, { force: true }).catch(() => {});
    throw error;
  }
}

function parseDaemonHealth(raw) {
  let value;
  try {
    value = JSON.parse(String(raw));
  } catch {
    return null;
  }
  if (
    !isPlainObject(value) ||
    (value.status !== "starting" && value.status !== "running") ||
    !Number.isInteger(value.pid) ||
    value.pid < 1 ||
    typeof value.serverUrl !== "string" ||
    typeof value.cliVersion !== "string" ||
    (value.launchedBy !== undefined && typeof value.launchedBy !== "string") ||
    !Number.isSafeInteger(value.activeRunCount) ||
    value.activeRunCount < 0
  ) {
    return null;
  }
  try {
    return {
      status: value.status,
      pid: value.pid,
      serverUrl: normalizeServerUrl(value.serverUrl),
      cliVersion: value.cliVersion,
      cliDigest:
        typeof value.cliDigest === "string" &&
        /^[a-f0-9]{64}$/u.test(value.cliDigest)
          ? value.cliDigest
          : "",
      launchedBy: value.launchedBy ?? "",
      activeRunCount: value.activeRunCount,
      runningRunCount:
        Number.isSafeInteger(value.runningRunCount) &&
        value.runningRunCount >= 0
          ? value.runningRunCount
          : 0,
      daemonId: typeof value.daemonId === "string" ? value.daemonId : "",
      agents: (Array.isArray(value.agents) ? value.agents : [])
        .filter((provider) => typeof provider === "string")
        .sort(),
      agentVersions: stringMap(value.agentVersions),
      skippedAgents: stringMap(value.skippedAgents),
      reloadPendingReason:
        typeof value.reloadPendingReason === "string"
          ? value.reloadPendingReason
          : "",
    };
  } catch {
    return null;
  }
}

// Version strings stay unchanged during development. Compare the bundled
// executable to the daemon's startup digest and hash only when the file
// changes. A file being replaced or temporarily unavailable grants no restart.
function createBinaryDigestReader(binaryPath) {
  let cachedStamp = null;
  let cachedDigest = null;
  const stamp = (file) =>
    `${file.dev}:${file.ino}:${file.size}:${file.mtimeMs}:${file.ctimeMs}`;
  return async () => {
    try {
      const before = stamp(await stat(binaryPath));
      if (before === cachedStamp) return cachedDigest;
      const hash = createHash("sha256");
      for await (const chunk of createReadStream(binaryPath))
        hash.update(chunk);
      if (before !== stamp(await stat(binaryPath))) return null;
      cachedDigest = hash.digest("hex");
      cachedStamp = before;
      return cachedDigest;
    } catch {
      return null;
    }
  };
}

function stringMap(value) {
  if (!isPlainObject(value)) return {};
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => typeof entry === "string"),
  );
}

function normalizeServerUrl(rawValue) {
  const parsed = new URL(String(rawValue).trim().replace(/\/+$/u, ""));
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    (parsed.pathname !== "/" &&
      parsed.pathname !== "" &&
      parsed.pathname !== "/api/v1") ||
    (parsed.protocol === "http:" && !isLoopback(parsed.hostname))
  ) {
    throw new Error("Invalid Zenith Team daemon server URL");
  }
  return parsed.origin;
}

function ownerFromAccessToken(token) {
  const segments = String(token).split(".");
  if (segments.length !== 3) return null;
  try {
    const payload = JSON.parse(
      Buffer.from(segments[1], "base64url").toString("utf8"),
    );
    return payload?.tokenUse === "access" && typeof payload.sub === "string"
      ? payload.sub.trim() || null
      : null;
  } catch {
    return null;
  }
}

function isLoopback(hostname) {
  return ["localhost", "127.0.0.1", "[::1]"].includes(hostname);
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function stringEnvironment(environment) {
  return Object.fromEntries(
    Object.entries(environment).filter(
      ([, value]) => typeof value === "string",
    ),
  );
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

module.exports = {
  COMPUTER_HANDOVER_PENDING,
  COMPUTER_HOLDER_FILE_NAME,
  createDesktopDaemonRuntime,
  fetchDaemonHealth,
  normalizeServerUrl,
  ownerFromAccessToken,
  parseDaemonHealth,
  readComputerHolderFile,
  requestDaemonStop,
  writeComputerHolderFile,
};
