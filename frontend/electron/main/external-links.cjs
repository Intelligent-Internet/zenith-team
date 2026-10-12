const { shell } = require("electron");
const {
  isAppNavigationUrl,
  isSafeExternalUrl,
} = require("../shared/url-policy.cjs");

async function openExternalUrl(url) {
  if (!isSafeExternalUrl(url)) {
    return { ok: false, reason: "Blocked unsupported external URL." };
  }

  // The OS may have no handler for the link; the caller gets an answer, not
  // a rejection.
  try {
    await shell.openExternal(url);
  } catch {
    return { ok: false, reason: "The link could not be opened." };
  }
  return { ok: true };
}

module.exports = {
  isAppNavigationUrl,
  isSafeExternalUrl,
  openExternalUrl,
};
