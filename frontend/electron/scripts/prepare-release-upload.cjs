const fs = require("node:fs");
const path = require("node:path");
const {
  getDesktopVariant,
} = require("../shared/desktop-variant.cjs");

const DEFAULT_RELEASE_BASE_URL =
  "https://storage.googleapis.com/my-public-bucket/boardly";
const DISTRIBUTABLE_EXTENSIONS = new Set([
  ".dmg",
  ".exe",
  ".nupkg",
  ".sha256",
  ".zip",
]);

function prepareReleaseUpload({
  cwd = process.cwd(),
  env = process.env,
  makeDir = path.join(cwd, "out", "make"),
  outputDir = path.join(cwd, "out", "release-upload"),
} = {}) {
  const packageVersion = require(path.join(cwd, "package.json")).version;
  const desktopVariant = getDesktopVariant(env);
  const releaseBaseUrl = normalizeBaseUrl(
    env.BOARDLY_DESKTOP_RELEASE_BASE_URL || DEFAULT_RELEASE_BASE_URL,
  );
  const releasedAt =
    env.BOARDLY_DESKTOP_RELEASED_AT || new Date().toISOString();
  const releaseNotes =
    env.BOARDLY_DESKTOP_RELEASE_NOTES ||
    `${desktopVariant.appName} ${packageVersion}`;
  const fragment = {
    appName: desktopVariant.appName,
    environment: desktopVariant.environment,
    sourceSha: normalizeOptionalMetadata(env.BOARDLY_DESKTOP_SOURCE_SHA),
    version: packageVersion,
    releasedAt,
    workflowRunUrl: normalizeOptionalMetadata(
      env.BOARDLY_DESKTOP_WORKFLOW_RUN_URL,
    ),
    downloads: {},
    updates: {},
  };
  const copied = [];

  fs.rmSync(outputDir, { force: true, recursive: true });
  fs.mkdirSync(outputDir, { recursive: true });

  if (!fs.existsSync(makeDir)) {
    throw new Error(`Release make directory does not exist: ${makeDir}`);
  }

  for (const artifactPath of findFiles(makeDir, isReleaseArtifactPath)) {
    const metadata = getArtifactMetadata({ artifactPath, makeDir });

    if (!metadata) {
      continue;
    }

    const publishRelativePath = path.join(
      packageVersion,
      metadata.platform,
      metadata.arch,
      metadata.filename,
    );
    const publishPath = path.join(outputDir, publishRelativePath);
    fs.mkdirSync(path.dirname(publishPath), { recursive: true });
    fs.copyFileSync(artifactPath, publishPath);
    copied.push(publishPath);

    if (metadata.platform === "win32" && metadata.maker.includes("squirrel")) {
      const updatePath = path.join(
        outputDir,
        "updates",
        metadata.platform,
        metadata.arch,
        metadata.filename,
      );
      fs.mkdirSync(path.dirname(updatePath), { recursive: true });
      fs.copyFileSync(artifactPath, updatePath);
      copied.push(updatePath);
    }

    if (metadata.isChecksum) {
      continue;
    }

    addDownloadMetadata({
      artifactPath,
      fragment,
      metadata,
      packageVersion,
      releaseBaseUrl,
    });
    addUpdateMetadata({
      fragment,
      metadata,
      packageVersion,
      releaseBaseUrl,
      releaseNotes,
      releasedAt,
      appName: desktopVariant.appName,
    });
  }

  const fragmentName = getFragmentName(fragment);
  const fragmentPath = path.join(outputDir, fragmentName);
  fs.writeFileSync(fragmentPath, `${JSON.stringify(fragment, null, 2)}\n`, "utf8");

  return {
    copied,
    fragment,
    fragmentPath,
    outputDir,
  };
}

function findFiles(rootDir, predicate) {
  const entries = fs.readdirSync(rootDir, { withFileTypes: true });
  const results = [];

  for (const entry of entries) {
    const entryPath = path.join(rootDir, entry.name);

    if (entry.isDirectory()) {
      results.push(...findFiles(entryPath, predicate));
      continue;
    }

    if (entry.isFile() && predicate(entryPath)) {
      results.push(entryPath);
    }
  }

  return results;
}

function isReleaseArtifactPath(filePath) {
  return (
    path.basename(filePath) === "RELEASES" ||
    DISTRIBUTABLE_EXTENSIONS.has(path.extname(filePath).toLowerCase())
  );
}

function getArtifactMetadata({ artifactPath, makeDir }) {
  const relativeParts = path.relative(makeDir, artifactPath).split(path.sep);
  const [maker, platformOrArch, maybeArch] = relativeParts;

  const platform = maker === "squirrel.windows" ? "win32" : platformOrArch;
  const arch =
    maker === "squirrel.windows"
      ? platformOrArch === "win32" && maybeArch
        ? maybeArch
        : platformOrArch
      : maybeArch;

  if (!maker || !platform || !arch) {
    return null;
  }

  const filename = path.basename(artifactPath);
  const isChecksum = filename.endsWith(".sha256");
  const artifactFilename = isChecksum
    ? filename.replace(/\.sha256$/, "")
    : filename;
  const extension =
    artifactFilename === "RELEASES"
      ? ""
      : path.extname(artifactFilename).toLowerCase();

  return {
    arch,
    extension,
    filename,
    isChecksum,
    maker,
    platform,
  };
}

function addDownloadMetadata({
  artifactPath,
  fragment,
  metadata,
  packageVersion,
  releaseBaseUrl,
}) {
  if (![".dmg", ".exe", ".zip"].includes(metadata.extension)) {
    return;
  }

  const platform = getDownloadPlatform(metadata.platform);

  if (!platform) {
    return;
  }

  if (metadata.platform === "darwin" && metadata.extension !== ".dmg") {
    setDownloadIfEmpty(fragment, platform, metadata.arch, {
      kind: "zip",
      url: getArtifactUrl({ metadata, packageVersion, releaseBaseUrl }),
      sha256Url: getChecksumUrl({
        artifactPath,
        metadata,
        packageVersion,
        releaseBaseUrl,
      }),
    });
    return;
  }

  if (metadata.platform === "win32" && metadata.extension === ".zip") {
    setDownloadIfEmpty(fragment, platform, metadata.arch, {
      kind: "zip",
      url: getArtifactUrl({ metadata, packageVersion, releaseBaseUrl }),
      sha256Url: getChecksumUrl({
        artifactPath,
        metadata,
        packageVersion,
        releaseBaseUrl,
      }),
    });
    return;
  }

  setDownload(fragment, platform, metadata.arch, {
    kind: metadata.extension.slice(1),
    url: getArtifactUrl({ metadata, packageVersion, releaseBaseUrl }),
    sha256Url: getChecksumUrl({
      artifactPath,
      metadata,
      packageVersion,
      releaseBaseUrl,
    }),
  });
}

function addUpdateMetadata({
  appName,
  fragment,
  metadata,
  packageVersion,
  releaseBaseUrl,
  releaseNotes,
  releasedAt,
}) {
  if (metadata.platform === "darwin" && metadata.extension === ".zip") {
    setNestedValue(fragment.updates, ["darwin", metadata.arch], {
      name: `${appName} ${packageVersion}`,
      notes: releaseNotes,
      pub_date: releasedAt,
      type: "squirrel-mac",
      url: getArtifactUrl({ metadata, packageVersion, releaseBaseUrl }),
      version: packageVersion,
    });
  }

  if (metadata.platform === "win32" && metadata.filename === "RELEASES") {
    setNestedValue(fragment.updates, ["win32", metadata.arch], {
      type: "squirrel-windows",
      url: `${releaseBaseUrl}/updates/win32/${encodeUrlSegment(metadata.arch)}`,
      version: packageVersion,
    });
  }
}

function setDownload(fragment, platform, arch, value) {
  setNestedValue(fragment.downloads, [platform, arch], value);
}

function setDownloadIfEmpty(fragment, platform, arch, value) {
  const existing = fragment.downloads[platform]?.[arch];

  if (!existing) {
    setDownload(fragment, platform, arch, value);
  }
}

function setNestedValue(target, keys, value) {
  let cursor = target;

  for (const key of keys.slice(0, -1)) {
    cursor[key] ??= {};
    cursor = cursor[key];
  }

  cursor[keys.at(-1)] = value;
}

function getDownloadPlatform(platform) {
  switch (platform) {
    case "darwin":
      return "macos";
    case "win32":
      return "windows";
    case "linux":
      return "linux";
    default:
      return null;
  }
}

function getArtifactUrl({ metadata, packageVersion, releaseBaseUrl }) {
  return [
    releaseBaseUrl,
    encodeUrlSegment(packageVersion),
    encodeUrlSegment(metadata.platform),
    encodeUrlSegment(metadata.arch),
    encodeUrlSegment(metadata.filename),
  ].join("/");
}

function getChecksumUrl({
  artifactPath,
  metadata,
  packageVersion,
  releaseBaseUrl,
}) {
  const checksumPath = `${artifactPath}.sha256`;

  if (!fs.existsSync(checksumPath)) {
    return null;
  }

  return `${getArtifactUrl({
    metadata,
    packageVersion,
    releaseBaseUrl,
  })}.sha256`;
}

function getFragmentName(fragment) {
  const targets = Object.entries(fragment.downloads).flatMap(
    ([platform, architectures]) =>
      Object.keys(architectures).map((arch) => `${platform}-${arch}`),
  );
  const updateTargets = Object.entries(fragment.updates).flatMap(
    ([platform, architectures]) =>
      Object.keys(architectures).map((arch) => `${platform}-${arch}`),
  );
  const downloads = targets.join("-") || "artifact";
  const updates = updateTargets.join("-") || "updates";

  return `release-fragment-${downloads}-${updates}.json`;
}

function encodeUrlSegment(value) {
  return encodeURIComponent(value).replace(/%2F/gi, "/");
}

function normalizeBaseUrl(value) {
  return String(value).trim().replace(/\/+$/, "");
}

function normalizeOptionalMetadata(value) {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

if (require.main === module) {
  const result = prepareReleaseUpload();
  console.log(
    `[electron] Prepared ${result.copied.length} release file(s) in ${result.outputDir}`,
  );
}

module.exports = {
  DEFAULT_RELEASE_BASE_URL,
  prepareReleaseUpload,
};
