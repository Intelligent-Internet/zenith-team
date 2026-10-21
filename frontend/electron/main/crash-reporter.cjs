const { writeMainLog: defaultWriteMainLog } = require("./diagnostics.cjs");
const { startSentryCrashReporter } = require("./sentry.cjs");
const {
  normalizeDesktopEnvironment: normalizeDesktopEnvironmentName,
} = require("../shared/desktop-variant.cjs");

const CRASH_REPORT_PROVIDERS = new Set([
  "backtrace",
  "bugsnag",
  "bugsplat",
  "custom",
  "first-party",
  "sentry",
]);
const GET_CRASH_REPORTING_STATE_CHANNEL = "desktop:get-crash-reporting-state";
let registeredCrashReportingIpc = false;

function startDesktopCrashReporter(options = {}) {
  const electron = options.electron ?? require("electron");
  const app = options.app ?? electron.app;
  const crashReporter = options.crashReporter ?? electron.crashReporter;
  const writeMainLog = options.writeMainLog ?? defaultWriteMainLog;
  const policy = getDesktopCrashReportPolicy({
    app,
    arch: options.arch ?? process.arch,
    env: options.env ?? process.env,
    platform: options.platform ?? process.platform,
  });

  if (!policy.enabled) {
    return policy;
  }

  try {
    if (policy.provider === "sentry") {
      startSentryCrashReporter({ app, policy });
    } else {
      crashReporter.start({
        compress: true,
        globalExtra: policy.globalExtra,
        productName: app.getName?.() ?? "Zenith Team",
        rateLimit: true,
        submitURL: policy.submitURL,
        uploadToServer: true,
      });
    }
    writeMainLog("crash-reporter-started", {
      provider: policy.provider,
      submitUrlOrigin: getUrlOrigin(policy.submitURL),
      uploadToServer: true,
    });

    return {
      ...policy,
      started: true,
    };
  } catch (error) {
    const reason = getErrorMessage(error);
    writeMainLog("crash-reporter-start-failed", { reason });
    return {
      ...policy,
      enabled: false,
      reason,
      started: false,
    };
  }
}

function getDesktopCrashReportPolicy({
  app,
  arch = process.arch,
  env = process.env,
  platform = process.platform,
} = {}) {
  if (env.ZENITH_TEAM_DESKTOP_CRASH_REPORTS_ENABLED !== "true") {
    return {
      enabled: false,
      reason: "Desktop crash reporting is disabled.",
      started: false,
      uploadToServer: false,
    };
  }

  const provider = normalizeCrashReportProvider(
    env.ZENITH_TEAM_DESKTOP_CRASH_REPORT_PROVIDER,
  );
  const dsn =
    provider === "sentry"
      ? normalizeSentryDsn(env.ZENITH_TEAM_DESKTOP_SENTRY_DSN)
      : null;
  const submitURL =
    provider === "sentry"
      ? dsn && new URL(dsn).origin
      : normalizeCrashSubmitUrl(env.ZENITH_TEAM_DESKTOP_CRASH_REPORT_SUBMIT_URL);

  if (provider === "sentry" && !dsn) {
    return {
      enabled: false,
      reason: "Sentry crash reporting requires a valid HTTPS DSN.",
      started: false,
      uploadToServer: false,
    };
  }

  if (provider !== "sentry" && !env.ZENITH_TEAM_DESKTOP_CRASH_REPORT_SUBMIT_URL) {
    return {
      enabled: false,
      reason: "Desktop crash reporting requires a submit URL.",
      started: false,
      uploadToServer: false,
    };
  }

  if (!submitURL) {
    return {
      enabled: false,
      reason:
        "Desktop crash report submit URL must be credential-free HTTPS without query or fragment.",
      started: false,
      uploadToServer: false,
    };
  }

  const desktopEnvironment = normalizeDesktopEnvironment(
    env.ZENITH_TEAM_DESKTOP_ENV,
    Boolean(app?.isPackaged),
  );

  return {
    dsn,
    enabled: true,
    globalExtra: createCrashReportGlobalExtra({
      arch,
      env,
      isPackaged: Boolean(app?.isPackaged),
      platform,
      provider,
      runtimeEnvironment: desktopEnvironment,
    }),
    provider,
    started: false,
    submitURL,
    uploadToServer: true,
  };
}

function normalizeSentryDsn(value) {
  if (typeof value !== "string" || !value.trim() || /\s/.test(value.trim())) {
    return null;
  }

  try {
    const url = new URL(value.trim());
    if (
      url.protocol !== "https:" ||
      !/^[a-z0-9]+$/i.test(url.username) ||
      url.password ||
      !/^\/\d+$/.test(url.pathname) ||
      url.search ||
      url.hash
    ) {
      return null;
    }
    return url.toString();
  } catch {
    return null;
  }
}

function registerDesktopCrashReportingIpcHandlers({ getState, ipcMain }) {
  if (!ipcMain || typeof ipcMain.handle !== "function") {
    return;
  }

  if (registeredCrashReportingIpc) {
    return;
  }

  registeredCrashReportingIpc = true;
  ipcMain.handle(GET_CRASH_REPORTING_STATE_CHANNEL, async () =>
    normalizeDesktopCrashReportingState(getState?.()),
  );
}

function createDesktopCrashReportingState(policy) {
  const enabled = Boolean(
    policy?.enabled && policy.started && policy.uploadToServer,
  );

  return normalizeDesktopCrashReportingState({
    enabled,
    metadataKeys: policy?.globalExtra
      ? Object.keys(policy.globalExtra).sort()
      : [],
    provider: enabled ? policy.provider : null,
    reason: enabled
      ? null
      : (policy?.reason ?? "Desktop crash reporting is disabled."),
    submitUrlOrigin: enabled ? getUrlOrigin(policy.submitURL) : null,
    uploadToServer: enabled,
  });
}

function normalizeDesktopCrashReportingState(state) {
  return {
    enabled: Boolean(state?.enabled),
    metadataKeys: Array.isArray(state?.metadataKeys)
      ? state.metadataKeys
          .filter((key) => typeof key === "string" && key.length <= 39)
          .sort()
      : [],
    provider:
      typeof state?.provider === "string" && state.provider.trim()
        ? state.provider.trim()
        : null,
    reason:
      typeof state?.reason === "string" && state.reason.trim()
        ? state.reason.trim()
        : null,
    submitUrlOrigin:
      typeof state?.submitUrlOrigin === "string" &&
      /^https:\/\/[^/?#]+$/i.test(state.submitUrlOrigin)
        ? state.submitUrlOrigin
        : null,
    uploadToServer: Boolean(state?.uploadToServer),
  };
}

function createCrashReportGlobalExtra({
  arch,
  env,
  isPackaged,
  platform,
  provider,
  runtimeEnvironment,
}) {
  return {
    zenith_team_arch: truncateCrashMetadataValue(normalizeToken(arch, "unknown")),
    zenith_team_channel: truncateCrashMetadataValue(
      getCrashReportReleaseChannel(env),
    ),
    zenith_team_env: truncateCrashMetadataValue(runtimeEnvironment),
    zenith_team_packaged: isPackaged ? "true" : "false",
    zenith_team_platform: truncateCrashMetadataValue(
      normalizeToken(platform, "unknown"),
    ),
    zenith_team_provider: truncateCrashMetadataValue(provider),
  };
}

function getCrashReportReleaseChannel(env) {
  if (env.ZENITH_TEAM_DESKTOP_RELEASE_CHANNELS_ENABLED !== "true") {
    return "none";
  }

  const channel = env.ZENITH_TEAM_DESKTOP_RELEASE_CHANNEL;
  return ["alpha", "beta", "stable"].includes(channel) ? channel : "stable";
}

function normalizeCrashSubmitUrl(value) {
  if (typeof value !== "string") {
    return null;
  }

  const normalized = value.trim();
  if (!normalized || /\s/.test(normalized)) {
    return null;
  }

  try {
    const url = new URL(normalized);

    if (
      url.protocol !== "https:" ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return null;
    }

    return url.toString();
  } catch {
    return null;
  }
}

function normalizeCrashReportProvider(value) {
  if (typeof value !== "string") {
    return "first-party";
  }

  const provider = value.trim().toLowerCase();
  return CRASH_REPORT_PROVIDERS.has(provider) ? provider : "custom";
}

function normalizeDesktopEnvironment(environment, isPackaged) {
  return normalizeDesktopEnvironmentName(
    environment,
    isPackaged ? "prod" : "local",
  );
}

function normalizeToken(value, fallback) {
  if (typeof value !== "string") {
    return fallback;
  }

  const normalized = value.trim().toLowerCase();
  return /^[a-z0-9._-]+$/.test(normalized) ? normalized : fallback;
}

function truncateCrashMetadataValue(value) {
  return value.slice(0, 127);
}

function getUrlOrigin(value) {
  try {
    return new URL(value).origin;
  } catch {
    return "unknown";
  }
}

function getErrorMessage(error) {
  return error instanceof Error
    ? error.message
    : "Desktop crash reporter failed to start.";
}

module.exports = {
  GET_CRASH_REPORTING_STATE_CHANNEL,
  createCrashReportGlobalExtra,
  createDesktopCrashReportingState,
  getDesktopCrashReportPolicy,
  normalizeCrashSubmitUrl,
  registerDesktopCrashReportingIpcHandlers,
  startDesktopCrashReporter,
};
