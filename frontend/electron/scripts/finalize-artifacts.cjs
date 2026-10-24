const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const {
  DEV_ENV_FILES,
  PROD_ENV_FILES,
  loadElectronEnvFiles,
} = require("./env-files.cjs");
const {
  getDesktopVariant,
  normalizeDesktopEnvironment,
} = require("../shared/desktop-variant.cjs");

const defaultDesktopEnvironment = normalizeDesktopEnvironment(
  process.env.ZENITH_TEAM_DESKTOP_ENV,
);
loadElectronEnvFiles({
  files: defaultDesktopEnvironment === "dev" ? DEV_ENV_FILES : PROD_ENV_FILES,
});
const supportedArtifactExtensions = new Set([".dmg", ".exe", ".nupkg", ".zip"]);

function finalizeArtifacts({
  desktopEnvironmentName = defaultDesktopEnvironment,
  makeDirPath = getMakeDir(),
  packageVersionValue = getPackageVersion(),
} = {}) {
  if (!fs.existsSync(makeDirPath)) {
    return [];
  }

  const artifactPaths = findFiles(makeDirPath, (filePath) =>
    isDistributableArtifactPath(filePath),
  )
    .map((artifactPath) =>
      renameArtifact(artifactPath, {
        desktopEnvironmentName,
        makeDirPath,
        packageVersionValue,
      }),
    )
    .filter(Boolean);

  for (const artifactPath of artifactPaths) {
    writeSha256File(artifactPath);
  }

  return artifactPaths;
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

function isDistributableArtifactPath(filePath) {
  return (
    path.basename(filePath) === "RELEASES" ||
    supportedArtifactExtensions.has(path.extname(filePath))
  );
}

function renameArtifact(
  artifactPath,
  {
    desktopEnvironmentName = defaultDesktopEnvironment,
    makeDirPath = getMakeDir(),
    packageVersionValue = getPackageVersion(),
  } = {},
) {
  const filename = path.basename(artifactPath);
  const extension = path.extname(filename);
  const desktopVariant = getDesktopVariant({
    ZENITH_TEAM_DESKTOP_ENV: desktopEnvironmentName,
  });
  const artifactMetadata = getArtifactMetadata(
    artifactPath,
    filename,
    extension,
    {
      appName: desktopVariant.appName,
      makeDirPath,
      packageVersionValue,
    },
  );

  if (
    filename === "RELEASES" ||
    extension === ".nupkg" ||
    isSquirrelWindowsArtifact(artifactMetadata)
  ) {
    return artifactPath;
  }

  if (!artifactMetadata || !packageVersionValue) {
    return artifactPath;
  }

  if (!filename.startsWith(`${desktopVariant.appName}-`)) {
    return artifactPath;
  }

  const { arch, maker, platform } = artifactMetadata;
  const directory = path.join(makeDirPath, maker, platform, arch);
  const nextPath = path.join(
    directory,
    `Zenith-Team-${desktopEnvironmentName}-${platform}-${arch}-${packageVersionValue}${extension}`,
  );

  if (artifactPath === nextPath) {
    return artifactPath;
  }

  fs.mkdirSync(directory, { recursive: true });
  fs.renameSync(artifactPath, nextPath);

  const staleChecksumPath = `${artifactPath}.sha256`;

  if (fs.existsSync(staleChecksumPath)) {
    fs.rmSync(staleChecksumPath);
  }

  return nextPath;
}

function getArtifactMetadata(
  artifactPath,
  filename,
  extension,
  {
    appName = getDesktopVariant({
      ZENITH_TEAM_DESKTOP_ENV: defaultDesktopEnvironment,
    }).appName,
    makeDirPath = getMakeDir(),
    packageVersionValue = getPackageVersion(),
  } = {},
) {
  const relativeParts = path
    .relative(makeDirPath, artifactPath)
    .split(path.sep);
  const [maker, platformOrArch, maybeArch] = relativeParts;

  if (maker === "squirrel.windows" && platformOrArch) {
    const squirrelArch =
      platformOrArch === "win32" && maybeArch ? maybeArch : platformOrArch;
    return { maker, platform: "win32", arch: squirrelArch };
  }

  // Forge 8's dmg maker writes dmg/<arch>/<name>.dmg, with no platform folder.
  if (maker === "dmg" && platformOrArch && relativeParts.length === 3) {
    return { maker, platform: "darwin", arch: platformOrArch };
  }

  const platform = platformOrArch;
  const arch = maybeArch;

  if (maker && platform && arch) {
    return { maker, platform, arch };
  }

  if (extension !== ".dmg") {
    return null;
  }

  const archMatch = filename.match(
    new RegExp(
      `^${escapeRegExp(appName)}-${escapeRegExp(packageVersionValue)}-(.+)\\.dmg$`,
    ),
  );

  if (!archMatch) {
    return null;
  }

  return { maker: "dmg", platform: "darwin", arch: archMatch[1] };
}

function isSquirrelWindowsArtifact(artifactMetadata) {
  return (
    artifactMetadata?.platform === "win32" &&
    artifactMetadata.maker === "squirrel.windows"
  );
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function writeSha256File(artifactPath) {
  const checksum = crypto
    .createHash("sha256")
    .update(fs.readFileSync(artifactPath))
    .digest("hex");
  const checksumPath = `${artifactPath}.sha256`;
  const checksumBody = `${checksum}  ${path.basename(artifactPath)}\n`;

  fs.writeFileSync(checksumPath, checksumBody, "utf8");
}

function getMakeDir(cwd = process.cwd()) {
  return path.join(cwd, "out", "make");
}

function getPackageVersion(cwd = process.cwd()) {
  return require(path.join(cwd, "package.json")).version;
}

if (require.main === module) {
  finalizeArtifacts();
}

module.exports = {
  finalizeArtifacts,
  getArtifactMetadata,
  isDistributableArtifactPath,
  renameArtifact,
};
