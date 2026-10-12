export type BrowserStorageKind = "local" | "session";

function getBrowserStorage(kind: BrowserStorageKind): Storage | null {
  if (typeof window === "undefined") {
    return null;
  }

  try {
    return kind === "local" ? window.localStorage : window.sessionStorage;
  } catch {
    return null;
  }
}

export function readStorageItem(
  kind: BrowserStorageKind,
  key: string,
): string | null {
  try {
    return getBrowserStorage(kind)?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeStorageItem(
  kind: BrowserStorageKind,
  key: string,
  value: string,
): boolean {
  try {
    getBrowserStorage(kind)?.setItem(key, value);
    return true;
  } catch {
    return false;
  }
}

export function removeStorageItem(
  kind: BrowserStorageKind,
  key: string,
): boolean {
  try {
    getBrowserStorage(kind)?.removeItem(key);
    return true;
  } catch {
    return false;
  }
}

export function readStorageJson<T>(
  kind: BrowserStorageKind,
  key: string,
  normalize: (value: unknown) => T,
  fallback: T,
): T {
  const rawValue = readStorageItem(kind, key);

  if (!rawValue) {
    return fallback;
  }

  try {
    return normalize(JSON.parse(rawValue));
  } catch {
    return fallback;
  }
}

export function writeStorageJson(
  kind: BrowserStorageKind,
  key: string,
  value: unknown,
): boolean {
  try {
    return writeStorageItem(kind, key, JSON.stringify(value));
  } catch {
    return false;
  }
}
