const {
  DESKTOP_VARIANTS,
  getDesktopVariant,
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");
const DEFAULT_LOCAL_UPDATE_URL = DESKTOP_VARIANTS.local.manualUpdateUrl;
const DEFAULT_PROD_UPDATE_URL = DESKTOP_VARIANTS.prod.manualUpdateUrl;
const DEFAULT_RELEASE_CHANNEL = "stable";
const RELEASE_CHANNELS = new Set(["alpha", "beta", "stable"]);
const SUPPORTED_NATIVE_UPDATE_PLATFORMS = new Set(["darwin", "win32"]);
const WINDOWS_NATIVE_UPDATE_PACKAGES = new Set(["msix", "squirrel"]);

function getDesktopUpdateUrl(env = process.env) {
  const configuredUrl = env.ZENITH_TEAM_DESKTOP_UPDATE_URL?.trim();

  if (configuredUrl) {
    return configuredUrl;
  }

  return getDesktopVariant(env).manualUpdateUrl;
}

function getDesktopNativeUpdatePolicy({
  appVersion,
  arch = process.arch,
  argv = process.argv,
  env = process.env,
  isPackaged = false,
  platform = process.platform,
} = {}) {
  const manualUrl = getDesktopUpdateUrl(env);
  const version = normalizeUpdateSegment(appVersion);

  function manual(reason) {
    return {
      enabled: false,
      manualUrl,
      mode: "manual",
      reason,
      version: version || null,
    };
  }

  if (env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE !== "true") {
    return manual("Native auto-update is disabled.");
  }

  if (!isPackaged) {
    return manual("Native auto-update only runs in packaged builds.");
  }

  if (!SUPPORTED_NATIVE_UPDATE_PLATFORMS.has(platform)) {
    return manual("Native auto-update is only supported on macOS and Windows.");
  }

  if (!version) {
    return manual("Native auto-update requires an app version.");
  }

  if (platform === "darwin" && env.ZENITH_TEAM_DESKTOP_MAC_SIGN !== "true") {
    return manual("Native auto-update requires signed macOS builds.");
  }

  const packageFormat =
    platform === "win32" ? normalizeWindowsUpdatePackage(env) : "squirrel-mac";

  if (platform === "win32" && !WINDOWS_NATIVE_UPDATE_PACKAGES.has(packageFormat)) {
    return manual(
      "Native Windows auto-update requires Squirrel.Windows or MSIX packaging.",
    );
  }

  if (platform === "win32" && env.ZENITH_TEAM_DESKTOP_WINDOWS_SIGN !== "true") {
    return manual("Native auto-update requires signed Windows builds.");
  }

  if (platform === "win32" && isSquirrelFirstRun(argv)) {
    return manual(
      "Native Windows auto-update waits until after Squirrel first-run setup.",
    );
  }

  const feedUrl = getNativeUpdateFeedUrl(env, {
    appVersion: version,
    arch,
    platform,
  });
  const staticFeed = env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE_STATIC_FEED === "true";
  const manifestUrl = staticFeed
    ? getStaticUpdateManifestUrl(env, { feedUrl, platform })
    : null;
  const releaseChannelPolicy = getDesktopReleaseChannelPolicy(env);

  if (
    env.ZENITH_TEAM_DESKTOP_RELEASE_CHANNELS_ENABLED === "true" &&
    !releaseChannelPolicy.enabled
  ) {
    return manual(releaseChannelPolicy.reason);
  }

  if (!feedUrl) {
    return manual(
      "Native auto-update feed URL must be a credential-free HTTPS URL.",
    );
  }

  return {
    arch,
    enabled: true,
    feedUrl,
    manualUrl,
    mode: "native",
    packageFormat,
    platform,
    staticFeed,
    manifestUrl,
    ...(releaseChannelPolicy.enabled
      ? { releaseChannel: releaseChannelPolicy.channel }
      : {}),
    version,
  };
}

function getDesktopEnvironmentName(env = process.env) {
  return normalizeDesktopEnvironment(env.ZENITH_TEAM_DESKTOP_ENV);
}

function getDesktopReleaseChannelPolicy(env = process.env) {
  if (env.ZENITH_TEAM_DESKTOP_RELEASE_CHANNELS_ENABLED !== "true") {
    return {
      channel: null,
      enabled: false,
      reason: "Desktop release channels are disabled.",
    };
  }

  const channel = normalizeReleaseChannel(
    env.ZENITH_TEAM_DESKTOP_RELEASE_CHANNEL || DEFAULT_RELEASE_CHANNEL,
  );

  if (!RELEASE_CHANNELS.has(channel)) {
    return {
      channel: null,
      enabled: false,
      reason: "Desktop release channel is not allowed.",
    };
  }

  return {
    channel,
    enabled: true,
  };
}

function getNativeUpdateFeedUrl(env, { appVersion, arch, platform }) {
  const configuredUrl = normalizeHttpsUrl(env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE_FEED_URL);
  if (configuredUrl) {
    return configuredUrl;
  }

  const baseUrl = normalizeHttpsUrl(
    env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE_FEED_BASE_URL,
    { stripTrailingSlash: true },
  );
  if (!baseUrl) {
    return null;
  }

  const releaseChannelPolicy = getDesktopReleaseChannelPolicy(env);
  const releaseChannel = releaseChannelPolicy.enabled
    ? releaseChannelPolicy.channel
    : null;
  const staticFeed = env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE_STATIC_FEED === "true";
  const baseWithSlash = baseUrl.endsWith("/") ? baseUrl : `${baseUrl}/`;
  return new URL(
    [
      releaseChannel,
      normalizeUpdateSegment(platform),
      normalizeUpdateSegment(arch),
      staticFeed && platform === "darwin"
        ? "latest.json"
        : staticFeed
          ? null
          : normalizeUpdateSegment(appVersion),
    ]
      .filter(Boolean)
      .map(encodeURIComponent)
      .join("/"),
    baseWithSlash,
  ).href;
}

function getStaticUpdateManifestUrl(env, { feedUrl, platform }) {
  const configuredUrl = normalizeHttpsUrl(
    env.ZENITH_TEAM_DESKTOP_AUTO_UPDATE_MANIFEST_URL,
  );
  if (configuredUrl) {
    return configuredUrl;
  }

  return platform === "darwin" ? feedUrl : null;
}

function normalizeHttpsUrl(value, { stripTrailingSlash = false } = {}) {
  if (typeof value !== "string" || value.length > 2048) {
    return null;
  }

  const normalized = value.trim();
  if (!normalized || /\s/.test(normalized)) {
    return null;
  }

  try {
    const url = new URL(normalized);
    if (url.protocol === "https:" && !url.username && !url.password) {
      return stripTrailingSlash ? url.href.replace(/\/$/, "") : url.href;
    }
  } catch (_error) {
    return null;
  }

  return null;
}

function normalizeUpdateSegment(value) {
  if (typeof value !== "string" && typeof value !== "number") {
    return "";
  }

  return String(value)
    .replace(/[^a-z0-9._-]+/gi, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 120);
}

function normalizeWindowsUpdatePackage(env) {
  const value = env.ZENITH_TEAM_DESKTOP_WINDOWS_UPDATE_PACKAGE;
  return typeof value === "string" ? value.trim().toLowerCase() : "";
}

function normalizeReleaseChannel(value) {
  return typeof value === "string" ? value.trim().toLowerCase() : "";
}

function isSquirrelFirstRun(argv) {
  return Array.isArray(argv) && argv.includes("--squirrel-firstrun");
}

module.exports = {
  DEFAULT_LOCAL_UPDATE_URL,
  DEFAULT_PROD_UPDATE_URL,
  DEFAULT_RELEASE_CHANNEL,
  getDesktopNativeUpdatePolicy,
  getDesktopEnvironmentName,
  getDesktopReleaseChannelPolicy,
  getDesktopUpdateUrl,
  getNativeUpdateFeedUrl,
  getStaticUpdateManifestUrl,
};
