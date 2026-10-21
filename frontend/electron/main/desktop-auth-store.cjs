"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFile } = require("node:child_process");
const { app, safeStorage } = require("electron");

const { daemonEnvironment } = require("./daemon-environment.cjs");
const { resolveZenithTeamExecutable } = require("./agent-discovery.cjs");
const { writeMainLog } = require("./diagnostics.cjs");
const { createDesktopRuntimeEnv } = require("./release-env.cjs");
const { getDesktopVariant } = require("../shared/desktop-variant.cjs");

// The app's own sign-in. It lives in userData, encrypted with the
// operating system's protection for this app (Electron safeStorage), and
// never in the daemon's home. The daemon has a session of its own,
// daemon-auth.json in the daemon's home, which the app starts after sign-in
// (POST /auth/daemon-session) and hands to `zen desktop-session install`.
const DESKTOP_SESSION_FILENAME = "desktop-session.v1.bin";
// Older versions had the app and its daemon share one session in the
// daemon's home. The first launch after the update moves it into the app's
// store.
const SHARED_SESSION_FILENAME = "auth.json";
const SHARED_SESSION_LOCK_FILENAME = "auth.lock";
const DAEMON_SESSION_FILENAME = "daemon-auth.json";
const LEGACY_AUTH_DIRECTORY_NAME = "auth";
const LEGACY_ACCESS_TOKEN_FILENAME = "zenith-team-access-token.v1.bin";
const LEGACY_AUTH_SESSION_FILENAME = "zenith-team-auth-session.v2.bin";
const LEGACY_MIGRATION_MARKER = "legacy-session-migrated.v1";
const DESKTOP_SESSION_MAX_BUFFER_BYTES = 64 << 10;
const DESKTOP_SESSION_TIMEOUT_MS = 35_000;
const AUTH_REQUEST_TIMEOUT_MS = 30_000;
// Renew this long before the access token expires: request latency and
// clock drift between this computer and the server (the CLI uses the same).
const REFRESH_SKEW_MS = 30_000;
// The CLI's cross-process lock on auth.json (internal/authfile): the same
// file name, the same stale age and the same wait.
const SHARED_SESSION_LOCK_STALE_MS = 2 * 60_000;
const SHARED_SESSION_LOCK_WAIT_MS = 10_000;
const SHARED_SESSION_LOCK_POLL_MS = 50;

// The app's store could not be written: secure storage is unavailable (the
// operating system refused the app its key) or the file could not be
// written. The message is the cause, for the main log; it never holds a
// token.
class SessionNotSavedError extends Error {
  constructor(cause) {
    super(cause?.message || "Desktop auth session could not be saved");
    this.name = "SessionNotSavedError";
    this.code = cause?.code ?? null;
  }
}

let sharedSessionInUse = async () => false;

const defaultStore = createDesktopAuthTokenStore({
  app,
  env: createDesktopRuntimeEnv(),
  execFile,
  fetch: (...args) => globalThis.fetch(...args),
  fs,
  homeDirectory: os.homedir(),
  path,
  resolveExecutable: resolveZenithTeamExecutable,
  safeStorage,
  sharedSessionInUse: () => sharedSessionInUse(),
  writeMainLog,
});

function createDesktopAuthTokenStore({
  app: electronApp,
  env = {},
  execFile: runFile,
  fetch: fetchImplementation,
  fs: fsModule,
  homeDirectory,
  path: pathModule,
  resolveExecutable,
  safeStorage: secureStorage,
  sharedSessionInUse: isSharedSessionInUse = async () => false,
  writeMainLog: writeLog = () => {},
}) {
  const desktopVariant = getDesktopVariant(env, {
    fallback: electronApp.isPackaged ? "prod" : "local",
  });
  const zenithTeamHomeDirectory = () =>
    pathModule.join(electronApp.getPath("userData"), "zenith-team-home");
  const sessionStorePath = () =>
    pathModule.join(electronApp.getPath("userData"), DESKTOP_SESSION_FILENAME);
  const sharedSessionPath = () =>
    pathModule.join(zenithTeamHomeDirectory(), SHARED_SESSION_FILENAME);
  const daemonSessionPath = () =>
    pathModule.join(zenithTeamHomeDirectory(), DAEMON_SESSION_FILENAME);
  const legacyDirectory = () =>
    pathModule.join(
      electronApp.getPath("userData"),
      LEGACY_AUTH_DIRECTORY_NAME,
    );

  // Every write to the app's store runs one at a time, so two refreshes
  // never redeem the same refresh token.
  let storeChain = Promise.resolve();
  function exclusive(operation) {
    const next = storeChain.then(operation, operation);
    storeChain = next.then(
      () => {},
      () => {},
    );
    return next;
  }

  // The store is written only by this process, so the last value read or
  // written stands until the next write.
  let cachedSession;

  function invokeCli(args, input = null) {
    const executablePath = resolveExecutable({ app: electronApp, env });
    if (!executablePath) {
      throw new Error("Zenith Team CLI is unavailable");
    }
    const childEnvironment = daemonEnvironment(env, {
      ZENITH_TEAM_HOME: zenithTeamHomeDirectory(),
    });
    return executeCommand({
      args,
      environment: childEnvironment,
      executablePath,
      input,
      runFile,
    });
  }

  async function invokeDesktopSession(action, session) {
    const input = session ? `${JSON.stringify(session)}\n` : null;
    const raw = await invokeCli(["desktop-session", action], input);
    return parseDesktopSessionResponse(raw);
  }

  function readSessionStore() {
    if (cachedSession !== undefined) return cachedSession;
    if (!fsModule.existsSync(sessionStorePath())) {
      cachedSession = null;
      return null;
    }
    requireSecureStorage();
    const parsed = JSON.parse(
      secureStorage.decryptString(fsModule.readFileSync(sessionStorePath())),
    );
    const session = normalizeSession(parsed);
    if (!session) {
      throw new Error("Desktop auth session store is invalid");
    }
    cachedSession = session;
    return session;
  }

  // writeSessionStore saves the app's session, or throws
  // SessionNotSavedError. There is no second place to save it: never a
  // plain file.
  function writeSessionStore(session) {
    try {
      requireSecureStorage();
      const target = sessionStorePath();
      const temporary = `${target}.${process.pid}.${crypto.randomUUID()}.tmp`;
      const encrypted = secureStorage.encryptString(
        JSON.stringify({ version: 1, ...session }),
      );
      fsModule.mkdirSync(pathModule.dirname(target), {
        mode: 0o700,
        recursive: true,
      });
      try {
        fsModule.writeFileSync(temporary, encrypted, { mode: 0o600 });
        fsModule.renameSync(temporary, target);
      } finally {
        fsModule.rmSync(temporary, { force: true });
      }
    } catch (error) {
      throw new SessionNotSavedError(error);
    }
    cachedSession = session;
  }

  function removeSessionStore() {
    fsModule.rmSync(sessionStorePath(), { force: true });
    cachedSession = null;
  }

  function requireSecureStorage() {
    if (!canSaveSession()) {
      throw new Error("Secure storage is unavailable on this computer");
    }
  }

  // canSaveSession says whether the store can be written at all. A sign-in
  // asks before it replaces an older session, so one this computer cannot
  // read right now is not deleted for a new one it cannot save either.
  function canSaveSession() {
    return Boolean(secureStorage?.isEncryptionAvailable());
  }

  // currentSession is the app's session: its own store, or, on the first
  // launch after the update, the session it shared with its daemon. That
  // one moves into the store as soon as no older daemon still renews it;
  // until then it stays in auth.json, and the app renews it there only
  // under the CLI's lock (refreshSharedSession), as that daemon does, so
  // the two never redeem the same refresh token.
  async function currentSession() {
    const stored = readSessionStore();
    if (stored) return stored;
    if (fsModule.existsSync(sharedSessionPath())) {
      if (await isSharedSessionInUse()) {
        const shared = readSharedSession();
        return shared ? { ...shared, shared: true } : null;
      }
      const moved = await exclusive(moveSharedSession);
      if (moved) return moved;
    }
    return exclusive(migrateLegacySession);
  }

  function readSharedSession() {
    try {
      const raw = JSON.parse(
        fsModule.readFileSync(sharedSessionPath(), "utf8"),
      );
      return raw?.version === 1 ? normalizeSession(raw) : null;
    } catch (error) {
      writeLog("desktop-auth-shared-session-read-failed", {
        message: error?.message,
      });
      return null;
    }
  }

  // writeSharedSession writes auth.json in the CLI's own format
  // (internal/authfile): version 1, 0600, replaced in one rename.
  function writeSharedSession(session) {
    const target = sharedSessionPath();
    const temporary = `${target}.${process.pid}.${crypto.randomUUID()}.tmp`;
    const document = {
      version: 1,
      apiBaseUrl: session.apiBaseUrl,
      accessToken: session.accessToken,
      accessExpiresAt: session.accessExpiresAt,
      refreshToken: session.refreshToken,
      refreshExpiresAt: session.refreshExpiresAt,
    };
    if (session.pendingRotationId) {
      document.pendingRotationId = session.pendingRotationId;
    }
    try {
      fsModule.writeFileSync(
        temporary,
        `${JSON.stringify(document, null, 2)}\n`,
        { mode: 0o600 },
      );
      fsModule.renameSync(temporary, target);
    } finally {
      fsModule.rmSync(temporary, { force: true });
    }
  }

  // refreshSharedSession renews the session the app still shares with an
  // older daemon exactly as that daemon does (authfile.Ensure): under the
  // CLI's lock it reads auth.json again, renews only when it is still due,
  // stores the rotation id before it asks, and writes the new pair back.
  // One file and one lock, so the two never redeem the same refresh token,
  // and the app stays signed in even when that daemon is late to renew.
  // A session moved into the app's store in the meantime is renewed there.
  async function refreshSharedSession() {
    if (readSessionStore()) return refreshSession();
    const renewed = await withSharedSessionLock(async () => {
      let session = readSharedSession();
      if (!session) return null;
      if (!needsRefresh(session)) return session;
      if (
        session.refreshExpiresAt &&
        Date.parse(session.refreshExpiresAt) <= Date.now()
      ) {
        throw new Error("Desktop authentication session has expired");
      }
      if (!session.pendingRotationId) {
        session = { ...session, pendingRotationId: crypto.randomUUID() };
        writeSharedSession(session);
      }
      const payload = await postJSON(`${session.apiBaseUrl}/auth/refresh`, {
        client: "desktop",
        refreshToken: session.refreshToken,
        rotationId: session.pendingRotationId,
      });
      const next = sessionFromBearerPayload(payload, session.apiBaseUrl);
      writeSharedSession(next);
      return next;
    });
    if (renewed) return { ...renewed, shared: true };
    if (readSessionStore()) return refreshSession();
    throw new Error("Desktop authentication session is missing");
  }

  // moveSharedSession is the upgrade path: under the CLI's own lock on
  // auth.json, the shared session becomes the app's and leaves the daemon's
  // home. The daemon then gets a session of its own before it next starts
  // (ensureDaemonSession), so nobody else holds the app's refresh token.
  async function moveSharedSession() {
    const stored = readSessionStore();
    if (stored) return stored;
    return withSharedSessionLock(() => {
      const shared = readSharedSession();
      if (!shared) return null;
      writeSessionStore(shared);
      fsModule.rmSync(sharedSessionPath(), { force: true });
      writeLog("desktop-auth-session-moved", {});
      return shared;
    });
  }

  async function withSharedSessionLock(operation) {
    const lockPath = pathModule.join(
      zenithTeamHomeDirectory(),
      SHARED_SESSION_LOCK_FILENAME,
    );
    const owner = { id: crypto.randomUUID(), pid: process.pid };
    const deadline = Date.now() + SHARED_SESSION_LOCK_WAIT_MS;
    fsModule.mkdirSync(zenithTeamHomeDirectory(), {
      mode: 0o700,
      recursive: true,
    });
    for (;;) {
      try {
        fsModule.writeFileSync(lockPath, `${JSON.stringify(owner)}\n`, {
          flag: "wx",
          mode: 0o600,
        });
        break;
      } catch (error) {
        if (error?.code !== "EEXIST") throw error;
      }
      try {
        const age = Date.now() - fsModule.statSync(lockPath).mtimeMs;
        if (age > SHARED_SESSION_LOCK_STALE_MS) {
          fsModule.rmSync(lockPath, { force: true });
          continue;
        }
      } catch {
        continue;
      }
      if (Date.now() >= deadline) {
        throw new Error("Timed out waiting for the Zenith Team auth lock");
      }
      await delay(SHARED_SESSION_LOCK_POLL_MS);
    }
    try {
      return await operation();
    } finally {
      try {
        const current = JSON.parse(fsModule.readFileSync(lockPath, "utf8"));
        if (current?.id === owner.id)
          fsModule.rmSync(lockPath, { force: true });
      } catch {
        // The lock is already gone.
      }
    }
  }

  async function readStoredSession() {
    try {
      const session = await currentSession();
      return session
        ? successfulSession(publicSession(session))
        : emptySession();
    } catch (error) {
      writeLog("desktop-auth-session-read-failed", {
        message: error?.message,
      });
      return failedSession("Unable to read stored desktop auth session.");
    }
  }

  async function readStoredAccessToken() {
    const stored = await readStoredSession();
    return stored.ok ? { ok: true, accessToken: stored.accessToken } : stored;
  }

  async function installStoredSession(session) {
    const normalized = normalizeSession(
      session,
      defaultApiBaseUrl(desktopVariant),
    );
    if (!normalized) {
      writeLog("desktop-auth-session-install-failed", {
        message: "Desktop auth session is invalid",
      });
      return failedSession("Desktop auth session is invalid.");
    }
    try {
      await exclusive(() => writeSessionStore(normalized));
    } catch (error) {
      // The caller ends the session the server just started: nothing on
      // this computer holds it.
      writeLog("desktop-auth-session-install-failed", {
        code: error?.code ?? null,
        message: error?.message,
      });
      return failedSession("Unable to store desktop auth session.");
    }
    // The session is saved. A leftover from an older build that cannot be
    // tidied away does not undo the sign-in.
    try {
      markLegacyMigrationComplete();
      clearEncryptedLegacyFiles();
    } catch (error) {
      writeLog("desktop-auth-legacy-cleanup-failed", {
        message: error?.message,
      });
    }
    return successfulSession(publicSession(normalized));
  }

  // ensureStoredSession renews the app's session when its access token is
  // about to expire, with a rotation id kept beside the refresh token so a
  // lost answer can be recovered with the same rotation, as the CLI does.
  async function ensureStoredSession() {
    try {
      const session = await currentSession();
      if (!session) return emptySession();
      if (!needsRefresh(session)) {
        return successfulSession(publicSession(session));
      }
      const renewed = await exclusive(
        session.shared ? refreshSharedSession : refreshSession,
      );
      return successfulSession(publicSession(renewed));
    } catch (error) {
      writeLog("desktop-auth-session-refresh-failed", {
        code: error?.code ?? null,
        message: error?.message,
      });
      if (error instanceof SessionNotSavedError) {
        // The stored session stays the person's: its refresh token and
        // rotation id renew it once the store can be written again.
        return {
          ...failedSession("Unable to save the desktop session."),
          notSaved: true,
        };
      }
      return failedSession("Unable to refresh the desktop session.");
    }
  }

  async function refreshSession() {
    let session = readSessionStore();
    if (!session) throw new Error("Desktop authentication session is missing");
    if (!needsRefresh(session)) return session;
    if (
      session.refreshExpiresAt &&
      Date.parse(session.refreshExpiresAt) <= Date.now()
    ) {
      throw new Error("Desktop authentication session has expired");
    }
    if (!session.pendingRotationId) {
      session = { ...session, pendingRotationId: crypto.randomUUID() };
      writeSessionStore(session);
    }
    const payload = await postJSON(`${session.apiBaseUrl}/auth/refresh`, {
      client: "desktop",
      refreshToken: session.refreshToken,
      rotationId: session.pendingRotationId,
    });
    const next = sessionFromBearerPayload(payload, session.apiBaseUrl);
    writeSessionStore(next);
    return next;
  }

  // signOutStoredSession ends the app's session on the server with its own
  // refresh token. A server that refuses it, or cannot be reached, fails
  // the sign-out so the app can say so; a session the server already ended
  // is signed out.
  async function signOutStoredSession() {
    const session = await currentSession();
    if (!session) return;
    let response;
    try {
      response = await fetchImplementation(
        `${session.apiBaseUrl}/auth/logout`,
        {
          body: JSON.stringify({
            client: "desktop",
            refreshToken: session.refreshToken,
          }),
          headers: {
            Accept: "application/json",
            "Content-Type": "application/json",
          },
          method: "POST",
          signal: AbortSignal.timeout(AUTH_REQUEST_TIMEOUT_MS),
        },
      );
    } catch (error) {
      throw new Error(
        `Zenith Team Desktop logout failed: ${error?.message ?? "unreachable"}`,
      );
    }
    if (!response?.ok && response?.status !== 401) {
      throw new Error("Zenith Team Desktop logout failed");
    }
  }

  async function clearStoredAccessToken() {
    try {
      await exclusive(async () => {
        removeSessionStore();
        if (fsModule.existsSync(sharedSessionPath())) {
          await withSharedSessionLock(() =>
            fsModule.rmSync(sharedSessionPath(), { force: true }),
          );
        }
      });
      markLegacyMigrationComplete();
      clearEncryptedLegacyFiles();
      await invokeDesktopSession("clear");
      return { ok: true };
    } catch (error) {
      writeLog("desktop-auth-token-clear-failed", {
        message: error?.message,
      });
      return {
        ok: false,
        reason: "Unable to clear desktop auth token.",
      };
    }
  }

  // ensureDaemonSession gives the daemon its own session before it
  // starts, and a fresh one after every sign-in (replace), which also
  // replaces one the server ended. Before a start, one already in the
  // daemon's home for the same person and server is kept. The app's session
  // starts the new one and the CLI stores it over the old file, so a running
  // daemon picks it up on its next call; the old one is signed out after.
  let daemonSessionChain = Promise.resolve();
  function ensureDaemonSession(options = {}) {
    const provision = () => provisionDaemonSession(options);
    const next = daemonSessionChain.then(provision, provision);
    daemonSessionChain = next.then(
      () => {},
      () => {},
    );
    return next;
  }

  async function provisionDaemonSession({ replace = false } = {}) {
    try {
      const appSession = await ensureStoredSession();
      if (!appSession.ok || !appSession.accessToken) {
        return failedSession("Sign in to give the agent service its session.");
      }
      if (appSession.shared) {
        return failedSession(
          "An older agent service still uses the shared sign-in.",
        );
      }
      if (!replace) await removeLeftoverSharedSession();
      const existing = readDaemonSession();
      if (
        !replace &&
        existing &&
        existing.apiBaseUrl === appSession.apiBaseUrl &&
        ownerFromAccessToken(existing.accessToken) ===
          ownerFromAccessToken(appSession.accessToken) &&
        !(
          existing.refreshExpiresAt &&
          Date.parse(existing.refreshExpiresAt) <= Date.now()
        )
      ) {
        return { ok: true };
      }
      const payload = await postJSON(
        `${appSession.apiBaseUrl}/auth/daemon-session`,
        {},
        appSession.accessToken,
      );
      if (payload?.client !== "daemon") {
        throw new Error("Zenith Team daemon session response is invalid");
      }
      const daemonSession = sessionFromBearerPayload(
        payload,
        appSession.apiBaseUrl,
      );
      await invokeDesktopSession("install", {
        apiBaseUrl: daemonSession.apiBaseUrl,
        accessToken: daemonSession.accessToken,
        accessExpiresAt: daemonSession.accessExpiresAt,
        refreshToken: daemonSession.refreshToken,
        refreshExpiresAt: daemonSession.refreshExpiresAt,
      });
      writeLog("desktop-daemon-session-installed", {});
      if (existing) {
        // Signing out a refresh token the daemon already rotated still
        // ends the whole session family on the server.
        await postJSON(`${existing.apiBaseUrl}/auth/logout`, {
          client: "desktop",
          refreshToken: existing.refreshToken,
        }).catch((error) => {
          writeLog("desktop-daemon-session-sign-out-failed", {
            message: error?.message,
          });
        });
      }
      return { ok: true };
    } catch (error) {
      writeLog("desktop-daemon-session-failed", {
        message: error?.message,
      });
      return failedSession("The agent service could not get its own session.");
    }
  }

  // removeLeftoverSharedSession runs before a launch, when no daemon answers
  // its health port. A person's session still in the daemon's home
  // (auth.json) beside the app's own store is nobody's any more, for
  // example one an older daemon kept while the app signed in again, so it
  // goes: the daemon's home holds only the daemon's own session.
  async function removeLeftoverSharedSession() {
    if (!fsModule.existsSync(sharedSessionPath())) return;
    await exclusive(() =>
      withSharedSessionLock(() => {
        if (!readSessionStore()) return;
        fsModule.rmSync(sharedSessionPath(), { force: true });
        writeLog("desktop-auth-shared-session-removed", {});
      }),
    );
  }

  // readDaemonSession reads the daemon's session to tell it apart (its
  // server, its person, its expiry) and to sign a replaced one out. The
  // daemon renews it; the app never does.
  function readDaemonSession() {
    try {
      const raw = JSON.parse(
        fsModule.readFileSync(daemonSessionPath(), "utf8"),
      );
      return normalizeSession(raw);
    } catch {
      return null;
    }
  }

  async function postJSON(url, body, accessToken = null) {
    const headers = {
      Accept: "application/json",
      "Content-Type": "application/json",
    };
    if (accessToken) headers.Authorization = `Bearer ${accessToken}`;
    const response = await fetchImplementation(url, {
      body: JSON.stringify(body),
      headers,
      method: "POST",
      signal: AbortSignal.timeout(AUTH_REQUEST_TIMEOUT_MS),
    });
    if (!response?.ok) {
      let code = null;
      try {
        code = (await response.json())?.code ?? null;
      } catch {
        code = null;
      }
      throw new Error(
        `Zenith Team answered ${response?.status ?? "nothing"}${code ? ` ${code}` : ""}`,
      );
    }
    // A sign-out answers 204 with no body.
    return response.status === 204 ? null : response.json();
  }

  async function migrateLegacySession() {
    const stored = readSessionStore();
    if (stored) return stored;
    if (legacyMigrationComplete()) return null;
    const legacy = readEncryptedLegacySession() ?? readPlainLegacySession();
    if (legacy) writeSessionStore(legacy);
    markLegacyMigrationComplete();
    clearEncryptedLegacyFiles();
    return legacy;
  }

  function readEncryptedLegacySession() {
    if (!secureStorage?.isEncryptionAvailable()) return null;
    const sessionPath = pathModule.join(
      legacyDirectory(),
      LEGACY_AUTH_SESSION_FILENAME,
    );
    if (!fsModule.existsSync(sessionPath)) return null;
    try {
      const parsed = JSON.parse(
        secureStorage.decryptString(fsModule.readFileSync(sessionPath)),
      );
      return normalizeSession(parsed, defaultApiBaseUrl(desktopVariant), true);
    } catch (error) {
      writeLog("desktop-auth-session-migration-failed", {
        message: error?.message,
      });
      return null;
    }
  }

  function readPlainLegacySession() {
    const legacyPath = pathModule.join(
      homeDirectory,
      legacyZenithTeamDirectoryName(desktopVariant),
      "auth.json",
    );
    if (!fsModule.existsSync(legacyPath)) return null;
    try {
      return normalizeSession(
        JSON.parse(fsModule.readFileSync(legacyPath, "utf8")),
        defaultApiBaseUrl(desktopVariant),
        true,
      );
    } catch (error) {
      writeLog("desktop-auth-session-migration-failed", {
        message: error?.message,
      });
      return null;
    }
  }

  function clearEncryptedLegacyFiles() {
    fsModule.rmSync(
      pathModule.join(legacyDirectory(), LEGACY_ACCESS_TOKEN_FILENAME),
      { force: true },
    );
    fsModule.rmSync(
      pathModule.join(legacyDirectory(), LEGACY_AUTH_SESSION_FILENAME),
      { force: true },
    );
    fsModule.rmSync(
      pathModule.join(legacyDirectory(), `${LEGACY_AUTH_SESSION_FILENAME}.tmp`),
      { force: true },
    );
  }

  function legacyMigrationComplete() {
    return fsModule.existsSync(
      pathModule.join(legacyDirectory(), LEGACY_MIGRATION_MARKER),
    );
  }

  function markLegacyMigrationComplete() {
    fsModule.mkdirSync(legacyDirectory(), { mode: 0o700, recursive: true });
    fsModule.writeFileSync(
      pathModule.join(legacyDirectory(), LEGACY_MIGRATION_MARKER),
      "migrated\n",
      { mode: 0o600 },
    );
  }

  return {
    canSaveSession,
    clearStoredAccessToken,
    ensureDaemonSession,
    ensureStoredSession,
    getCredentialDirectoryPath: zenithTeamHomeDirectory,
    getDaemonSessionPath: daemonSessionPath,
    getSessionStorePath: sessionStorePath,
    installStoredSession,
    readStoredAccessToken,
    readStoredSession,
    signOutStoredSession,
  };
}

function executeCommand({ args, environment, executablePath, input, runFile }) {
  return new Promise((resolve, reject) => {
    const child = runFile(
      executablePath,
      args,
      {
        encoding: "utf8",
        env: environment,
        maxBuffer: DESKTOP_SESSION_MAX_BUFFER_BYTES,
        shell: false,
        timeout: DESKTOP_SESSION_TIMEOUT_MS,
        windowsHide: true,
      },
      (error, stdout) => {
        if (error) reject(error);
        else resolve(stdout);
      },
    );
    if (input !== null) {
      if (!child?.stdin) {
        reject(new Error("Zenith Team CLI input is unavailable"));
        return;
      }
      child.stdin.end(input, "utf8");
    } else {
      child?.stdin?.end();
    }
  });
}

function parseDesktopSessionResponse(raw) {
  let value;
  try {
    value = JSON.parse(String(raw));
  } catch {
    throw new Error("Zenith Team Desktop session response is invalid");
  }
  if (
    !isPlainObject(value) ||
    value.ok !== true ||
    (value.session !== null && !isPlainObject(value.session))
  ) {
    throw new Error("Zenith Team Desktop session response is invalid");
  }
  return { ok: true };
}

// sessionFromBearerPayload reads a bearer session answer (refresh, or the
// daemon session) for the server it came from.
function sessionFromBearerPayload(payload, apiBaseUrl) {
  const session = payload?.session;
  if (session?.transport !== "bearer") {
    throw new Error("Zenith Team Desktop authentication response is invalid");
  }
  const normalized = normalizeSession({ ...session, apiBaseUrl });
  if (!normalized) {
    throw new Error("Zenith Team Desktop authentication response is invalid");
  }
  return normalized;
}

// publicSession is the session as the rest of the app sees it: everything
// but the refresh token, which stays in the store.
function publicSession(session) {
  const view = {
    apiBaseUrl: session.apiBaseUrl,
    accessToken: session.accessToken,
    accessExpiresAt: session.accessExpiresAt,
    refreshExpiresAt: session.refreshExpiresAt,
  };
  if (session.shared) view.shared = true;
  return view;
}

function needsRefresh(session) {
  if (!session.accessExpiresAt) return false;
  return Date.parse(session.accessExpiresAt) - REFRESH_SKEW_MS <= Date.now();
}

function normalizeSession(
  value,
  fallbackApiBaseUrl = null,
  allowLegacyKeys = false,
) {
  if (!isPlainObject(value)) return null;
  const accessToken = normalizeString(
    value.accessToken ?? (allowLegacyKeys ? value.access_token : null),
  );
  const refreshToken = normalizeString(
    value.refreshToken ?? (allowLegacyKeys ? value.refresh_token : null),
  );
  const apiBaseUrl = normalizeApiBaseUrl(
    value.apiBaseUrl ??
      (allowLegacyKeys ? value.api_base_url : null) ??
      fallbackApiBaseUrl,
  );
  if (!accessToken || !refreshToken || !apiBaseUrl) return null;
  const session = {
    apiBaseUrl,
    accessToken,
    accessExpiresAt: normalizeTimestamp(
      value.accessExpiresAt ??
        (allowLegacyKeys ? value.access_expires_at : null),
    ),
    refreshToken,
    refreshExpiresAt: normalizeTimestamp(
      value.refreshExpiresAt ??
        (allowLegacyKeys ? value.refresh_expires_at : null),
    ),
  };
  const rotationId = normalizeString(value.pendingRotationId);
  if (rotationId) session.pendingRotationId = rotationId;
  return session;
}

function normalizeApiBaseUrl(value) {
  const normalized = normalizeString(value);
  if (!normalized) return null;
  try {
    const parsed = new URL(normalized.replace(/\/+$/u, ""));
    if (
      (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
      parsed.username ||
      parsed.password ||
      parsed.search ||
      parsed.hash ||
      parsed.pathname.replace(/\/+$/u, "") !== "/api/v1" ||
      (parsed.protocol === "http:" &&
        !["localhost", "127.0.0.1", "[::1]"].includes(parsed.hostname))
    ) {
      return null;
    }
    return parsed.href.replace(/\/$/u, "");
  } catch {
    return null;
  }
}

function defaultApiBaseUrl(variant) {
  return `${variant.apiOrigin}/api/v1`;
}

function legacyZenithTeamDirectoryName(variant) {
  switch (variant.environment) {
    case "dev":
      return ".zenith-team-dev";
    case "local":
      return ".zenith-team-local";
    default:
      return ".zenith-team";
  }
}

function ownerFromAccessToken(token) {
  const segments = String(token).split(".");
  if (segments.length !== 3) return null;
  try {
    const claims = JSON.parse(
      Buffer.from(segments[1], "base64url").toString("utf8"),
    );
    return typeof claims?.sub === "string" ? claims.sub : null;
  } catch {
    return null;
  }
}

function successfulSession(session) {
  return { ok: true, ...session };
}

function emptySession() {
  return {
    ok: true,
    apiBaseUrl: null,
    accessToken: null,
    accessExpiresAt: null,
    refreshExpiresAt: null,
  };
}

function failedSession(reason) {
  return { ok: false, reason };
}

function normalizeTimestamp(value) {
  const normalized = normalizeString(value);
  return normalized && Number.isFinite(Date.parse(normalized))
    ? normalized
    : null;
}

function normalizeString(value) {
  return typeof value === "string" ? value.trim() || null : null;
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function delay(milliseconds) {
  return new Promise((resolve) => {
    setTimeout(resolve, milliseconds).unref?.();
  });
}

// setSharedSessionInUseCheck tells the store how to know an older daemon
// still renews the session the app shared with it (the first launch after
// the update, while that daemon finishes its runs).
function setSharedSessionInUseCheck(callback) {
  sharedSessionInUse =
    typeof callback === "function" ? callback : async () => false;
}

module.exports = {
  createDesktopAuthTokenStore,
  canSaveSession: defaultStore.canSaveSession,
  clearStoredAccessToken: defaultStore.clearStoredAccessToken,
  ensureDaemonSession: defaultStore.ensureDaemonSession,
  ensureStoredSession: defaultStore.ensureStoredSession,
  getCredentialDirectoryPath: defaultStore.getCredentialDirectoryPath,
  getDaemonSessionPath: defaultStore.getDaemonSessionPath,
  getSessionStorePath: defaultStore.getSessionStorePath,
  installStoredSession: defaultStore.installStoredSession,
  readStoredAccessToken: defaultStore.readStoredAccessToken,
  readStoredSession: defaultStore.readStoredSession,
  setSharedSessionInUseCheck,
  signOutStoredSession: defaultStore.signOutStoredSession,
};
