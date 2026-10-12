function applyLocalMacSigningDefaults({
  env = process.env,
  shellEnvKeys = new Set(),
  targetPlatform = process.platform,
} = {}) {
  if (targetPlatform !== "darwin") {
    return env;
  }

  if (env.BOARDLY_DESKTOP_RELEASE_PACKAGE === "true") {
    return env;
  }

  if (!shellEnvKeys.has("BOARDLY_DESKTOP_MAC_SIGN")) {
    env.BOARDLY_DESKTOP_MAC_SIGN = "false";
  }

  if (!shellEnvKeys.has("BOARDLY_DESKTOP_MAC_NOTARIZE")) {
    env.BOARDLY_DESKTOP_MAC_NOTARIZE = "false";
  }

  return env;
}

module.exports = {
  applyLocalMacSigningDefaults,
};
