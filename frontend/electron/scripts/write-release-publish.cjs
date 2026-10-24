// Prepares one desktop build for publishing: the installers and their
// checksums copied flat into the output folder, the download and update
// manifests, and release-publish.json, which says where each file goes.
// publish-desktop.yml uploads the files under <base URL>/<version>/ and the
// manifests at fixed paths; the manifest URLs already point there, so the
// upload copies bytes and never rewrites them. A pre-release publishes its
// files but moves no manifest.
//
// Two channels, one per app: prod (Zenith Team), version X.Y.Z or
// X.Y.Z-(alpha|beta|rc).N from a v* tag, and dev (Zenith Team Dev), version
// X.Y.Z-dev.N, or X.Y.Z-dev.N.A for a re-run attempt A. Every dev build moves
// the dev manifests.
//
// Usage: node write-release-publish.cjs --artifacts <dir> --out <dir> --version <version>
// Reads ZENITH_TEAM_DESKTOP_ENV ("prod", the default, or "dev"), GITHUB_SHA,
// GITHUB_REF and ZENITH_TEAM_DESKTOP_RELEASE_BASE_URL (https, no trailing slash).
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { DESKTOP_VARIANTS } = require("../shared/desktop-variant.cjs");

const CHANNELS = new Set(["dev", "prod"]);
const DOWNLOAD_PLATFORMS = { darwin: "macos", linux: "linux" };

function writeReleasePublish({
  artifactsDir,
  outDir,
  channel = process.env.ZENITH_TEAM_DESKTOP_ENV || "prod",
  version: requestedVersion,
  commit = process.env.GITHUB_SHA,
  ref = process.env.GITHUB_REF,
  baseUrl = process.env.ZENITH_TEAM_DESKTOP_RELEASE_BASE_URL,
  releasedAt = new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
} = {}) {
  if (!CHANNELS.has(channel)) {
    throw new Error(`ZENITH_TEAM_DESKTOP_ENV must be prod or dev, got "${channel}".`);
  }
  const appName = DESKTOP_VARIANTS[channel].appName;
  const version = parseVersion(requestedVersion, channel);
  // A dev build is the newest of its channel, so it always moves the dev
  // manifests; a prod pre-release never moves the prod ones.
  const prerelease = channel === "prod" && version.includes("-");
  if (!/^[0-9a-f]{40}$/.test(commit ?? "")) {
    throw new Error("GITHUB_SHA must be a full commit SHA.");
  }
  const releaseBaseUrl = parseBaseUrl(baseUrl);
  const artifacts = findArtifacts(artifactsDir, version, channel);
  checkArtifactSet(artifacts);

  fs.mkdirSync(outDir, { recursive: true });
  const objects = [];
  const urlOf = (artifact) =>
    `${releaseBaseUrl}/${version}/${artifact.platform}/${artifact.arch}/${artifact.name}`;

  for (const artifact of artifacts) {
    for (const name of [artifact.name, `${artifact.name}.sha256`]) {
      const target = path.join(outDir, name);
      fs.copyFileSync(path.join(artifact.dir, name), target);
      objects.push({
        asset: name,
        path: `${version}/${artifact.platform}/${artifact.arch}/${name}`,
        sha256: sha256Of(target),
      });
    }
  }

  const pointers = [];
  if (!prerelease) {
    const latest = {
      appName,
      environment: channel,
      sourceSha: commit,
      version,
      releasedAt,
      downloads: {},
      updates: {},
    };
    for (const artifact of artifacts) {
      const platform = DOWNLOAD_PLATFORMS[artifact.platform];
      // macOS users download the dmg; the zip is the Squirrel.Mac update.
      if (artifact.platform === "darwin" && artifact.kind === "zip") {
        const update = {
          name: `${appName} ${version}`,
          pub_date: releasedAt,
          type: "squirrel-mac",
          url: urlOf(artifact),
          version,
        };
        latest.updates.darwin ??= {};
        latest.updates.darwin[artifact.arch] = update;
        const feedAsset = `update-darwin-${artifact.arch}.json`;
        pointers.push(
          writeJson(outDir, feedAsset, `updates/darwin/${artifact.arch}/latest.json`, {
            url: update.url,
            name: update.name,
            notes: "",
            pub_date: releasedAt,
            version,
          }),
        );
        continue;
      }
      latest.downloads[platform] ??= {};
      latest.downloads[platform][artifact.arch] = {
        kind: artifact.kind,
        url: urlOf(artifact),
        sha256Url: `${urlOf(artifact)}.sha256`,
      };
    }
    pointers.unshift(writeJson(outDir, "latest.json", "latest.json", latest));
  }

  const publish = {
    schema: 1,
    app: "zenith-team",
    channel,
    version,
    commit,
    ref: ref ?? null,
    prerelease,
    objects,
    pointers,
  };
  fs.writeFileSync(
    path.join(outDir, "release-publish.json"),
    `${JSON.stringify(publish, null, 2)}\n`,
    "utf8",
  );
  return publish;
}

function parseVersion(version, channel = "prod") {
  const num = "(?:0|[1-9]\\d*)";
  const core = `${num}\\.${num}\\.${num}`;
  const pattern =
    channel === "dev"
      ? new RegExp(`^${core}-dev\\.${num}(?:\\.${num})?$`)
      : new RegExp(`^${core}(?:-(?:alpha|beta|rc)\\.${num})?$`);
  if (!pattern.test(version ?? "")) {
    const shape = channel === "dev" ? "X.Y.Z-dev.N or X.Y.Z-dev.N.A" : "X.Y.Z or X.Y.Z-(alpha|beta|rc).N";
    throw new Error(`A ${channel} desktop version must be ${shape}, got "${version}".`);
  }
  return version;
}

function parseBaseUrl(value) {
  const trimmed = (value ?? "").trim();
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    url = null;
  }
  if (!url || url.protocol !== "https:" || trimmed.endsWith("/") || url.search || url.hash) {
    throw new Error(
      "ZENITH_TEAM_DESKTOP_RELEASE_BASE_URL must be an https URL with no trailing slash.",
    );
  }
  return trimmed;
}

function findArtifacts(rootDir, version, channel = "prod") {
  // finalize-artifacts.cjs names them Zenith-Team-<channel>-<platform>-<arch>-<version>.
  const pattern = new RegExp(
    `^Zenith-Team-${channel}-(darwin|linux)-(arm64|x64)-(.+)\\.(dmg|zip)$`,
  );
  const artifacts = [];
  for (const filePath of listFiles(rootDir)) {
    const name = path.basename(filePath);
    const match = pattern.exec(name);
    if (!match) {
      continue;
    }
    const [, platform, arch, artifactVersion, kind] = match;
    if (artifactVersion !== version) {
      throw new Error(`${name} is version ${artifactVersion}, not ${version}.`);
    }
    const checksumPath = `${filePath}.sha256`;
    if (!fs.existsSync(checksumPath)) {
      throw new Error(`${name} has no .sha256 file.`);
    }
    const expected = fs.readFileSync(checksumPath, "utf8").trim().split(/\s+/)[0];
    if (expected !== sha256Of(filePath)) {
      throw new Error(`${name} does not match its .sha256 file.`);
    }
    artifacts.push({ arch, dir: path.dirname(filePath), kind, name, platform });
  }
  return artifacts.sort((a, b) => a.name.localeCompare(b.name));
}

// Each platform and arch ships once, and a macOS build ships both its dmg
// (download) and its zip (update).
function checkArtifactSet(artifacts) {
  if (artifacts.length === 0) {
    throw new Error("No desktop artifacts found.");
  }
  const seen = new Set();
  for (const artifact of artifacts) {
    const key = `${artifact.platform}/${artifact.arch}/${artifact.kind}`;
    if (seen.has(key)) {
      throw new Error(`More than one ${key} artifact.`);
    }
    seen.add(key);
  }
  for (const artifact of artifacts) {
    if (artifact.platform !== "darwin") {
      continue;
    }
    for (const kind of ["dmg", "zip"]) {
      if (!seen.has(`darwin/${artifact.arch}/${kind}`)) {
        throw new Error(`macOS ${artifact.arch} is missing its ${kind}.`);
      }
    }
  }
}

function writeJson(outDir, asset, publishPath, value) {
  const target = path.join(outDir, asset);
  fs.writeFileSync(target, `${JSON.stringify(value, null, 2)}\n`, "utf8");
  return { asset, path: publishPath, sha256: sha256Of(target) };
}

function listFiles(rootDir) {
  const results = [];
  for (const entry of fs.readdirSync(rootDir, { withFileTypes: true })) {
    const entryPath = path.join(rootDir, entry.name);
    if (entry.isDirectory()) {
      results.push(...listFiles(entryPath));
    } else if (entry.isFile()) {
      results.push(entryPath);
    }
  }
  return results;
}

function sha256Of(filePath) {
  return crypto.createHash("sha256").update(fs.readFileSync(filePath)).digest("hex");
}

function readArgs(argv) {
  const usage =
    "Usage: write-release-publish.cjs --artifacts <dir> --out <dir> --version <version>";
  const args = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!["--artifacts", "--out", "--version"].includes(key) || !value) {
      throw new Error(usage);
    }
    args[key.slice(2)] = value;
  }
  if (!args.artifacts || !args.out || !args.version) {
    throw new Error(usage);
  }
  return args;
}

if (require.main === module) {
  const args = readArgs(process.argv.slice(2));
  const publish = writeReleasePublish({
    artifactsDir: args.artifacts,
    outDir: args.out,
    version: args.version,
  });
  console.log(
    `[electron] ${publish.objects.length} desktop files and ${publish.pointers.length} manifests for ${publish.channel} ${publish.version}.`,
  );
}

module.exports = {
  checkArtifactSet,
  findArtifacts,
  parseBaseUrl,
  parseVersion,
  writeReleasePublish,
};
