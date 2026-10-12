"use strict";

// `managed` is emitted by the daemon with the exact reviewed installation.
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const args = process.argv.slice(2);
const commands = [
  "init",
  "lint",
  "check",
  "validate",
  "inspect",
  "snapshot",
  "render",
  "preview",
  "doctor",
  "browser",
];
const command = args[0];
function reject(message) {
  console.error(message);
  process.exit(64);
}
if (!command || command === "--help" || command === "-h") {
  console.log(
    `HyperFrames local renderer managed by Zenith Team\nCommands: ${commands.join(", ")}\nUse <command> --help for CLI flags. Authoring guidance comes from the Zenith Studio skill.`,
  );
  process.exit(0);
}
if (
  command !== "--version" &&
  command !== "-v" &&
  !commands.includes(command)
) {
  reject(
    "This managed CLI supports local rendering commands. Use the Zenith Studio skill for authoring guidance; use the daemon-managed installation.",
  );
}
const initializing =
  command === "init" && !args.includes("--help") && !args.includes("-h");
let scaffold;
if (initializing) {
  // Even with SKIP_SKILLS, upstream init copies AGENTS.md and CLAUDE.md from
  // its shared template. Stage outside the workspace and publish only project
  // files, so those instructions never enter the agent's project.
  if (!args[1] || args[1].startsWith("-"))
    reject("Usage: hyperframes init PROJECT_DIRECTORY [options]");
  const destination = path.resolve(args[1]);
  if (fs.existsSync(destination))
    reject(
      "Composition directory already exists. Resume it or choose a new directory.",
    );
  let hasExample = false;
  for (let index = 2; index < args.length; index++) {
    if (args[index] !== "--example" && !args[index].startsWith("--example="))
      continue;
    hasExample = true;
    const value = args[index].includes("=")
      ? args[index].slice("--example=".length)
      : args[index + 1];
    if (value !== "blank")
      reject(
        "Use the blank composition with Zenith Studio's authoring guidance.",
      );
  }
  if (!hasExample) {
    args.push("--example", "blank");
  }
  args.push("--non-interactive");
  const temporary = fs.mkdtempSync(
    path.join(os.tmpdir(), "zenith-hyperframes-"),
  );
  const project = path.join(temporary, path.basename(destination));
  scaffold = { temporary, project, destination };
  args[1] = project;
}
function cleanup() {
  if (scaffold) fs.rmSync(scaffold.temporary, { recursive: true, force: true });
}
function publishComposition() {
  const blocked = new Set([
    "AGENTS.md",
    "CLAUDE.md",
    "GEMINI.md",
    "SKILL.md",
    ".agents",
    ".claude",
    ".codex",
    ".cursor",
    ".gemini",
  ]);
  for (const entry of fs.readdirSync(scaffold.project)) {
    if (blocked.has(entry))
      fs.rmSync(path.join(scaffold.project, entry), {
        recursive: true,
        force: true,
      });
  }
  const packageFile = path.join(scaffold.project, "package.json");
  const manifest = JSON.parse(fs.readFileSync(packageFile, "utf8"));
  manifest.scripts = {
    dev: "hyperframes preview",
    check: "hyperframes check",
    render: "hyperframes render",
  };
  fs.writeFileSync(packageFile, JSON.stringify(manifest, null, 2) + "\n");
  fs.mkdirSync(path.dirname(scaffold.destination), { recursive: true });
  fs.mkdirSync(scaffold.destination); // Refuse to overwrite a concurrently created project.
  fs.cpSync(scaffold.project, scaffold.destination, {
    recursive: true,
    errorOnExist: true,
    force: false,
  });
}
const child = spawn(process.execPath, [managed.entrypoint, ...args], {
  env: { ...process.env, ...managed.env },
  // init's success output advertises upstream skill installation. Team owns
  // that guidance; preserve diagnostics on stderr and report our own success.
  stdio: ["inherit", initializing ? "ignore" : "inherit", "inherit"],
  windowsHide: true,
});
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => child.kill(signal));
}
child.on("error", () => {
  cleanup();
  console.error(
    "The managed renderer could not start. Check the Runtime computer and retry the Run.",
  );
  process.exitCode = 78;
});
child.on("exit", (code, signal) => {
  try {
    if (initializing && code === 0) {
      publishComposition();
      console.log(
        "Local composition created. Continue with Zenith Studio's assembly guide.",
      );
    }
    process.exitCode = code ?? (signal === "SIGINT" ? 130 : 143);
  } catch (error) {
    console.error(`Could not publish the local composition: ${error.message}`);
    process.exitCode = 1;
  } finally {
    cleanup();
  }
});
