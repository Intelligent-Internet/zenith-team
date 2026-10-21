const {
  DEV_ENV_FILES,
  PROD_ENV_FILES,
  loadElectronEnvFiles,
} = require("./env-files.cjs");
const {
  pruneBrokenPublicDownloadSymlinks,
} = require("./local-download-staging.cjs");
const { spawnPnpm } = require("./spawn-pnpm.cjs");
const {
  getDesktopVariant,
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");

const desktopEnvironment = normalizeDesktopEnvironment(
  process.env.ZENITH_TEAM_DESKTOP_ENV,
);
loadElectronEnvFiles({
  files: desktopEnvironment === "dev" ? DEV_ENV_FILES : PROD_ENV_FILES,
});
const removedDownloadSymlinks = pruneBrokenPublicDownloadSymlinks();

if (removedDownloadSymlinks.length > 0) {
  console.warn(
    `[electron] Removed ${removedDownloadSymlinks.length} stale public download symlink(s) before renderer build.`,
  );
}

const DEFAULT_LOCAL_API_URL = "http://127.0.0.1:8788";
const rendererEnv = createRendererBuildEnv(desktopEnvironment);
const studioApiOrigin = resolveStudioApiOrigin();
// Studio renders cloud assets from these hosts. Without Studio the CSP
// allows none of them.
const STUDIO_MEDIA_ORIGINS = studioApiOrigin
  ? (process.env.ZENITH_STUDIO_MEDIA_ORIGINS ?? "https://storage.googleapis.com")
      .split(",")
      .map((origin) => origin.trim())
      .filter(Boolean)
  : [];

const pnpmProcess = spawnPnpm(["build"], {
  env: {
    ...process.env,
    ...rendererEnv,
    ZENITH_TEAM_ELECTRON_BUILD: "true",
    ZENITH_TEAM_DESKTOP_ENV: desktopEnvironment,
    VITE_ZENITH_TEAM_DESKTOP_ENV: desktopEnvironment,
  },
});

pnpmProcess.on("exit", (code) => {
  if (code === 0) {
    applyPackagedContentSecurityPolicy(rendererEnv);
  }

  process.exit(code ?? 1);
});

function applyPackagedContentSecurityPolicy(env) {
  const fs = require("node:fs");
  const path = require("node:path");
  const indexPath = path.join(process.cwd(), "dist", "index.html");
  const connectSources = createConnectSources(env);
  const mediaSources = createMediaSources();
  const csp = [
    "default-src 'self'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline'",
    "font-src 'self' data:",
    `img-src 'self' data: blob: file: zenith-team-generated-image: zenith-team-plugin-icon: https://files.openai.com https://chatgpt.com https://*.googleusercontent.com ${STUDIO_MEDIA_ORIGINS.join(" ")}`,
    `connect-src ${connectSources.join(" ")}`,
    `media-src ${mediaSources.join(" ")}`,
    "object-src 'none'",
    "base-uri 'self'",
    "form-action 'self'",
  ].join("; ");
  const cspMeta = `<meta http-equiv="Content-Security-Policy" content="${csp}" />`;
  const html = fs.readFileSync(indexPath, "utf8");

  if (html.includes('http-equiv="Content-Security-Policy"')) {
    return;
  }

  fs.writeFileSync(
    indexPath,
    html.replace("<head>", `<head>\n    ${cspMeta}`),
    "utf8",
  );
}

function createConnectSources(env) {
  const sources = new Set([
    "'self'",
    "blob:",
    "ws://127.0.0.1:*",
    // Huddles: LiveKit Cloud signals over wss and validates a failed
    // connection over https, and may move a session to a regional host.
    "https://*.livekit.cloud",
    "wss://*.livekit.cloud",
    // Studio renders cloud assets and fetches signed uploads/downloads.
    ...(studioApiOrigin ? STUDIO_MEDIA_ORIGINS : []),
  ]);

  addUrlSource(sources, env.VITE_API_URL);
  addRealtimeUrlSource(sources, env.VITE_API_URL);
  // Studio HTTP and Socket.IO connect directly from the renderer after
  // Team resolves the editor session through the daemon.
  addUrlSource(sources, studioApiOrigin);
  addRealtimeUrlSource(sources, studioApiOrigin);
  addLiveKitUrlSources(sources, process.env.VITE_LIVEKIT_URL);

  if (desktopEnvironment === "local") {
    for (const source of ["http://localhost:*", "http://127.0.0.1:*"]) {
      sources.add(source);
    }
  }

  return [...sources];
}

function resolveStudioApiOrigin() {
  const { isIP } = require("node:net");
  // Zenith Studio is optional: without ZENITH_STUDIO_API_URL it stays off.
  const rawUrl = process.env.ZENITH_STUDIO_API_URL?.trim();
  if (!rawUrl) {
    return null;
  }
  let url;
  try {
    url = new URL(rawUrl);
  } catch {
    throw new Error("ZENITH_STUDIO_API_URL must be an absolute Studio origin.");
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, "");
  const loopback = ["localhost", "127.0.0.1", "::1"].includes(hostname);
  const validHost =
    isIP(hostname) !== 0 ||
    hostname
      .split(".")
      .every((label) => /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(label));
  if (
    !validHost ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    rawUrl.includes("?") ||
    rawUrl.includes("#") ||
    (url.protocol !== "https:" &&
      !(desktopEnvironment === "local" && loopback && url.protocol === "http:"))
  ) {
    throw new Error(
      "ZENITH_STUDIO_API_URL must be a credential-free HTTPS origin without a path, query, fragment, or wildcard; HTTP loopback is allowed only for local desktop builds.",
    );
  }
  if (desktopEnvironment !== "local") {
    assertNonLocalCloudUrl("ZENITH_STUDIO_API_URL", rawUrl, desktopEnvironment);
  }
  return url.origin;
}

function addRealtimeUrlSource(sources, rawUrl) {
  if (!rawUrl) {
    return;
  }

  try {
    const url = new URL(rawUrl);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    sources.add(url.origin);
  } catch {
    return;
  }
}

// A self-hosted LiveKit server needs both its signal (ws/wss) and its
// validation (http/https) origin.
function addLiveKitUrlSources(sources, rawUrl) {
  if (!rawUrl) {
    return;
  }

  try {
    const url = new URL(rawUrl);
    const secure = url.protocol === "wss:" || url.protocol === "https:";
    for (const protocol of secure ? ["wss:", "https:"] : ["ws:", "http:"]) {
      url.protocol = protocol;
      sources.add(url.origin);
    }
  } catch {
    return;
  }
}

function createMediaSources() {
  return ["'self'", "data:", "blob:", ...STUDIO_MEDIA_ORIGINS];
}

function addUrlSource(sources, rawUrl) {
  if (!rawUrl) {
    return;
  }

  try {
    const url = new URL(rawUrl);
    sources.add(url.origin);
  } catch {
    return;
  }
}

function createRendererBuildEnv(desktopEnvironment) {
  if (desktopEnvironment === "local") {
    return {
      VITE_API_URL: process.env.VITE_API_URL ?? DEFAULT_LOCAL_API_URL,
    };
  }

  if (desktopEnvironment !== "dev" && desktopEnvironment !== "prod") {
    return {};
  }

  const configuredOrigin =
    desktopEnvironment === "dev"
      ? process.env.ZENITH_TEAM_DESKTOP_DEV_ORIGIN
      : process.env.ZENITH_TEAM_DESKTOP_PROD_ORIGIN;
  const defaultOrigin = getDesktopVariant({
    ZENITH_TEAM_DESKTOP_ENV: desktopEnvironment,
  }).apiOrigin;
  const cloudOrigin = normalizeCloudOrigin(
    configuredOrigin,
    defaultOrigin,
    desktopEnvironment,
  );
  const apiUrl = process.env.VITE_API_URL ?? cloudOrigin;

  assertNonLocalCloudUrl("VITE_API_URL", apiUrl, desktopEnvironment);

  return {
    VITE_API_URL: apiUrl,
  };
}

function normalizeCloudOrigin(value, defaultOrigin, desktopEnvironment) {
  const origin = value?.trim() || defaultOrigin;
  const name =
    desktopEnvironment === "dev"
      ? "ZENITH_TEAM_DESKTOP_DEV_ORIGIN"
      : "ZENITH_TEAM_DESKTOP_PROD_ORIGIN";

  if (!origin) {
    throw new Error(
      `${name} is required for ${desktopEnvironment} desktop builds: set it to your Zenith Team server origin.`,
    );
  }
  assertNonLocalCloudUrl(name, origin, desktopEnvironment);
  return origin.replace(/\/+$/, "");
}

function assertNonLocalCloudUrl(name, rawUrl, desktopEnvironment) {
  let parsedUrl;

  try {
    parsedUrl = new URL(rawUrl);
  } catch {
    throw new Error(
      `${name} must be an absolute URL for ${desktopEnvironment} desktop builds.`,
    );
  }

  if (
    parsedUrl.hostname === "localhost" ||
    parsedUrl.hostname === "127.0.0.1" ||
    parsedUrl.hostname === "0.0.0.0" ||
    parsedUrl.hostname === "::1" ||
    parsedUrl.hostname === "[::1]" ||
    parsedUrl.hostname.endsWith(".localhost")
  ) {
    throw new Error(
      `${name} must not point at loopback in ${desktopEnvironment} desktop builds.`,
    );
  }
}
