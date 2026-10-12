const http = require("node:http");
const { app } = require("electron");
const { writeMainLog } = require("./diagnostics.cjs");
const { getDesktopVariant } = require("../shared/desktop-variant.cjs");

const LOOPBACK_HOST = "127.0.0.1";
const DEFAULT_OAUTH_LOOPBACK_PORT = 42813;
const OAUTH_LOOPBACK_PATH = "/";
const OAUTH_RENDERER_CALLBACK_PATH = "/auth/google/callback";
const OAUTH_CALLBACK_ROUTE_CHANNEL = "desktop:google-oauth-callback";
const OAUTH_LOOPBACK_PORT_ENV = "BOARDLY_GOOGLE_OAUTH_LOOPBACK_PORT";
const OAUTH_LOOPBACK_TIMEOUT_MS = 10 * 60 * 1000;

let activeLoopback = null;

async function startGoogleOAuthLoopback({ env = process.env, getMainWindow }) {
  await stopGoogleOAuthLoopback();

  const loopbackPort = getGoogleOAuthLoopbackPort(env);

  if (!loopbackPort) {
    writeMainLog("google-oauth-loopback-invalid-port", {
      envKey: OAUTH_LOOPBACK_PORT_ENV,
    });
    return {
      ok: false,
      reason: `${OAUTH_LOOPBACK_PORT_ENV} must be an integer from 1024 to 65535.`,
    };
  }

  return new Promise((resolve) => {
    let settled = false;
    const server = http.createServer((request, response) => {
      handleLoopbackRequest({ getMainWindow, request, response, server });
    });

    server.once("error", (error) => {
      if (!settled) {
        settled = true;
        writeMainLog("google-oauth-loopback-error", {
          code: error?.code,
          port: loopbackPort,
        });
        resolve({
          ok: false,
          reason: createLoopbackListenErrorMessage(error, loopbackPort),
        });
      }
    });

    server.listen(loopbackPort, LOOPBACK_HOST, () => {
      const address = server.address();

      if (!address || typeof address === "string") {
        void stopGoogleOAuthLoopback();
        if (!settled) {
          settled = true;
          resolve({
            ok: false,
            reason: "Unable to start Google sign-in callback port.",
          });
        }
        return;
      }

      const redirectUri = `http://${LOOPBACK_HOST}:${address.port}${OAUTH_LOOPBACK_PATH}`;
      writeMainLog("google-oauth-loopback-listening", {
        port: address.port,
      });
      activeLoopback = {
        redirectUri,
        server,
        timeoutId: setTimeout(() => {
          void stopGoogleOAuthLoopback();
        }, OAUTH_LOOPBACK_TIMEOUT_MS),
      };

      if (!settled) {
        settled = true;
        resolve({
          ok: true,
          redirectUri,
        });
      }
    });
  });
}

function stopGoogleOAuthLoopback() {
  if (!activeLoopback) {
    return Promise.resolve();
  }

  const { server, timeoutId } = activeLoopback;
  activeLoopback = null;
  clearTimeout(timeoutId);
  return new Promise((resolve) => {
    if (!server.listening) {
      resolve();
      return;
    }

    server.close(() => {
      resolve();
    });
    server.closeAllConnections?.();
  });
}

function getGoogleOAuthLoopbackPort(env = process.env) {
  const rawPort = env[OAUTH_LOOPBACK_PORT_ENV]?.trim();

  if (!rawPort) {
    return getDesktopVariant(env).oauthLoopbackPort;
  }

  const port = Number(rawPort);

  if (!Number.isInteger(port) || port < 1024 || port > 65535) {
    return null;
  }

  return port;
}

function createLoopbackListenErrorMessage(error, port) {
  if (error?.code === "EADDRINUSE") {
    return `Google sign-in callback port ${port} is already in use.`;
  }

  return error?.message || "Unable to start Google sign-in callback.";
}

function handleLoopbackRequest({ getMainWindow, request, response, server }) {
  const requestUrl = createLoopbackRequestUrl(request);

  if (request.method !== "GET") {
    writeMainLog("google-oauth-loopback-rejected", {
      method: request.method,
      reason: "method",
    });
    writeLoopbackResponse(response, {
      body: "Method not allowed.",
      statusCode: 405,
    });
    return;
  }

  if (!requestUrl || requestUrl.pathname !== OAUTH_LOOPBACK_PATH) {
    writeMainLog("google-oauth-loopback-rejected", {
      pathname: requestUrl?.pathname,
      reason: "path",
    });
    writeLoopbackResponse(response, {
      body: "Not found.",
      statusCode: 404,
    });
    return;
  }

  const route = `${OAUTH_RENDERER_CALLBACK_PATH}${requestUrl.search}`;
  const didRoute = routeOAuthCallbackToWindow(getMainWindow(), route);
  writeMainLog("google-oauth-loopback-callback", {
    didRoute,
    hasCode: requestUrl.searchParams.has("code"),
    hasError: requestUrl.searchParams.has("error"),
    hasState: requestUrl.searchParams.has("state"),
    pathname: requestUrl.pathname,
  });

  writeLoopbackResponse(response, {
    body: didRoute
      ? "Google sign-in is complete. You can return to Zenith Team."
      : "Google sign-in returned, but Zenith Team is not ready.",
    statusCode: didRoute ? 200 : 503,
  });

  setTimeout(() => {
    if (activeLoopback?.server === server) {
      void stopGoogleOAuthLoopback();
    }
  }, 250);
}

function createLoopbackRequestUrl(request) {
  try {
    return new URL(request.url || "/", `http://${LOOPBACK_HOST}`);
  } catch {
    return null;
  }
}

function routeOAuthCallbackToWindow(
  mainWindow,
  route,
  channel = OAUTH_CALLBACK_ROUTE_CHANNEL,
) {
  if (!mainWindow || mainWindow.isDestroyed()) {
    writeMainLog("google-oauth-loopback-route-window-missing");
    return false;
  }

  const navigate = () => {
    mainWindow.webContents.send(channel, route);
    writeMainLog("google-oauth-loopback-route-sent");

    if (mainWindow.isMinimized?.()) {
      mainWindow.restore();
    }

    if (!mainWindow.isVisible?.()) {
      mainWindow.show();
    }

    if (process.platform === "darwin") {
      app.focus({ steal: true });
    }

    mainWindow.focus();
  };

  if (mainWindow.webContents.isLoading()) {
    mainWindow.webContents.once("did-finish-load", navigate);
  } else {
    navigate();
  }

  return true;
}

function writeLoopbackResponse(response, { body, statusCode }) {
  response.writeHead(statusCode, {
    "Cache-Control": "no-store",
    "Content-Type": "text/html; charset=utf-8",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
  });
  response.end(createLoopbackHtml(body));
}

function createLoopbackHtml(message) {
  return `<!doctype html><html><head><meta charset="utf-8"><title>Zenith Team authorization</title></head><body>${escapeHtml(message)}</body></html>`;
}

function escapeHtml(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

module.exports = {
  routeOAuthCallbackToWindow,
  writeLoopbackResponse,
  DEFAULT_OAUTH_LOOPBACK_PORT,
  OAUTH_CALLBACK_PATH: OAUTH_LOOPBACK_PATH,
  OAUTH_CALLBACK_ROUTE_CHANNEL,
  OAUTH_LOOPBACK_PORT_ENV,
  getGoogleOAuthLoopbackPort,
  startGoogleOAuthLoopback,
  stopGoogleOAuthLoopback,
};
