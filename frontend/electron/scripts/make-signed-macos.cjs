const { spawnSync } = require("node:child_process");
const path = require("node:path");
const { PROD_ENV_FILES, loadElectronEnvFiles } = require("./env-files.cjs");

function createSignedMacBuildEnv({
  cwd = process.cwd(),
  env = process.env,
  platform = process.platform,
} = {}) {
  if (platform !== "darwin") {
    throw new Error("Signed macOS desktop builds must run on macOS.");
  }

  const buildEnv = { ...env };
  loadElectronEnvFiles({ cwd, env: buildEnv, files: PROD_ENV_FILES });

  buildEnv.BOARDLY_DESKTOP_RELEASE_PACKAGE = "true";
  buildEnv.BOARDLY_DESKTOP_TARGET_PLATFORM = "darwin";
  buildEnv.BOARDLY_DESKTOP_MAC_SIGN = "true";
  buildEnv.BOARDLY_DESKTOP_MAC_NOTARIZE = "true";

  validateSignedMacBuildEnv(buildEnv);
  return buildEnv;
}

function validateSignedMacBuildEnv(env) {
  const missing = [];

  if (!hasValue(env.BOARDLY_DESKTOP_MAC_SIGN_IDENTITY)) {
    missing.push("BOARDLY_DESKTOP_MAC_SIGN_IDENTITY");
  }

  if (!getNotarizationCredentialKind(env)) {
    missing.push(
      "one notarization credential set: BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE, or BOARDLY_DESKTOP_MAC_NOTARY_API_KEY + BOARDLY_DESKTOP_MAC_NOTARY_API_KEY_ID + BOARDLY_DESKTOP_MAC_NOTARY_API_ISSUER, or BOARDLY_DESKTOP_MAC_NOTARY_APPLE_ID + BOARDLY_DESKTOP_MAC_NOTARY_APP_PASSWORD + BOARDLY_DESKTOP_MAC_NOTARY_TEAM_ID",
    );
  }

  if (missing.length > 0) {
    throw new Error(
      `Signed macOS build is missing ${missing.join(", ")}. Set these in the shell or frontend/.env.prod.`,
    );
  }
}

function getNotarizationCredentialKind(env) {
  if (hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE)) {
    return "keychain profile";
  }

  if (
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY) &&
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_API_KEY_ID) &&
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_API_ISSUER)
  ) {
    return "App Store Connect API key";
  }

  if (
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_APPLE_ID) &&
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_APP_PASSWORD) &&
    hasValue(env.BOARDLY_DESKTOP_MAC_NOTARY_TEAM_ID)
  ) {
    return "Apple ID";
  }

  return "";
}

function hasValue(value) {
  return typeof value === "string" && value.trim().length > 0;
}

function makeSignedMacos({ cwd = process.cwd(), env = process.env } = {}) {
  const buildEnv = createSignedMacBuildEnv({ cwd, env });

  console.log("[electron] Building signed and notarized macOS package.");
  console.log(
    `[electron] Signing identity: ${buildEnv.BOARDLY_DESKTOP_MAC_SIGN_IDENTITY}`,
  );
  console.log(
    `[electron] Notarization credential: ${getNotarizationCredentialKind(buildEnv)}`,
  );

  runNodeScript(cwd, buildEnv, "electron/scripts/clean-output.cjs");
  runNodeScript(cwd, buildEnv, "electron/scripts/build-preload.cjs");
  runNodeScript(cwd, buildEnv, "electron/scripts/build-boardly-binaries.cjs");
  runNodeScript(cwd, buildEnv, "electron/scripts/build-renderer.cjs");
  runNodeScript(cwd, buildEnv, "electron/scripts/run-forge.cjs", [
    "make",
    "--platform=darwin",
  ]);
  runNodeScript(cwd, buildEnv, "electron/scripts/finalize-artifacts.cjs");
}

function runNodeScript(cwd, env, scriptPath, args = []) {
  const result = spawnSync(
    process.execPath,
    [path.join(cwd, scriptPath), ...args],
    {
      cwd,
      env,
      stdio: "inherit",
    },
  );

  if (result.error) {
    throw result.error;
  }

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

if (require.main === module) {
  makeSignedMacos();
}

module.exports = {
  createSignedMacBuildEnv,
  getNotarizationCredentialKind,
  makeSignedMacos,
  validateSignedMacBuildEnv,
};
