const { spawnSync } = require("node:child_process");
const path = require("node:path");
const { FusesPlugin } = require("@electron-forge/plugin-fuses");
const { FuseV1Options, FuseVersion } = require("@electron/fuses");
const { getDesktopVariant } = require("./electron/shared/desktop-variant.cjs");

const packageVersion = require("./package.json").version;
const desktopVariant = getDesktopVariant(process.env);
const appIconRootPath = path.join(__dirname, "electron", "assets", "icons");
const targetPlatform =
  process.env.ZENITH_TEAM_DESKTOP_TARGET_PLATFORM ?? process.platform;
const appIconPath = getPackagerIconPath(targetPlatform);
const zenithTeamResourcePath = path.join(
  __dirname,
  ".vite",
  "bin",
  targetPlatform === "win32" ? "zen.exe" : "zen",
);
const zenithTeamMCPResourcePath = path.join(
  __dirname,
  ".vite",
  "bin",
  targetPlatform === "win32" ? "zenith-team-mcp.exe" : "zenith-team-mcp",
);
const licensePath = path.join(__dirname, "..", "LICENSE");
const thirdPartyNoticesPath = path.join(
  __dirname,
  "..",
  "THIRD_PARTY_NOTICES.md",
);
const macEntitlementsPath = path.join(
  __dirname,
  "electron",
  "assets",
  "entitlements.mac.plist",
);
const shouldSignMac =
  targetPlatform === "darwin" &&
  process.env.ZENITH_TEAM_DESKTOP_MAC_SIGN === "true";
const shouldSignWindows =
  targetPlatform === "win32" &&
  process.env.ZENITH_TEAM_DESKTOP_WINDOWS_SIGN === "true";
const macNotarizeConfig =
  targetPlatform === "darwin" ? getMacNotarizeConfig(process.env) : null;

if (macNotarizeConfig && !shouldSignMac) {
  throw new Error(
    "ZENITH_TEAM_DESKTOP_MAC_NOTARIZE=true requires ZENITH_TEAM_DESKTOP_MAC_SIGN=true.",
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
    extraResource: [
      zenithTeamResourcePath,
      zenithTeamMCPResourcePath,
      // The MIT license and third-party notices travel with every install.
      licensePath,
      thirdPartyNoticesPath,
    ],
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
            identity: process.env.ZENITH_TEAM_DESKTOP_MAC_SIGN_IDENTITY,
            // @electron/osx-sign 1.x reads entitlements and the hardened
            // runtime only per file; top-level keys are ignored.
            optionsForFile: () => ({
              entitlements: macEntitlementsPath,
              hardenedRuntime: true,
            }),
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
  hooks: {
    postMake: async (_forgeConfig, makeResults) => {
      if (shouldSignMac) {
        for (const { artifacts } of makeResults) {
          for (const artifactPath of artifacts) {
            if (path.extname(artifactPath) === ".dmg") {
              await signMacDiskImage(artifactPath);
            }
          }
        }
      }

      return makeResults;
    },
  },
};

// The app inside is already signed, notarized and stapled. The disk image
// around it gets the same, so Gatekeeper can check the download itself, even
// offline. This runs before finalize-artifacts writes the checksums.
async function signMacDiskImage(dmgPath) {
  const result = spawnSync(
    "codesign",
    [
      "--sign",
      process.env.ZENITH_TEAM_DESKTOP_MAC_SIGN_IDENTITY,
      "--timestamp",
      dmgPath,
    ],
    { stdio: "inherit" },
  );

  if (result.error) {
    throw result.error;
  }

  if (result.status !== 0) {
    throw new Error(`codesign could not sign ${dmgPath}.`);
  }

  if (macNotarizeConfig) {
    // notarize() also staples the ticket to the disk image.
    const { notarize } = await import("@electron/notarize");
    await notarize({ appPath: dmgPath, ...macNotarizeConfig });
  }
}

function getMacNotarizeConfig(env) {
  if (env.ZENITH_TEAM_DESKTOP_MAC_NOTARIZE !== "true") {
    return null;
  }

  if (env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE) {
    return {
      keychainProfile: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE,
      ...(env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN
        ? { keychain: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN }
        : {}),
    };
  }

  if (
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY &&
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY_ID &&
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_ISSUER
  ) {
    return {
      appleApiKey: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY,
      appleApiKeyId: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY_ID,
      appleApiIssuer: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_ISSUER,
    };
  }

  if (
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_APPLE_ID &&
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_APP_PASSWORD &&
    env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_TEAM_ID
  ) {
    return {
      appleId: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_APPLE_ID,
      appleIdPassword: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_APP_PASSWORD,
      teamId: env.ZENITH_TEAM_DESKTOP_MAC_NOTARY_TEAM_ID,
    };
  }

  throw new Error(
    "ZENITH_TEAM_DESKTOP_MAC_NOTARIZE=true requires keychain, API key, or Apple ID notarization credentials.",
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
  if (env.ZENITH_TEAM_DESKTOP_WINDOWS_CERTIFICATE_FILE) {
    return {
      certificateFile: env.ZENITH_TEAM_DESKTOP_WINDOWS_CERTIFICATE_FILE,
      certificatePassword: env.ZENITH_TEAM_DESKTOP_WINDOWS_CERTIFICATE_PASSWORD,
    };
  }

  if (env.ZENITH_TEAM_DESKTOP_WINDOWS_SIGN_WITH_PARAMS) {
    return {
      signWithParams: env.ZENITH_TEAM_DESKTOP_WINDOWS_SIGN_WITH_PARAMS,
    };
  }

  throw new Error(
    "ZENITH_TEAM_DESKTOP_WINDOWS_SIGN=true requires ZENITH_TEAM_DESKTOP_WINDOWS_CERTIFICATE_FILE or ZENITH_TEAM_DESKTOP_WINDOWS_SIGN_WITH_PARAMS.",
  );
}
