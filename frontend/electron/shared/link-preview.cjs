"use strict";

const LINK_PREVIEW_CHANNEL = "desktop:load-link-preview";
const MAX_LINK_PREVIEW_IMAGE_BYTES = 5_000_000;

function linkPreviewUrl(value) {
  if (typeof value !== "string" || value.length > 8192) return null;
  try {
    const url = new URL(value);
    if (
      !["https:", "http:"].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.port
    )
      return null;
    url.hash = "";
    return url.href;
  } catch {
    return null;
  }
}

function sanitizeLinkPreview(value) {
  if (!value || typeof value !== "object") return {};
  return {
    title:
      typeof value.title === "string" ? value.title.slice(0, 200) : undefined,
    description:
      typeof value.description === "string"
        ? value.description.slice(0, 320)
        : undefined,
    image:
      typeof value.image === "string" &&
      value.image.length <=
        Math.ceil(MAX_LINK_PREVIEW_IMAGE_BYTES / 3) * 4 + 32 &&
      /^data:image\/(?:png|jpeg|webp|gif);base64,/.test(value.image)
        ? value.image
        : undefined,
  };
}

module.exports = {
  LINK_PREVIEW_CHANNEL,
  MAX_LINK_PREVIEW_IMAGE_BYTES,
  linkPreviewUrl,
  sanitizeLinkPreview,
};
