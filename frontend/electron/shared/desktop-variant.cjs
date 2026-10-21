const DESKTOP_ENVIRONMENTS = new Set(["dev", "local", "prod"]);

// Cloud builds (dev, prod) take their API origin from the release env, so a
// build never points at a server its maker did not choose.
const DESKTOP_ORIGIN_ENV_KEYS = {
  dev: "ZENITH_TEAM_DESKTOP_DEV_ORIGIN",
  prod: "ZENITH_TEAM_DESKTOP_PROD_ORIGIN",
};
const DESKTOP_RELEASES_URL =
  "https://github.com/Intelligent-Internet/zenith-team/releases";

const DESKTOP_VARIANTS = {
  dev: {
    apiOrigin: null,
    appBundleId: "com.ii.zenith-team.dev",
    appName: "Zenith Team Dev",
    appUserModelId: {
      darwin: "com.ii.zenith-team.dev",
      default: "com.ii.zenith-team.dev",
      win32: "com.squirrel.zenithteamdev.ZenithTeamDev",
    },
    artifactEnvironment: "dev",
    deepLinkProtocol: "zenith-team-dev",
    daemonHealthPort: 19715,
    environment: "dev",
    executableName: "Zenith Team Dev",
    manualUpdateUrl: DESKTOP_RELEASES_URL,
    oauthLoopbackPort: 42814,
    squirrelName: "zenithteamdev",
    userDataDirectoryName: "Zenith Team Dev",
  },
  local: {
    apiOrigin: "http://127.0.0.1:8788",
    appBundleId: "com.ii.zenith-team.local",
    appName: "Zenith Team",
    appUserModelId: {
      darwin: "com.ii.zenith-team.local",
      default: "com.ii.zenith-team.local",
      win32: "com.squirrel.zenithteamlocal.ZenithTeamLocal",
    },
    artifactEnvironment: "local",
    deepLinkProtocol: "zenith-team",
    daemonHealthPort: 19716,
    environment: "local",
    executableName: "Zenith Team",
    manualUpdateUrl: "http://127.0.0.1:5173",
    oauthLoopbackPort: 42813,
    squirrelName: "zenithteamlocal",
    userDataDirectoryName: "Zenith Team Local",
  },
  prod: {
    apiOrigin: null,
    appBundleId: "com.ii.zenith-team",
    appName: "Zenith Team",
    appUserModelId: {
      darwin: "com.ii.zenith-team",
      default: "com.ii.zenith-team",
      win32: "com.squirrel.zenithteam.ZenithTeam",
    },
    artifactEnvironment: "prod",
    deepLinkProtocol: "zenith-team",
    daemonHealthPort: 19714,
    environment: "prod",
    executableName: "Zenith Team",
    manualUpdateUrl: DESKTOP_RELEASES_URL,
    oauthLoopbackPort: 42813,
    squirrelName: "zenithteam",
    userDataDirectoryName: "Zenith Team",
  },
};

function normalizeDesktopEnvironment(value, fallback = "prod") {
  const normalized =
    typeof value === "string" ? value.trim().toLowerCase() : "";

  return DESKTOP_ENVIRONMENTS.has(normalized) ? normalized : fallback;
}

function getDesktopVariant(env = process.env, { fallback = "prod" } = {}) {
  const environment = normalizeDesktopEnvironment(
    env?.ZENITH_TEAM_DESKTOP_ENV,
    fallback,
  );
  const variant = DESKTOP_VARIANTS[environment];
  const originKey = DESKTOP_ORIGIN_ENV_KEYS[environment];
  const configuredOrigin = originKey ? env?.[originKey]?.trim() : "";

  return configuredOrigin
    ? { ...variant, apiOrigin: configuredOrigin }
    : variant;
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
