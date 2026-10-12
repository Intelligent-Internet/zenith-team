const GET_POWER_LIFECYCLE_STATE_CHANNEL = "desktop:get-power-lifecycle-state";
const POWER_LIFECYCLE_UPDATED_CHANNEL = "desktop:power-lifecycle-updated";
const POWER_IDLE_THRESHOLD_SECONDS = 60;

const batteryStates = new Set(["ac", "battery", "unknown"]);
const idleStates = new Set(["active", "idle", "locked", "unknown"]);
const lifecycleStates = new Set(["active", "locked", "suspended"]);
const thermalStates = new Set([
  "critical",
  "fair",
  "nominal",
  "serious",
  "unknown",
]);
const powerLifecycleEventTypes = new Set([
  "lock-screen",
  "on-ac",
  "on-battery",
  "resume",
  "speed-limit-change",
  "suspend",
  "thermal-state-change",
  "unlock-screen",
  "user-did-become-active",
  "user-did-resign-active",
]);

function registerDesktopPowerLifecycle(options = {}) {
  const electron = options.electron ?? require("electron");
  const dependencies = {
    app: options.app ?? electron.app,
    getMainWindow: options.getMainWindow,
    ipcMain: options.ipcMain ?? electron.ipcMain,
    now: options.now,
    powerMonitor: options.powerMonitor ?? electron.powerMonitor,
  };
  const lifecycle = createDesktopPowerLifecycle(dependencies);

  dependencies.ipcMain.handle(GET_POWER_LIFECYCLE_STATE_CHANNEL, () =>
    lifecycle.getState(),
  );
  dependencies.app?.on?.("will-quit", () => {
    lifecycle.dispose();
  });

  return lifecycle.dispose;
}

function createDesktopPowerLifecycle({
  getMainWindow,
  now = Date.now,
  powerMonitor,
}) {
  const state = normalizePowerLifecycleState({
    battery: readBatteryState(powerMonitor),
    idleState: readIdleState(powerMonitor),
    idleTimeSeconds: readIdleTimeSeconds(powerMonitor),
    lastEvent: null,
    lifecycle: "active",
    speedLimitPercent: null,
    thermalState: readThermalState(powerMonitor),
  });
  state.lifecycle = state.idleState === "locked" ? "locked" : "active";

  const disposeListeners = [
    addPowerMonitorListener(powerMonitor, "suspend", () => {
      applyLifecycleEvent("suspend", { lifecycle: "suspended" });
    }),
    addPowerMonitorListener(powerMonitor, "resume", () => {
      refreshTelemetry({ lifecycle: "active" });
      applyLifecycleEvent("resume");
    }),
    addPowerMonitorListener(powerMonitor, "lock-screen", () => {
      applyLifecycleEvent("lock-screen", {
        idleState: "locked",
        lifecycle: "locked",
      });
    }),
    addPowerMonitorListener(powerMonitor, "unlock-screen", () => {
      refreshTelemetry({
        idleState: "active",
        lifecycle: "active",
      });
      applyLifecycleEvent("unlock-screen");
    }),
    addPowerMonitorListener(powerMonitor, "on-ac", () => {
      applyLifecycleEvent("on-ac", { battery: "ac" });
    }),
    addPowerMonitorListener(powerMonitor, "on-battery", () => {
      applyLifecycleEvent("on-battery", { battery: "battery" });
    }),
    addPowerMonitorListener(powerMonitor, "thermal-state-change", (...args) => {
      const details = findEventDetails(args);
      applyLifecycleEvent("thermal-state-change", {
        thermalState: normalizeThermalState(details.state),
      });
    }),
    addPowerMonitorListener(powerMonitor, "speed-limit-change", (...args) => {
      const details = findEventDetails(args);
      applyLifecycleEvent("speed-limit-change", {
        speedLimitPercent: normalizePercent(details.limit),
      });
    }),
    addPowerMonitorListener(powerMonitor, "user-did-become-active", () => {
      applyLifecycleEvent("user-did-become-active", {
        idleState: "active",
        lifecycle: "active",
      });
    }),
    addPowerMonitorListener(powerMonitor, "user-did-resign-active", () => {
      applyLifecycleEvent("user-did-resign-active", {
        idleState: "idle",
      });
    }),
  ];

  function getState() {
    return clonePowerLifecycleState(state);
  }

  function refreshTelemetry(overrides = {}) {
    Object.assign(
      state,
      normalizePowerLifecycleState({
        ...state,
        battery: readBatteryState(powerMonitor),
        idleState: readIdleState(powerMonitor),
        idleTimeSeconds: readIdleTimeSeconds(powerMonitor),
        thermalState: readThermalState(powerMonitor),
        ...overrides,
      }),
    );
  }

  function applyLifecycleEvent(type, patch = {}) {
    Object.assign(
      state,
      normalizePowerLifecycleState({
        ...state,
        ...patch,
        lastEvent: {
          createdAt: createTimestamp(now),
          type,
        },
      }),
    );
    broadcastState();
  }

  function broadcastState() {
    const mainWindow = getMainWindow?.();

    if (!mainWindow || mainWindow.isDestroyed?.()) {
      return;
    }

    mainWindow.webContents.send(POWER_LIFECYCLE_UPDATED_CHANNEL, getState());
  }

  function dispose() {
    for (const disposeListener of disposeListeners) {
      disposeListener();
    }
  }

  return {
    dispose,
    getState,
  };
}

function normalizePowerLifecycleState(state) {
  const normalizedIdleState = normalizeIdleState(state?.idleState);

  return {
    battery: normalizeBatteryState(state?.battery),
    idleState: normalizedIdleState,
    idleTimeSeconds: normalizeSeconds(state?.idleTimeSeconds),
    lastEvent: normalizeLastEvent(state?.lastEvent),
    lifecycle: normalizeLifecycleState(state?.lifecycle),
    speedLimitPercent: normalizePercent(state?.speedLimitPercent),
    thermalState: normalizeThermalState(state?.thermalState),
  };
}

function clonePowerLifecycleState(state) {
  return {
    ...state,
    lastEvent: state.lastEvent ? { ...state.lastEvent } : null,
  };
}

function addPowerMonitorListener(powerMonitor, eventName, listener) {
  if (typeof powerMonitor?.on !== "function") {
    return () => {};
  }

  powerMonitor.on(eventName, listener);

  return () => {
    if (typeof powerMonitor.off === "function") {
      powerMonitor.off(eventName, listener);
      return;
    }

    powerMonitor.removeListener?.(eventName, listener);
  };
}

function findEventDetails(args) {
  for (const arg of args) {
    if (arg && typeof arg === "object") {
      return arg;
    }
  }

  return {};
}

function readBatteryState(powerMonitor) {
  try {
    if (typeof powerMonitor?.isOnBatteryPower === "function") {
      return powerMonitor.isOnBatteryPower() ? "battery" : "ac";
    }

    if (typeof powerMonitor?.onBatteryPower === "boolean") {
      return powerMonitor.onBatteryPower ? "battery" : "ac";
    }
  } catch {
    return "unknown";
  }

  return "unknown";
}

function readIdleState(powerMonitor) {
  try {
    if (typeof powerMonitor?.getSystemIdleState === "function") {
      return normalizeIdleState(
        powerMonitor.getSystemIdleState(POWER_IDLE_THRESHOLD_SECONDS),
      );
    }
  } catch {
    return "unknown";
  }

  return "unknown";
}

function readIdleTimeSeconds(powerMonitor) {
  try {
    if (typeof powerMonitor?.getSystemIdleTime === "function") {
      return normalizeSeconds(powerMonitor.getSystemIdleTime());
    }
  } catch {
    return null;
  }

  return null;
}

function readThermalState(powerMonitor) {
  try {
    if (typeof powerMonitor?.getCurrentThermalState === "function") {
      return normalizeThermalState(powerMonitor.getCurrentThermalState());
    }
  } catch {
    return "unknown";
  }

  return "unknown";
}

function normalizeBatteryState(value) {
  return batteryStates.has(value) ? value : "unknown";
}

function normalizeIdleState(value) {
  return idleStates.has(value) ? value : "unknown";
}

function normalizeLifecycleState(value) {
  return lifecycleStates.has(value) ? value : "active";
}

function normalizeThermalState(value) {
  return thermalStates.has(value) ? value : "unknown";
}

function normalizeSeconds(value) {
  if (!Number.isFinite(value)) {
    return null;
  }

  return Math.max(0, Math.floor(value));
}

function normalizePercent(value) {
  if (!Number.isFinite(value)) {
    return null;
  }

  return Math.max(0, Math.min(100, Math.round(value)));
}

function normalizeLastEvent(event) {
  if (
    !event ||
    typeof event !== "object" ||
    !powerLifecycleEventTypes.has(event.type)
  ) {
    return null;
  }

  const createdAt = new Date(event.createdAt);

  if (Number.isNaN(createdAt.getTime())) {
    return null;
  }

  return {
    createdAt: createdAt.toISOString(),
    type: event.type,
  };
}

function createTimestamp(now) {
  const current = typeof now === "function" ? now() : Date.now();
  const timestamp = new Date(current);

  if (Number.isNaN(timestamp.getTime())) {
    return new Date().toISOString();
  }

  return timestamp.toISOString();
}

module.exports = {
  GET_POWER_LIFECYCLE_STATE_CHANNEL,
  POWER_LIFECYCLE_UPDATED_CHANNEL,
  createDesktopPowerLifecycle,
  normalizePowerLifecycleState,
  registerDesktopPowerLifecycle,
};
