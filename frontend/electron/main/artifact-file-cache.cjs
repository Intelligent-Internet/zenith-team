"use strict";

const { createHash, randomUUID } = require("node:crypto");
const { constants } = require("node:fs");
const fs = require("node:fs/promises");
const path = require("node:path");
const { ipcMain } = require("electron");
const {
  ensureStoredSession,
  readStoredSession,
} = require("./desktop-auth-store.cjs");
const { trustedApiBaseUrl } = require("./desktop-auth-api.cjs");
const { isTrustedMainFrame } = require("./trusted-frame.cjs");
const {
  artifactCacheChannels,
  isArtifactFileRequest,
  isArtifactRequestId,
} = require("../shared/artifact-cache.cjs");

const MAX_FILE_BYTES = 100 * 1024 * 1024;
const ETAG = /^"attachment:[0-9a-f-]{36}:[1-9][0-9]*"$/;

// This is disposable byte storage, never offline authority. A cached entry is
// returned only after an authorized conditional GET against the original route.
function createArtifactFileCache({
  app,
  env,
  ensureSession = ensureStoredSession,
  readSession = readStoredSession,
  fetch = globalThis.fetch,
  maximumBytes = 500 * 1024 * 1024,
}) {
  const root = path.join(app.getPath("userData"), "artifact-cache", "v1");
  const pending = new Map();
  let epoch = 0;
  let blocked = false;
  let owner = null;
  let sessionRead = null;
  let diskQueue = Promise.resolve();
  let workers = 0;
  const queue = [];

  const disk = (action) => {
    const next = diskQueue.then(action);
    diskQueue = next.catch(() => {});
    return next;
  };
  const guard = (generation, signal) => {
    if (blocked || epoch !== generation || signal?.aborted)
      throw failure("canceled");
  };
  const identity = (session) => {
    if (!session?.ok || !session.accessToken) throw failure("session_missing");
    const baseUrl = trustedApiBaseUrl(session.apiBaseUrl, { app, env });
    let claims;
    try {
      claims = JSON.parse(
        Buffer.from(session.accessToken.split(".")[1], "base64url").toString(),
      );
    } catch {
      throw failure("session_missing");
    }
    if (!isArtifactRequestId(claims.sub) || !isArtifactRequestId(claims.sid))
      throw failure("session_missing");
    return {
      baseUrl,
      userId: claims.sub,
      sessionId: claims.sid,
      token: session.accessToken,
      owner: hash([baseUrl, claims.sub]),
    };
  };
  const current = async (actor, generation, signal) => {
    guard(generation, signal);
    const latest = identity(await readSession());
    guard(generation, signal);
    if (
      actor.baseUrl !== latest.baseUrl ||
      actor.userId !== latest.userId ||
      actor.sessionId !== latest.sessionId
    )
      throw failure("session_changed");
  };

  function stopPending() {
    epoch++;
    for (const entry of pending.values()) entry.controller.abort();
    pending.clear();
  }

  async function selectOwner(nextOwner, generation, signal) {
    await disk(async () => {
      guard(generation, signal);
      if (owner === nextOwner) return;
      // Across restarts, remove previous account directories before any reads.
      await fs.mkdir(root, { recursive: true, mode: 0o700 });
      for (const item of await fs.readdir(root, { withFileTypes: true })) {
        if (item.name !== nextOwner)
          await fs.rm(path.join(root, item.name), {
            recursive: true,
            force: true,
          });
      }
      await fs.mkdir(path.join(root, nextOwner), {
        recursive: true,
        mode: 0o700,
      });
      guard(generation, signal);
      owner = nextOwner;
      await prune(path.join(root, owner));
    });
  }

  async function read(request, signal) {
    if (!isArtifactFileRequest(request)) throw failure("invalid_request");
    const generation = epoch;
    guard(generation, signal);
    sessionRead ??= Promise.resolve()
      .then(ensureSession)
      .finally(() => {
        sessionRead = null;
      });
    const actor = identity(await sessionRead);
    guard(generation, signal);
    await selectOwner(actor.owner, generation, signal).catch(() => {});
    guard(generation, signal);
    const route =
      `/companies/${request.companyId}` +
      (request.runId ? `/runs/${request.runId}` : "") +
      `/artifacts/${request.artifactId}/items/${request.ordinal}/download`;
    // A different route must recheck its own Run/Artifact access, even if its
    // file happens to share a disk entry with another authorized route.
    const pendingKey = `${generation}:${actor.owner}:${actor.sessionId}:${route}`;
    let entry = pending.get(pendingKey);
    if (!entry || entry.controller.signal.aborted) {
      if (pending.size >= 32) throw failure("busy");
      const controller = new AbortController();
      entry = { controller, waiters: 0, settled: false };
      pending.set(pendingKey, entry);
      entry.promise = schedule(() =>
        load(actor, request, route, generation, controller.signal),
      ).finally(() => {
        entry.settled = true;
        if (pending.get(pendingKey) === entry) pending.delete(pendingKey);
      });
    }
    const result = await subscribe(entry, signal);
    await current(actor, generation, signal);
    return result;
  }

  function schedule(action) {
    return new Promise((resolve, reject) => {
      queue.push({ action, resolve, reject });
      drain();
    });
  }

  function drain() {
    while (workers < 2 && queue.length) {
      const job = queue.shift();
      workers++;
      Promise.resolve()
        .then(job.action)
        .then(job.resolve, job.reject)
        .finally(() => {
          workers--;
          drain();
        });
    }
  }

  async function load(actor, request, route, generation, signal) {
    guard(generation, signal);
    const directory = path.join(root, actor.owner);
    const key = hash([request.companyId, request.artifactId, request.ordinal]);
    const filename = path.join(directory, key);
    const metadata = await disk(() => readMetadata(filename)).catch(() => null);
    let tag = metadata?.etag;
    for (let attempt = 0; attempt < 2; attempt++) {
      guard(generation, signal);
      const response = await fetch(`${actor.baseUrl}${route}`, {
        method: "GET",
        redirect: "error",
        cache: "no-store",
        headers: {
          Authorization: `Bearer ${actor.token}`,
          "Accept-Encoding": "identity",
          "X-Zenith-Team-Artifact-Cache": "revalidate",
          ...(tag ? { "If-None-Match": tag } : {}),
        },
        signal: AbortSignal.any([signal, AbortSignal.timeout(120_000)]),
      });
      if (response.status === 304) {
        await response.body?.cancel();
        await current(actor, generation, signal);
        if (tag && response.headers.get("etag") === tag) {
          const bytes = await disk(() => readBytes(filename, metadata)).catch(
            () => null,
          );
          if (bytes)
            return { ok: true, bytes, contentType: metadata.contentType };
        }
        // The cache may have been evicted or corrupted while revalidating.
        await disk(() => removeEntry(filename)).catch(() => {});
        tag = null;
        continue;
      }
      if (response.status !== 200) {
        await response.body?.cancel();
        if ([401, 403, 404, 410].includes(response.status))
          await disk(() => removeEntry(filename)).catch(() => {});
        throw failure(
          response.status === 401 ? "session_missing" : "unavailable",
        );
      }
      const size = response.headers.has("content-length")
        ? Number(response.headers.get("content-length"))
        : NaN;
      if (
        !Number.isSafeInteger(size) ||
        size < 0 ||
        size > MAX_FILE_BYTES ||
        !response.body
      ) {
        await response.body?.cancel();
        return { ok: false, code: "uncacheable" };
      }
      const chunks = [];
      let length = 0;
      const reader = response.body.getReader();
      try {
        while (true) {
          const chunk = await reader.read();
          if (chunk.done) break;
          length += chunk.value.byteLength;
          if (length > MAX_FILE_BYTES) throw failure("uncacheable");
          chunks.push(chunk.value);
        }
      } finally {
        await reader.cancel().catch(() => {});
      }
      if (length !== size) throw failure("unavailable");
      const bytes = Buffer.concat(chunks, length);
      const contentType =
        response.headers.get("content-type") || "application/octet-stream";
      await current(actor, generation, signal);
      const etag = response.headers.get("etag");
      if (
        ETAG.test(etag || "") &&
        contentType.length <= 256 &&
        length <= maximumBytes
      ) {
        const next = { etag, contentType, size: length, sha256: hash(bytes) };
        // Disk failures degrade to a freshly authorized, uncached download.
        await disk(async () => {
          guard(generation, signal);
          const temporary = `${filename}.${randomUUID()}.tmp`;
          try {
            await fs.writeFile(temporary, bytes, { mode: 0o600, flag: "wx" });
            guard(generation, signal);
            await fs.rename(temporary, `${filename}.bin`);
            await fs.writeFile(temporary, JSON.stringify(next), {
              mode: 0o600,
              flag: "wx",
            });
            guard(generation, signal);
            await fs.rename(temporary, `${filename}.json`);
            await prune(directory);
          } finally {
            await fs.rm(temporary, { force: true });
          }
        }).catch(() => {});
      }
      guard(generation, signal);
      return { ok: true, bytes, contentType };
    }
    throw failure("unavailable");
  }

  async function prune(directory) {
    const files = await fs.readdir(directory);
    const entries = [];
    for (const name of files) {
      const file = path.join(directory, name);
      if (
        name.endsWith(".tmp") ||
        (name.endsWith(".json") &&
          !files.includes(name.replace(/\.json$/, ".bin")))
      ) {
        await fs.rm(file, { force: true });
      } else if (/^[0-9a-f]{64}\.bin$/.test(name)) {
        const stat = await fs.lstat(file);
        if (
          !stat.isFile() ||
          !files.includes(name.replace(/\.bin$/, ".json"))
        ) {
          await removeEntry(file.slice(0, -4));
        } else
          entries.push({
            file: file.slice(0, -4),
            bytes: stat.size,
            used: stat.mtimeMs,
          });
      }
    }
    entries.sort((a, b) => a.used - b.used);
    let total = entries.reduce((sum, item) => sum + item.bytes, 0);
    while (entries.length > 256 || total > maximumBytes) {
      const oldest = entries.shift();
      await removeEntry(oldest.file);
      total -= oldest.bytes;
    }
  }

  return {
    read,
    resume() {
      blocked = false;
    },
    async clear() {
      blocked = true;
      stopPending();
      await disk(async () => {
        await fs.rm(root, { recursive: true, force: true });
        owner = null;
      });
    },
  };
}

async function readMetadata(filename) {
  const bytes = await readBoundedFile(`${filename}.json`, 4096);
  const value = JSON.parse(bytes.toString());
  if (
    !ETAG.test(value.etag) ||
    typeof value.contentType !== "string" ||
    value.contentType.length > 256 ||
    !Number.isSafeInteger(value.size) ||
    value.size < 0 ||
    value.size > MAX_FILE_BYTES ||
    !/^[0-9a-f]{64}$/.test(value.sha256)
  )
    return null;
  return value;
}

async function readBytes(filename, metadata) {
  const bytes = await readBoundedFile(`${filename}.bin`, metadata.size);
  if (bytes.length !== metadata.size || hash(bytes) !== metadata.sha256)
    return null;
  const now = new Date();
  await fs.utimes(`${filename}.bin`, now, now);
  return bytes;
}

async function readBoundedFile(filename, maximumBytes) {
  const file = await fs.open(
    filename,
    constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0),
  );
  try {
    const stat = await file.stat();
    if (!stat.isFile() || stat.size > maximumBytes)
      throw failure("unavailable");
    const bytes = Buffer.alloc(stat.size);
    let offset = 0;
    while (offset < bytes.length) {
      const { bytesRead } = await file.read(
        bytes,
        offset,
        bytes.length - offset,
        offset,
      );
      if (!bytesRead) throw failure("unavailable");
      offset += bytesRead;
    }
    return bytes;
  } finally {
    await file.close();
  }
}

async function removeEntry(filename) {
  await fs.rm(`${filename}.json`, { force: true });
  await fs.rm(`${filename}.bin`, { force: true });
}

function hash(value) {
  return createHash("sha256")
    .update(Buffer.isBuffer(value) ? value : JSON.stringify(value))
    .digest("hex");
}

function failure(code) {
  return Object.assign(new Error("Artifact file is unavailable."), { code });
}

function subscribe(entry, signal) {
  entry.waiters++;
  let aborted;
  return new Promise((resolve, reject) => {
    aborted = () => reject(failure("canceled"));
    if (signal?.aborted) aborted();
    else signal?.addEventListener("abort", aborted, { once: true });
    entry.promise.then(resolve, reject);
  }).finally(() => {
    signal?.removeEventListener("abort", aborted);
    entry.waiters--;
    if (!entry.waiters && !entry.settled) entry.controller.abort();
  });
}

function registerArtifactFileCacheHandlers({ cache, getMainWindow }) {
  const requests = new Map();
  const watched = new WeakSet();
  ipcMain.handle(artifactCacheChannels.read, async (event, request) => {
    if (
      !isTrustedMainFrame(event, getMainWindow()) ||
      !isArtifactFileRequest(request)
    )
      return { ok: false, code: "invalid_request" };
    const key = `${event.sender.id}:${request.requestId}`;
    if (requests.has(key) || requests.size >= 64)
      return { ok: false, code: "busy" };
    const controller = new AbortController();
    requests.set(key, controller);
    if (!watched.has(event.sender)) {
      watched.add(event.sender);
      const senderId = event.sender.id;
      event.sender.once("destroyed", () => {
        for (const [id, pending] of requests) {
          if (id.startsWith(`${senderId}:`)) pending.abort();
        }
      });
    }
    try {
      return await cache.read(request, controller.signal);
    } catch (error) {
      return {
        ok: false,
        code: [
          "canceled",
          "uncacheable",
          "session_missing",
          "session_changed",
          "busy",
        ].includes(error?.code)
          ? error.code
          : "unavailable",
      };
    } finally {
      requests.delete(key);
    }
  });
  ipcMain.handle(artifactCacheChannels.cancel, (event, requestId) => {
    if (
      isTrustedMainFrame(event, getMainWindow()) &&
      isArtifactRequestId(requestId)
    )
      requests.get(`${event.sender.id}:${requestId}`)?.abort();
  });
}

module.exports = { createArtifactFileCache, registerArtifactFileCacheHandlers };
