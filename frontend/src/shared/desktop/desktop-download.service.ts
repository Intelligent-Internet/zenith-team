import type {
  DesktopPlatform,
  DesktopEnvironmentName,
} from "../../../electron/shared/desktop-bridge.types";

export type MacosArchitecture = "arm64" | "x64" | "unknown";

export type DesktopDownloadUrls = Partial<Record<DesktopPlatform, string>>;
export type DesktopDownloadOptionId =
  "macos-arm64" | "macos-x64" | "windows" | "linux";

export type DesktopDownloadTarget = {
  platform: DesktopPlatform;
  label: string;
  url: string | null;
  environment: DesktopEnvironmentName;
  isAvailable: boolean;
  disabledReason?: string;
};

export type DesktopDownloadOption = {
  id: DesktopDownloadOptionId;
  platform: Exclude<DesktopPlatform, "unknown">;
  label: string;
  title: string;
  subtitle: string;
  url: string | null;
  environment: DesktopEnvironmentName;
  isAvailable: boolean;
  disabledReason?: string;
};

export type DesktopReleaseDownload = {
  kind?: string;
  sha256Url?: string | null;
  url?: string | null;
};

export type DesktopReleaseManifest = {
  version?: string;
  releasedAt?: string;
  downloads?: Partial<
    Record<
      Exclude<DesktopPlatform, "unknown">,
      Partial<Record<string, DesktopReleaseDownload>>
    >
  >;
};

type DesktopDownloadEnv = {
  VITE_BOARDLY_DESKTOP_MAC_ARM64_URL?: string;
  VITE_BOARDLY_DESKTOP_MAC_X64_URL?: string;
  VITE_BOARDLY_DESKTOP_RELEASE_MANIFEST_URL?: string;
  VITE_BOARDLY_DESKTOP_WINDOWS_URL?: string;
  VITE_BOARDLY_DESKTOP_LINUX_URL?: string;
  VITE_BOARDLY_DESKTOP_ENV?: string;
};

type NavigatorLike = {
  platform?: string;
  userAgent?: string;
  userAgentData?: {
    getHighEntropyValues?: (
      hints: string[],
    ) => Promise<{ architecture?: string }>;
    platform?: string;
  };
};

const PLATFORM_ORDER: DesktopPlatform[] = ["macos", "windows", "linux"];
const DEFAULT_DESKTOP_DOWNLOAD_ENVIRONMENT = "dev";
const DEFAULT_RELEASE_MANIFEST_URL =
  "https://storage.googleapis.com/my-public-bucket/boardly-dev/latest.json";

export function normalizeDesktopEnvironmentName(
  value: string | undefined,
): DesktopEnvironmentName {
  if (value === "dev" || value === "local" || value === "prod") {
    return value;
  }

  return "unknown";
}

export function detectDesktopPlatformFromText(value: string | undefined) {
  const normalized = value?.toLowerCase() ?? "";

  if (
    normalized.includes("mac") ||
    normalized.includes("darwin") ||
    normalized.includes("iphone") ||
    normalized.includes("ipad")
  ) {
    return "macos";
  }

  if (normalized.includes("win")) {
    return "windows";
  }

  if (
    normalized.includes("linux") ||
    normalized.includes("x11") ||
    normalized.includes("ubuntu")
  ) {
    return "linux";
  }

  return "unknown";
}

export function detectDesktopPlatform(
  navigatorLike: NavigatorLike | undefined = globalThis.navigator,
): DesktopPlatform {
  const userAgentDataPlatform = detectDesktopPlatformFromText(
    navigatorLike?.userAgentData?.platform,
  );

  if (userAgentDataPlatform !== "unknown") {
    return userAgentDataPlatform;
  }

  const navigatorPlatform = detectDesktopPlatformFromText(
    navigatorLike?.platform,
  );

  if (navigatorPlatform !== "unknown") {
    return navigatorPlatform;
  }

  return detectDesktopPlatformFromText(navigatorLike?.userAgent);
}

export async function detectMacosArchitecture(
  navigatorLike: NavigatorLike | undefined = globalThis.navigator,
): Promise<MacosArchitecture> {
  if (detectDesktopPlatform(navigatorLike) !== "macos") {
    return "unknown";
  }

  const getHighEntropyValues =
    navigatorLike?.userAgentData?.getHighEntropyValues;

  if (!getHighEntropyValues) {
    return "unknown";
  }

  try {
    const { architecture } = await getHighEntropyValues.call(
      navigatorLike.userAgentData,
      ["architecture"],
    );

    return normalizeMacosArchitecture(architecture);
  } catch (_error) {
    return "unknown";
  }
}

function normalizeMacosArchitecture(
  value: string | undefined,
): MacosArchitecture {
  switch (value?.trim().toLowerCase()) {
    case "arm":
    case "arm64":
    case "aarch64":
      return "arm64";
    case "x86":
    case "x86_64":
    case "x64":
    case "amd64":
      return "x64";
    default:
      return "unknown";
  }
}

export function resolveDesktopDownloadTarget({
  platform,
  urls,
  environment = "unknown",
}: {
  platform: DesktopPlatform;
  urls: DesktopDownloadUrls;
  environment?: DesktopEnvironmentName;
}): DesktopDownloadTarget {
  const platformUrl = platform === "unknown" ? undefined : urls[platform];
  const fallbackPlatform = PLATFORM_ORDER.find((candidate) => urls[candidate]);
  const resolvedPlatform = platformUrl
    ? platform
    : (fallbackPlatform ?? platform);
  const url =
    platformUrl ?? (fallbackPlatform ? urls[fallbackPlatform] : undefined);
  const isKnownPlatform = platform !== "unknown" && Boolean(platformUrl);
  const label = isKnownPlatform
    ? `Download for ${getPlatformLabel(platform)}`
    : "Download desktop app";

  if (!url) {
    return {
      platform,
      label,
      url: null,
      environment,
      isAvailable: false,
      disabledReason: "Desktop installer is not available yet.",
    };
  }

  return {
    platform: resolvedPlatform,
    label,
    url,
    environment,
    isAvailable: true,
  };
}

export function getDesktopDownloadTarget({
  navigatorLike,
  env,
  manifest,
}: {
  navigatorLike?: NavigatorLike;
  env?: DesktopDownloadEnv;
  manifest?: DesktopReleaseManifest | null;
} = {}): DesktopDownloadTarget {
  const downloadEnv = env ?? (import.meta.env as unknown as DesktopDownloadEnv);
  const urls = resolveDesktopDownloadUrls({ env: downloadEnv, manifest });

  return resolveDesktopDownloadTarget({
    platform: detectDesktopPlatform(navigatorLike),
    environment: normalizeDesktopEnvironmentName(
      downloadEnv.VITE_BOARDLY_DESKTOP_ENV ??
        DEFAULT_DESKTOP_DOWNLOAD_ENVIRONMENT,
    ),
    urls,
  });
}

export function getDesktopDownloadOptions({
  env,
  manifest,
}: {
  env?: DesktopDownloadEnv;
  manifest?: DesktopReleaseManifest | null;
} = {}): DesktopDownloadOption[] {
  const downloadEnv = env ?? (import.meta.env as unknown as DesktopDownloadEnv);
  const environment = normalizeDesktopEnvironmentName(
    downloadEnv.VITE_BOARDLY_DESKTOP_ENV ??
      DEFAULT_DESKTOP_DOWNLOAD_ENVIRONMENT,
  );
  const urls = resolveDesktopDownloadUrls({ env: downloadEnv, manifest });
  const options: DesktopDownloadOption[] = [
    {
      id: "macos-arm64",
      platform: "macos",
      label: "Download Zenith Team for macOS Apple Silicon",
      title: "Apple Silicon",
      subtitle: "M1, M2, M3, M4 Macs",
      url: urls.macosArm64 ?? null,
      environment,
      isAvailable: Boolean(urls.macosArm64),
      disabledReason: urls.macosArm64 ? undefined : "Coming soon.",
    },
    {
      id: "macos-x64",
      platform: "macos",
      label: "Download Zenith Team for macOS Intel",
      title: "Intel Mac",
      subtitle: "Intel-based Macs",
      url: urls.macosX64 ?? null,
      environment,
      isAvailable: Boolean(urls.macosX64),
      disabledReason: urls.macosX64 ? undefined : "Coming soon.",
    },
  ];

  options.push(
    {
      id: "windows",
      platform: "windows",
      label: "Download Zenith Team for Windows",
      title: "Windows",
      subtitle: "Windows desktop",
      url: urls.windows ?? null,
      environment,
      isAvailable: Boolean(urls.windows),
      disabledReason: urls.windows ? undefined : "Coming soon.",
    },
    {
      id: "linux",
      platform: "linux",
      label: "Download Zenith Team for Linux",
      title: "Linux",
      subtitle: "Linux desktop",
      url: urls.linux ?? null,
      environment,
      isAvailable: Boolean(urls.linux),
      disabledReason: urls.linux ? undefined : "Coming soon.",
    },
  );

  return options;
}

export async function loadDesktopDownloadOptions({
  env,
  fetchImpl,
}: {
  env?: DesktopDownloadEnv;
  fetchImpl?: typeof fetch;
} = {}): Promise<DesktopDownloadOption[]> {
  const downloadEnv = env ?? (import.meta.env as unknown as DesktopDownloadEnv);
  const manifest = await loadDesktopReleaseManifest({
    env: downloadEnv,
    fetchImpl,
  });

  return getDesktopDownloadOptions({ env: downloadEnv, manifest });
}

export async function loadDesktopReleaseManifest({
  env,
  fetchImpl = globalThis.fetch?.bind(globalThis),
}: {
  env?: DesktopDownloadEnv;
  fetchImpl?: typeof fetch;
} = {}): Promise<DesktopReleaseManifest | null> {
  const manifestUrl = getDesktopReleaseManifestUrl({ env });

  if (!manifestUrl || !fetchImpl) {
    return null;
  }

  try {
    const response = await fetchImpl(manifestUrl, {
      cache: "no-store",
      credentials: "omit",
    });

    if (!response.ok) {
      return null;
    }

    return normalizeDesktopReleaseManifest(await response.json());
  } catch (_error) {
    return null;
  }
}

export function getDesktopReleaseManifestUrl({
  env,
}: {
  env?: DesktopDownloadEnv;
} = {}) {
  const downloadEnv = env ?? (import.meta.env as unknown as DesktopDownloadEnv);

  return normalizeUrl(
    downloadEnv.VITE_BOARDLY_DESKTOP_RELEASE_MANIFEST_URL ??
      DEFAULT_RELEASE_MANIFEST_URL,
  );
}

function resolveDesktopDownloadUrls({
  env,
  manifest,
}: {
  env: DesktopDownloadEnv;
  manifest?: DesktopReleaseManifest | null;
}) {
  const macosArm64 =
    normalizeUrl(env.VITE_BOARDLY_DESKTOP_MAC_ARM64_URL) ??
    getManifestDownloadUrl(manifest, "macos", "arm64");
  const macosX64 =
    normalizeUrl(env.VITE_BOARDLY_DESKTOP_MAC_X64_URL) ??
    getManifestDownloadUrl(manifest, "macos", "x64");
  const windows =
    normalizeUrl(env.VITE_BOARDLY_DESKTOP_WINDOWS_URL) ??
    getManifestDownloadUrl(manifest, "windows", "x64");
  const linux =
    normalizeUrl(env.VITE_BOARDLY_DESKTOP_LINUX_URL) ??
    getManifestDownloadUrl(manifest, "linux", "x64");

  return {
    linux,
    macos: macosArm64,
    macosArm64,
    macosX64,
    windows,
  };
}

function getManifestDownloadUrl(
  manifest: DesktopReleaseManifest | null | undefined,
  platform: Exclude<DesktopPlatform, "unknown">,
  arch: string,
) {
  return normalizeUrl(
    manifest?.downloads?.[platform]?.[arch]?.url ?? undefined,
  );
}

function normalizeDesktopReleaseManifest(
  value: unknown,
): DesktopReleaseManifest | null {
  if (!value || typeof value !== "object") {
    return null;
  }

  const rawManifest = value as DesktopReleaseManifest;

  return {
    downloads: normalizeManifestDownloads(rawManifest.downloads),
    releasedAt:
      typeof rawManifest.releasedAt === "string"
        ? rawManifest.releasedAt
        : undefined,
    version:
      typeof rawManifest.version === "string" ? rawManifest.version : undefined,
  };
}

function normalizeManifestDownloads(
  downloads: DesktopReleaseManifest["downloads"],
) {
  if (!downloads || typeof downloads !== "object") {
    return undefined;
  }

  const normalized: DesktopReleaseManifest["downloads"] = {};

  for (const platform of ["macos", "windows", "linux"] as const) {
    const platformDownloads = downloads[platform];

    if (!platformDownloads || typeof platformDownloads !== "object") {
      continue;
    }

    normalized[platform] = {};

    for (const [arch, entry] of Object.entries(platformDownloads)) {
      if (!entry || typeof entry !== "object") {
        continue;
      }

      const url = normalizeUrl(entry.url ?? undefined);

      if (!url) {
        continue;
      }

      normalized[platform][arch] = {
        kind: typeof entry.kind === "string" ? entry.kind : undefined,
        sha256Url: normalizeUrl(entry.sha256Url ?? undefined) ?? null,
        url,
      };
    }
  }

  return normalized;
}

function normalizeUrl(value: string | undefined) {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

function getPlatformLabel(platform: DesktopPlatform) {
  switch (platform) {
    case "macos":
      return "macOS";
    case "windows":
      return "Windows";
    case "linux":
      return "Linux";
    case "unknown":
      return "desktop";
  }
}
