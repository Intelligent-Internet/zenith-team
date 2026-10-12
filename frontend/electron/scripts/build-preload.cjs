const fs = require("node:fs/promises");
const path = require("node:path");
const { build } = require("esbuild");

const PRELOAD_ENTRY_PATH = path.join(__dirname, "..", "preload", "index.cjs");
const PRELOAD_OUTPUT_PATH = path.join(
  __dirname,
  "..",
  "generated",
  "preload.cjs",
);

async function buildPreload() {
  await fs.mkdir(path.dirname(PRELOAD_OUTPUT_PATH), { recursive: true });
  await build({
    bundle: true,
    entryPoints: [PRELOAD_ENTRY_PATH],
    external: ["electron"],
    format: "cjs",
    legalComments: "none",
    logLevel: "silent",
    outfile: PRELOAD_OUTPUT_PATH,
    platform: "browser",
    target: "chrome140",
  });
  console.log(`[electron] Built sandboxed preload at ${PRELOAD_OUTPUT_PATH}`);
}

if (require.main === module) {
  buildPreload().catch((error) => {
    console.error("[electron] Failed to build sandboxed preload.", error);
    process.exitCode = 1;
  });
}

module.exports = {
  PRELOAD_OUTPUT_PATH,
  buildPreload,
};
