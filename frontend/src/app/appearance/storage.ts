import {
  defaultAppearancePreferences,
  normalizeAppearancePreferences,
  type AppearancePreferences,
} from "./preferences";

// Only visual preferences are retained for entry pages after logout.
export const lastAppearanceStorageKey = "boardly:appearance:last-used:v1";

// Failed writes remain usable when revisiting a company during this app session.
const unsavedPreferences = new Map<string, AppearancePreferences>();

export function appearanceStorageKey(
  userId?: string | null,
  companyId?: string | null,
): string | null {
  if (!userId || !companyId) return null;
  return `boardly:appearance:v1:${encodeURIComponent(userId)}:${encodeURIComponent(companyId)}`;
}

export function parseAppearancePreferences(
  serialized: string | null,
): AppearancePreferences {
  if (!serialized) return defaultAppearancePreferences();
  try {
    return normalizeAppearancePreferences(JSON.parse(serialized));
  } catch {
    return defaultAppearancePreferences();
  }
}

export function readAppearancePreferences(scopeKey: string | null) {
  if (!scopeKey) {
    return { preferences: defaultAppearancePreferences(), storageError: false };
  }
  const unsaved = unsavedPreferences.get(scopeKey);
  if (unsaved) return { preferences: unsaved, storageError: true };

  try {
    return {
      preferences: parseAppearancePreferences(
        window.localStorage.getItem(scopeKey),
      ),
      storageError: false,
    };
  } catch {
    return { preferences: defaultAppearancePreferences(), storageError: true };
  }
}

export function writeAppearancePreferences(
  scopeKey: string | null,
  preferences: AppearancePreferences,
): boolean {
  if (!scopeKey) return true;
  try {
    window.localStorage.setItem(scopeKey, JSON.stringify(preferences));
    unsavedPreferences.delete(scopeKey);
    return true;
  } catch {
    unsavedPreferences.set(scopeKey, preferences);
    return false;
  }
}

export function receiveAppearancePreferences(
  scopeKey: string,
  serialized: string | null,
): AppearancePreferences {
  unsavedPreferences.delete(scopeKey);
  return parseAppearancePreferences(serialized);
}
