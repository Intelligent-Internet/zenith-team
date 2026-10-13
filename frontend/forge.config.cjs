const path = require("node:path");
const { FusesPlugin } = require("@electron-forge/plugin-fuses");
const { FuseV1Options, FuseVersion } = require("@electron/fuses");
const { getDesktopVariant } = require("./electron/shared/desktop-variant.cjs");

const packageVersion = require("./package.json").version;
const desktopVariant = getDesktopVariant(process.env);
const appIconRootPath = path.join(__dirname, "electron", "assets", "icons");
const targetPlatform =
  process.env.BOARDLY_DESKTOP_TARGET_PLATFORM ?? process.platform;
const appIconPath = getPackagerIconPath(targetPlatform);
const boardlyResourcePath = path.join(
  __dirname,
  ".vite",
  "bin",
  targetPlatform === "win32" ? "zen.exe" : "zen",
);
const boardlyMCPResourcePath = path.join(
  __dirname,
  ".vite",
  "bin",
  targetPlatform === "win32" ? "boardly-mcp.exe" : "boardly-mcp",
);
const macEntitlementsPath = path.join(
  __dirname,
  "electron",
  "assets",
  "entitlements.mac.plist",
);
const shouldSignMac =
  targetPlatform === "darwin" &&
  process.env.BOARDLY_DESKTOP_MAC_SIGN === "true";
const shouldSignWindows =
  targetPlatform === "win32" &&
  process.env.BOARDLY_DESKTOP_WINDOWS_SIGN === "true";
const macNotarizeConfig =
  targetPlatform === "darwin" ? getMacNotarizeConfig(process.env) : null;

if (macNotarizeConfig && !shouldSignMac) {
  throw new Error(
    "BOARDLY_DESKTOP_MAC_NOTARIZE=true requires BOARDLY_DESKTOP_MAC_SIGN=true.",
  );
}

module.exports = {
  packagerConfig: {
    name: desktopVariant.appName,
    executableName: desktopVariant.executableName,
    appBundleId: desktopVariant.appBundleId,
    appCategoryType: "public.app-category.productivity",
    extendInfo: {
      NSMicrophoneUsageDescription:
        "Zenith Team uses the microphone for Huddles.",
      NSCameraUsageDescription:
        "Zenith Team uses the camera when you turn on video in a Huddle.",
      NSScreenCaptureUsageDescription:
        "Zenith Team shares a screen or window only when you choose one in a Huddle.",
      NSAudioCaptureUsageDescription:
        "Zenith Team shares system audio only when you choose to share it in a Huddle.",
    },
    asar: true,
    extraResource: [boardlyResourcePath, boardlyMCPResourcePath],
    icon: appIconPath,
    protocols: [
      {
        name: desktopVariant.appName,
        schemes: [desktopVariant.deepLinkProtocol],
      },
    ],
    ...(shouldSignMac
      ? {
          osxSign: {
            entitlements: macEntitlementsPath,
            "entitlements-inherit": macEntitlementsPath,
            "hardened-runtime": true,
            identity: process.env.BOARDLY_DESKTOP_MAC_SIGN_IDENTITY,
          },
        }
      : {}),
    ...(macNotarizeConfig ? { osxNotarize: macNotarizeConfig } : {}),
    ignore: [
      /^\/\.DS_Store$/,
      /^\/\.env($|\.)/,
      /^\/.*\.rdb$/,
      /^\/\.gitignore$/,
      /^\/\.npmrc$/,
      /^\/\.vite($|\/)/,
      /^\/\.vscode($|\/)/,
      /^\/node_modules\/\.vite($|\/)/,
      /^\/node_modules\/\.vite-temp($|\/)/,
      /^\/src($|\/)/,
      /^\/functions($|\/)/,
      /^\/e2e($|\/)/,
      /^\/scripts($|\/)/,
      /^\/coverage($|\/)/,
      /^\/playwright-report($|\/)/,
      /^\/test-results($|\/)/,
      /^\/public\/downloads($|\/)/,
      /^\/dist\/downloads($|\/)/,
      /^\/.*\.spec\.d\.ts$/,
      /^\/.*\.spec\.[cm]?[jt]sx?$/,
      /^\/.*\.test\.d\.ts$/,
      /^\/.*\.test\.[cm]?[jt]sx?$/,
    ],
  },
  rebuildConfig: {},
  makers: [
    {
      name: "@electron-forge/maker-dmg",
      platforms: ["darwin"],
      config: {
        format: "UDZO",
        icon: path.join(appIconRootPath, "macos", "icon.icns"),
        overwrite: true,
      },
    },
    {
      name: "@electron-forge/maker-squirrel",
      platforms: ["win32"],
      config: (arch) => ({
        name: desktopVariant.squirrelName,
        authors: "ii.inc",
        description: `${desktopVariant.appName} desktop app`,
        exe: `${desktopVariant.executableName}.exe`,
        noMsi: true,
        setupExe: `Zenith-Team-${desktopVariant.artifactEnvironment}-win32-${arch}-${packageVersion}-setup.exe`,
        setupIcon: path.join(appIconRootPath, "windows", "icon.ico"),
        ...(shouldSignWindows ? getWindowsSigningConfig(process.env) : {}),
      }),
    },
    {
      name: "@electron-forge/maker-zip",
      platforms: ["darwin", "win32", "linux"],
    },
  ],
  plugins: [
    new FusesPlugin({
      version: FuseVersion.V1,
      [FuseV1Options.RunAsNode]: false,
      [FuseV1Options.EnableCookieEncryption]: true,
      [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
      [FuseV1Options.EnableNodeCliInspectArguments]: false,
      [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
      [FuseV1Options.OnlyLoadAppFromAsar]: true,
      [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
    }),
  ],
};

function getMacNotarizeConfig(env) {
  if (env.BOARDLY_DESKTOP_MAC_NOTARIZE !== "true") {
    return null;
  }

  if (env.BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE) {
    return {
      keychainProfile: env.BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE,
      ...(env.BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN
        ? { keychain: env.BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN }
        : {}),
    };
  }

  if (
    env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY &&
    env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY_ID &&
    env.BOARDLY_DESKTOP_MAC_NOTARY_API_ISSUER
  ) {
    return {
      appleApiKey: env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY,
      appleApiKeyId: env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY_ID,
      appleApiIssuer: env.BOARDLY_DESKTOP_MAC_NOTARY_API_ISSUER,
    };
  }

  if (
    env.BOARDLY_DESKTOP_MAC_NOTARY_APPLE_ID &&
    env.BOARDLY_DESKTOP_MAC_NOTARY_APP_PASSWORD &&
    env.BOARDLY_DESKTOP_MAC_NOTARY_TEAM_ID
  ) {
    return {
      appleId: env.BOARDLY_DESKTOP_MAC_NOTARY_APPLE_ID,
      appleIdPassword: env.BOARDLY_DESKTOP_MAC_NOTARY_APP_PASSWORD,
      teamId: env.BOARDLY_DESKTOP_MAC_NOTARY_TEAM_ID,
    };
  }

  throw new Error(
    "BOARDLY_DESKTOP_MAC_NOTARIZE=true requires keychain, API key, or Apple ID notarization credentials.",
  );
}

function getPackagerIconPath(platform) {
  if (platform === "darwin") {
    return path.join(appIconRootPath, "macos", "icon");
  }

  if (platform === "win32") {
    return path.join(appIconRootPath, "windows", "icon");
  }

  return path.join(appIconRootPath, "linux", "icons", "512x512.png");
}

function getWindowsSigningConfig(env) {
  if (env.BOARDLY_DESKTOP_WINDOWS_CERTIFICATE_FILE) {
    return {
      certificateFile: env.BOARDLY_DESKTOP_WINDOWS_CERTIFICATE_FILE,
      certificatePassword: env.BOARDLY_DESKTOP_WINDOWS_CERTIFICATE_PASSWORD,
    };
  }

  if (env.BOARDLY_DESKTOP_WINDOWS_SIGN_WITH_PARAMS) {
    return {
      signWithParams: env.BOARDLY_DESKTOP_WINDOWS_SIGN_WITH_PARAMS,
    };
  }

  throw new Error(
    "BOARDLY_DESKTOP_WINDOWS_SIGN=true requires BOARDLY_DESKTOP_WINDOWS_CERTIFICATE_FILE or BOARDLY_DESKTOP_WINDOWS_SIGN_WITH_PARAMS.",
  );
}
