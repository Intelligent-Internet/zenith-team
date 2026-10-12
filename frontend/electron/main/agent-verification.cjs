"use strict";

const AGENT_VERIFICATION_CHANNEL = "desktop:verify-agent";

function failedVerification(provider) {
  return {
    schemaVersion: 1,
    provider,
    ok: false,
    code: "provider_failed",
  };
}

function createDesktopAgentVerifier({ discoverAgents }) {
  const inFlight = new Map();
  return function verifyAgent(provider) {
    if (provider !== "codex" && provider !== "claude") {
      return Promise.resolve(failedVerification("codex"));
    }
    const current = inFlight.get(provider);
    if (current) return current;
    const request = Promise.resolve(discoverAgents())
      .then((result) =>
        result?.ok &&
        result.snapshot.agents.some((agent) => agent.provider === provider)
          ? { schemaVersion: 1, provider, ok: true }
          : failedVerification(provider),
      )
      .catch(() => failedVerification(provider))
      .finally(() => inFlight.delete(provider));
    inFlight.set(provider, request);
    return request;
  };
}

function registerDesktopAgentVerification({
  discoverAgents,
  ipcMain,
  verifyAgent,
} = {}) {
  if (!ipcMain?.handle) return () => {};
  const handler = verifyAgent ?? createDesktopAgentVerifier({ discoverAgents });
  ipcMain.handle(AGENT_VERIFICATION_CHANNEL, (_event, provider) =>
    handler(provider),
  );
  return () => ipcMain.removeHandler?.(AGENT_VERIFICATION_CHANNEL);
}

module.exports = {
  AGENT_VERIFICATION_CHANNEL,
  createDesktopAgentVerifier,
  registerDesktopAgentVerification,
};
