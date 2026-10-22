// Prepares a release's desktop files for the GitHub Release: the installers
// and their checksums copied flat into the output folder, the download and
// update manifests, and release-publish.json, which tells a static host where
// each file goes. A host publishes the files under <base URL>/<version>/ and
// the manifests at fixed paths; the manifest URLs already point there, so the
// host copies bytes and never rewrites them. A pre-release publishes its files
// but moves no manifest.
//
// Usage: node write-release-publish.cjs --artifacts <dir> --out <dir>
// Reads GITHUB_REF_NAME (the vX.Y.Z tag), GITHUB_SHA, PRERELEASE ("true" or
// "false") and ZENITH_TEAM_DESKTOP_RELEASE_BASE_URL (https, no trailing slash).
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const APP_NAME = "Zenith Team";
const ARTIFACT_PATTERN =
  /^Zenith-Team-prod-(darwin|linux)-(arm64|x64)-(.+)\.(dmg|zip)$/;
const DOWNLOAD_PLATFORMS = { darwin: "macos", linux: "linux" };

function writeReleasePublish({
  artifactsDir,
  outDir,
  tag = process.env.GITHUB_REF_NAME,
  commit = process.env.GITHUB_SHA,
  prerelease = process.env.PRERELEASE === "true",
  baseUrl = process.env.ZENITH_TEAM_DESKTOP_RELEASE_BASE_URL,
  releasedAt = new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
} = {}) {
  const version = parseVersion(tag);
  if (!/^[0-9a-f]{40}$/.test(commit ?? "")) {
    throw new Error("GITHUB_SHA must be a full commit SHA.");
  }
  const releaseBaseUrl = parseBaseUrl(baseUrl);
  const artifacts = findArtifacts(artifactsDir, version);
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
      appName: APP_NAME,
      environment: "prod",
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
          name: `${APP_NAME} ${version}`,
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
    tag,
    version,
    commit,
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

function parseVersion(tag) {
  const match = /^v(\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.\d+)?)$/.exec(tag ?? "");
  if (!match) {
    throw new Error(`Release tag must be vX.Y.Z or vX.Y.Z-(alpha|beta|rc).N, got "${tag}".`);
  }
  return match[1];
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

function findArtifacts(rootDir, version) {
  const artifacts = [];
  for (const filePath of listFiles(rootDir)) {
    const name = path.basename(filePath);
    const match = ARTIFACT_PATTERN.exec(name);
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
  const args = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if ((key !== "--artifacts" && key !== "--out") || !value) {
      throw new Error("Usage: write-release-publish.cjs --artifacts <dir> --out <dir>");
    }
    args[key.slice(2)] = value;
  }
  if (!args.artifacts || !args.out) {
    throw new Error("Usage: write-release-publish.cjs --artifacts <dir> --out <dir>");
  }
  return args;
}

if (require.main === module) {
  const args = readArgs(process.argv.slice(2));
  const publish = writeReleasePublish({ artifactsDir: args.artifacts, outDir: args.out });
  console.log(
    `[electron] ${publish.objects.length} desktop files and ${publish.pointers.length} manifests for ${publish.tag}.`,
  );
}

module.exports = {
  checkArtifactSet,
  findArtifacts,
  parseBaseUrl,
  parseVersion,
  writeReleasePublish,
};
