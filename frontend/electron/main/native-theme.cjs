const { ipcMain, nativeTheme } = require("electron");

const GET_NATIVE_THEME_CHANNEL = "desktop:get-native-theme";
const NATIVE_THEME_UPDATED_CHANNEL = "desktop:native-theme-updated";

function registerNativeTheme({
  getMainWindow,
  ipcMain: ipcMainModule = ipcMain,
  nativeTheme: nativeThemeModule = nativeTheme,
} = {}) {
  if (!ipcMainModule || !nativeThemeModule) {
    return () => {};
  }

  ipcMainModule.handle(GET_NATIVE_THEME_CHANNEL, () =>
    createNativeThemeState(nativeThemeModule),
  );

  const sendThemeState = () => {
    const mainWindow = getMainWindow?.();

    if (!mainWindow || mainWindow.isDestroyed()) {
      return;
    }

    mainWindow.webContents.send(
      NATIVE_THEME_UPDATED_CHANNEL,
      createNativeThemeState(nativeThemeModule),
    );
  };

  nativeThemeModule.on?.("updated", sendThemeState);

  return () => {
    nativeThemeModule.off?.("updated", sendThemeState);
  };
}

function createNativeThemeState(nativeThemeModule) {
  const shouldUseDarkColors = Boolean(nativeThemeModule.shouldUseDarkColors);

  return {
    colorScheme: shouldUseDarkColors ? "dark" : "light",
    shouldDifferentiateWithoutColor: Boolean(
      nativeThemeModule.shouldDifferentiateWithoutColor,
    ),
    shouldUseDarkColors,
    shouldUseHighContrastColors: Boolean(
      nativeThemeModule.shouldUseHighContrastColors,
    ),
    shouldUseInvertedColorScheme: Boolean(
      nativeThemeModule.shouldUseInvertedColorScheme,
    ),
    inForcedColorsMode: Boolean(nativeThemeModule.inForcedColorsMode),
    prefersReducedTransparency: Boolean(
      nativeThemeModule.prefersReducedTransparency,
    ),
    themeSource: normalizeThemeSource(nativeThemeModule.themeSource),
  };
}

function normalizeThemeSource(themeSource) {
  if (themeSource === "light" || themeSource === "dark") {
    return themeSource;
  }

  return "system";
}

module.exports = {
  GET_NATIVE_THEME_CHANNEL,
  NATIVE_THEME_UPDATED_CHANNEL,
  createNativeThemeState,
  registerNativeTheme,
};
