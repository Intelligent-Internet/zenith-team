const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

function zenithTeamFilename(platform) {
  return platform === "win32" ? "zen.exe" : "zen";
}

function zenithTeamMCPFilename(platform) {
  return platform === "win32" ? "zenith-team-mcp.exe" : "zenith-team-mcp";
}

function createZenithTeamBuildPlan({
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
    outputName: zenithTeamFilename(platform),
    packagePath: "./cmd/zen",
    ldflags: `-s -w -X main.version=${normalizedVersion}`,
  });
}

function createZenithTeamMCPBuildPlan({
  frontendDirectory = process.cwd(),
  platform = process.platform,
} = {}) {
  return createBuildPlan({
    frontendDirectory,
    outputName: zenithTeamMCPFilename(platform),
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

function buildZenithTeamBinaries({
  frontendDirectory = process.cwd(),
  platform = process.platform,
  version,
} = {}) {
  const packageJson = require(path.join(frontendDirectory, "package.json"));
  const zenithTeamPlan = createZenithTeamBuildPlan({
    frontendDirectory,
    platform,
    version: version ?? packageJson.version,
  });
  const mcpPlan = createZenithTeamMCPBuildPlan({ frontendDirectory, platform });

  const prepared = spawnSync("go", ["run", "./cmd/bundlectl", "prepare"], {
    cwd: zenithTeamPlan.cwd,
    env: process.env,
    stdio: "inherit",
  });
  if (prepared.error || prepared.status !== 0) {
    throw new Error("Unable to prepare verified Zenith Studio build assets.");
  }

  runGoBuild(zenithTeamPlan, "Zenith Team CLI");
  runGoBuild(mcpPlan, "Zenith Team MCP");

  return {
    zenithTeamPath: zenithTeamPlan.outputPath,
    zenithTeamMCPPath: mcpPlan.outputPath,
  };
}

if (require.main === module) {
  try {
    const output = buildZenithTeamBinaries();
    console.log(`[electron] Built Zenith Team CLI at ${output.zenithTeamPath}`);
    console.log(
      `[electron] Built Zenith Team MCP at ${output.zenithTeamMCPPath}`,
    );
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
  zenithTeamFilename,
  zenithTeamMCPFilename,
  buildZenithTeamBinaries,
  createZenithTeamBuildPlan,
  createZenithTeamMCPBuildPlan,
};
