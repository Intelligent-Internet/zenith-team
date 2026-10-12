"use strict";

const { parse } = require("parse5");
const { isTrustedMainFrame } = require("./trusted-frame.cjs");
const {
  openPreviewResponse,
  readPreviewHead,
  readPreviewImage,
} = require("./link-preview-request.cjs");
const {
  LINK_PREVIEW_CHANNEL,
  MAX_LINK_PREVIEW_IMAGE_BYTES,
  linkPreviewUrl,
  sanitizeLinkPreview,
} = require("../shared/link-preview.cjs");

function registerDesktopLinkPreview({ ipcMain, getMainWindow }) {
  const load = createLinkPreviewLoader();
  ipcMain.handle(LINK_PREVIEW_CHANNEL, (event, url) => {
    if (!isTrustedMainFrame(event, getMainWindow())) return {};
    return load(url);
  });
}

function createLinkPreviewLoader() {
  const cache = new Map();
  const pending = new Map();
  const queue = [];
  let active = 0;
  let cacheBytes = 0;

  function removeCached(url) {
    cacheBytes -= cache.get(url).bytes;
    cache.delete(url);
  }

  function remember(url, preview) {
    const hasMetadata = Boolean(
      preview.title || preview.description || preview.image,
    );
    const bytes = 2 * (url.length + JSON.stringify(preview).length);
    cache.set(url, {
      preview,
      bytes,
      expiresAt: Date.now() + (hasMetadata ? 15 * 60_000 : 60_000),
    });
    cacheBytes += bytes;
    while (cache.size > 100 || cacheBytes > 24 * 1024 * 1024)
      removeCached(cache.keys().next().value);
  }

  function drain() {
    while (active < 3 && queue.length) {
      const job = queue.shift();
      active++;
      void fetchLinkPreview(job.url)
        .catch(() => ({}))
        .then((preview) => {
          remember(job.url, preview);
          pending.delete(job.url);
          active--;
          job.resolve(preview);
          drain();
        });
    }
  }

  return function load(value) {
    const url = linkPreviewUrl(value);
    if (!url) return Promise.resolve({});
    const cached = cache.get(url);
    if (cached) {
      if (cached.expiresAt > Date.now()) {
        cache.delete(url);
        cache.set(url, cached);
        return Promise.resolve(cached.preview);
      }
      removeCached(url);
    }
    if (pending.has(url)) return pending.get(url);
    if (pending.size >= 32) return Promise.resolve({});
    const promise = new Promise((resolve) => queue.push({ url, resolve }));
    pending.set(url, promise);
    drain();
    return promise;
  };
}

async function fetchLinkPreview(url) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 8_000);
  try {
    const page = await openPreviewResponse(url, controller.signal);
    if (contentType(page.response) !== "text/html") {
      page.response.destroy();
      return {};
    }
    const metadata = parseMetadata(await readPreviewHead(page.response));
    let image;
    if (metadata.image) {
      try {
        image = await fetchPreviewImage(
          new URL(metadata.image, page.url).href,
          controller.signal,
        );
      } catch {
        // A missing thumbnail should not discard the page's text metadata.
      }
    }
    return sanitizeLinkPreview({ ...metadata, image });
  } finally {
    clearTimeout(timeout);
  }
}

function parseMetadata(html) {
  const document = parse(html);
  const root = document.childNodes.find((node) => node.tagName === "html");
  const head = root?.childNodes.find((node) => node.tagName === "head");
  const tags = new Map();
  let title;
  for (const node of head?.childNodes ?? []) {
    if (node.tagName === "title" && !title)
      title = node.childNodes
        .map((child) => child.value ?? "")
        .join("")
        .trim();
    if (node.tagName !== "meta") continue;
    const attributes = new Map(
      node.attrs.map(({ name, value }) => [name, value]),
    );
    const key = (
      attributes.get("property") || attributes.get("name")
    )?.toLowerCase();
    const content = attributes.get("content")?.trim();
    if (content && !tags.has(key)) tags.set(key, content);
  }
  return {
    title: tags.get("og:title") || tags.get("twitter:title") || title,
    description:
      tags.get("og:description") ||
      tags.get("description") ||
      tags.get("twitter:description"),
    image: tags.get("og:image") || tags.get("twitter:image"),
  };
}

function contentType(response) {
  return response.headers["content-type"]?.split(";")[0].trim().toLowerCase();
}

async function fetchPreviewImage(url, signal) {
  const { response } = await openPreviewResponse(url, signal);
  const type = contentType(response);
  if (!["image/png", "image/jpeg", "image/webp", "image/gif"].includes(type)) {
    response.destroy();
    return;
  }
  const bytes = await readPreviewImage(response, MAX_LINK_PREVIEW_IMAGE_BYTES);
  if (!isRasterImage(bytes, type)) return;
  return `data:${type};base64,${bytes.toString("base64")}`;
}

function isRasterImage(bytes, type) {
  if (type === "image/png")
    return bytes
      .subarray(0, 8)
      .equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
  if (type === "image/jpeg")
    return bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255;
  if (type === "image/gif")
    return ["GIF87a", "GIF89a"].includes(bytes.toString("ascii", 0, 6));
  return (
    type === "image/webp" &&
    bytes.toString("ascii", 0, 4) === "RIFF" &&
    bytes.toString("ascii", 8, 12) === "WEBP"
  );
}

module.exports = { registerDesktopLinkPreview, createLinkPreviewLoader };
