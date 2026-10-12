const fs = require("node:fs");
const settings = fs.existsSync("settings.json")
  ? JSON.parse(fs.readFileSync("settings.json"))
  : {};
const args = process.argv.slice(2);
fs.appendFileSync(
  process.env.RECORD,
  JSON.stringify({
    args,
    home: process.env.ZENITH_STUDIO_CLI_HOME,
    apiUrl: process.env.ZENITH_STUDIO_CLI_API_URL,
  }) + "\n",
);
const [group, command] = args.filter((arg) => arg !== "--json");
if (group === "auth") {
  console.log(
    JSON.stringify({
      ok: true,
      data: { authenticated: true, user: { id: "account" } },
    }),
  );
  process.exit(0);
}
if (settings.failStage === group) {
  console.log(
    JSON.stringify({
      ok: false,
      error: { code: "fixture_failure", message: "private-upstream-value" },
    }),
  );
  process.exit(7);
}
let data;
if (group === "models") {
  data = {
    models: [
      {
        id: "wanted",
        display_name: "Wanted Model",
        available: true,
        capabilities: { supported_params: ["quality"], resolutions: ["2K"] },
      },
      {
        id: "other",
        display_name: "Other Model",
        available: false,
        capabilities: {},
      },
    ],
  };
} else if (group === "jobs" && command === "wait") {
  if (settings.revokeAfterWait) fs.writeFileSync("revoked", "yes");
  data = {
    project_id: args[3],
    node_id: args[4],
    execution_id: "execution",
    created_at: "2026-10-05T18:59:12Z",
    updated_at: "2026-10-05T19:00:05Z",
    status: "succeeded",
    output: {
      asset_id: "asset",
      mime_type: settings.mimeType || "image/png",
      size_bytes: 5,
      checksum: "sha256:fixture",
      download_url: "https://private.invalid/signed-secret",
    },
  };
} else if (group === "assets" && command === "download") {
  const path = args[args.indexOf("--output") + 1];
  fs.writeFileSync(path, "media", { flag: "wx" });
  data = {
    project_id: args[3],
    node_id: args[4],
    path,
    size_bytes: 5,
    mime_type: settings.mimeType || "image/png",
    checksum: "sha256:fixture",
  };
} else if (group === "canvas" && command === "export") {
  const path = args[args.indexOf("--output") + 1];
  fs.writeFileSync(
    path,
    JSON.stringify({ projectId: args[3], nodes: [{ id: "node" }] }),
    { flag: "wx" },
  );
  data = { path, node_count: 1, edge_count: 0 };
} else {
  process.exit(9);
}
console.log(JSON.stringify({ ok: true, data }));
