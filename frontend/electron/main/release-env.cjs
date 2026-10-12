const path = require("node:path");

function createDesktopRuntimeEnv(env = process.env) {
  return mergeDesktopRuntimeEnv(loadGeneratedReleaseEnv(), env);
}

function mergeDesktopRuntimeEnv(generatedEnv, env) {
  const explicitEnvironment =
    typeof env?.BOARDLY_DESKTOP_ENV === "string"
      ? env.BOARDLY_DESKTOP_ENV.trim().toLowerCase()
      : "";

  if (explicitEnvironment === "local") {
    return { ...env };
  }

  return {
    ...generatedEnv,
    ...env,
  };
}

function loadGeneratedReleaseEnv() {
  try {
    const releaseEnvPath = path.join(
      __dirname,
      "..",
      "generated",
      "release-env.cjs",
    );
    const generated = require(releaseEnvPath);

    if (generated && typeof generated.releaseEnv === "object") {
      return normalizeReleaseEnv(generated.releaseEnv);
    }
  } catch (_error) {
    return {};
  }

  return {};
}

function normalizeReleaseEnv(value) {
  const result = {};

  for (const [key, rawValue] of Object.entries(value)) {
    if (typeof rawValue !== "string") {
      continue;
    }

    result[key] = rawValue;
  }

  return result;
}

module.exports = {
  createDesktopRuntimeEnv,
  loadGeneratedReleaseEnv,
  mergeDesktopRuntimeEnv,
};
