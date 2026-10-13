import type {
  DesktopEnvironment,
  DesktopPowerLifecycleState,
  DesktopUpdateState,
} from "../../../electron/shared/desktop-bridge.types";

export const DESKTOP_RUNTIME_SLOW_THRESHOLD_MS = 2500;
export const DESKTOP_RUNTIME_PROBE_INTERVAL_MS = 30_000;

export type DesktopRuntimeProbeStatus =
  "checking" | "ready" | "slow" | "unreachable";

export type DesktopRuntimeStatusTone =
  "checking" | "danger" | "neutral" | "ready" | "warning";

export type DesktopRuntimeProbe = {
  checkedAt: number | null;
  durationMs: number | null;
  status: DesktopRuntimeProbeStatus;
};

export type DesktopRuntimeStatusItem = {
  detail: string;
  id: "api" | "build" | "network" | "power" | "realtime" | "updates";
  label: string;
  title: string;
  tone: DesktopRuntimeStatusTone;
};

export type DesktopRuntimeStatusModel = {
  ariaLabel: string;
  items: DesktopRuntimeStatusItem[];
};

type CreateDesktopRuntimeProbeInput = {
  checkedAt: number | null;
  durationMs: number | null;
  ok: boolean | null;
  slowThresholdMs?: number;
};

type CreateDesktopRuntimeStatusModelInput = {
  api: DesktopRuntimeProbe;
  environment: DesktopEnvironment;
  online: boolean;
  power: DesktopPowerLifecycleState;
  realtime: DesktopRuntimeProbe;
  updates: DesktopUpdateState;
};

export function createDesktopRuntimeProbe({
  checkedAt,
  durationMs,
  ok,
  slowThresholdMs = DESKTOP_RUNTIME_SLOW_THRESHOLD_MS,
}: CreateDesktopRuntimeProbeInput): DesktopRuntimeProbe {
  const normalizedDurationMs = normalizeDurationMs(durationMs);

  if (ok === null) {
    return {
      checkedAt,
      durationMs: normalizedDurationMs,
      status: "checking",
    };
  }

  if (!ok) {
    return {
      checkedAt,
      durationMs: normalizedDurationMs,
      status: "unreachable",
    };
  }

  return {
    checkedAt,
    durationMs: normalizedDurationMs,
    status:
      normalizedDurationMs !== null && normalizedDurationMs >= slowThresholdMs
        ? "slow"
        : "ready",
  };
}

export function createDesktopRuntimeStatusModel({
  api,
  environment,
  online,
  power,
  realtime,
  updates,
}: CreateDesktopRuntimeStatusModelInput): DesktopRuntimeStatusModel {
  const items: DesktopRuntimeStatusItem[] = [
    createNetworkItem(online),
    createApiItem(online, api),
    createRealtimeItem(online, realtime),
    createPowerItem(power),
    createDesktopUpdateStatusItem(updates),
    createBuildItem(environment),
  ];

  return {
    ariaLabel: `Zenith Team desktop runtime: ${items
      .map((item) => `${item.label}, ${item.detail}`)
      .join("; ")}`,
    items,
  };
}

export function createDesktopUpdateStatusItem(
  updates: DesktopUpdateState,
): DesktopRuntimeStatusItem {
  if (updates.status === "checking") {
    return {
      detail: "Checking for update",
      id: "updates",
      label: "Updates checking",
      title: "Zenith Team is checking the native update feed.",
      tone: "checking",
    };
  }

  if (updates.status === "available") {
    return {
      detail: updates.releaseName ?? "Download pending",
      id: "updates",
      label: "Update available",
      title: "A Zenith Team desktop update is available.",
      tone: "warning",
    };
  }

  if (updates.status === "downloading") {
    return {
      detail: updates.releaseName ?? "Downloading in background",
      id: "updates",
      label: "Update downloading",
      title: "Zenith Team is downloading a native desktop update.",
      tone: "checking",
    };
  }

  if (updates.status === "downloaded") {
    return {
      detail: updates.releaseName ?? "Ready to install",
      id: "updates",
      label: "Update downloaded",
      title:
        "A Zenith Team desktop update has downloaded and is ready to install.",
      tone: "warning",
    };
  }

  if (updates.status === "restart-required") {
    return {
      detail: updates.releaseName ?? "Restart to install",
      id: "updates",
      label: "Restart required",
      title: "Restart Zenith Team to install the downloaded desktop update.",
      tone: "warning",
    };
  }

  if (updates.status === "failed") {
    return {
      detail: updates.errorMessage ?? "Check failed",
      id: "updates",
      label: "Updates failed",
      title: "Zenith Team could not complete the native update check.",
      tone: "danger",
    };
  }

  if (updates.status === "up-to-date") {
    return {
      detail: "No update available",
      id: "updates",
      label: "Updates current",
      title: "Zenith Team desktop is up to date.",
      tone: "ready",
    };
  }

  if (updates.status === "idle" && updates.policy === "native") {
    return {
      detail: "Native checks ready",
      id: "updates",
      label: "Updates ready",
      title: "Native desktop update checks are available.",
      tone: "neutral",
    };
  }

  return {
    detail: "Manual download",
    id: "updates",
    label: "Updates manual",
    title: "Native desktop auto-update is not enabled for this build.",
    tone: "neutral",
  };
}

function createPowerItem(
  power: DesktopPowerLifecycleState,
): DesktopRuntimeStatusItem {
  if (power.lifecycle === "suspended") {
    return {
      detail: "System sleep",
      id: "power",
      label: "Suspended",
      title:
        "The operating system suspended Zenith Team. Connectivity will refresh on resume.",
      tone: "warning",
    };
  }

  if (power.lifecycle === "locked" || power.idleState === "locked") {
    return {
      detail: "Screen locked",
      id: "power",
      label: "Locked",
      title:
        "The desktop session is locked. Zenith Team will refresh interactive work after unlock.",
      tone: "neutral",
    };
  }

  if (power.thermalState === "critical") {
    return {
      detail: formatPowerLimit(power.speedLimitPercent, "Reduce local work"),
      id: "power",
      label: "Thermal critical",
      title: "The operating system is reporting critical thermal pressure.",
      tone: "danger",
    };
  }

  if (power.thermalState === "serious") {
    return {
      detail: formatPowerLimit(power.speedLimitPercent, "Reduce local work"),
      id: "power",
      label: "Thermal high",
      title: "The operating system is reporting serious thermal pressure.",
      tone: "warning",
    };
  }

  if (power.speedLimitPercent !== null && power.speedLimitPercent < 100) {
    return {
      detail: formatPowerLimit(power.speedLimitPercent, "CPU limited"),
      id: "power",
      label: "Power limited",
      title: "The operating system is limiting available CPU speed.",
      tone: "warning",
    };
  }

  if (power.battery === "battery") {
    return {
      detail: "Conserve local work",
      id: "power",
      label: "On battery",
      title: "Zenith Team is running on battery power.",
      tone: "warning",
    };
  }

  if (power.battery === "ac") {
    return {
      detail: "AC power",
      id: "power",
      label: "Power ready",
      title: "Zenith Team is running on AC power.",
      tone: "ready",
    };
  }

  return {
    detail: "Lifecycle signals unavailable",
    id: "power",
    label: "Power unknown",
    title: "Zenith Team has not received desktop power lifecycle signals yet.",
    tone: "neutral",
  };
}

function createNetworkItem(online: boolean): DesktopRuntimeStatusItem {
  return {
    detail: online ? "Network available" : "Network unavailable",
    id: "network",
    label: online ? "Online" : "Offline",
    title: online
      ? "The desktop app is receiving network connectivity signals."
      : "The desktop app is currently offline.",
    tone: online ? "ready" : "danger",
  };
}

function createApiItem(
  online: boolean,
  api: DesktopRuntimeProbe,
): DesktopRuntimeStatusItem {
  if (!online) {
    return {
      detail: "Waiting for network",
      id: "api",
      label: "API offline",
      title: "Zenith Team API checks are paused until the network returns.",
      tone: "danger",
    };
  }

  if (api.status === "checking") {
    return {
      detail: "Checking reachability",
      id: "api",
      label: "API checking",
      title: "Zenith Team is checking the API connection.",
      tone: "checking",
    };
  }

  if (api.status === "unreachable") {
    return {
      detail: formatDuration(api.durationMs, "last attempt"),
      id: "api",
      label: "API down",
      title: "Zenith Team could not reach the API.",
      tone: "danger",
    };
  }

  if (api.status === "slow") {
    return {
      detail: formatDuration(api.durationMs, "response"),
      id: "api",
      label: "API slow",
      title: "Zenith Team API is reachable but slower than expected.",
      tone: "warning",
    };
  }

  return {
    detail: formatDuration(api.durationMs, "response"),
    id: "api",
    label: "API ready",
    title: "Zenith Team API is reachable.",
    tone: "ready",
  };
}

function createRealtimeItem(
  online: boolean,
  realtime: DesktopRuntimeProbe,
): DesktopRuntimeStatusItem {
  if (!online) {
    return {
      detail: "Waiting for network",
      id: "realtime",
      label: "Realtime offline",
      title: "Realtime checks are paused until the network returns.",
      tone: "danger",
    };
  }

  if (realtime.status === "checking") {
    return {
      detail: "Checking connection",
      id: "realtime",
      label: "Realtime checking",
      title: "Zenith Team is checking realtime connectivity.",
      tone: "checking",
    };
  }

  if (realtime.status === "unreachable") {
    return {
      detail: formatDuration(realtime.durationMs, "last attempt"),
      id: "realtime",
      label: "Realtime down",
      title: "Zenith Team could not connect to realtime updates.",
      tone: "danger",
    };
  }

  if (realtime.status === "slow") {
    return {
      detail: formatDuration(realtime.durationMs, "connect"),
      id: "realtime",
      label: "Realtime slow",
      title: "Zenith Team realtime is reachable but slower than expected.",
      tone: "warning",
    };
  }

  return {
    detail: formatDuration(realtime.durationMs, "connect"),
    id: "realtime",
    label: "Realtime ready",
    title: "Zenith Team realtime updates are reachable.",
    tone: "ready",
  };
}

function createBuildItem(
  environment: DesktopEnvironment,
): DesktopRuntimeStatusItem {
  const environmentLabel =
    environment.environment === "unknown" ? "unknown" : environment.environment;
  const versionLabel = environment.appVersion?.trim() || "dev";
  return {
    detail: `${formatPlatform(environment.platform)} desktop`,
    id: "build",
    label: `${environmentLabel} ${versionLabel}`,
    title: "Current Zenith Team desktop environment and app version.",
    tone: "neutral",
  };
}

function normalizeDurationMs(durationMs: number | null): number | null {
  if (durationMs === null || !Number.isFinite(durationMs)) {
    return null;
  }

  return Math.max(0, Math.round(durationMs));
}

function formatDuration(durationMs: number | null, noun: string): string {
  if (durationMs === null) {
    return "No timing yet";
  }

  if (durationMs < 1000) {
    return `${durationMs}ms ${noun}`;
  }

  const seconds = durationMs / 1000;
  const formattedSeconds =
    seconds >= 10 ? Math.round(seconds).toString() : seconds.toFixed(1);
  return `${formattedSeconds}s ${noun}`;
}

function formatPowerLimit(
  speedLimitPercent: number | null,
  fallback: string,
): string {
  if (speedLimitPercent === null) {
    return fallback;
  }

  return `${speedLimitPercent}% CPU limit`;
}

function formatPlatform(platform: DesktopEnvironment["platform"]): string {
  switch (platform) {
    case "macos":
      return "macOS";
    case "windows":
      return "Windows";
    case "linux":
      return "Linux";
    default:
      return "Unknown";
  }
}
