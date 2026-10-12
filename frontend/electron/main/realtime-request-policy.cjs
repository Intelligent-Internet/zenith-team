"use strict";

const { getDesktopVariant } = require("../shared/desktop-variant.cjs");

function resolveDesktopRealtimeUrl(apiBaseUrl) {
  const url = new URL(apiBaseUrl);
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username ||
    url.password
  ) {
    throw new Error("Desktop realtime API URL is invalid");
  }
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  const apiPath = url.pathname.replace(/\/+$/, "");
  url.pathname = apiPath.endsWith("/api/v1")
    ? `${apiPath}/ws`
    : apiPath.endsWith("/api")
      ? `${apiPath}/v1/ws`
      : `${apiPath}/api/v1/ws`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

function resolveDesktopRendererApiUrl(env, isPackaged) {
  const variant = getDesktopVariant(env, {
    fallback: isPackaged ? "prod" : "local",
  });
  const configured =
    env.VITE_API_URL || env.BOARDLY_DESKTOP_API_BASE_URL || variant.apiOrigin;
  const url = new URL(configured);
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error("Desktop renderer API URL is invalid");
  }
  return url.toString().replace(/\/+$/, "");
}

function registerDesktopRealtimeRequestPolicy(
  webRequest,
  apiBaseUrl,
  { readStoredSession = async () => ({ ok: true, accessToken: null }) } = {},
) {
  const realtimeRoute = new URL(resolveDesktopRealtimeUrl(apiBaseUrl));
  webRequest.onBeforeSendHeaders((details, callback) => {
    if (!isRealtimeRequest(details.url, realtimeRoute)) {
      callback({ requestHeaders: details.requestHeaders });
      return;
    }
    void Promise.resolve(readStoredSession())
      .then((stored) => {
        const headers = stripRendererCredentials(details.requestHeaders);
        if (stored?.ok && stored.accessToken) {
          headers.Authorization = `Bearer ${stored.accessToken}`;
        }
        callback({ requestHeaders: headers });
      })
      .catch(() => {
        callback({
          requestHeaders: stripRendererCredentials(details.requestHeaders),
        });
      });
  });

  let disposed = false;
  return () => {
    if (disposed) return;
    disposed = true;
    webRequest.onBeforeSendHeaders(null);
  };
}

function isRealtimeRequest(rawUrl, realtimeRoute) {
  try {
    const candidate = new URL(rawUrl);
    return (
      candidate.protocol === realtimeRoute.protocol &&
      isSameRealtimeHost(candidate, realtimeRoute) &&
      candidate.pathname === realtimeRoute.pathname &&
      candidate.hash === "" &&
      candidate.searchParams.size === 1 &&
      Boolean(candidate.searchParams.get("company_id"))
    );
  } catch {
    return false;
  }
}

function isSameRealtimeHost(candidate, realtimeRoute) {
  if (candidate.port !== realtimeRoute.port) {
    return false;
  }
  if (candidate.hostname === realtimeRoute.hostname) {
    return true;
  }
  return (
    isLoopbackHostname(candidate.hostname) &&
    isLoopbackHostname(realtimeRoute.hostname)
  );
}

function isLoopbackHostname(hostname) {
  return (
    hostname === "localhost" || hostname === "127.0.0.1" || hostname === "[::1]"
  );
}

function stripRendererCredentials(headers) {
  const sanitized = {};
  for (const [name, value] of Object.entries(headers || {})) {
    const normalized = name.toLowerCase();
    if (
      normalized !== "authorization" &&
      normalized !== "origin" &&
      normalized !== "cookie"
    ) {
      sanitized[name] = value;
    }
  }
  return sanitized;
}

module.exports = {
  isRealtimeRequest,
  registerDesktopRealtimeRequestPolicy,
  resolveDesktopRendererApiUrl,
  resolveDesktopRealtimeUrl,
};
