"use strict";

// The login belongs to the desktop session, so returning to the app must also
// work when the originating chat or capabilities panel has been closed.
function createToolAuthReturn({ getWindow, readStatus, activateApp }) {
  const pending = new Map();

  function cancel(request) {
    const key = `${request.companyId}/${request.runtimeId}/${request.agentId}/${request.toolKey}`;
    const entry = pending.get(key);
    if (entry) clearTimeout(entry.timer);
    pending.delete(key);
  }

  function start(request) {
    cancel(request);
    const key = `${request.companyId}/${request.runtimeId}/${request.agentId}/${request.toolKey}`;
    const entry = { deadline: Date.now() + 5 * 60_000, timer: null };
    pending.set(key, entry);
    async function check() {
      if (pending.get(key) !== entry) return;
      let result;
      try {
        result = await readStatus({ ...request, action: "status" });
      } catch {
        // A temporary daemon disconnect can recover before the login expires.
      }
      if (pending.get(key) !== entry) return;
      const tool = result?.ok
        ? result.value.items.find(
            (item) =>
              item.key === request.toolKey && item.agentId === request.agentId,
          )
        : null;
      if (tool?.authState === "authenticated") {
        cancel(request);
        const window = getWindow();
        if (window && !window.isDestroyed()) {
          if (window.isMinimized()) window.restore();
          window.show();
          activateApp();
          window.focus();
        }
        return;
      }
      if (
        Date.now() >= entry.deadline ||
        tool?.authState === "error" ||
        tool?.authState === "needs_auth"
      ) {
        cancel(request);
        return;
      }
      entry.timer = setTimeout(check, 1_000);
      entry.timer.unref?.();
    }
    entry.timer = setTimeout(check, 1_000);
    entry.timer.unref?.();
  }

  function clear() {
    for (const entry of pending.values()) clearTimeout(entry.timer);
    pending.clear();
  }

  return { start, cancel, clear };
}

module.exports = { createToolAuthReturn };
