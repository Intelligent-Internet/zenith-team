const DESKTOP_ENVIRONMENTS = new Set(["dev", "local", "prod"]);

const DESKTOP_VARIANTS = {
  dev: {
    apiOrigin: "https://api-dev.example.com",
    studioApiOrigin: "https://studio-dev.example.com",
    appBundleId: "com.ii.boardly.dev",
    appName: "Zenith Team Dev",
    appUserModelId: {
      darwin: "com.ii.boardly.dev",
      default: "com.ii.boardly.dev",
      win32: "com.squirrel.boardlydev.BoardlyDev",
    },
    artifactEnvironment: "dev",
    deepLinkProtocol: "boardly-dev",
    daemonHealthPort: 19715,
    environment: "dev",
    executableName: "Boardly Dev",
    gcsPrefix: "boardly-dev",
    manualUpdateUrl: "https://dev.example.com",
    oauthLoopbackPort: 42814,
    squirrelName: "boardlydev",
    userDataDirectoryName: "Boardly Dev",
  },
  local: {
    apiOrigin: "http://127.0.0.1:8788",
    studioApiOrigin: "https://api-agent.ii.inc",
    appBundleId: "com.ii.boardly.local",
    appName: "Zenith Team",
    appUserModelId: {
      darwin: "com.ii.boardly.local",
      default: "com.ii.boardly.local",
      win32: "com.squirrel.boardlylocal.BoardlyLocal",
    },
    artifactEnvironment: "local",
    deepLinkProtocol: "boardly",
    daemonHealthPort: 19716,
    environment: "local",
    executableName: "Boardly",
    gcsPrefix: null,
    manualUpdateUrl: "http://127.0.0.1:5173",
    oauthLoopbackPort: 42813,
    squirrelName: "boardlylocal",
    userDataDirectoryName: "Boardly Local",
  },
  prod: {
    apiOrigin: "https://api.example.com",
    studioApiOrigin: "https://api-agent.ii.inc",
    appBundleId: "com.ii.boardly",
    appName: "Zenith Team",
    appUserModelId: {
      darwin: "com.ii.boardly",
      default: "com.ii.boardly",
      win32: "com.squirrel.boardly.Boardly",
    },
    artifactEnvironment: "prod",
    deepLinkProtocol: "boardly",
    daemonHealthPort: 19714,
    environment: "prod",
    executableName: "Boardly",
    gcsPrefix: "boardly",
    manualUpdateUrl: "https://app.example.com",
    oauthLoopbackPort: 42813,
    squirrelName: "boardly",
    userDataDirectoryName: "Boardly",
  },
};

function normalizeDesktopEnvironment(value, fallback = "prod") {
  const normalized =
    typeof value === "string" ? value.trim().toLowerCase() : "";

  return DESKTOP_ENVIRONMENTS.has(normalized) ? normalized : fallback;
}

function getDesktopVariant(env = process.env, { fallback = "prod" } = {}) {
  const environment = normalizeDesktopEnvironment(
    env?.BOARDLY_DESKTOP_ENV,
    fallback,
  );

  return DESKTOP_VARIANTS[environment];
}

function getDesktopAppUserModelId(variant, platform = process.platform) {
  return variant.appUserModelId[platform] ?? variant.appUserModelId.default;
}

module.exports = {
  DESKTOP_ENVIRONMENTS,
  DESKTOP_VARIANTS,
  getDesktopAppUserModelId,
  getDesktopVariant,
  normalizeDesktopEnvironment,
};
