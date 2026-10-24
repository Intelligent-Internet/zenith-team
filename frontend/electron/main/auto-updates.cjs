const https = require("node:https");
const { getDesktopNativeUpdatePolicy } = require("./update-policy.cjs");
const {
  createDesktopUpdatePreferenceStore,
} = require("./update-preferences.cjs");
const { stageStaticUpdate } = require("./update-download.cjs");

const AUTOMATIC_UPDATE_INITIAL_DELAY_MS = 10_000;
const AUTOMATIC_UPDATE_INTERVAL_MS = 4 * 60 * 60 * 1000;
const CHECK_FOR_UPDATES_CHANNEL = "desktop:check-for-updates";
const DOWNLOAD_UPDATE_CHANNEL = "desktop:download-update";
const GET_UPDATE_STATE_CHANNEL = "desktop:get-update-state";
const RESTART_TO_UPDATE_CHANNEL = "desktop:restart-to-update";
const SET_AUTOMATIC_UPDATES_CHANNEL = "desktop:set-automatic-updates";
const SHOW_UPDATE_DIALOG_CHANNEL = "desktop:show-update-dialog";
const UPDATE_STATE_UPDATED_CHANNEL = "desktop:update-state-updated";
let registeredIpc = false;

function registerDesktopAutoUpdates(options = {}) {
  const electron = options.electron ?? require("electron");
  const app = options.app ?? electron.app;

  const updater = createDesktopAutoUpdater({
    app,
    arch: options.arch ?? process.arch,
    argv: options.argv ?? process.argv,
    autoUpdater: options.autoUpdater ?? electron.autoUpdater,
    env: options.env ?? process.env,
    getMainWindow: options.getMainWindow,
    onBeforeQuitForUpdate: options.onBeforeQuitForUpdate,
    onUpdateDownloaded: options.onUpdateDownloaded,
    onUpdateDownloadFailed: options.onUpdateDownloadFailed,
    platform: options.platform ?? process.platform,
    stageUpdate: options.stageUpdate,
    preferenceStore:
      options.preferenceStore ??
      createDesktopUpdatePreferenceStore({
        app,
      }),
  });

  registerDesktopUpdateIpcHandlers({
    ipcMain: options.ipcMain ?? electron.ipcMain,
    onManualCheckResult: options.onManualCheckResult,
    updater,
  });
  updater.startAutomaticChecks();

  return updater;
}

function createDesktopAutoUpdater({
  app,
  arch = process.arch,
  argv = process.argv,
  autoUpdater,
  env = process.env,
  getMainWindow = () => null,
  loadUpdateInfo = loadStaticUpdateFeedInfo,
  stageUpdate = stageStaticUpdate,
  onBeforeQuitForUpdate = () => undefined,
  onUpdateDownloaded = () => undefined,
  onUpdateDownloadFailed = () => undefined,
  platform = process.platform,
  preferenceStore,
  scheduleTimeout = setTimeout,
  clearScheduledTimeout = clearTimeout,
  automaticUpdateInitialDelayMs = AUTOMATIC_UPDATE_INITIAL_DELAY_MS,
  automaticUpdateIntervalMs = AUTOMATIC_UPDATE_INTERVAL_MS,
} = {}) {
  const policy = getDesktopNativeUpdatePolicy({
    appVersion: app?.getVersion?.(),
    arch,
    argv,
    env,
    isPackaged: Boolean(app?.isPackaged),
    platform,
  });

  let registrationError = null;
  let automaticUpdateTimer = null;
  let automaticUpdatesEnabled =
    policy.enabled && readAutomaticUpdatesPreference(preferenceStore);
  let downloadRequested = false;
  let stagedUpdate = null;
  let state = createInitialUpdateState(policy, automaticUpdatesEnabled);

  if (policy.version) {
    try {
      preferenceStore?.setInstalledVersion?.(policy.version);
    } catch {
      // app.getVersion() remains authoritative if local history cannot be saved.
    }
  }

  if (policy.enabled) {
    try {
      autoUpdater.setFeedURL({ url: policy.feedUrl });
      registerAutoUpdaterEvents(autoUpdater, {
        applyUpdateState,
        isDownloadRequested: () => downloadRequested,
        onBeforeQuitForUpdate,
        onUpdateDownloaded,
        onUpdateDownloadFailed,
        onDownloadFinished: () => {
          downloadRequested = false;
          disposeStagedUpdate();
        },
      });
    } catch (error) {
      registrationError = getErrorMessage(error);
      applyUpdateState({
        errorMessage: registrationError,
        status: "failed",
      });
    }
  }

  async function checkForUpdates() {
    if (!policy.enabled) {
      applyUpdateState({
        errorMessage: policy.reason,
        status: "manual",
      });
      return {
        manualUrl: policy.manualUrl,
        mode: "manual",
        ok: false,
        reason: policy.reason,
      };
    }

    if (
      state.status === "available" ||
      state.status === "checking" ||
      state.status === "downloading" ||
      state.status === "downloaded" ||
      state.status === "restart-required"
    ) {
      return {
        mode: "native",
        ok: true,
        status: state.status,
      };
    }

    if (registrationError) {
      applyUpdateState({
        errorMessage: registrationError,
        status: "failed",
      });
      return {
        mode: "native",
        ok: false,
        reason: registrationError,
      };
    }

    try {
      applyUpdateState({
        downloadPercent: null,
        errorMessage: null,
        status: "checking",
      });
      if (policy.staticFeed && policy.manifestUrl) {
        const updateInfo = await loadUpdateInfo(policy.manifestUrl);
        const release = getStaticUpdateRelease(updateInfo, policy);

        if (!isStaticUpdateAvailable(release, policy.version)) {
          applyUpdateState({
            availableVersion: release.version,
            downloadPercent: null,
            errorMessage: null,
            releaseDate: release.releaseDate,
            releaseName: release.releaseName,
            status: "up-to-date",
            updateAvailable: false,
            updateUrl: release.updateUrl,
          });
          return {
            mode: "native",
            ok: true,
            status: "up-to-date",
          };
        }

        applyUpdateState({
          availableVersion: release.version,
          downloadPercent: null,
          errorMessage: null,
          releaseDate: release.releaseDate,
          releaseName: release.releaseName,
          status: "available",
          updateAvailable: true,
          updateUrl: release.updateUrl,
        });
        return {
          mode: "native",
          ok: true,
          status: "available",
        };
      }

      await autoUpdater.checkForUpdates();
      return {
        mode: "native",
        ok: true,
      };
    } catch (error) {
      const reason = getErrorMessage(error);
      applyUpdateState({
        errorMessage: reason,
        status: "failed",
      });
      return {
        mode: "native",
        ok: false,
        reason,
      };
    }
  }

  async function downloadUpdate() {
    if (!policy.enabled) {
      return {
        manualUrl: policy.manualUrl,
        mode: "manual",
        ok: false,
        reason: policy.reason,
      };
    }

    if (
      state.status === "downloading" ||
      state.status === "downloaded" ||
      state.status === "restart-required"
    ) {
      return {
        mode: "native",
        ok: true,
        status: state.status,
      };
    }

    if (state.status !== "available") {
      return {
        mode: "native",
        ok: false,
        reason: "No desktop update is ready to download.",
      };
    }

    try {
      downloadRequested = true;
      applyUpdateState({
        downloadPercent: policy.staticFeed ? 0 : null,
        errorMessage: null,
        status: "downloading",
      });

      if (policy.staticFeed && state.updateUrl && state.availableVersion) {
        stagedUpdate = await stageUpdate({
          app,
          onProgress: (downloadPercent) => {
            if (state.status !== "downloading") {
              return;
            }
            applyUpdateState({ downloadPercent });
          },
          platform: policy.platform,
          release: {
            releaseDate: state.releaseDate,
            releaseName: state.releaseName,
            updateUrl: state.updateUrl,
            version: state.availableVersion,
          },
        });
        autoUpdater.setFeedURL({ url: stagedUpdate.feedUrl });
      }

      await autoUpdater.checkForUpdates();
      return {
        mode: "native",
        ok: true,
        status: "downloading",
      };
    } catch (error) {
      downloadRequested = false;
      disposeStagedUpdate();
      const reason = getErrorMessage(error, "Desktop update download failed.");
      applyUpdateState({
        errorMessage: reason,
        status: "failed",
      });
      return {
        mode: "native",
        ok: false,
        reason,
      };
    }
  }

  function startAutomaticChecks() {
    if (!automaticUpdatesEnabled || !policy.enabled) {
      return;
    }

    scheduleAutomaticUpdateCheck(automaticUpdateInitialDelayMs);
  }

  function stopAutomaticChecks() {
    if (automaticUpdateTimer !== null) {
      clearScheduledTimeout(automaticUpdateTimer);
      automaticUpdateTimer = null;
    }
  }

  async function setAutomaticUpdates(enabled) {
    if (typeof enabled !== "boolean") {
      return {
        ok: false,
        reason: "Automatic updates setting must be enabled or disabled.",
        state: getState(),
      };
    }

    if (!policy.enabled) {
      return {
        ok: false,
        reason: policy.reason,
        state: getState(),
      };
    }

    try {
      preferenceStore?.setAutomaticUpdatesEnabled?.(enabled);
    } catch (error) {
      return {
        ok: false,
        reason: getErrorMessage(
          error,
          "Automatic update preference could not be saved.",
        ),
        state: getState(),
      };
    }

    automaticUpdatesEnabled = enabled;
    applyUpdateState({ automaticUpdatesEnabled: enabled });
    stopAutomaticChecks();

    if (enabled) {
      void runAutomaticUpdateCheck();
    }

    return {
      ok: true,
      state: getState(),
    };
  }

  async function runAutomaticUpdateCheck() {
    if (!automaticUpdatesEnabled || !policy.enabled) {
      return;
    }

    await checkForUpdates();

    if (automaticUpdatesEnabled) {
      scheduleAutomaticUpdateCheck(automaticUpdateIntervalMs);
    }
  }

  function scheduleAutomaticUpdateCheck(delayMs) {
    stopAutomaticChecks();
    automaticUpdateTimer = scheduleTimeout(() => {
      automaticUpdateTimer = null;
      void runAutomaticUpdateCheck();
    }, delayMs);
    automaticUpdateTimer?.unref?.();
  }

  async function restartToUpdate() {
    if (state.status !== "restart-required" && state.status !== "downloaded") {
      return {
        ok: false,
        reason: "No downloaded update is ready to install.",
      };
    }

    try {
      // Electron's updater must own an uninterrupted quit. Finish cleanup first
      // so the app lifecycle does not defer and cancel the native relaunch.
      // false means the user kept unsaved changes open: nothing was torn down.
      if ((await onBeforeQuitForUpdate()) === false) {
        return {
          ok: false,
          reason: "Restart cancelled to keep your unsaved changes.",
        };
      }
      autoUpdater.quitAndInstall();
      return { ok: true };
    } catch (error) {
      applyUpdateState({
        errorMessage: getErrorMessage(error),
        status: "failed",
      });
      return {
        ok: false,
        reason: getErrorMessage(error),
      };
    }
  }

  function getState() {
    return state;
  }

  function applyUpdateState(patch) {
    state = normalizeDesktopUpdateState({
      ...state,
      ...patch,
      checkedAt:
        patch.status === "checking" ||
        patch.status === "available" ||
        patch.status === "up-to-date" ||
        patch.status === "failed"
          ? new Date().toISOString()
          : state.checkedAt,
      updateAvailable:
        patch.status === "available" ||
        patch.status === "downloading" ||
        patch.status === "downloaded" ||
        patch.status === "restart-required"
          ? true
          : patch.status === "up-to-date"
            ? false
            : state.updateAvailable,
    });
    sendUpdateState(getMainWindow?.(), state);
  }

  function disposeStagedUpdate() {
    const updateToDispose = stagedUpdate;
    stagedUpdate = null;

    if (!updateToDispose) {
      return;
    }

    try {
      autoUpdater.setFeedURL({ url: policy.feedUrl });
    } catch {
      // The next check will surface a feed-registration failure if restoration
      // is no longer possible during app shutdown.
    }
    void updateToDispose.dispose().catch(() => undefined);
  }

  return {
    checkForUpdates,
    downloadUpdate,
    getState,
    policy,
    restartToUpdate,
    setAutomaticUpdates,
    startAutomaticChecks,
    stopAutomaticChecks,
  };
}

function registerAutoUpdaterEvents(
  autoUpdater,
  {
    applyUpdateState,
    isDownloadRequested,
    onBeforeQuitForUpdate,
    onUpdateDownloaded,
    onUpdateDownloadFailed,
    onDownloadFinished,
  },
) {
  autoUpdater.on("checking-for-update", () => {
    applyUpdateState({
      status: isDownloadRequested() ? "downloading" : "checking",
    });
  });
  autoUpdater.on("update-available", () => {
    applyUpdateState({
      errorMessage: null,
      status: "downloading",
    });
  });
  autoUpdater.on("update-not-available", () => {
    onDownloadFinished();
    applyUpdateState({
      downloadPercent: null,
      errorMessage: null,
      status: "up-to-date",
    });
  });
  autoUpdater.on(
    "update-downloaded",
    (_event, _releaseNotes, releaseName, releaseDate, updateURL) => {
      onDownloadFinished();
      const availableVersion = normalizeReleaseVersion(releaseName);
      const normalizedUpdateUrl = normalizeNullableString(updateURL);
      applyUpdateState({
        ...(availableVersion ? { availableVersion } : {}),
        downloadPercent: 100,
        releaseDate: normalizeNullableString(
          releaseDate instanceof Date ? releaseDate.toISOString() : releaseDate,
        ),
        releaseName: normalizeNullableString(releaseName),
        status: "restart-required",
        ...(normalizedUpdateUrl && !isLoopbackUrl(normalizedUpdateUrl)
          ? { updateUrl: normalizedUpdateUrl }
          : {}),
      });
      onUpdateDownloaded();
    },
  );
  autoUpdater.on("error", (error) => {
    const wasDownloadRequested = isDownloadRequested();
    onDownloadFinished();
    applyUpdateState({
      errorMessage: getErrorMessage(error),
      status: "failed",
    });
    if (wasDownloadRequested) {
      onUpdateDownloadFailed(error);
    }
  });
  autoUpdater.on("before-quit-for-update", () => {
    void onBeforeQuitForUpdate();
  });
}

function createInitialUpdateState(policy, automaticUpdatesEnabled = false) {
  return normalizeDesktopUpdateState({
    availableVersion: null,
    automaticUpdatesEnabled,
    automaticUpdatesSupported: policy.enabled,
    checkedAt: null,
    downloadPercent: null,
    errorMessage: policy.enabled ? null : policy.reason,
    feedUrl: policy.enabled ? policy.feedUrl : null,
    installedVersion: policy.version,
    manualUrl: policy.manualUrl,
    policy: policy.enabled ? "native" : "manual",
    releaseDate: null,
    releaseName: null,
    status: policy.enabled ? "idle" : "manual",
    updateAvailable: false,
    updateUrl: null,
  });
}

function normalizeDesktopUpdateState(state) {
  return {
    availableVersion: normalizeNullableVersion(state.availableVersion),
    automaticUpdatesEnabled: Boolean(state.automaticUpdatesEnabled),
    automaticUpdatesSupported: Boolean(state.automaticUpdatesSupported),
    checkedAt: normalizeNullableString(state.checkedAt),
    downloadPercent: normalizeDownloadPercent(state.downloadPercent),
    errorMessage: normalizeNullableString(state.errorMessage),
    feedUrl: normalizeNullableString(state.feedUrl),
    installedVersion: normalizeNullableVersion(state.installedVersion),
    manualUrl: normalizeNullableString(state.manualUrl),
    policy: state.policy === "native" ? "native" : "manual",
    releaseDate: normalizeNullableString(state.releaseDate),
    releaseName: normalizeNullableString(state.releaseName),
    status: normalizeUpdateStatus(state.status),
    updateAvailable: Boolean(state.updateAvailable),
    updateUrl: normalizeNullableString(state.updateUrl),
  };
}

function normalizeDownloadPercent(value) {
  if (!Number.isFinite(value)) {
    return null;
  }

  return Math.min(100, Math.max(0, Math.round(value)));
}

function normalizeUpdateStatus(status) {
  return [
    "available",
    "checking",
    "downloaded",
    "downloading",
    "failed",
    "idle",
    "manual",
    "restart-required",
    "up-to-date",
  ].includes(status)
    ? status
    : "idle";
}

function normalizeNullableString(value) {
  if (typeof value !== "string") {
    return null;
  }

  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized || null;
}

function normalizeNullableVersion(value) {
  const normalized = normalizeReleaseVersion(value);
  return normalized || null;
}

function isLoopbackUrl(value) {
  if (!value) {
    return false;
  }

  try {
    const url = new URL(value);
    return url.hostname === "127.0.0.1" || url.hostname === "localhost";
  } catch {
    return false;
  }
}

function getStaticUpdateRelease(updateInfo, policy) {
  const platformRelease =
    updateInfo?.updates?.[policy.platform]?.[policy.arch] ?? updateInfo;
  const version = normalizeReleaseVersion(
    platformRelease?.version ?? updateInfo?.version ?? platformRelease?.name,
  );
  const updateUrl = normalizeNullableString(platformRelease?.url);

  if (!version || !updateUrl) {
    throw new Error("Static update manifest is missing this platform release.");
  }

  return {
    releaseDate: normalizeNullableString(
      platformRelease?.pub_date ?? updateInfo?.releasedAt,
    ),
    releaseName:
      normalizeNullableString(platformRelease?.name) ??
      `Zenith Team ${version}`,
    updateUrl,
    url: updateUrl,
    version,
  };
}

function sendUpdateState(mainWindow, updateState) {
  if (!mainWindow || mainWindow.isDestroyed?.()) {
    return;
  }

  mainWindow.webContents.send(UPDATE_STATE_UPDATED_CHANNEL, updateState);
}

function registerDesktopUpdateIpcHandlers({
  ipcMain,
  onManualCheckResult,
  updater,
}) {
  if (!ipcMain || registeredIpc) {
    return;
  }

  registeredIpc = true;
  ipcMain.handle(GET_UPDATE_STATE_CHANNEL, () => updater.getState());
  ipcMain.handle(CHECK_FOR_UPDATES_CHANNEL, async () => {
    const result = await updater.checkForUpdates();
    if (result.mode === "native") {
      await onManualCheckResult?.(result);
    }
    return result;
  });
  ipcMain.handle(DOWNLOAD_UPDATE_CHANNEL, () => updater.downloadUpdate());
  ipcMain.handle(RESTART_TO_UPDATE_CHANNEL, () => updater.restartToUpdate());
  ipcMain.handle(SET_AUTOMATIC_UPDATES_CHANNEL, (_event, enabled) =>
    updater.setAutomaticUpdates(enabled),
  );
}

function readAutomaticUpdatesPreference(preferenceStore) {
  try {
    return preferenceStore?.getAutomaticUpdatesEnabled?.() !== false;
  } catch {
    return true;
  }
}

function getErrorMessage(error, fallback = "Native update check failed.") {
  return error instanceof Error && error.message ? error.message : fallback;
}

function isStaticUpdateAvailable(updateInfo, currentVersion) {
  if (!updateInfo || typeof updateInfo.url !== "string") {
    throw new Error("Static update feed is missing an update URL.");
  }

  const updateVersion = normalizeReleaseVersion(
    updateInfo.version ?? updateInfo.name,
  );
  const normalizedCurrentVersion = normalizeReleaseVersion(currentVersion);

  if (!updateVersion || !normalizedCurrentVersion) {
    throw new Error("Static update feed is missing a comparable version.");
  }

  return compareReleaseVersions(updateVersion, normalizedCurrentVersion) > 0;
}

function normalizeReleaseVersion(value) {
  if (typeof value !== "string" && typeof value !== "number") {
    return "";
  }

  const normalized = String(value)
    .trim()
    .replace(/^v/i, "")
    .replace(/^Zenith Team\s+/i, "");

  return /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(normalized)
    ? normalized
    : "";
}

function compareReleaseVersions(current, next) {
  const currentParts = parseReleaseVersion(current);
  const nextParts = parseReleaseVersion(next);

  for (let index = 0; index < 3; index += 1) {
    const diff = currentParts.numbers[index] - nextParts.numbers[index];

    if (diff !== 0) {
      return diff;
    }
  }

  if (currentParts.prerelease === nextParts.prerelease) {
    return 0;
  }

  if (!currentParts.prerelease) {
    return 1;
  }

  if (!nextParts.prerelease) {
    return -1;
  }

  return comparePrereleases(currentParts.prerelease, nextParts.prerelease);
}

// SemVer precedence for pre-release identifiers: dot-separated, numeric ones
// compared as numbers and ranked below alphanumeric ones, and a shorter list
// lower when it is a prefix of the other. A plain string compare would rank
// dev.10 below dev.9, so a dev install would stop updating.
function comparePrereleases(current, next) {
  const currentIds = current.split(".");
  const nextIds = next.split(".");

  for (
    let index = 0;
    index < Math.max(currentIds.length, nextIds.length);
    index += 1
  ) {
    const currentId = currentIds[index];
    const nextId = nextIds[index];

    if (currentId === undefined) {
      return -1;
    }

    if (nextId === undefined) {
      return 1;
    }

    if (currentId === nextId) {
      continue;
    }

    const currentNumeric = /^\d+$/.test(currentId);
    const nextNumeric = /^\d+$/.test(nextId);

    if (currentNumeric && nextNumeric) {
      return Number(currentId) - Number(nextId);
    }

    if (currentNumeric !== nextNumeric) {
      return currentNumeric ? -1 : 1;
    }

    return currentId < nextId ? -1 : 1;
  }

  return 0;
}

function parseReleaseVersion(value) {
  // Build metadata (+...) never affects precedence.
  const [withoutBuild] = normalizeReleaseVersion(value).split("+", 1);
  const dash = withoutBuild.indexOf("-");
  const versionPart = dash === -1 ? withoutBuild : withoutBuild.slice(0, dash);
  const prerelease = dash === -1 ? "" : withoutBuild.slice(dash + 1);
  const numbers = versionPart
    .split(".")
    .slice(0, 3)
    .map((part) => Number.parseInt(part, 10));

  while (numbers.length < 3) {
    numbers.push(0);
  }

  return {
    numbers: numbers.map((part) => (Number.isFinite(part) ? part : 0)),
    prerelease,
  };
}

function loadStaticUpdateFeedInfo(feedUrl) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const request = https.get(feedUrl, (response) => {
      if (
        response.statusCode &&
        response.statusCode >= 300 &&
        response.statusCode < 400 &&
        response.headers.location
      ) {
        response.resume();
        loadStaticUpdateFeedInfo(
          new URL(response.headers.location, feedUrl).href,
        ).then(resolve, reject);
        return;
      }

      if (response.statusCode !== 200) {
        response.resume();
        reject(
          new Error(
            `Static update feed returned HTTP ${response.statusCode ?? "error"}.`,
          ),
        );
        return;
      }

      let body = "";
      response.setEncoding("utf8");
      response.on("data", (chunk) => {
        body += chunk;
        if (body.length > 1024 * 1024) {
          request.destroy(new Error("Static update feed is too large."));
        }
      });
      response.on("end", () => {
        if (settled) {
          return;
        }

        settled = true;
        try {
          resolve(JSON.parse(body));
        } catch (_error) {
          reject(new Error("Static update feed is not valid JSON."));
        }
      });
    });

    request.setTimeout(10000, () => {
      request.destroy(new Error("Static update feed timed out."));
    });
    request.on("error", (error) => {
      if (settled) {
        return;
      }

      settled = true;
      reject(error);
    });
  });
}

module.exports = {
  AUTOMATIC_UPDATE_INITIAL_DELAY_MS,
  AUTOMATIC_UPDATE_INTERVAL_MS,
  CHECK_FOR_UPDATES_CHANNEL,
  DOWNLOAD_UPDATE_CHANNEL,
  GET_UPDATE_STATE_CHANNEL,
  RESTART_TO_UPDATE_CHANNEL,
  SET_AUTOMATIC_UPDATES_CHANNEL,
  SHOW_UPDATE_DIALOG_CHANNEL,
  UPDATE_STATE_UPDATED_CHANNEL,
  compareReleaseVersions,
  createDesktopAutoUpdater,
  getStaticUpdateRelease,
  isStaticUpdateAvailable,
  loadStaticUpdateFeedInfo,
  registerDesktopUpdateIpcHandlers,
  registerDesktopAutoUpdates,
};
