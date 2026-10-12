"use strict";

const websitePreviewChannels = {
  open: "desktop:website-preview-open",
  bounds: "desktop:website-preview-bounds",
  command: "desktop:website-preview-command",
  close: "desktop:website-preview-close",
  event: "desktop:website-preview-event",
};

function isWebsitePreviewId(value) {
  return typeof value === "string" && /^[a-zA-Z0-9-]{1,80}$/.test(value);
}

function websitePreviewUrl(value) {
  if (typeof value !== "string" || value.length > 8192) return null;
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) &&
      !url.username &&
      !url.password
      ? url.href
      : null;
  } catch {
    return null;
  }
}

function isWebsitePreviewBounds(value) {
  return (
    value &&
    ["x", "y", "width", "height"].every(
      (key) => Number.isFinite(value[key]) && Math.abs(value[key]) <= 100000,
    ) &&
    value.width >= 0 &&
    value.height >= 0
  );
}

function isWebsitePreviewCommand(value) {
  return ["back", "forward", "reload", "focus"].includes(value);
}

function isWebsitePreviewEvent(value) {
  if (value?.type === "escape") return isWebsitePreviewId(value.id);
  const state = value?.state;
  return (
    value?.type === "state" &&
    state &&
    isWebsitePreviewId(state.id) &&
    Boolean(websitePreviewUrl(state.url)) &&
    typeof state.title === "string" &&
    typeof state.loading === "boolean" &&
    typeof state.canGoBack === "boolean" &&
    typeof state.canGoForward === "boolean" &&
    (state.error === null || typeof state.error === "string")
  );
}

module.exports = {
  websitePreviewChannels,
  isWebsitePreviewId,
  websitePreviewUrl,
  isWebsitePreviewBounds,
  isWebsitePreviewCommand,
  isWebsitePreviewEvent,
};
