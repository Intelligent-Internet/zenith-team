"use strict";

const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const crypto = require("node:crypto");
const { Transform } = require("node:stream");
const { pipeline } = require("node:stream/promises");

const LOCAL_PREVIEW_CONFIG_FILE = "local-preview.json";
const LOCAL_PREVIEW_MAX_LINE_BYTES = 512 << 10;
const LOCAL_PREVIEW_MAX_PENDING_BYTES = 256 << 10;
const LOCAL_PREVIEW_MAX_PENDING_FRAMES = 256;
const LOCAL_PREVIEW_RECONNECT_DELAYS_MS = [250, 500, 1_000, 2_500, 5_000];
const LOCAL_PREVIEW_STABLE_CONNECTION_MS = 10_000;
// A Task diff is bounded to 4 MiB before JSON encoding; escaping and asset
// base64 can expand it, so the trusted main-process transport has its own
// explicit encoded-response ceiling.
const LOCAL_OPERATION_MAX_JSON_BYTES = 25 << 20;
const LOCAL_OPERATION_MAX_ICON_BYTES = 4 << 20;
const LOCAL_OPERATION_MAX_EXPORT_BYTES = 100 << 20;
const LOCAL_OPERATION_MAX_ERROR_BYTES = 64 << 10;
const LOCAL_OPERATION_TIMEOUT_MS = 120_000;
const LOCAL_PLUGIN_ICON_TIMEOUT_MS = 5_000;
const LOCAL_JSON_OPERATION_PATHS = new Set([
  "/v1/tools/operate",
  "/v1/connectors/connect",
  "/v2/app-previews/attach",
  "/v2/app-previews/ensure",
  "/v2/app-previews/inspect",
  "/v2/app-previews/release",
  "/v2/app-previews/renew",
  "/v2/app-previews/restart",
  "/v2/app-previews/stop",
  "/v1/connectors/refresh",
  "/v1/connectors/status",
  "/v1/plugins/icon",
  "/v1/plugins/install",
  "/v1/plugins/read",
  "/v1/plugins/refresh",
  "/v1/plugins/uninstall",
  "/v1/task-review/accept",
  "/v1/task-review/prepare",
  "/v1/workspace-assets/list",
  "/v1/workspace-assets/preview",
  "/v1/workspace-files/preview",
  "/v1/workspace-files/resolve",
]);
const LOCAL_EXPORT_PATHS = new Set([
  "/v1/workspace-assets/export",
  "/v1/workspace-files/export",
]);
const LOCAL_PLUGIN_ICON_CONTENT_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/svg+xml",
  "image/webp",
]);

function createDesktopLocalPreview({
  boardlyHomeDirectory,
  onStateChange = () => {},
} = {}) {
  let enabled = false;
  let generation = 0;
  let reconnectAttempt = 0;
  let reconnectTimer = null;
  let request = null;
  let response = null;
  let lastReason = null;
  let lastState = null;
  let pendingBytes = 0;
  let resetPending = false;
  const pendingFrames = new Map();

  function update(state, reason = null) {
    if (state === lastState && reason === lastReason) return;
    lastState = state;
    lastReason = reason;
    try {
      onStateChange({ reason, state });
    } catch {
      // Observers do not own the preview connection.
    }
  }

  function start() {
    if (enabled) return;
    enabled = true;
    generation += 1;
    reconnectAttempt = 0;
    update("connecting");
    void connect(generation);
  }

  function stop() {
    if (
      !enabled &&
      !request &&
      !response &&
      !reconnectTimer &&
      pendingFrames.size === 0
    ) {
      return;
    }
    enabled = false;
    generation += 1;
    clearReconnectTimer();
    queueLocalPreviewReset();
    request?.destroy();
    response?.destroy();
    request = null;
    response = null;
    reconnectAttempt = 0;
    update("stopped");
  }

  async function connect(connectionGeneration) {
    let config;
    try {
      config = await readLocalPreviewConfig(boardlyHomeDirectory);
    } catch {
      scheduleReconnect(connectionGeneration, "config_unavailable");
      return;
    }
    if (!isCurrent(connectionGeneration)) return;
    openStream(connectionGeneration, config);
  }

  function openStream(connectionGeneration, config) {
    const endpoint = new URL(config.endpoint);
    let settled = false;
    let connected = false;
    let nextRequest;
    let streamResponse = null;
    let stableTimer = null;
    const connectionTimer = setTimeout(() => {
      nextRequest?.destroy(new Error("Local preview connection timed out"));
    }, 5_000);
    connectionTimer.unref?.();

    nextRequest = http.request(endpoint, {
      headers: {
        Accept: "application/x-ndjson",
        Authorization: `Bearer ${config.capability}`,
      },
      method: "GET",
    });
    request = nextRequest;

    const reconnect = (reason) => {
      if (settled) return;
      settled = true;
      clearTimeout(connectionTimer);
      clearTimeout(stableTimer);
      if (connected && isCurrent(connectionGeneration)) {
        connected = false;
        queueLocalPreviewReset();
      } else if (isCurrent(connectionGeneration)) {
        clearPendingFrames();
      }
      if (request === nextRequest) request = null;
      if (response === streamResponse) {
        response = null;
      }
      streamResponse?.destroy();
      scheduleReconnect(connectionGeneration, reason);
    };

    nextRequest.on("response", (nextResponse) => {
      streamResponse = nextResponse;
      clearTimeout(connectionTimer);
      if (!isCurrent(connectionGeneration)) {
        nextResponse.destroy();
        return;
      }
      if (
        nextResponse.statusCode !== 200 ||
        String(nextResponse.headers["content-type"] ?? "")
          .split(";", 1)[0]
          .trim() !== "application/x-ndjson"
      ) {
        nextResponse.resume();
        reconnect("stream_rejected");
        return;
      }

      response = nextResponse;
      connected = true;
      update("connected");
      stableTimer = setTimeout(() => {
        stableTimer = null;
        if (!settled && isCurrent(connectionGeneration)) {
          reconnectAttempt = 0;
        }
      }, LOCAL_PREVIEW_STABLE_CONNECTION_MS);
      stableTimer.unref?.();
      let pending = "";
      nextResponse.setEncoding("utf8");
      nextResponse.on("data", (chunk) => {
        if (!isCurrent(connectionGeneration)) return;
        pending += chunk;
        for (;;) {
          const lineEnd = pending.indexOf("\n");
          if (lineEnd < 0) break;
          const line = pending.slice(0, lineEnd);
          pending = pending.slice(lineEnd + 1);
          const frame = parseLocalPreviewFrame(line);
          if (frame?.type === "run.preview.reset") {
            queueLocalPreviewReset();
          } else if (frame) {
            enqueueLocalPreviewFrame(frame);
          }
        }
        if (Buffer.byteLength(pending, "utf8") > LOCAL_PREVIEW_MAX_LINE_BYTES) {
          reconnect("frame_too_large");
        }
      });
      nextResponse.once("end", () => reconnect("stream_ended"));
      nextResponse.once("error", () => reconnect("stream_failed"));
      nextResponse.once("aborted", () => reconnect("stream_aborted"));
    });
    nextRequest.once("error", () => reconnect("connection_failed"));
    nextRequest.end();
  }

  function scheduleReconnect(connectionGeneration, reason) {
    if (!isCurrent(connectionGeneration) || reconnectTimer) return;
    update("connecting", reason);
    const delay =
      LOCAL_PREVIEW_RECONNECT_DELAYS_MS[
        Math.min(reconnectAttempt, LOCAL_PREVIEW_RECONNECT_DELAYS_MS.length - 1)
      ];
    reconnectAttempt += 1;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (isCurrent(connectionGeneration)) {
        void connect(connectionGeneration);
      }
    }, delay);
    reconnectTimer.unref?.();
  }

  function clearReconnectTimer() {
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  function enqueueLocalPreviewFrame(frame) {
    const key = localPreviewFrameKey(frame);
    const previous = pendingFrames.get(key);
    const next = mergeLocalPreviewFrames(previous, frame);
    if (previous) pendingBytes -= localPreviewFrameBytes(previous);
    pendingFrames.set(key, next);
    pendingBytes += localPreviewFrameBytes(next);

    while (
      pendingFrames.size > LOCAL_PREVIEW_MAX_PENDING_FRAMES ||
      pendingBytes > LOCAL_PREVIEW_MAX_PENDING_BYTES
    ) {
      const oldestKey = pendingFrames.keys().next().value;
      if (oldestKey === undefined) break;
      const oldest = pendingFrames.get(oldestKey);
      pendingFrames.delete(oldestKey);
      if (oldest) pendingBytes -= localPreviewFrameBytes(oldest);
    }
  }

  function drain() {
    const frames = resetPending ? [{ type: "run.preview.reset" }] : [];
    frames.push(...pendingFrames.values());
    resetPending = false;
    clearPendingFrames();
    return frames;
  }

  function queueLocalPreviewReset() {
    clearPendingFrames();
    resetPending = true;
  }

  function clearPendingFrames() {
    pendingFrames.clear();
    pendingBytes = 0;
  }

  function isCurrent(connectionGeneration) {
    return enabled && generation === connectionGeneration;
  }

  async function requestJSON(pathname, payload) {
    if (!LOCAL_JSON_OPERATION_PATHS.has(pathname) || !isPlainObject(payload)) {
      return null;
    }
    const response = await requestLocalOperation({
      accept: "application/json",
      boardlyHomeDirectory,
      maximumResponseBytes: LOCAL_OPERATION_MAX_JSON_BYTES,
      pathname,
      payload,
      timeoutMilliseconds: LOCAL_OPERATION_TIMEOUT_MS,
    });
    if (!response) return null;
    try {
      const value = JSON.parse(response.body.toString("utf8"));
      return isPlainObject(value)
        ? {
            ok: response.status >= 200 && response.status < 300,
            payload: value,
            status: response.status,
          }
        : null;
    } catch {
      // Preserve the transport status without forwarding an untrusted body.
      // An older daemon returns plain-text 404 for a newly added operation.
      return { ok: false, payload: null, status: response.status };
    }
  }

  async function exportFile(pathname, payload, destinationPath) {
    if (
      !LOCAL_EXPORT_PATHS.has(pathname) ||
      !isPlainObject(payload) ||
      !validExportDestination(destinationPath)
    ) {
      return null;
    }
    let config;
    try {
      config = await readLocalPreviewConfig(boardlyHomeDirectory);
    } catch {
      return null;
    }
    return streamLocalExport({
      config,
      destinationPath,
      pathname,
      payload,
    });
  }

  async function requestPluginIcon(payload) {
    if (!isPlainObject(payload)) return null;
    const response = await requestLocalOperation({
      accept: "image/png, image/jpeg, image/webp, image/svg+xml",
      boardlyHomeDirectory,
      maximumResponseBytes: LOCAL_OPERATION_MAX_ICON_BYTES,
      pathname: "/v1/plugins/icon",
      payload,
      timeoutMilliseconds: LOCAL_PLUGIN_ICON_TIMEOUT_MS,
    });
    if (!response || response.status < 200 || response.status >= 300) {
      return null;
    }
    const contentType = String(response.headers["content-type"] ?? "")
      .split(";", 1)[0]
      .trim()
      .toLowerCase();
    if (
      !LOCAL_PLUGIN_ICON_CONTENT_TYPES.has(contentType) ||
      response.body.byteLength < 1
    ) {
      return null;
    }
    const contentSecurityPolicy = response.headers["content-security-policy"];
    return {
      bytes: new Uint8Array(response.body),
      contentType,
      ...(typeof contentSecurityPolicy === "string" &&
      contentSecurityPolicy.length <= 1024 &&
      !/[\u0000-\u001f\u007f]/u.test(contentSecurityPolicy)
        ? { contentSecurityPolicy }
        : {}),
    };
  }

  return {
    dispose: stop,
    drain,
    exportFile,
    requestJSON,
    requestPluginIcon,
    start,
    stop,
  };
}

async function requestLocalOperation({
  accept,
  boardlyHomeDirectory,
  maximumResponseBytes,
  pathname,
  payload,
  timeoutMilliseconds,
}) {
  let config;
  try {
    config = await readLocalPreviewConfig(boardlyHomeDirectory);
  } catch {
    return null;
  }
  if (!LOCAL_JSON_OPERATION_PATHS.has(pathname)) return null;

  const endpoint = new URL(config.endpoint);
  endpoint.pathname = pathname;
  endpoint.search = "";
  endpoint.hash = "";
  const body = Buffer.from(JSON.stringify(payload), "utf8");
  if (body.byteLength > 32 << 10) return null;

  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const request = http.request(endpoint, {
      headers: {
        Accept: accept,
        Authorization: `Bearer ${config.capability}`,
        "Content-Length": body.byteLength,
        "Content-Type": "application/json",
      },
      method: "POST",
    });
    const timeout = setTimeout(() => {
      request.destroy(new Error("Local operation timed out"));
    }, timeoutMilliseconds);
    timeout.unref?.();
    request.once("error", () => {
      clearTimeout(timeout);
      finish(null);
    });
    request.once("response", (response) => {
      const chunks = [];
      let receivedBytes = 0;
      response.on("data", (chunk) => {
        receivedBytes += chunk.byteLength;
        if (receivedBytes > maximumResponseBytes) {
          response.destroy(new Error("Local operation response is too large"));
          return;
        }
        chunks.push(chunk);
      });
      response.once("aborted", () => {
        clearTimeout(timeout);
        finish(null);
      });
      response.once("error", () => {
        clearTimeout(timeout);
        finish(null);
      });
      response.once("end", () => {
        clearTimeout(timeout);
        finish({
          body: Buffer.concat(chunks),
          headers: response.headers,
          status: response.statusCode ?? 0,
        });
      });
    });
    request.end(body);
  });
}

async function streamLocalExport({
  config,
  destinationPath,
  pathname,
  payload,
}) {
  const endpoint = new URL(config.endpoint);
  endpoint.pathname = pathname;
  endpoint.search = "";
  endpoint.hash = "";
  const body = Buffer.from(JSON.stringify(payload), "utf8");
  if (body.byteLength > 32 << 10) return null;

  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const request = http.request(endpoint, {
      headers: {
        Accept: "application/octet-stream",
        Authorization: `Bearer ${config.capability}`,
        "Content-Length": body.byteLength,
        "Content-Type": "application/json",
      },
      method: "POST",
    });
    const timeout = setTimeout(() => {
      request.destroy(new Error("Local export timed out"));
    }, LOCAL_OPERATION_TIMEOUT_MS);
    timeout.unref?.();
    request.once("error", () => {
      clearTimeout(timeout);
      finish(null);
    });
    request.once("response", (response) => {
      void consumeLocalExportResponse(response, destinationPath)
        .then((value) => {
          clearTimeout(timeout);
          finish(value);
        })
        .catch(() => {
          clearTimeout(timeout);
          finish(null);
        });
    });
    request.end(body);
  });
}

async function consumeLocalExportResponse(response, destinationPath) {
  const status = response.statusCode ?? 0;
  if (status < 200 || status >= 300) {
    const body = await readBoundedResponse(
      response,
      LOCAL_OPERATION_MAX_ERROR_BYTES,
    );
    if (!body) return null;
    try {
      const payload = JSON.parse(body.toString("utf8"));
      return isPlainObject(payload) ? { ok: false, payload, status } : null;
    } catch {
      return null;
    }
  }

  const contentType = String(response.headers["content-type"] ?? "")
    .split(";", 1)[0]
    .trim()
    .toLowerCase();
  const disposition = String(response.headers["content-disposition"] ?? "");
  const contentLength = parseExportContentLength(
    response.headers["content-length"],
  );
  if (
    contentType !== "application/octet-stream" ||
    !validExportDisposition(disposition) ||
    contentLength === null
  ) {
    response.destroy();
    return null;
  }

  const temporaryPath = `${destinationPath}.boardly-${crypto.randomUUID()}.tmp`;
  try {
    const noFollow = fs.constants.O_NOFOLLOW ?? 0;
    const handle = await fs.promises.open(
      temporaryPath,
      fs.constants.O_CREAT |
        fs.constants.O_EXCL |
        fs.constants.O_WRONLY |
        noFollow,
      0o600,
    );
    await handle.chmod(0o600);
    const writer = handle.createWriteStream();
    let receivedBytes = 0;
    const counter = new Transform({
      transform(chunk, _encoding, callback) {
        receivedBytes += chunk.byteLength;
        callback(
          receivedBytes <= contentLength &&
            receivedBytes <= LOCAL_OPERATION_MAX_EXPORT_BYTES
            ? null
            : new Error("Local export response is too large"),
          chunk,
        );
      },
    });
    await pipeline(response, counter, writer);
    if (receivedBytes !== contentLength) {
      throw new Error("Local export response was incomplete");
    }
    const completed = await fs.promises.open(
      temporaryPath,
      fs.constants.O_RDWR | noFollow,
    );
    try {
      await completed.chmod(0o600);
      await completed.sync();
    } finally {
      await completed.close();
    }
    await replaceExportDestination(temporaryPath, destinationPath);
    return { ok: true, status };
  } catch {
    response.destroy();
    return null;
  } finally {
    await fs.promises.rm(temporaryPath, { force: true }).catch(() => {});
  }
}

async function replaceExportDestination(temporaryPath, destinationPath) {
  try {
    await fs.promises.rename(temporaryPath, destinationPath);
  } catch (error) {
    if (
      process.platform !== "win32" ||
      !["EACCES", "EEXIST", "EPERM"].includes(error?.code)
    ) {
      throw error;
    }
    await fs.promises.rm(destinationPath, { force: true });
    await fs.promises.rename(temporaryPath, destinationPath);
  }
}

function parseExportContentLength(value) {
  if (
    Array.isArray(value) ||
    typeof value !== "string" ||
    !/^(?:0|[1-9][0-9]*)$/u.test(value)
  ) {
    return null;
  }
  const size = Number(value);
  return Number.isSafeInteger(size) && size <= LOCAL_OPERATION_MAX_EXPORT_BYTES
    ? size
    : null;
}

function validExportDisposition(value) {
  return (
    value.length > 0 &&
    value.length <= 1024 &&
    !/[\r\n]/u.test(value) &&
    /^attachment(?:;|$)/iu.test(value)
  );
}

function validExportDestination(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    Buffer.byteLength(value, "utf8") <= 4096 &&
    path.isAbsolute(value) &&
    path.normalize(value) === value
  );
}

async function readBoundedResponse(response, maximumBytes) {
  const chunks = [];
  let receivedBytes = 0;
  try {
    for await (const chunk of response) {
      receivedBytes += chunk.byteLength;
      if (receivedBytes > maximumBytes) {
        response.destroy();
        return null;
      }
      chunks.push(chunk);
    }
  } catch {
    return null;
  }
  return Buffer.concat(chunks);
}

async function readLocalPreviewConfig(boardlyHomeDirectory) {
  const configPath = path.join(boardlyHomeDirectory, LOCAL_PREVIEW_CONFIG_FILE);
  const [raw, info] = await Promise.all([
    fs.promises.readFile(configPath, "utf8"),
    fs.promises.stat(configPath),
  ]);
  if (process.platform !== "win32" && (info.mode & 0o077) !== 0) {
    throw new Error("Local preview capability file is not private");
  }
  const parsed = JSON.parse(raw);
  if (!isLocalPreviewConfig(parsed)) {
    throw new Error("Local preview configuration is invalid");
  }
  return parsed;
}

function isLocalPreviewConfig(value) {
  if (
    !isPlainObject(value) ||
    value.version !== 1 ||
    typeof value.endpoint !== "string" ||
    typeof value.capability !== "string" ||
    !/^[A-Za-z0-9_-]{43}$/u.test(value.capability)
  ) {
    return false;
  }
  try {
    const endpoint = new URL(value.endpoint);
    return (
      endpoint.protocol === "http:" &&
      endpoint.hostname === "127.0.0.1" &&
      endpoint.pathname === "/local-preview" &&
      endpoint.port.length > 0 &&
      !endpoint.username &&
      !endpoint.password &&
      !endpoint.search &&
      !endpoint.hash
    );
  } catch {
    return false;
  }
}

function parseLocalPreviewFrame(source) {
  if (
    !source ||
    Buffer.byteLength(source, "utf8") > LOCAL_PREVIEW_MAX_LINE_BYTES
  ) {
    return null;
  }
  let value;
  try {
    value = JSON.parse(source);
  } catch {
    return null;
  }
  return isLocalPreviewFrame(value) ? value : null;
}

function isLocalPreviewFrame(value) {
  if (isPlainObject(value) && value.type === "run.preview.reset") {
    return Object.keys(value).length === 1;
  }
  return (
    isPlainObject(value) &&
    value.type === "run.preview.delta" &&
    isBoundedText(value.companyId, 512) &&
    isBoundedText(value.runId, 512) &&
    isBoundedText(value.conversationId, 512) &&
    isBoundedText(value.itemKey, 512) &&
    (value.channel === "text" || value.channel === "reasoning") &&
    (value.operation === "append" || value.operation === "replace") &&
    isBoundedText(value.content, 64 << 10)
  );
}

function localPreviewFrameKey(frame) {
  return [
    frame.companyId,
    frame.runId,
    frame.conversationId,
    frame.itemKey,
    frame.channel,
  ].join("\u0000");
}

function mergeLocalPreviewFrames(previous, next) {
  if (!previous || next.operation === "replace") return next;
  const content = `${previous.content}${next.content}`.slice(0, 64 << 10);
  return {
    ...next,
    content,
    operation: previous.operation === "replace" ? "replace" : "append",
  };
}

function localPreviewFrameBytes(frame) {
  return Buffer.byteLength(frame.content, "utf8");
}

function isBoundedText(value, maxBytes) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    Buffer.byteLength(value, "utf8") <= maxBytes
  );
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

module.exports = { createDesktopLocalPreview };
