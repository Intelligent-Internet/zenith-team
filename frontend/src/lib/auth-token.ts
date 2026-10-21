const LOCAL_DEV_ACCESS_TOKEN_STORAGE_KEY = "ZENITH_TEAM_ACCESS_TOKEN";

let accessToken: string | null = null;
let hasHydratedDesktopAccessToken = false;

function isLocalhost(hostname: string): boolean {
  return (
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname.endsWith(".localhost")
  );
}

function getLocalStorage(): Storage | null {
  if (typeof window === "undefined") {
    return null;
  }

  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function getDesktopBridge() {
  if (typeof window === "undefined") {
    return null;
  }

  return window.zenithTeamDesktop ?? null;
}

function normalizeAccessToken(token: string | null | undefined): string | null {
  const nextToken = token?.trim();
  return nextToken || null;
}

export async function hydrateStoredAccessToken(): Promise<void> {
  if (hasHydratedDesktopAccessToken || accessToken) {
    return;
  }

  const bridge = getDesktopBridge();

  if (!bridge?.getStoredAccessToken) {
    hasHydratedDesktopAccessToken = true;
    return;
  }

  try {
    const result = await bridge.getStoredAccessToken();

    if (result.ok) {
      accessToken = normalizeAccessToken(result.accessToken);
    }
  } catch {
    // Desktop credential hydration is best-effort; route guards can still
    // recover through the refresh-cookie path when available.
  } finally {
    hasHydratedDesktopAccessToken = true;
  }
}

export function getStoredAccessToken(): string | null {
  if (accessToken) {
    return accessToken;
  }

  if (typeof window === "undefined" || !isLocalhost(window.location.hostname)) {
    return null;
  }

  const localDevToken = getLocalStorage()
    ?.getItem(LOCAL_DEV_ACCESS_TOKEN_STORAGE_KEY)
    ?.trim();

  return localDevToken || null;
}

export function setStoredAccessToken(token: string | null | undefined): void {
  cacheStoredAccessToken(token);
  persistDesktopAccessToken(accessToken);
}

export function cacheStoredAccessToken(token: string | null | undefined): void {
  accessToken = normalizeAccessToken(token);
  hasHydratedDesktopAccessToken = Boolean(accessToken);
}

export function clearStoredAccessToken(): void {
  setStoredAccessToken(null);

  if (typeof window !== "undefined" && isLocalhost(window.location.hostname)) {
    getLocalStorage()?.removeItem(LOCAL_DEV_ACCESS_TOKEN_STORAGE_KEY);
  }
}

export function getStoredAuthorizationHeader(): string | null {
  const token = getStoredAccessToken();
  return token ? `Bearer ${token}` : null;
}

function persistDesktopAccessToken(token: string | null) {
  const bridge = getDesktopBridge();

  // Full Desktop bearer sessions are installed by the main process through
  // the Go credential bridge. A renderer access token is only an in-memory
  // cache and must never become a partial auth.json write.
  if (!bridge || token) return;

  const request = bridge.clearStoredAccessToken?.();
  void request?.catch(() => undefined);
}
