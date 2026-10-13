import type { DesktopLinkPreview } from "../../../../electron/shared/desktop-bridge.types";
import { getBoardlyDesktopBridge } from "@/shared/desktop/desktop-environment.service";

export type ChatLinkPreview = DesktopLinkPreview;

export function messagePreviewLinks(text: string) {
  let fence: { character: string; length: number } | undefined;
  const prose = text
    .split("\n")
    .filter((line) => {
      const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
      if (marker) {
        if (!fence)
          fence = { character: marker[1][0], length: marker[1].length };
        else if (
          marker[1][0] === fence.character &&
          marker[1].length >= fence.length &&
          line.slice(marker[0].length).trim() === ""
        )
          fence = undefined;
        return false;
      }
      return !fence && !/^(?: {4}|\t)/.test(line);
    })
    .join("\n")
    .replace(/(`+)[\s\S]*?\1|!\[[^\]]*\]\([^\n]*?\)/g, "");
  const links = new Set<string>();
  for (const match of prose.matchAll(/https?:\/\/[^\s<>"`]+/gi)) {
    let value = match[0].replace(/[.,!?;:'\]}]+$/, "");
    while (
      value.endsWith(")") &&
      (value.match(/\)/g)?.length ?? 0) > (value.match(/\(/g)?.length ?? 0)
    )
      value = value.slice(0, -1);
    try {
      const url = new URL(value);
      if (!url.username && !url.password) links.add(url.href);
    } catch {
      /* An incomplete URL stays in the message until it is complete. */
    }
    if (links.size === 3) break;
  }
  return [...links];
}

const previews = new Map<string, Promise<ChatLinkPreview>>();

export function loadChatLinkPreview(url: string): Promise<ChatLinkPreview> {
  const desktop = getBoardlyDesktopBridge();
  // Desktop owns its bounded cache; retaining its image data here would duplicate it.
  if (desktop?.loadLinkPreview)
    return desktop.loadLinkPreview(url).catch(() => ({}));
  const cached = previews.get(url);
  if (cached) return cached;
  if (previews.size >= 100) previews.delete(previews.keys().next().value!);
  const preview = fetchPreview(url)
    .catch((): ChatLinkPreview => ({}))
    .then((result) => {
      if (!result.title && !result.description && !result.image)
        previews.delete(url);
      return result;
    });
  previews.set(url, preview);
  return preview;
}

async function fetchPreview(url: string): Promise<ChatLinkPreview> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 5_000);
  try {
    // A CORS failure intentionally falls back to the original, clickable URL.
    const response = await fetch(url, {
      signal: controller.signal,
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    if (
      !response.ok ||
      !response.headers.get("content-type")?.includes("text/html") ||
      !response.body
    )
      return {};
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let html = "";
    let bytes = 0;
    try {
      while (bytes < 512_000) {
        const { done, value } = await reader.read();
        if (done) break;
        html += decoder.decode(value.subarray(0, 512_000 - bytes), {
          stream: true,
        });
        bytes += value.length;
        if (/<\/head\s*>/i.test(html)) break;
      }
    } finally {
      await reader.cancel();
    }
    // Template content is inert and never mounted. Only metadata strings leave it.
    const template = document.createElement("template");
    template.innerHTML = html;
    const meta = (selector: string) =>
      template.content
        .querySelector<HTMLMetaElement>(selector)
        ?.content?.trim();
    const title =
      meta('meta[property="og:title"]') ||
      meta('meta[name="twitter:title"]') ||
      template.content.querySelector("title")?.textContent?.trim();
    const description =
      meta('meta[property="og:description"]') ||
      meta('meta[name="description"]') ||
      meta('meta[name="twitter:description"]');
    const image =
      meta('meta[property="og:image"]') || meta('meta[name="twitter:image"]');
    let imageUrl: string | undefined;
    if (image) {
      try {
        const parsed = new URL(image, response.url || url);
        if (
          ["https:", "http:"].includes(parsed.protocol) &&
          !parsed.username &&
          !parsed.password
        )
          imageUrl = parsed.href;
      } catch {
        /* Keep the text preview when the thumbnail URL is invalid. */
      }
    }
    return {
      title: title?.slice(0, 200),
      description: description?.slice(0, 320),
      image: imageUrl
        ? await loadPreviewImage(imageUrl, controller.signal).catch(
            () => undefined,
          )
        : undefined,
    };
  } finally {
    window.clearTimeout(timeout);
  }
}

// Render only credential-free, bounded raster bytes, never an external image URL.
async function loadPreviewImage(
  url: string,
  signal: AbortSignal,
): Promise<string | undefined> {
  const response = await fetch(url, {
    signal,
    credentials: "omit",
    referrerPolicy: "no-referrer",
  });
  const type = response.headers.get("content-type")?.split(";")[0].trim();
  if (
    !response.ok ||
    !type ||
    !["image/png", "image/jpeg", "image/webp", "image/gif"].includes(type) ||
    !response.body
  )
    return;
  const reader = response.body.getReader();
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let length = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.length;
      if (length > 5_000_000) return;
      chunks.push(new Uint8Array(value));
    }
  } finally {
    await reader.cancel();
  }
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onerror = () => resolve(undefined);
    reader.onload = () =>
      resolve(typeof reader.result === "string" ? reader.result : undefined);
    reader.readAsDataURL(new Blob(chunks, { type }));
  });
}
