const DEFAULT_POWER_SAVE_BLOCKER_TYPE = "prevent-app-suspension";

const approvedPowerSaveBlockerReasons = new Set(["download"]);

function createDesktopPowerSaveBlockerManager({ powerSaveBlocker } = {}) {
  const activeBlockers = new Map();

  function start({ reason } = {}) {
    if (!approvedPowerSaveBlockerReasons.has(reason)) {
      return {
        ok: false,
        reason: "Power save blocker reason is not approved.",
      };
    }

    if (typeof powerSaveBlocker?.start !== "function") {
      return {
        ok: false,
        reason: "Power save blocker is not available.",
      };
    }

    let id = null;

    try {
      id = powerSaveBlocker.start(DEFAULT_POWER_SAVE_BLOCKER_TYPE);
    } catch {
      return {
        ok: false,
        reason: "Power save blocker could not be started.",
      };
    }

    if (!Number.isSafeInteger(id)) {
      return {
        ok: false,
        reason: "Power save blocker returned an invalid id.",
      };
    }

    activeBlockers.set(id, {
      reason,
      type: DEFAULT_POWER_SAVE_BLOCKER_TYPE,
    });

    return {
      id,
      ok: true,
      type: DEFAULT_POWER_SAVE_BLOCKER_TYPE,
    };
  }

  function stop(id) {
    if (!activeBlockers.has(id)) {
      return { ok: true, stopped: false };
    }

    let stopped = false;

    try {
      stopped = Boolean(powerSaveBlocker?.stop?.(id));
    } catch {
      stopped = false;
    } finally {
      activeBlockers.delete(id);
    }

    return { ok: true, stopped };
  }

  function dispose() {
    for (const id of activeBlockers.keys()) {
      stop(id);
    }
  }

  function getActiveCount() {
    return activeBlockers.size;
  }

  return {
    dispose,
    getActiveCount,
    start,
    stop,
  };
}

module.exports = {
  DEFAULT_POWER_SAVE_BLOCKER_TYPE,
  createDesktopPowerSaveBlockerManager,
};
