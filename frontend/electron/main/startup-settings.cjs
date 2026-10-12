const { app, ipcMain } = require("electron");

const GET_STARTUP_SETTINGS_CHANNEL = "desktop:get-startup-settings";
const SET_OPEN_AT_LOGIN_CHANNEL = "desktop:set-open-at-login";

function registerDesktopStartupSettings({
  app: appModule = app,
  ipcMain: ipcMainModule = ipcMain,
  platform = process.platform,
} = {}) {
  if (!ipcMainModule || typeof ipcMainModule.handle !== "function") {
    return;
  }

  const startupSettings = createDesktopStartupSettings({
    app: appModule,
    platform,
  });

  ipcMainModule.handle(GET_STARTUP_SETTINGS_CHANNEL, () =>
    startupSettings.getState(),
  );
  ipcMainModule.handle(SET_OPEN_AT_LOGIN_CHANNEL, (_event, enabled) =>
    startupSettings.setOpenAtLogin(enabled),
  );
}

function createDesktopStartupSettings({ app, platform = process.platform } = {}) {
  function isSupported() {
    return (
      (platform === "darwin" || platform === "win32") &&
      typeof app?.getLoginItemSettings === "function" &&
      typeof app?.setLoginItemSettings === "function"
    );
  }

  function getState() {
    if (!isSupported()) {
      return createUnsupportedStartupSettingsState(platform);
    }

    try {
      return normalizeLoginItemSettingsState(
        app.getLoginItemSettings(),
        platform,
      );
    } catch (error) {
      return {
        openAsHidden: false,
        openAtLogin: false,
        reason: getErrorMessage(error),
        status: "unavailable",
        supported: false,
      };
    }
  }

  function setOpenAtLogin(enabled) {
    if (typeof enabled !== "boolean") {
      return {
        ok: false,
        reason: "Open at login setting must be enabled or disabled.",
        state: getState(),
      };
    }

    if (!isSupported()) {
      return {
        ok: false,
        reason: "Open at login is not supported on this platform.",
        state: getState(),
      };
    }

    try {
      app.setLoginItemSettings({
        openAsHidden: enabled,
        openAtLogin: enabled,
      });
      return { ok: true, state: getState() };
    } catch (error) {
      return {
        ok: false,
        reason: getErrorMessage(error),
        state: getState(),
      };
    }
  }

  return {
    getState,
    setOpenAtLogin,
  };
}

function normalizeLoginItemSettingsState(settings, platform = process.platform) {
  const openAtLogin = Boolean(settings?.openAtLogin);
  const status = normalizeLoginItemStatus(settings?.status, openAtLogin);
  const reason =
    status === "requires-approval"
      ? "macOS requires approval in System Settings before Zenith Team can open at login."
      : null;

  return {
    openAsHidden: Boolean(settings?.openAsHidden),
    openAtLogin,
    reason,
    status,
    supported: platform === "darwin" || platform === "win32",
  };
}

function normalizeLoginItemStatus(status, openAtLogin) {
  if (
    status === "enabled" ||
    status === "not-found" ||
    status === "not-registered" ||
    status === "requires-approval"
  ) {
    if (status === "not-found" || status === "not-registered") {
      return "disabled";
    }

    return status;
  }

  return openAtLogin ? "enabled" : "disabled";
}

function createUnsupportedStartupSettingsState(platform = process.platform) {
  return {
    openAsHidden: false,
    openAtLogin: false,
    reason:
      platform === "linux"
        ? "Open at login is not supported for the current Linux package."
        : "Open at login is not supported on this platform.",
    status: "unsupported",
    supported: false,
  };
}

function getErrorMessage(error) {
  return error instanceof Error && error.message
    ? error.message
    : "Desktop startup settings failed.";
}

module.exports = {
  GET_STARTUP_SETTINGS_CHANNEL,
  SET_OPEN_AT_LOGIN_CHANNEL,
  createDesktopStartupSettings,
  normalizeLoginItemSettingsState,
  registerDesktopStartupSettings,
};
