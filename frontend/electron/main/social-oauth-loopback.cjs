const http = require("node:http");
const { randomBytes } = require("node:crypto");
const {
  routeOAuthCallbackToWindow,
  writeLoopbackResponse,
} = require("./oauth-loopback.cjs");

let activeLoopback = null;
let generation = 0;

async function stopSocialOAuthLoopback() {
  generation += 1;
  await closeActiveLoopback();
}

async function closeActiveLoopback() {
  const active = activeLoopback;
  activeLoopback = null;
  if (!active) return;
  clearTimeout(active.timeout);
  await new Promise((resolve) => {
    active.server.close(resolve);
    active.server.closeAllConnections?.();
  });
}

async function startSocialOAuthLoopback({ getMainWindow }) {
  const attempt = ++generation;
  await closeActiveLoopback();
  if (attempt !== generation) {
    return { ok: false, reason: "Social connection was cancelled." };
  }
  const state = randomBytes(32).toString("base64url");
  return new Promise((resolve) => {
    let consumed = false;
    const server = http.createServer((request, response) => {
      const active = activeLoopback;
      let url;
      try {
        url = new URL(request.url || "/", "http://127.0.0.1");
      } catch {
        writeLoopbackResponse(response, {
          statusCode: 400,
          body: "Invalid social connection callback.",
        });
        return;
      }
      if (
        consumed ||
        active?.server !== server ||
        request.method !== "GET" ||
        request.headers.host !== new URL(active.redirectUri).host ||
        url.pathname !== "/social/callback" ||
        url.searchParams.get("state") !== state ||
        !["connected", "failed"].includes(url.searchParams.get("oauth")) ||
        !["x", "instagram", "facebook", "linkedin"].includes(
          url.searchParams.get("platform"),
        )
      ) {
        writeLoopbackResponse(response, {
          statusCode: 400,
          body: "Invalid social connection callback.",
        });
        return;
      }
      consumed = true;
      const query = new URLSearchParams({
        oauth: url.searchParams.get("oauth"),
        platform: url.searchParams.get("platform"),
      });
      const routed = routeOAuthCallbackToWindow(
        getMainWindow(),
        `/social?${query}`,
        "desktop:social-oauth-callback",
      );
      writeLoopbackResponse(response, {
        statusCode: routed ? 200 : 503,
        body: routed
          ? "You can return to Zenith Team."
          : "Open Zenith Team and try connecting again.",
      });
      setTimeout(() => {
        if (activeLoopback?.server === server) void stopSocialOAuthLoopback();
      }, 250);
    });
    server.once("error", () =>
      resolve({
        ok: false,
        reason: "Could not open the social connection callback.",
      }),
    );
    server.listen(0, "127.0.0.1", () => {
      if (attempt !== generation) {
        server.close();
        resolve({ ok: false, reason: "Social connection was cancelled." });
        return;
      }
      const redirectUri = `http://127.0.0.1:${server.address().port}/social/callback`;
      activeLoopback = {
        server,
        redirectUri,
        timeout: setTimeout(
          () => {
            if (activeLoopback?.server === server)
              void stopSocialOAuthLoopback();
          },
          10 * 60 * 1000,
        ),
      };
      resolve({ ok: true, redirectUri, state });
    });
  });
}

module.exports = { startSocialOAuthLoopback, stopSocialOAuthLoopback };
