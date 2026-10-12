import { normalizeWorkspaceFont, type WorkspaceFontId } from "./fonts";
import { contrastingText, hslToHex, mixColor } from "./color";

export type ThemePresetId =
  | "neutral"
  | "warm-paper"
  | "zenith-sky"
  | "warm-studio"
  | "plum-studio"
  | "navy-ivory"
  | "ivory-gold";

export const defaultThemePresetId: ThemePresetId = "neutral";

export type ThemeGradient = { start: string; end: string };
export type GradientStops = readonly [string, string, string];

export type ThemeColors = {
  navigation: string;
  accent: string;
  notifications: string;
};

export type AppearancePreferences = {
  version: 1;
  fontId: WorkspaceFontId;
  themeId: ThemePresetId | "custom";
  customTheme: {
    basePresetId: ThemePresetId;
    colors: ThemeColors;
    gradient?: ThemeGradient;
  } | null;
};

export const workspaceThemePresets: {
  id: ThemePresetId;
  name: string;
  colors: ThemeColors;
  gradient: GradientStops;
}[] = [
  {
    id: "neutral",
    name: "Neutral",
    colors: {
      navigation: "#F9F9F9",
      accent: "#171717",
      notifications: "#C93637",
    },
    gradient: ["#F9F9F9", "#F9F9F9", "#F9F9F9"],
  },
  {
    id: "warm-paper",
    name: "Warm Paper",
    colors: {
      navigation: "#E8E5DD",
      accent: "#3E554B",
      notifications: "#B6404D",
    },
    gradient: ["#E8E5DD", "#ECEBE5", "#DCDDD7"],
  },
  {
    // Keep the stored ID so existing Zenith Sky selections receive the refinement.
    id: "zenith-sky",
    name: "Zenith Mist",
    gradient: ["#183F5E", "#2B536A", "#496C7B"],
    colors: {
      navigation: "#183F5E",
      accent: "#244B61",
      notifications: "#B6404D",
    },
  },
  {
    id: "warm-studio",
    name: "Warm Studio",
    gradient: ["#383239", "#51454B", "#70615F"],
    colors: {
      navigation: "#383239",
      accent: "#383239",
      notifications: "#B6404D",
    },
  },
  {
    id: "plum-studio",
    name: "Plum Studio",
    gradient: ["#251C2D", "#493352", "#6B5075"],
    colors: {
      navigation: "#251C2D",
      accent: "#7556B9",
      notifications: "#B6404D",
    },
  },
  {
    id: "navy-ivory",
    name: "Navy & Ivory",
    gradient: ["#092859", "#3C62AF", "#607FE2"],
    colors: {
      navigation: "#092859",
      accent: "#092859",
      notifications: "#B6404D",
    },
  },
  {
    id: "ivory-gold",
    name: "Ivory Gold",
    gradient: ["#F2EEE2", "#EFE5CB", "#E1D1A8"],
    colors: {
      navigation: "#F2EEE2",
      accent: "#FFD84D",
      notifications: "#B6404D",
    },
  },
];

export function defaultAppearancePreferences(): AppearancePreferences {
  return {
    version: 1,
    fontId: "system",
    themeId: defaultThemePresetId,
    customTheme: null,
  };
}

export function getThemePreset(id: ThemePresetId) {
  return workspaceThemePresets.find((preset) => preset.id === id)!;
}

export function getThemeColors(
  preferences: AppearancePreferences,
): ThemeColors {
  if (preferences.themeId === "custom" && preferences.customTheme) {
    return preferences.customTheme.colors;
  }

  return getThemePreset(
    preferences.themeId === "custom"
      ? defaultThemePresetId
      : preferences.themeId,
  ).colors;
}

export function getCustomThemeGradient(
  preferences: AppearancePreferences,
): ThemeGradient {
  if (preferences.customTheme?.gradient)
    return preferences.customTheme.gradient;
  const colors = preferences.customTheme?.colors ?? getThemeColors(preferences);
  const preset = getThemePreset(
    preferences.customTheme?.basePresetId ??
      (preferences.themeId === "custom"
        ? defaultThemePresetId
        : preferences.themeId),
  );
  return {
    start: colors.navigation,
    end:
      colors.navigation === preset.colors.navigation
        ? preset.gradient[2]
        : mixColor(colors.navigation, contrastingText(colors.navigation), 0.14),
  };
}

export function normalizeHexColor(value: string): string | null {
  const hex = value.trim();
  if (/^#[\da-f]{3}$/i.test(hex)) {
    return `#${hex
      .slice(1)
      .split("")
      .map((digit) => digit + digit)
      .join("")}`.toUpperCase();
  }
  return /^#[\da-f]{6}$/i.test(hex) ? hex.toUpperCase() : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isThemePresetId(value: unknown): value is ThemePresetId {
  return workspaceThemePresets.some((preset) => preset.id === value);
}

function normalizeThemeColors(value: unknown): ThemeColors | null {
  if (!isRecord(value)) return null;
  const fields = ["navigation", "accent", "notifications"] as const;
  const colors = {} as ThemeColors;
  for (const field of fields) {
    const color = value[field];
    const normalized =
      typeof color === "string" ? normalizeHexColor(color) : null;
    if (!normalized) return null;
    colors[field] = normalized;
  }
  return colors;
}

export function normalizeAppearancePreferences(
  value: unknown,
): AppearancePreferences {
  if (
    !isRecord(value) ||
    value.version !== 1 ||
    (!isThemePresetId(value.themeId) && value.themeId !== "custom")
  ) {
    return defaultAppearancePreferences();
  }

  let customTheme: AppearancePreferences["customTheme"] = null;
  if (
    isRecord(value.customTheme) &&
    isThemePresetId(value.customTheme.basePresetId)
  ) {
    const colors = normalizeThemeColors(value.customTheme.colors);
    if (colors) {
      customTheme = { basePresetId: value.customTheme.basePresetId, colors };
      const gradient = value.customTheme.gradient;
      if (isRecord(gradient)) {
        const start =
          typeof gradient.start === "string"
            ? normalizeHexColor(gradient.start)
            : null;
        const end =
          typeof gradient.end === "string"
            ? normalizeHexColor(gradient.end)
            : null;
        if (start && end) customTheme.gradient = { start, end };
      }
    }
  }

  return {
    version: 1,
    fontId: normalizeWorkspaceFont(value.fontId),
    themeId:
      value.themeId === "custom" && !customTheme
        ? defaultThemePresetId
        : value.themeId,
    customTheme,
  };
}

export function customAppearancePreferences(
  preferences: AppearancePreferences,
): AppearancePreferences & {
  customTheme: NonNullable<AppearancePreferences["customTheme"]>;
} {
  return {
    ...preferences,
    themeId: "custom",
    customTheme: preferences.customTheme ?? {
      basePresetId:
        preferences.themeId === "custom"
          ? defaultThemePresetId
          : preferences.themeId,
      colors: { ...getThemeColors(preferences) },
    },
  };
}

export function randomAppearancePreferences(
  preferences: AppearancePreferences,
): AppearancePreferences {
  const next = customAppearancePreferences(preferences);
  const hue = Math.random() * 360;
  const saturation = 38 + Math.random() * 24;
  const pale = Math.random() < 0.3;
  const gradient = {
    start: hslToHex(hue, saturation, pale ? 93 : 22),
    end: hslToHex(hue + 24, saturation - 8, pale ? 83 : 40),
  };

  return {
    ...next,
    customTheme: {
      ...next.customTheme,
      colors: {
        navigation: gradient.start,
        accent: hslToHex(hue, saturation, 34),
        notifications: hslToHex(350 + Math.random() * 25, 62, 45),
      },
      gradient,
    },
  };
}
