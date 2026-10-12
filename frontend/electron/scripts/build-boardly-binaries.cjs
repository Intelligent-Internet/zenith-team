const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

function boardlyFilename(platform) {
  return platform === "win32" ? "zen.exe" : "zen";
}

function boardlyMCPFilename(platform) {
  return platform === "win32" ? "boardly-mcp.exe" : "boardly-mcp";
}

function createBoardlyBuildPlan({
  frontendDirectory = process.cwd(),
  platform = process.platform,
  version,
} = {}) {
  const normalizedVersion = String(version ?? "").trim();
  if (!normalizedVersion) {
    throw new Error("Desktop CLI version is required.");
  }

  return createBuildPlan({
    frontendDirectory,
    outputName: boardlyFilename(platform),
    packagePath: "./cmd/zen",
    ldflags: `-s -w -X main.version=${normalizedVersion}`,
  });
}

function createBoardlyMCPBuildPlan({
  frontendDirectory = process.cwd(),
  platform = process.platform,
} = {}) {
  return createBuildPlan({
    frontendDirectory,
    outputName: boardlyMCPFilename(platform),
    packagePath: "./cmd/mcp",
    ldflags: "-s -w",
  });
}

function createBuildPlan({
  frontendDirectory,
  outputName,
  packagePath,
  ldflags,
}) {
  const outputPath = path.join(frontendDirectory, ".vite", "bin", outputName);
  return {
    args: [
      "build",
      "-trimpath",
      "-ldflags",
      ldflags,
      "-o",
      outputPath,
      packagePath,
    ],
    cwd: path.resolve(frontendDirectory, "..", "server"),
    outputPath,
  };
}

function runGoBuild(plan, label) {
  fs.mkdirSync(path.dirname(plan.outputPath), { recursive: true });
  const result = spawnSync("go", plan.args, {
    cwd: plan.cwd,
    env: process.env,
    stdio: "inherit",
  });
  if (result.error) {
    throw new Error(`Unable to start the Go compiler for ${label}.`);
  }
  if (result.status !== 0) {
    throw new Error(`${label} build failed.`);
  }
}

function buildBoardlyBinaries({
  frontendDirectory = process.cwd(),
  platform = process.platform,
  version,
} = {}) {
  const packageJson = require(path.join(frontendDirectory, "package.json"));
  const boardlyPlan = createBoardlyBuildPlan({
    frontendDirectory,
    platform,
    version: version ?? packageJson.version,
  });
  const mcpPlan = createBoardlyMCPBuildPlan({ frontendDirectory, platform });

  const prepared = spawnSync("go", ["run", "./cmd/bundlectl", "prepare"], {
    cwd: boardlyPlan.cwd,
    env: process.env,
    stdio: "inherit",
  });
  if (prepared.error || prepared.status !== 0) {
    throw new Error("Unable to prepare verified Zenith Studio build assets.");
  }

  runGoBuild(boardlyPlan, "Zenith Team CLI");
  runGoBuild(mcpPlan, "Zenith Team MCP");

  return {
    boardlyPath: boardlyPlan.outputPath,
    boardlyMCPPath: mcpPlan.outputPath,
  };
}

if (require.main === module) {
  try {
    const output = buildBoardlyBinaries();
    console.log(`[electron] Built Zenith Team CLI at ${output.boardlyPath}`);
    console.log(`[electron] Built Zenith Team MCP at ${output.boardlyMCPPath}`);
  } catch (error) {
    console.error(
      error instanceof Error
        ? error.message
        : "Zenith Team binary build failed.",
    );
    process.exit(1);
  }
}

module.exports = {
  boardlyFilename,
  boardlyMCPFilename,
  buildBoardlyBinaries,
  createBoardlyBuildPlan,
  createBoardlyMCPBuildPlan,
};
