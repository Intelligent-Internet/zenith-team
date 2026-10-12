"use strict";

const APP_PREVIEW_EVENT_CHANNEL = "desktop:app-preview-status";
const APP_PREVIEW_REQUEST_TIMEOUT_MS = 5_000;
const APP_PREVIEW_PATHS = Object.freeze({
  attach: "/v2/app-previews/attach",
  ensure: "/v2/app-previews/ensure",
  inspect: "/v2/app-previews/inspect",
  release: "/v2/app-previews/release",
  renew: "/v2/app-previews/renew",
  restart: "/v2/app-previews/restart",
  stop: "/v2/app-previews/stop",
});

function createDesktopAppPreview({ getMainWindow, requestDaemonOperation }) {
  const viewers = new Map();
  const pendingOpens = new Map();
  const queues = new Map();
  const stopObservers = new Map();

  function enqueue(outputId, operation) {
    const previous = queues.get(outputId) ?? Promise.resolve();
    const next = previous.catch(() => {}).then(operation);
    queues.set(outputId, next);
    void next.then(
      () => clearQueue(outputId, next),
      () => clearQueue(outputId, next),
    );
    return next;
  }

  function clearQueue(outputId, operation) {
    if (queues.get(outputId) === operation) queues.delete(outputId);
  }

  async function call(pathname, request, extra = {}) {
    let result;
    try {
      result = await withTimeout(
        requestDaemonOperation(pathname, {
          schemaVersion: 2,
          companyId: request.companyId,
          outputId: request.outputId,
          runId: request.runId,
          runtimeId: request.runtimeId,
          ...extra,
        }),
        APP_PREVIEW_REQUEST_TIMEOUT_MS,
      );
    } catch {
      return failure("daemon_unavailable", "The local daemon is unavailable.");
    }
    const payload = sanitizeDaemonResponse(result?.payload, request);
    if (!payload) {
      return failure("invalid_response", "App preview response is invalid.");
    }
    if (!result?.ok) {
      return failure(
        payload.code || "app_preview_unavailable",
        payload.detail || "The app preview operation could not be completed.",
        payload,
      );
    }
    return { ok: true, value: payload };
  }

  async function open(sender, request) {
    const viewerId = request.viewerId;
    if (pendingOpens.has(viewerId) || viewers.has(viewerId)) {
      return failure(
        "invalid_request",
        "This preview viewer is already attached.",
      );
    }
    let closed = sender.isDestroyed?.() === true;
    let viewer;
    let cleanupPromise;
    let retainSenderLifecycle = false;
    const cleanup = async () => {
      closed = true;
      if (!viewer) return;
      cleanupPromise ??= releaseViewer(viewer);
      await cleanupPromise;
    };
    const senderLifecycle = closed
      ? null
      : observeSenderLifecycle(sender, cleanup);
    pendingOpens.set(viewerId, cleanup);

    try {
      if (closed) return closedViewerFailure();
      return await enqueue(request.outputId, async () => {
        if (closed) return closedViewerFailure();
        const inspected = await call(APP_PREVIEW_PATHS.inspect, request);
        if (closed) return closedViewerFailure();
        if (!inspected.ok) return stripFailure(inspected);

        const attached = await call(APP_PREVIEW_PATHS.attach, request, {
          stopToken: inspected.value.stopToken,
        });
        if (closed && !attached.ok) return closedViewerFailure();
        if (!attached.ok) return stripFailure(attached);

        viewer = {
          detached: false,
          leaseId: attached.value.leaseId,
          outputId: request.outputId,
          request,
          sender,
          senderLifecycle,
          snapshot: attached.value,
          stopToken: inspected.value.stopToken,
          timer: null,
          viewerId,
        };
        viewers.set(viewerId, viewer);
        if (closed) {
          await cleanup();
          return closedViewerFailure();
        }

        const ensured = await call(APP_PREVIEW_PATHS.ensure, request, {
          leaseId: viewer.leaseId,
        });
        if (closed || !viewers.has(viewerId)) {
          await cleanup();
          return closedViewerFailure();
        }
        if (!ensured.ok) {
          await cleanup();
          return publicFailure(ensured, viewer);
        }

        viewer.snapshot = ensured.value;
        if (closed || sender.isDestroyed?.() || !viewers.has(viewerId)) {
          await cleanup();
          return closedViewerFailure();
        }
        scheduleRenewal(viewer);
        retainSenderLifecycle = true;
        return { ok: true, value: publicStatus(viewer, ensured.value) };
      });
    } finally {
      pendingOpens.delete(viewerId);
      if (!retainSenderLifecycle) {
        removeSenderLifecycle(sender, senderLifecycle);
      }
    }
  }

  async function restart(viewerId) {
    const viewer = viewers.get(viewerId);
    if (!viewer)
      return failure(
        "app_preview_lease_invalid",
        "This preview viewer is no longer attached.",
      );
    return enqueue(viewer.outputId, async () => {
      if (!viewers.has(viewerId)) {
        return failure(
          "app_preview_lease_invalid",
          "This preview viewer is no longer attached.",
        );
      }
      const activationId = viewer.snapshot.activationId;
      if (!activationId) {
        return failure(
          "app_preview_changed",
          "The app preview has no active start attempt.",
        );
      }
      const result = await call(APP_PREVIEW_PATHS.restart, viewer.request, {
        activationId,
        leaseId: viewer.leaseId,
      });
      if (!result.ok) {
        await observeCurrent(viewer);
        return publicFailure(result, viewer);
      }
      broadcast(viewer.outputId, result.value);
      return { ok: true, value: publicStatus(viewer, result.value) };
    });
  }

  async function stop(viewerId) {
    const viewer = viewers.get(viewerId);
    if (!viewer)
      return failure(
        "app_preview_lease_invalid",
        "This preview viewer is no longer attached.",
      );
    return enqueue(viewer.outputId, async () => {
      const result = await call(APP_PREVIEW_PATHS.stop, viewer.request, {
        stopToken: viewer.stopToken,
      });
      if (!result.ok) {
        await observeCurrent(viewer);
        return publicFailure(result, viewer);
      }
      const value = publicStatus(viewer, result.value);
      for (const candidate of [...viewers.values()]) {
        if (candidate.outputId !== viewer.outputId) continue;
        clearTimeout(candidate.timer);
        candidate.detached = true;
        candidate.leaseId = "";
        candidate.snapshot = result.value;
        send(candidate, {
          ok: true,
          value: publicStatus(candidate, result.value),
        });
      }
      scheduleStopObservation(viewer.outputId, viewer.request);
      return { ok: true, value };
    });
  }

  async function close(viewerId) {
    const cancelOpen = pendingOpens.get(viewerId);
    if (cancelOpen) {
      await cancelOpen();
      return { ok: true };
    }
    const viewer = viewers.get(viewerId);
    if (!viewer) return { ok: true };
    forgetViewer(viewer);
    if (!viewer.leaseId) return { ok: true };
    return enqueue(viewer.outputId, async () => {
      await call(APP_PREVIEW_PATHS.release, viewer.request, {
        leaseId: viewer.leaseId,
      });
      return { ok: true };
    });
  }

  function scheduleRenewal(viewer) {
    if (viewer.detached || !viewers.has(viewer.viewerId)) return;
    clearTimeout(viewer.timer);
    const delay = Math.max(
      1_000,
      Math.min(30_000, viewer.snapshot.renewAfterMs),
    );
    viewer.timer = setTimeout(() => void renew(viewer.viewerId), delay);
    viewer.timer.unref?.();
  }

  async function renew(viewerId) {
    const viewer = viewers.get(viewerId);
    if (!viewer || viewer.detached) return;
    await enqueue(viewer.outputId, async () => {
      if (viewer.detached || !viewers.has(viewerId)) return;
      const result = await call(APP_PREVIEW_PATHS.renew, viewer.request, {
        leaseId: viewer.leaseId,
      });
      if (!result.ok) {
        await observeCurrent(viewer);
        if (!viewers.has(viewerId)) return;
        send(viewer, publicFailure(result, viewer));
        if (
          [
            "daemon_unavailable",
            "app_preview_unavailable",
            "invalid_response",
          ].includes(result.code)
        ) {
          scheduleRenewal(viewer);
        } else {
          forgetViewer(viewer);
        }
        return;
      }
      broadcast(viewer.outputId, result.value);
      scheduleRenewal(viewer);
    });
  }

  function broadcast(outputId, snapshot, { includeDetached = false } = {}) {
    for (const viewer of viewers.values()) {
      if (viewer.outputId !== outputId || (viewer.detached && !includeDetached))
        continue;
      viewer.snapshot = snapshot;
      send(viewer, { ok: true, value: publicStatus(viewer, snapshot) });
    }
  }

  function send(viewer, message) {
    const window = getMainWindow();
    if (
      !window ||
      window.isDestroyed?.() ||
      !window.webContents ||
      window.webContents.isDestroyed?.() ||
      viewer.sender !== window.webContents
    ) {
      return;
    }
    window.webContents.send(APP_PREVIEW_EVENT_CHANNEL, message);
  }

  function forgetViewer(viewer) {
    clearTimeout(viewer.timer);
    removeSenderLifecycle(viewer.sender, viewer.senderLifecycle);
    viewers.delete(viewer.viewerId);
    if (
      ![...viewers.values()].some(
        (candidate) => candidate.outputId === viewer.outputId,
      )
    ) {
      clearTimeout(stopObservers.get(viewer.outputId));
      stopObservers.delete(viewer.outputId);
    }
  }

  async function releaseViewer(viewer) {
    const leaseId = viewer.leaseId;
    forgetViewer(viewer);
    if (!leaseId) return;
    await call(APP_PREVIEW_PATHS.release, viewer.request, {
      leaseId,
    });
  }

  async function observeCurrent(viewer) {
    const result = await call(APP_PREVIEW_PATHS.inspect, viewer.request);
    if (result.ok) broadcast(viewer.outputId, result.value);
  }

  function scheduleStopObservation(outputId, request) {
    clearTimeout(stopObservers.get(outputId));
    const timer = setTimeout(() => void observeStop(outputId, request), 1_000);
    timer.unref?.();
    stopObservers.set(outputId, timer);
  }

  async function observeStop(outputId, request) {
    stopObservers.delete(outputId);
    if (![...viewers.values()].some((viewer) => viewer.outputId === outputId))
      return;
    await enqueue(outputId, async () => {
      const result = await call(APP_PREVIEW_PATHS.inspect, request);
      if (!result.ok) {
        for (const viewer of [...viewers.values()]) {
          if (viewer.outputId === outputId)
            send(viewer, publicFailure(result, viewer));
        }
        return;
      }
      broadcast(outputId, result.value, {
        includeDetached: ["stopped", "stopping"].includes(
          result.value.lifecycle,
        ),
      });
      if (result.value.lifecycle === "stopping") {
        scheduleStopObservation(outputId, request);
      }
    });
  }

  return Object.freeze({ close, open, restart, stop });
}

function observeSenderLifecycle(sender, onClose) {
  const closeHandler = () => void onClose();
  const navigationHandler = (_event, _url, isInPlace, isMainFrame) => {
    if (isMainFrame && !isInPlace) closeHandler();
  };
  sender.once?.("destroyed", closeHandler);
  sender.on?.("did-start-navigation", navigationHandler);
  sender.once?.("render-process-gone", closeHandler);
  return { closeHandler, navigationHandler };
}

function removeSenderLifecycle(sender, lifecycle) {
  if (!lifecycle) return;
  sender.removeListener?.("destroyed", lifecycle.closeHandler);
  sender.removeListener?.("did-start-navigation", lifecycle.navigationHandler);
  sender.removeListener?.("render-process-gone", lifecycle.closeHandler);
}

function publicFailure(result, viewer) {
  return {
    code: result.code,
    ok: false,
    reason: result.reason,
    value: result.value ? publicStatus(viewer, result.value) : undefined,
    viewerId: viewer.viewerId,
  };
}

function failure(code, reason, value) {
  return { code, ok: false, reason, value };
}

function closedViewerFailure() {
  return failure(
    "app_preview_lease_invalid",
    "This preview viewer is no longer attached.",
  );
}

function stripFailure(result) {
  return { code: result.code, ok: false, reason: result.reason };
}

function publicStatus(viewer, value) {
  return Object.freeze({
    activationId: value.activationId,
    availability: value.availability,
    companyId: value.companyId,
    lastObservedAt: value.lastObservedAt,
    leaseExpiresAt: value.leaseExpiresAt,
    lifecycle: value.lifecycle,
    outputId: value.outputId,
    ports: Object.freeze([...value.ports]),
    reason: value.reason,
    renewAfterMs: value.renewAfterMs,
    runId: value.runId,
    runtimeId: value.runtimeId,
    schemaVersion: 2,
    url: value.url,
    viewerId: viewer.viewerId,
  });
}

function sanitizeDaemonResponse(value, request) {
  if (
    !value ||
    typeof value !== "object" ||
    value.schemaVersion !== 2 ||
    (value.outputId !== undefined && value.outputId !== request.outputId) ||
    (value.companyId !== undefined && value.companyId !== request.companyId) ||
    (value.runId !== undefined && value.runId !== request.runId) ||
    (value.runtimeId !== undefined && value.runtimeId !== request.runtimeId) ||
    (value.lifecycle !== undefined &&
      !["stopped", "starting", "running", "stopping", "failed"].includes(
        value.lifecycle,
      )) ||
    (value.availability !== undefined &&
      !["checking", "available", "unavailable"].includes(value.availability)) ||
    (value.activationId !== undefined && !isUuid(value.activationId)) ||
    (value.leaseId !== undefined && !isUuid(value.leaseId)) ||
    (value.ports !== undefined &&
      (!Array.isArray(value.ports) ||
        value.ports.length > 8 ||
        new Set(value.ports).size !== value.ports.length ||
        !value.ports.every(
          (port) => Number.isSafeInteger(port) && port >= 1024 && port <= 65535,
        ))) ||
    (value.url !== undefined &&
      value.url !== "" &&
      !isPreviewUrl(value.url, value.ports?.[0])) ||
    (value.stopToken !== undefined &&
      (typeof value.stopToken !== "string" || value.stopToken.length > 128)) ||
    (value.renewAfterMs !== undefined &&
      (!Number.isSafeInteger(value.renewAfterMs) || value.renewAfterMs < 1))
  ) {
    return null;
  }
  return {
    activationId: value.activationId || "",
    availability: value.availability || "unavailable",
    code: safeString(value.code, 80),
    companyId: value.companyId || request.companyId,
    detail: safeString(value.detail, 240),
    lastObservedAt: safeString(value.lastObservedAt, 80),
    leaseExpiresAt: safeString(value.leaseExpiresAt, 80),
    leaseId: value.leaseId || "",
    lifecycle: value.lifecycle || "stopped",
    outputId: value.outputId || request.outputId,
    ports: Array.isArray(value.ports) ? [...value.ports] : [],
    reason: safeString(value.reason, 80),
    renewAfterMs: value.renewAfterMs || 5_000,
    runId: value.runId || request.runId,
    runtimeId: value.runtimeId || request.runtimeId,
    stopToken: value.stopToken || "",
    url: value.url || "",
  };
}

function isPreviewUrl(value, port) {
  try {
    const url = new URL(value);
    return (
      url.protocol === "http:" &&
      url.hostname === "127.0.0.1" &&
      url.port === String(port) &&
      url.pathname === "/" &&
      !url.search &&
      !url.hash &&
      !url.username &&
      !url.password
    );
  } catch {
    return false;
  }
}

function isUuid(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value,
    )
  );
}

function safeString(value, maximum) {
  return typeof value === "string" && value.length <= maximum ? value : "";
}

function withTimeout(operation, timeoutMs) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(
      () => reject(new Error("App preview request timed out")),
      timeoutMs,
    );
    timer.unref?.();
  });
  return Promise.race([operation, timeout]).finally(() => clearTimeout(timer));
}

module.exports = { APP_PREVIEW_EVENT_CHANNEL, createDesktopAppPreview };
