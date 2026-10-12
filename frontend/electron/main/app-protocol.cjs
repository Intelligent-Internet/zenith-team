const { constants } = require("node:fs");
const { open } = require("node:fs/promises");
const { homedir } = require("node:os");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { net, protocol } = require("electron");

const BOARDLY_APP_PROTOCOL = "boardly-app";
const BOARDLY_APP_HOST = "renderer";
const BOARDLY_APP_ORIGIN = `${BOARDLY_APP_PROTOCOL}://${BOARDLY_APP_HOST}`;
const BOARDLY_APP_INDEX_URL = `${BOARDLY_APP_ORIGIN}/index.html`;
const CODEX_GENERATED_IMAGE_PROTOCOL = "boardly-generated-image";
const CODEX_GENERATED_IMAGE_HOST = "codex";
const PLUGIN_ICON_PROTOCOL = "boardly-plugin-icon";
const PLUGIN_ICON_HOST = "local";
const MAX_GENERATED_IMAGE_BYTES = 32 * 1024 * 1024;
const MAX_PLUGIN_ICON_BYTES = 4 * 1024 * 1024;
const PLUGIN_ICON_CONTENT_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/svg+xml",
  "image/webp",
]);
// The daemon serves an SVG icon with this policy; it is the fallback should
// an SVG ever arrive without one.
const PLUGIN_ICON_SVG_CONTENT_SECURITY_POLICY =
  "default-src 'none'; style-src 'unsafe-inline'; sandbox";
const CODEX_THREAD_ID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;
const CODEX_IMAGE_FILE_PATTERN =
  /^(?:exec-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|call_[A-Za-z0-9_-]{16,128})\.(?:png|jpe?g|webp)$/u;

function registerPackagedAppProtocolScheme(protocolModule = protocol) {
  protocolModule.registerSchemesAsPrivileged([
    {
      scheme: BOARDLY_APP_PROTOCOL,
      privileges: {
        standard: true,
        secure: true,
        supportFetchAPI: true,
        stream: true,
        corsEnabled: true,
      },
    },
    {
      scheme: CODEX_GENERATED_IMAGE_PROTOCOL,
      privileges: {
        standard: true,
        secure: true,
        supportFetchAPI: true,
        stream: true,
        corsEnabled: true,
      },
    },
    {
      scheme: PLUGIN_ICON_PROTOCOL,
      privileges: {
        standard: true,
        secure: true,
        supportFetchAPI: true,
        stream: true,
        corsEnabled: true,
      },
    },
  ]);
}

function registerPluginIconProtocol({
  protocol: protocolModule = protocol,
  requestPluginIcon,
} = {}) {
  protocolModule.handle(PLUGIN_ICON_PROTOCOL, async (request) => {
    const iconRequest = getPluginIconRequestForProtocolUrl(request.url);
    if (!iconRequest || typeof requestPluginIcon !== "function") {
      return new Response("Not found", { status: 404 });
    }

    try {
      const icon = await requestPluginIcon(iconRequest);
      if (
        !icon ||
        !PLUGIN_ICON_CONTENT_TYPES.has(icon.contentType) ||
        !(icon.bytes instanceof Uint8Array) ||
        icon.bytes.byteLength < 1 ||
        icon.bytes.byteLength > MAX_PLUGIN_ICON_BYTES
      ) {
        return new Response("Not found", { status: 404 });
      }
      const contentSecurityPolicy =
        typeof icon.contentSecurityPolicy === "string"
          ? icon.contentSecurityPolicy
          : icon.contentType === "image/svg+xml"
            ? PLUGIN_ICON_SVG_CONTENT_SECURITY_POLICY
            : null;
      return new Response(icon.bytes, {
        headers: {
          "Cache-Control": "private, max-age=300",
          "Content-Type": icon.contentType,
          "X-Content-Type-Options": "nosniff",
          ...(contentSecurityPolicy
            ? { "Content-Security-Policy": contentSecurityPolicy }
            : {}),
        },
      });
    } catch {
      return new Response("Not found", { status: 404 });
    }
  });
}

function getPluginIconRequestForProtocolUrl(rawUrl) {
  if (typeof rawUrl !== "string" || rawUrl.length > 1024) return null;

  let parsedUrl;
  try {
    parsedUrl = new URL(rawUrl);
  } catch {
    return null;
  }
  if (
    parsedUrl.protocol !== `${PLUGIN_ICON_PROTOCOL}:` ||
    parsedUrl.hostname !== PLUGIN_ICON_HOST ||
    parsedUrl.username ||
    parsedUrl.password ||
    parsedUrl.port ||
    parsedUrl.search ||
    parsedUrl.hash
  ) {
    return null;
  }
  const segments = parsedUrl.pathname.split("/").filter(Boolean);
  if (segments.length !== 5 || segments[0] !== "v1") return null;

  const [, provider, companyId, runtimeId, encodedNativeId] = segments;
  let nativeId;
  try {
    nativeId = decodeURIComponent(encodedNativeId);
  } catch {
    return null;
  }
  if (
    !["codex", "claude"].includes(provider) ||
    !isCanonicalUuid(companyId) ||
    !isCanonicalUuid(runtimeId) ||
    typeof nativeId !== "string" ||
    Buffer.byteLength(nativeId, "utf8") > 240 ||
    !/^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(nativeId)
  ) {
    return null;
  }
  return Object.freeze({
    schemaVersion: 1,
    companyId,
    runtimeId,
    provider,
    nativeId,
  });
}

function isCanonicalUuid(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value,
    )
  );
}

function registerCodexGeneratedImageProtocol({
  protocol: protocolModule = protocol,
  codexHome = resolveCodexHome(),
} = {}) {
  protocolModule.handle(CODEX_GENERATED_IMAGE_PROTOCOL, async (request) => {
    const image = getCodexGeneratedImageForProtocolUrl(request.url, codexHome);
    if (!image) {
      return new Response("Not found", { status: 404 });
    }

    let handle;
    try {
      handle = await open(
        image.path,
        constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0),
      );
      const stats = await handle.stat();
      if (
        !stats.isFile() ||
        stats.size < 1 ||
        stats.size > MAX_GENERATED_IMAGE_BYTES
      ) {
        return new Response("Not found", { status: 404 });
      }
      const body = await handle.readFile();
      return new Response(body, {
        headers: {
          "Cache-Control": "private, max-age=31536000, immutable",
          "Content-Type": image.mimeType,
          "X-Content-Type-Options": "nosniff",
        },
      });
    } catch {
      return new Response("Not found", { status: 404 });
    } finally {
      await handle?.close().catch(() => undefined);
    }
  });
}

function getCodexGeneratedImageForProtocolUrl(rawUrl, codexHome) {
  if (typeof rawUrl !== "string" || !path.isAbsolute(codexHome)) {
    return null;
  }
  let parsedUrl;
  try {
    parsedUrl = new URL(rawUrl);
  } catch {
    return null;
  }
  if (
    parsedUrl.protocol !== `${CODEX_GENERATED_IMAGE_PROTOCOL}:` ||
    parsedUrl.hostname !== CODEX_GENERATED_IMAGE_HOST ||
    parsedUrl.username ||
    parsedUrl.password ||
    parsedUrl.search ||
    parsedUrl.hash
  ) {
    return null;
  }
  const decodedPath = decodeProtocolPath(parsedUrl.pathname);
  if (
    decodedPath === null ||
    decodedPath.includes("\\") ||
    decodedPath.includes("\0")
  ) {
    return null;
  }
  const segments = decodedPath.split("/").filter(Boolean);
  if (
    segments.length !== 2 ||
    !CODEX_THREAD_ID_PATTERN.test(segments[0]) ||
    !CODEX_IMAGE_FILE_PATTERN.test(segments[1])
  ) {
    return null;
  }
  const extension = path.extname(segments[1]).toLowerCase();
  const mimeType =
    extension === ".png"
      ? "image/png"
      : extension === ".webp"
        ? "image/webp"
        : "image/jpeg";
  return {
    mimeType,
    path: path.join(
      path.resolve(codexHome),
      "generated_images",
      segments[0],
      segments[1],
    ),
  };
}

function resolveCodexHome(environment = process.env) {
  const configured = environment.CODEX_HOME?.trim();
  return configured && path.isAbsolute(configured)
    ? path.resolve(configured)
    : path.join(homedir(), ".codex");
}

function registerPackagedAppProtocol({
  app,
  net: netModule = net,
  protocol: protocolModule = protocol,
} = {}) {
  if (!app?.isPackaged) {
    return;
  }

  protocolModule.handle(BOARDLY_APP_PROTOCOL, (request) => {
    const filePath = getDistFilePathForProtocolUrl(
      request.url,
      app.getAppPath(),
    );

    if (!filePath) {
      return new Response("Not found", { status: 404 });
    }

    return netModule.fetch(pathToFileURL(filePath).toString());
  });
}

function getDistFilePathForProtocolUrl(rawUrl, appPath) {
  if (!appPath || typeof rawUrl !== "string") {
    return null;
  }

  if (hasUnsafeRawPathSegments(rawUrl)) {
    return null;
  }

  let parsedUrl;

  try {
    parsedUrl = new URL(rawUrl);
  } catch {
    return null;
  }

  if (
    parsedUrl.protocol !== `${BOARDLY_APP_PROTOCOL}:` ||
    parsedUrl.hostname !== BOARDLY_APP_HOST
  ) {
    return null;
  }

  const routePath = decodeProtocolPath(parsedUrl.pathname);

  if (routePath === null) {
    return null;
  }

  const pathSegments = routePath.split("/").filter(Boolean);

  if (
    pathSegments.some((segment) => segment === "." || segment === "..") ||
    pathSegments.some((segment) => segment.includes("\0"))
  ) {
    return null;
  }

  const relativePath =
    pathSegments.length === 0 ? "index.html" : pathSegments.join("/");
  const normalizedRelativePath = path.posix.normalize(relativePath);

  if (
    normalizedRelativePath === "." ||
    normalizedRelativePath === ".." ||
    normalizedRelativePath.startsWith("../") ||
    path.isAbsolute(normalizedRelativePath)
  ) {
    return null;
  }

  return path.join(appPath, "dist", normalizedRelativePath);
}

function decodeProtocolPath(pathname) {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return null;
  }
}

function hasUnsafeRawPathSegments(rawUrl) {
  const protocolPrefix = `${BOARDLY_APP_PROTOCOL}://${BOARDLY_APP_HOST}`;

  if (!rawUrl.startsWith(protocolPrefix)) {
    return false;
  }

  const rawPath = rawUrl.slice(protocolPrefix.length).split(/[?#]/, 1)[0];
  const decodedPath = decodeProtocolPath(rawPath);

  if (decodedPath === null || decodedPath.includes("\\")) {
    return true;
  }

  return decodedPath
    .split("/")
    .filter(Boolean)
    .some((segment) => segment === "." || segment === "..");
}

module.exports = {
  BOARDLY_APP_HOST,
  BOARDLY_APP_INDEX_URL,
  BOARDLY_APP_ORIGIN,
  BOARDLY_APP_PROTOCOL,
  CODEX_GENERATED_IMAGE_PROTOCOL,
  PLUGIN_ICON_PROTOCOL,
  getDistFilePathForProtocolUrl,
  getCodexGeneratedImageForProtocolUrl,
  getPluginIconRequestForProtocolUrl,
  registerCodexGeneratedImageProtocol,
  registerPackagedAppProtocol,
  registerPackagedAppProtocolScheme,
  registerPluginIconProtocol,
};
