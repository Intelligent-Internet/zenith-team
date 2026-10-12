const {
  DEV_ENV_FILES,
  PROD_ENV_FILES,
  loadElectronEnvFiles,
} = require("./env-files.cjs");
const {
  applyLocalMacSigningDefaults,
} = require("./release-signing-env.cjs");
const { spawnPnpm } = require("./spawn-pnpm.cjs");
const { writeReleaseEnvFile } = require("./write-release-env.cjs");
const {
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");

const shellEnvKeys = new Set(Object.keys(process.env));
const desktopEnvironment = normalizeDesktopEnvironment(
  process.env.BOARDLY_DESKTOP_ENV,
);
loadElectronEnvFiles({
  files: desktopEnvironment === "dev" ? DEV_ENV_FILES : PROD_ENV_FILES,
});

const forgeArgs = process.argv.slice(2);
const targetPlatform = getForgeArgValue(forgeArgs, "--platform");

if (forgeArgs.length === 0) {
  console.error("Usage: node electron/scripts/run-forge.cjs <forge-command>");
  process.exit(1);
}

const forgeEnv = createForgeEnv({ shellEnvKeys, targetPlatform });
writeReleaseEnvFile({ env: forgeEnv });

const forgeProcess = spawnPnpm(["exec", "electron-forge", ...forgeArgs], {
  env: {
    ...forgeEnv,
    BOARDLY_DESKTOP_ENV: desktopEnvironment,
    BOARDLY_DESKTOP_TARGET_PLATFORM:
      process.env.BOARDLY_DESKTOP_TARGET_PLATFORM ??
      targetPlatform ??
      process.platform,
    // Forge only uses the package manager for its environment check here.
    // The project still installs and builds with pnpm via packageManager.
    NODE_INSTALLER: process.env.NODE_INSTALLER ?? "npm",
  },
});

forgeProcess.on("exit", (code) => {
  process.exit(code ?? 1);
});

function getForgeArgValue(args, flagName) {
  for (const [index, arg] of args.entries()) {
    if (arg === flagName) {
      return args[index + 1];
    }

    if (arg.startsWith(`${flagName}=`)) {
      return arg.slice(flagName.length + 1);
    }
  }

  return undefined;
}

function createForgeEnv({ shellEnvKeys, targetPlatform }) {
  const env = { ...process.env };

  applyLocalMacSigningDefaults({
    env,
    shellEnvKeys,
    targetPlatform: env.BOARDLY_DESKTOP_TARGET_PLATFORM ??
      targetPlatform ??
      process.platform,
  });

  return env;
}
