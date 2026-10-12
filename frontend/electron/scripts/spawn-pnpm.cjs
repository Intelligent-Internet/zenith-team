const { spawn } = require("node:child_process");
const path = require("node:path");

function spawnPnpm(args, { env = process.env, stdio = "inherit" } = {}) {
  const inheritedPnpm = resolveInheritedPnpm(env.npm_execpath);
  // Standalone pnpm is a native executable; only JavaScript entry points
  // should be passed to Node. Preserve the package manager selected by pnpm.
  const isScript = inheritedPnpm && /\.(?:cjs|mjs|js)$/iu.test(inheritedPnpm);
  const command = isScript
    ? process.execPath
    : (inheritedPnpm ?? pnpmCommand());
  const commandArgs = isScript ? [inheritedPnpm, ...args] : args;

  return spawn(command, commandArgs, { env, stdio });
}

function resolveInheritedPnpm(npmExecPath) {
  if (
    typeof npmExecPath !== "string" ||
    !path.basename(npmExecPath).toLowerCase().includes("pnpm")
  ) {
    return null;
  }

  return npmExecPath;
}

function pnpmCommand() {
  return process.platform === "win32" ? "pnpm.cmd" : "pnpm";
}

module.exports = {
  spawnPnpm,
};
