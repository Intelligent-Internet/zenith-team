"use strict";

const { posix } = require("node:path");

const POSIX_PROVIDER_BIN_DIRECTORIES = [
  "/opt/homebrew/bin",
  "/opt/homebrew/sbin",
  "/usr/local/bin",
  "/usr/local/sbin",
];
const POSIX_HOME_PROVIDER_BIN_DIRECTORIES = [
  ".local/bin",
  ".bun/bin",
  ".cargo/bin",
  "bin",
];

function safeHostEnvironmentKey(key) {
  return (
    /^(?:PATH|HOME|USERPROFILE|SYSTEMROOT|WINDIR|TMPDIR|TMP|TEMP|LANG|SHELL|USER|LOGNAME|XDG_CONFIG_HOME|XDG_CACHE_HOME|XDG_DATA_HOME)$/iu.test(
      key,
    ) ||
    /^LC_[A-Z_]+$/iu.test(key) ||
    /^BOARDLY_(?:CLAUDE|CODEX)_(?:PATH|MODEL)$/iu.test(key)
  );
}

function providerBinDirectories(home, platform = process.platform) {
  if (platform === "win32") return [];
  const directories = [...POSIX_PROVIDER_BIN_DIRECTORIES];
  const normalizedHome = typeof home === "string" ? home.trim() : "";
  if (!normalizedHome) return directories;
  return directories.concat(
    POSIX_HOME_PROVIDER_BIN_DIRECTORIES.map((relative) =>
      posix.join(normalizedHome, relative),
    ),
  );
}

function augmentProviderPath(currentPath, home, platform = process.platform) {
  const additions = providerBinDirectories(home, platform);
  if (additions.length === 0) return currentPath ?? "";
  const existing = (currentPath ?? "")
    .split(":")
    .filter((entry) => entry.length > 0);
  const seen = new Set(existing);
  return existing
    .concat(additions.filter((directory) => !seen.has(directory)))
    .join(":");
}

function filterHostEnvironment(source, platform = process.platform) {
  const result = {};
  for (const [key, value] of Object.entries(source ?? {})) {
    if (typeof value === "string" && safeHostEnvironmentKey(key)) {
      result[key] = value;
    }
  }
  const augmentedPath = augmentProviderPath(
    result.PATH,
    result.HOME ?? result.USERPROFILE,
    platform,
  );
  if (augmentedPath) result.PATH = augmentedPath;
  return result;
}

function daemonEnvironment(source, additions) {
  const result = { ...filterHostEnvironment(source), ...additions };
  return result;
}

module.exports = {
  augmentProviderPath,
  daemonEnvironment,
  filterHostEnvironment,
  providerBinDirectories,
  safeHostEnvironmentKey,
};
