"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { execFile } = require("node:child_process");

const { daemonEnvironment } = require("./daemon-environment.cjs");

const AGENT_DISCOVERY_CHANNEL = "desktop:discover-agents";
const DISCOVERY_TIMEOUT_MS = 20_000;
const DISCOVERY_MAX_BUFFER_BYTES = 64 << 10;
const DISCOVERY_ERROR_REASON =
  "Zenith Team could not inspect local agent providers.";
const SUPPORTED_PROVIDERS = ["claude", "codex"];

function zenithTeamFilename(platform) {
  return platform === "win32" ? "zen.exe" : "zen";
}

function zenithTeamMCPFilename(platform) {
  return platform === "win32" ? "zenith-team-mcp.exe" : "zenith-team-mcp";
}

function resolveZenithTeamExecutable({
  app,
  env = process.env,
  existsSync = fs.existsSync,
  platform = process.platform,
  resourcesPath = process.resourcesPath,
  rootDirectory = path.resolve(__dirname, "..", ".."),
} = {}) {
  return resolveBundledExecutable({
    app,
    existsSync,
    filename: zenithTeamFilename(platform),
    override: env.ZENITH_TEAM_CLI_PATH,
    resourcesPath,
    rootDirectory,
  });
}

function resolveZenithTeamMCPExecutable({
  app,
  env = process.env,
  existsSync = fs.existsSync,
  platform = process.platform,
  resourcesPath = process.resourcesPath,
  rootDirectory = path.resolve(__dirname, "..", ".."),
} = {}) {
  return resolveBundledExecutable({
    app,
    existsSync,
    filename: zenithTeamMCPFilename(platform),
    override: env.ZENITH_TEAM_MCP_EXECUTABLE,
    resourcesPath,
    rootDirectory,
  });
}

function resolveBundledExecutable({
  app,
  existsSync,
  filename,
  override,
  resourcesPath,
  rootDirectory,
}) {
  const configured = typeof override === "string" ? override.trim() : "";
  const candidate = configured
    ? path.resolve(configured)
    : app?.isPackaged
      ? path.join(resourcesPath, filename)
      : path.join(rootDirectory, ".vite", "bin", filename);
  return path.isAbsolute(candidate) && existsSync(candidate) ? candidate : null;
}

function createDesktopAgentDiscoverer({
  app,
  zenithTeamHomeDirectory,
  environment = process.env,
  execFile: runFile = execFile,
  executablePath,
  healthPort,
  resolveExecutable = resolveZenithTeamExecutable,
  serverUrl,
} = {}) {
  const childEnvironment = daemonEnvironment(environment, {
    ZENITH_TEAM_HEALTH_PORT: String(healthPort),
    ZENITH_TEAM_HOME: zenithTeamHomeDirectory,
    ZENITH_TEAM_LAUNCHED_BY: "desktop",
    ZENITH_TEAM_SERVER_URL: normalizeServerUrl(serverUrl),
  });
  let inFlight = null;

  return function discoverAgents() {
    if (inFlight) return inFlight;
    inFlight = new Promise((resolve) => {
      const resolvedPath = executablePath ?? resolveExecutable({ app });
      if (!resolvedPath) {
        resolve({ ok: false, reason: DISCOVERY_ERROR_REASON });
        return;
      }
      runFile(
        resolvedPath,
        ["daemon", "probe-runtimes"],
        {
          encoding: "utf8",
          env: childEnvironment,
          maxBuffer: DISCOVERY_MAX_BUFFER_BYTES,
          shell: false,
          timeout: DISCOVERY_TIMEOUT_MS,
          windowsHide: true,
        },
        (error, stdout) => {
          if (error) {
            resolve({ ok: false, reason: DISCOVERY_ERROR_REASON });
            return;
          }
          const snapshot = parseDiscoverySnapshot(stdout);
          resolve(
            snapshot
              ? { ok: true, snapshot }
              : { ok: false, reason: DISCOVERY_ERROR_REASON },
          );
        },
      );
    }).finally(() => {
      inFlight = null;
    });
    return inFlight;
  };
}

function registerDesktopAgentDiscovery({ app, discoverAgents, ipcMain } = {}) {
  if (!ipcMain?.handle) return () => {};
  const handler = discoverAgents ?? createDesktopAgentDiscoverer({ app });
  ipcMain.handle(AGENT_DISCOVERY_CHANNEL, handler);
  return () => ipcMain.removeHandler?.(AGENT_DISCOVERY_CHANNEL);
}

function parseDiscoverySnapshot(stdout) {
  let value;
  try {
    value = JSON.parse(String(stdout));
  } catch {
    return null;
  }
  if (
    !isPlainObject(value) ||
    value.probeResult !== "success" ||
    !isPlainObject(value.providers)
  ) {
    return null;
  }
  const providers = Object.keys(value.providers);
  if (
    providers.some((provider) => !SUPPORTED_PROVIDERS.includes(provider)) ||
    providers.some((provider) => !isPlainObject(value.providers[provider]))
  ) {
    return null;
  }
  return {
    schemaVersion: 1,
    agents: SUPPORTED_PROVIDERS.filter((provider) =>
      Object.hasOwn(value.providers, provider),
    ).map((provider) => ({ capabilities: {}, provider })),
  };
}

function normalizeServerUrl(rawValue) {
  const parsed = new URL(String(rawValue).trim().replace(/\/+$/u, ""));
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    (parsed.pathname !== "/" &&
      parsed.pathname !== "" &&
      parsed.pathname !== "/api/v1") ||
    (parsed.protocol === "http:" &&
      !["localhost", "127.0.0.1", "[::1]"].includes(parsed.hostname))
  ) {
    throw new Error("Invalid Zenith Team daemon server URL");
  }
  return parsed.origin;
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

module.exports = {
  AGENT_DISCOVERY_CHANNEL,
  createDesktopAgentDiscoverer,
  parseDiscoverySnapshot,
  registerDesktopAgentDiscovery,
  resolveZenithTeamExecutable,
  resolveZenithTeamMCPExecutable,
};
