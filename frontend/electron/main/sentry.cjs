const sentryOptions = require("../shared/sentry-options.json");
const {
  redactDiagnosticsText,
} = require("../shared/diagnostics-redaction.cjs");

function startSentryCrashReporter({ app, policy }) {
  const Sentry = require("@sentry/electron/main");

  Sentry.init({
    ...sentryOptions,
    dsn: policy.dsn,
    environment: policy.globalExtra.boardly_env,
    release: `zenith-team-desktop@${app.getVersion()}`,
    initialScope: { tags: policy.globalExtra },
    // The SDK's sandbox-compatible preload carries events through Electron IPC.
    // Keep reporting independent of the renderer's network and CSP permissions.
    ipcMode: Sentry.IPCMode.Classic,
    beforeSend: redactSentryEvent,
    integrations: (defaults) =>
      defaults.filter(
        (integration) => integration.name !== "MainProcessSession",
      ),
  });
}

function redactSentryEvent(event) {
  delete event.user;
  delete event.request;
  delete event.extra;
  delete event.breadcrumbs;
  delete event.server_name;

  if (event.message) {
    event.message = redactDiagnosticsText(event.message);
  }
  for (const exception of event.exception?.values ?? []) {
    if (exception.value) {
      exception.value = redactDiagnosticsText(exception.value);
    }
  }

  return event;
}

module.exports = { startSentryCrashReporter };
