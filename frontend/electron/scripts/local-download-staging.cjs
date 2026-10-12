const fs = require("node:fs");
const path = require("node:path");

const DOWNLOAD_STAGING_EXTENSIONS = new Set([
  ".dmg",
  ".exe",
  ".msi",
  ".pkg",
  ".sha256",
  ".zip",
]);

function pruneBrokenPublicDownloadSymlinks({
  downloadsDir = path.join(process.cwd(), "public", "downloads"),
  fsModule = fs,
} = {}) {
  if (!fsModule.existsSync(downloadsDir)) {
    return [];
  }

  const removed = [];
  const entries = fsModule.readdirSync(downloadsDir, { withFileTypes: true });

  for (const entry of entries) {
    if (!entry.isSymbolicLink() || !isLocalDownloadArtifactName(entry.name)) {
      continue;
    }

    const artifactPath = path.join(downloadsDir, entry.name);

    if (fsModule.existsSync(artifactPath)) {
      continue;
    }

    fsModule.rmSync(artifactPath, { force: true });
    removed.push(artifactPath);
  }

  return removed;
}

function isLocalDownloadArtifactName(filename) {
  return DOWNLOAD_STAGING_EXTENSIONS.has(path.extname(filename).toLowerCase());
}

module.exports = {
  isLocalDownloadArtifactName,
  pruneBrokenPublicDownloadSymlinks,
};
