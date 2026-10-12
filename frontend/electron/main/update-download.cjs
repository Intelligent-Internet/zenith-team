const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const path = require("node:path");
const { pipeline } = require("node:stream/promises");
const { Transform } = require("node:stream");

const MAX_RELEASES_BYTES = 1024 * 1024;
const MAX_UPDATE_BYTES = 4 * 1024 * 1024 * 1024;
const UPDATE_REQUEST_TIMEOUT_MS = 30_000;

async function stageStaticUpdate({ app, onProgress, platform, release }) {
  if (platform !== "darwin" && platform !== "win32") {
    throw new Error("Progress-aware desktop updates are not supported here.");
  }

  const temporaryDirectory = await fs.promises.mkdtemp(
    path.join(app.getPath("temp"), "zenith-update-"),
  );
  let server = null;

  try {
    const stagedUpdate =
      platform === "darwin"
        ? await stageMacUpdate({
            onProgress,
            release,
            temporaryDirectory,
          })
        : await stageWindowsUpdate({
            onProgress,
            release,
            temporaryDirectory,
          });

    server = await createLoopbackUpdateServer(stagedUpdate.routes);
    const origin = `http://127.0.0.1:${server.address().port}`;

    if (platform === "darwin") {
      stagedUpdate.setFeedOrigin(origin);
    }

    return {
      dispose: () => disposeStagedUpdate(server, temporaryDirectory),
      feedUrl: platform === "darwin" ? `${origin}/latest.json` : `${origin}/`,
    };
  } catch (error) {
    server?.closeAllConnections?.();
    server?.close?.();
    await fs.promises.rm(temporaryDirectory, {
      force: true,
      recursive: true,
    });
    throw error;
  }
}

async function stageMacUpdate({ onProgress, release, temporaryDirectory }) {
  const updateUrl = requireHttpsUrl(release.updateUrl);
  const updatePath = path.join(temporaryDirectory, "update.zip");

  await downloadHttpsFile(updateUrl, updatePath, onProgress);

  const feed = {
    name: release.releaseName ?? `Zenith Team ${release.version}`,
    pub_date: release.releaseDate ?? new Date().toISOString(),
    url: null,
    version: release.version,
  };
  const routes = new Map([
    [
      "/update.zip",
      {
        contentType: "application/zip",
        filePath: updatePath,
      },
    ],
  ]);

  return {
    routes,
    setFeedOrigin(origin) {
      feed.url = `${origin}/update.zip`;
      routes.set("/latest.json", {
        body: `${JSON.stringify(feed)}\n`,
        contentType: "application/json; charset=utf-8",
      });
    },
  };
}

async function stageWindowsUpdate({ onProgress, release, temporaryDirectory }) {
  const updateBaseUrl = requireHttpsUrl(release.updateUrl, {
    trailingSlash: true,
  });
  const releasesUrl = new URL("RELEASES", updateBaseUrl).href;
  const releasesBody = await loadHttpsText(releasesUrl, MAX_RELEASES_BYTES);
  const packageEntry = selectWindowsUpdatePackage(
    releasesBody,
    release.version,
  );
  const packageUrl = new URL(
    encodeURIComponent(packageEntry.filename),
    updateBaseUrl,
  ).href;
  const packagePath = path.join(temporaryDirectory, packageEntry.filename);

  await downloadHttpsFile(packageUrl, packagePath, onProgress);

  return {
    routes: new Map([
      [
        "/RELEASES",
        {
          body: `${packageEntry.line}\n`,
          contentType: "text/plain; charset=utf-8",
        },
      ],
      [
        `/${packageEntry.filename}`,
        {
          contentType: "application/octet-stream",
          filePath: packagePath,
        },
      ],
    ]),
    setFeedOrigin() {},
  };
}

function selectWindowsUpdatePackage(releasesBody, version) {
  const entries = releasesBody
    .split(/\r?\n/u)
    .map((line) => {
      const match = line.trim().match(/^([a-f\d]{40,64})\s+(\S+)\s+(\d+)$/iu);

      if (!match) {
        return null;
      }

      const filename = match[2];
      if (
        !filename.toLowerCase().endsWith("-full.nupkg") ||
        filename.includes("/") ||
        filename.includes("\\")
      ) {
        return null;
      }

      return { filename, line: line.trim() };
    })
    .filter(Boolean);

  if (entries.length === 0) {
    throw new Error("The Windows update feed has no full update package.");
  }

  const versionMarker = `-${version}-full.nupkg`.toLowerCase();
  const matchingEntry = entries.find((entry) =>
    entry.filename.toLowerCase().endsWith(versionMarker),
  );

  if (!matchingEntry) {
    throw new Error("The Windows update feed has no package for this version.");
  }

  return matchingEntry;
}

async function downloadHttpsFile(url, destinationPath, onProgress) {
  const response = await openHttpsResponse(url);
  const contentLength = parseContentLength(response.headers["content-length"]);
  let transferredBytes = 0;
  let lastPercent = null;

  if (contentLength > MAX_UPDATE_BYTES) {
    response.destroy();
    throw new Error("The desktop update package is too large.");
  }

  const progress = new Transform({
    transform(chunk, _encoding, callback) {
      transferredBytes += chunk.length;
      if (transferredBytes > MAX_UPDATE_BYTES) {
        callback(new Error("The desktop update package is too large."));
        return;
      }

      if (contentLength > 0) {
        const percent = Math.min(
          99,
          Math.floor((transferredBytes / contentLength) * 100),
        );
        if (percent !== lastPercent) {
          lastPercent = percent;
          onProgress(percent);
        }
      }

      callback(null, chunk);
    },
  });

  try {
    await pipeline(
      response,
      progress,
      fs.createWriteStream(destinationPath, { flags: "wx" }),
    );
  } catch (error) {
    await fs.promises.rm(destinationPath, { force: true });
    throw error;
  }

  onProgress(99);
}

async function loadHttpsText(url, maxBytes) {
  const response = await openHttpsResponse(url);
  const chunks = [];
  let receivedBytes = 0;

  for await (const chunk of response) {
    receivedBytes += chunk.length;
    if (receivedBytes > maxBytes) {
      response.destroy();
      throw new Error("The desktop update feed is too large.");
    }
    chunks.push(chunk);
  }

  return Buffer.concat(chunks).toString("utf8");
}

function openHttpsResponse(url, redirectsRemaining = 5) {
  const safeUrl = requireHttpsUrl(url);

  return new Promise((resolve, reject) => {
    const request = https.get(
      safeUrl,
      {
        headers: {
          "User-Agent": "Zenith Team Desktop Updater",
        },
      },
      (response) => {
        if (
          response.statusCode &&
          response.statusCode >= 300 &&
          response.statusCode < 400 &&
          response.headers.location
        ) {
          response.resume();
          if (redirectsRemaining === 0) {
            reject(new Error("The desktop update redirected too many times."));
            return;
          }

          const redirectUrl = new URL(response.headers.location, safeUrl).href;
          openHttpsResponse(redirectUrl, redirectsRemaining - 1).then(
            resolve,
            reject,
          );
          return;
        }

        if (response.statusCode !== 200) {
          response.resume();
          reject(
            new Error(
              `Desktop update download returned HTTP ${response.statusCode ?? "error"}.`,
            ),
          );
          return;
        }

        resolve(response);
      },
    );

    request.setTimeout(UPDATE_REQUEST_TIMEOUT_MS, () => {
      request.destroy(new Error("Desktop update download timed out."));
    });
    request.on("error", reject);
  });
}

function createLoopbackUpdateServer(routes) {
  return new Promise((resolve, reject) => {
    const server = http.createServer((request, response) => {
      serveUpdateRoute(routes, request, response);
    });

    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      server.unref();
      resolve(server);
    });
  });
}

function serveUpdateRoute(routes, request, response) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    response.writeHead(405, { Allow: "GET, HEAD" });
    response.end();
    return;
  }

  let pathname;
  try {
    pathname = decodeURIComponent(
      new URL(request.url ?? "/", "http://127.0.0.1").pathname,
    );
  } catch {
    response.writeHead(400);
    response.end();
    return;
  }

  const route = routes.get(pathname);
  if (!route) {
    response.writeHead(404);
    response.end();
    return;
  }

  if (typeof route.body === "string") {
    const body = Buffer.from(route.body);
    response.writeHead(200, {
      "Cache-Control": "no-store",
      "Content-Length": body.length,
      "Content-Type": route.contentType,
    });
    response.end(request.method === "HEAD" ? undefined : body);
    return;
  }

  serveUpdateFile(route, request, response);
}

function serveUpdateFile(route, request, response) {
  let stats;
  try {
    stats = fs.statSync(route.filePath);
  } catch {
    response.writeHead(404);
    response.end();
    return;
  }

  const byteRange = parseByteRange(request.headers.range, stats.size);
  const start = byteRange?.start ?? 0;
  const end = byteRange?.end ?? stats.size - 1;
  const contentLength = end - start + 1;
  response.writeHead(byteRange ? 206 : 200, {
    "Accept-Ranges": "bytes",
    "Cache-Control": "no-store",
    "Content-Length": contentLength,
    "Content-Type": route.contentType,
    ...(byteRange
      ? { "Content-Range": `bytes ${start}-${end}/${stats.size}` }
      : {}),
  });

  if (request.method === "HEAD") {
    response.end();
    return;
  }

  const file = fs.createReadStream(route.filePath, { end, start });
  file.on("error", () => response.destroy());
  file.pipe(response);
}

function parseByteRange(value, fileSize) {
  if (typeof value !== "string") {
    return null;
  }

  const match = value.match(/^bytes=(\d+)-(\d*)$/u);
  if (!match) {
    return null;
  }

  const start = Number.parseInt(match[1], 10);
  const requestedEnd = match[2] ? Number.parseInt(match[2], 10) : fileSize - 1;
  const end = Math.min(requestedEnd, fileSize - 1);

  return start <= end && start >= 0 ? { end, start } : null;
}

function parseContentLength(value) {
  if (typeof value !== "string") {
    return 0;
  }

  const parsed = Number.parseInt(value, 10);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0;
}

function requireHttpsUrl(value, { trailingSlash = false } = {}) {
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error("The desktop update URL is invalid.");
  }

  if (url.protocol !== "https:" || url.username || url.password) {
    throw new Error("The desktop update URL must be credential-free HTTPS.");
  }

  if (trailingSlash && !url.pathname.endsWith("/")) {
    url.pathname += "/";
  }

  return url.href;
}

async function disposeStagedUpdate(server, temporaryDirectory) {
  server.closeAllConnections?.();
  await new Promise((resolve) => server.close(resolve));
  await fs.promises.rm(temporaryDirectory, {
    force: true,
    recursive: true,
  });
}

module.exports = {
  selectWindowsUpdatePackage,
  stageStaticUpdate,
};
