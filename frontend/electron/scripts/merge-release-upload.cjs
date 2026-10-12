const fs = require("node:fs");
const path = require("node:path");

function mergeReleaseUpload({
  cwd = process.cwd(),
  stagingDir = path.join(cwd, "out", "release-staging"),
  outputDir = path.join(cwd, "out", "gcs-release"),
} = {}) {
  if (!fs.existsSync(stagingDir)) {
    throw new Error(`Release staging directory does not exist: ${stagingDir}`);
  }

  fs.rmSync(outputDir, { force: true, recursive: true });
  fs.mkdirSync(outputDir, { recursive: true });

  copyPublishTree({ outputDir, stagingDir });

  const fragments = findFiles(stagingDir, (filePath) =>
    /^release-fragment-.+\.json$/.test(path.basename(filePath)),
  ).map((filePath) => JSON.parse(fs.readFileSync(filePath, "utf8")));
  const manifest = mergeFragments(fragments);

  fs.writeFileSync(
    path.join(outputDir, "latest.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
    "utf8",
  );
  writeVersionReleaseMetadata({ manifest, outputDir });
  writeMacUpdateFeeds({ manifest, outputDir });

  return {
    manifest,
    outputDir,
  };
}

function copyPublishTree({ outputDir, stagingDir }) {
  for (const entry of fs.readdirSync(stagingDir, { withFileTypes: true })) {
    if (/^release-fragment-.+\.json$/.test(entry.name)) {
      continue;
    }

    const sourcePath = path.join(stagingDir, entry.name);
    const targetPath = path.join(outputDir, entry.name);

    fs.cpSync(sourcePath, targetPath, { recursive: true });
  }
}

function mergeFragments(fragments) {
  if (fragments.length === 0) {
    throw new Error("No release fragments were found.");
  }

  const [first] = fragments;
  const manifest = {
    appName: first.appName,
    environment: first.environment,
    sourceSha: first.sourceSha ?? null,
    version: first.version,
    releasedAt: first.releasedAt,
    workflowRunUrl: first.workflowRunUrl ?? null,
    downloads: {},
    updates: {},
  };

  for (const fragment of fragments) {
    if (fragment.version !== manifest.version) {
      throw new Error("Release fragments contain mismatched versions.");
    }

    for (const field of [
      "appName",
      "environment",
      "sourceSha",
      "workflowRunUrl",
    ]) {
      if ((fragment[field] ?? null) !== (manifest[field] ?? null)) {
        throw new Error(
          `Release fragments contain mismatched ${field} metadata.`,
        );
      }
    }

    if (fragment.releasedAt < manifest.releasedAt) {
      manifest.releasedAt = fragment.releasedAt;
    }

    deepMerge(manifest.downloads, fragment.downloads ?? {});
    deepMerge(manifest.updates, fragment.updates ?? {});
  }

  return manifest;
}

function writeVersionReleaseMetadata({ manifest, outputDir }) {
  const versionDirectory = path.join(outputDir, manifest.version);
  fs.mkdirSync(versionDirectory, { recursive: true });
  fs.writeFileSync(
    path.join(versionDirectory, "release.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
    "utf8",
  );
}

function deepMerge(target, source) {
  for (const [key, value] of Object.entries(source)) {
    if (
      value &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      target[key] &&
      typeof target[key] === "object" &&
      !Array.isArray(target[key])
    ) {
      deepMerge(target[key], value);
      continue;
    }

    target[key] = value;
  }
}

function writeMacUpdateFeeds({ manifest, outputDir }) {
  const darwinFeeds = manifest.updates.darwin ?? {};

  for (const [arch, feed] of Object.entries(darwinFeeds)) {
    if (feed.type !== "squirrel-mac") {
      continue;
    }

    const feedPath = path.join(outputDir, "updates", "darwin", arch, "latest.json");
    fs.mkdirSync(path.dirname(feedPath), { recursive: true });
    fs.writeFileSync(
      feedPath,
      `${JSON.stringify(
        {
          name: feed.name,
          notes: feed.notes,
          pub_date: feed.pub_date,
          url: feed.url,
          version: feed.version,
        },
        null,
        2,
      )}\n`,
      "utf8",
    );
  }
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

if (require.main === module) {
  const [stagingDir, outputDir] = process.argv.slice(2);
  const result = mergeReleaseUpload({
    ...(stagingDir ? { stagingDir: path.resolve(stagingDir) } : {}),
    ...(outputDir ? { outputDir: path.resolve(outputDir) } : {}),
  });

  console.log(
    `[electron] Wrote release manifest for ${result.manifest.version} to ${result.outputDir}`,
  );
}

module.exports = {
  mergeFragments,
  mergeReleaseUpload,
};
