const path = require("node:path");

function assertReleaseVersion({
  cwd = process.cwd(),
  packageVersion = require(path.join(cwd, "package.json")).version,
  tagName = process.env.GITHUB_REF_NAME,
} = {}) {
  const normalizedTag = normalizeReleaseTag(tagName);

  if (!normalizedTag) {
    throw new Error("Release tag is required and must look like v0.1.0.");
  }

  if (normalizedTag !== packageVersion) {
    throw new Error(
      `Release tag v${normalizedTag} does not match frontend/package.json version ${packageVersion}.`,
    );
  }

  return packageVersion;
}

function normalizeReleaseTag(tagName) {
  if (typeof tagName !== "string") {
    return "";
  }

  const normalized = tagName.trim().replace(/^desktop-/i, "").replace(/^v/i, "");

  return /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(normalized)
    ? normalized
    : "";
}

if (require.main === module) {
  const version = assertReleaseVersion();
  console.log(version);
}

module.exports = {
  assertReleaseVersion,
  normalizeReleaseTag,
};
