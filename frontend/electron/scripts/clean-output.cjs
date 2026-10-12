const fs = require("node:fs");
const path = require("node:path");

const outputDir = path.join(process.cwd(), "out");
const generatedPreloadPath = path.join(
  process.cwd(),
  "electron",
  "generated",
  "preload.cjs",
);
const generatedReleaseEnvPath = path.join(
  process.cwd(),
  "electron",
  "generated",
  "release-env.cjs",
);

fs.rmSync(outputDir, { force: true, recursive: true });
fs.rmSync(generatedPreloadPath, { force: true });
fs.rmSync(generatedReleaseEnvPath, { force: true });
