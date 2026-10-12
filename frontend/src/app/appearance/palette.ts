import type { CSSProperties } from "react";

import {
  contrastRatio,
  contrastingText,
  mixColor,
  readableColor,
} from "./color";
import {
  getThemeColors,
  defaultThemePresetId,
  getCustomThemeGradient,
  getThemePreset,
  type AppearancePreferences,
  type ThemeColors,
  type GradientStops,
} from "./preferences";

export type WorkspacePalette = CSSProperties &
  Record<`--workspace-${string}`, string>;

const plumPalette: WorkspacePalette = {
  "--workspace-native-text": "#2D2631",
  "--workspace-native-text-muted": "#706875",
  "--workspace-native-text-faint": "#786E7D",
  "--workspace-native-success": "#286544",
  "--workspace-success-soft": "#E5F1E9",
  "--workspace-native-info": "#3561A0",
  "--workspace-info-soft": "#E9F0FF",
  "--workspace-native-approval": "#8B5A0A",
  "--workspace-approval-soft": "#FCF0D6",
  "--workspace-native-danger": "#B6404D",
  "--workspace-danger-soft": "#FBE9EC",
  "--workspace-danger-foreground": "#FFFFFF",
  "--workspace-artifact-bg": "#F3CEB8",
  "--workspace-artifact-text": "#5E332A",
  "--workspace-notification-bg": "#B6404D",
  "--workspace-notification-foreground": "#FFFFFF",
  "--workspace-color-scheme": "light",
  "--workspace-overlay-shadow": "0 16px 48px rgb(37 28 45 / 16%)",
};

const zenithPalette: WorkspacePalette = {
  ...plumPalette,
  "--workspace-native-text": "#303B40",
  "--workspace-native-text-muted": "#56676D",
  "--workspace-native-text-faint": "#617278",
  "--workspace-native-approval": "#785712",
  "--workspace-approval-soft": "#EFE7CD",
  "--workspace-overlay-shadow": "0 16px 48px rgb(24 63 94 / 14%)",
};

const warmPalette: WorkspacePalette = {
  ...plumPalette,
  "--workspace-native-text": "#302E32",
  "--workspace-native-text-muted": "#6F6963",
  "--workspace-native-text-faint": "#776C61",
  "--workspace-overlay-shadow": "0 16px 48px rgb(48 41 35 / 16%)",
};

const paperPalette: WorkspacePalette = {
  ...warmPalette,
  "--workspace-native-text": "#252824",
  "--workspace-native-text-muted": "#62685F",
  "--workspace-native-text-faint": "#6D736A",
  "--workspace-overlay-shadow":
    "0 8px 28px rgb(40 40 30 / 12%), 0 2px 6px rgb(40 40 30 / 4%)",
};

const neutralPalette: WorkspacePalette = {
  ...plumPalette,
  "--workspace-native-text": "#0D0D0D",
  "--workspace-native-text-muted": "#6E6E6E",
  "--workspace-native-text-faint": "#737373",
  "--workspace-overlay-shadow":
    "0 8px 32px rgb(0 0 0 / 10%), 0 2px 8px rgb(0 0 0 / 4%)",
};

const darkPalette: WorkspacePalette = {
  "--workspace-app-bg": "#282828",
  "--workspace-titlebar-bg": "#282828",
  "--workspace-rail-bg": "#282828",
  "--workspace-shell-gradient": "none",
  "--workspace-gradient-start": "#282828",
  "--workspace-gradient-middle": "#282828",
  "--workspace-gradient-end": "#282828",
  "--workspace-sidebar-bg": "#1E1E1E",
  "--workspace-content-bg": "#181818",
  "--workspace-chat-bg": "#181818",
  "--workspace-panel-bg": "#181818",
  "--workspace-panel-raised-bg": "#242424",
  "--workspace-thread-bg": "#181818",
  "--workspace-input-bg": "#363636",
  "--workspace-overlay-bg": "#242424",
  "--workspace-control-bg": "#262626",
  "--workspace-hover-bg": "#2A2A2A",
  "--workspace-selected-bg": "#303030",
  "--workspace-accent-soft": "#262626",
  "--workspace-divider": "#333333",
  "--workspace-divider-strong": "#424242",
  "--workspace-native-text": "#ECECEC",
  "--workspace-native-text-muted": "#A3A3A3",
  "--workspace-native-text-faint": "#949494",
  "--workspace-native-accent": "#ECECEC",
  "--workspace-accent-fill": "#FFFFFF",
  "--workspace-accent-hover": "#E3E3E3",
  "--workspace-accent-active": "#CCCCCC",
  "--workspace-accent-foreground": "#171717",
  "--workspace-native-success": "#74BE95",
  "--workspace-success-soft": "#233729",
  "--workspace-native-info": "#8CB9F4",
  "--workspace-info-soft": "#1B333E",
  "--workspace-native-approval": "#D9B87B",
  "--workspace-approval-soft": "#302A20",
  "--workspace-native-danger": "#F47B82",
  "--workspace-danger-soft": "#3C2933",
  "--workspace-danger-foreground": "#17191C",
  "--workspace-artifact-bg": "#262626",
  "--workspace-artifact-text": "#ECECEC",
  "--workspace-nav-bg": "#1E1E1E",
  "--workspace-nav-control": "#262626",
  "--workspace-nav-hover": "#2A2A2A",
  "--workspace-nav-selected": "#303030",
  "--workspace-nav-text": "#ECECEC",
  "--workspace-nav-muted": "#A3A3A3",
  "--workspace-nav-divider": "#2B2B2B",
  "--workspace-nav-accent": "#ECECEC",
  "--workspace-nav-success": "#74BE95",
  "--workspace-nav-danger": "#F47B82",
  "--workspace-notification-bg": "#B6404D",
  "--workspace-notification-foreground": "#FFFFFF",
  "--workspace-color-scheme": "dark",
  "--workspace-nav-color-scheme": "dark",
  "--workspace-overlay-shadow": "0 20px 48px rgb(0 0 0 / 34%)",
  "--workspace-shell-shadow":
    "0 0 0 1px rgb(255 255 255 / 4%), 0 8px 24px rgb(0 0 0 / 28%)",
  "--workspace-composer-shadow": "none",
};

function gradientSamples(stops: GradientStops): string[] {
  return [0, 1].flatMap((segment) =>
    Array.from({ length: 21 }, (_, step) =>
      mixColor(stops[segment], stops[segment + 1], step / 20),
    ),
  );
}

function readableGradient(stops: GradientStops, text: string): GradientStops {
  const minimumContrast = 4.6;
  const balanced: GradientStops = [
    readableColor(stops[0], [text], minimumContrast),
    readableColor(stops[1], [text], minimumContrast),
    readableColor(stops[2], [text], minimumContrast),
  ];
  const legible = (gradient: GradientStops) =>
    gradientSamples(gradient).every(
      (color) => contrastRatio(text, color) >= minimumContrast,
    );
  if (legible(balanced)) return balanced;

  // Opposing hues can have a darker midpoint than either endpoint. Balance
  // the whole interpolation, not only the three authored stops.
  const target = text === "#FFFFFF" ? "#000000" : "#FFFFFF";
  const blend = (amount: number): GradientStops => [
    mixColor(balanced[0], target, amount),
    mixColor(balanced[1], target, amount),
    mixColor(balanced[2], target, amount),
  ];
  let lower = 0;
  let upper = 1;
  for (let step = 0; step < 16; step += 1) {
    const amount = (lower + upper) / 2;
    if (legible(blend(amount))) upper = amount;
    else lower = amount;
  }
  return blend(upper);
}

function navigationPalette(
  colors: ThemeColors,
  stops: GradientStops,
): WorkspacePalette {
  // A custom gradient can span black to white. Keep the chosen hue while
  // balancing its stops so one foreground remains legible across the shell.
  const text = contrastingText(stops[0]);
  const gradient = readableGradient(stops, text);
  const surface = (amount: number) =>
    readableColor(mixColor(gradient[1], text, amount), [text], 5);
  const backgrounds = [
    ...gradientSamples(gradient),
    surface(0.06),
    surface(0.12),
    surface(0.18),
  ];
  return {
    "--workspace-app-bg": gradient[0],
    "--workspace-titlebar-bg": gradient[0],
    "--workspace-rail-bg": gradient[0],
    "--workspace-gradient-start": gradient[0],
    "--workspace-gradient-middle": gradient[1],
    "--workspace-gradient-end": gradient[2],
    // Scale the ellipse to both axes so the exposed rail also reaches the
    // brighter stop; a diagonal gradient leaves most of it behind the panes.
    "--workspace-shell-gradient": `radial-gradient(ellipse 130% 110% at 0% 0%, ${gradient[0]} 0%, ${gradient[1]} 45%, ${gradient[2]} 90%)`,
    "--workspace-nav-bg": surface(0.06),
    "--workspace-nav-control": surface(0.12),
    "--workspace-nav-hover": surface(0.12),
    "--workspace-nav-selected": text === "#FFFFFF" ? surface(0.18) : "#FFFEFC",
    "--workspace-nav-text": readableColor(text, backgrounds),
    "--workspace-nav-muted": readableColor(
      mixColor(text, gradient[1], 0.3),
      backgrounds,
      4.55,
    ),
    "--workspace-nav-divider": surface(0.22),
    "--workspace-nav-accent": readableColor(colors.accent, backgrounds),
    "--workspace-nav-success": readableColor("#286544", backgrounds),
    "--workspace-nav-danger": readableColor("#B6404D", backgrounds),
    "--workspace-nav-color-scheme": text === "#FFFFFF" ? "dark" : "light",
  };
}

function surfacePalette(
  colors: ThemeColors,
  palette: WorkspacePalette,
): WorkspacePalette {
  // Light screens share white reading surfaces and neutral controls.
  const background = "#FFFFFF";
  const text = palette["--workspace-native-text"];
  const control = "#F7F7F7";
  const accent = readableColor(colors.accent, [background, control]);
  const selected = mixColor(background, accent, 0.08);
  return {
    "--workspace-sidebar-bg": background,
    "--workspace-content-bg": background,
    "--workspace-panel-bg": background,
    "--workspace-chat-bg": background,
    "--workspace-panel-raised-bg": background,
    "--workspace-thread-bg": background,
    "--workspace-input-bg": background,
    "--workspace-overlay-bg": background,
    "--workspace-control-bg": control,
    "--workspace-hover-bg": mixColor(background, accent, 0.04),
    "--workspace-selected-bg": selected,
    "--workspace-accent-soft": selected,
    "--workspace-divider": mixColor(background, text, 0.08),
    "--workspace-divider-strong": mixColor(background, text, 0.16),
  };
}

function contentBackgrounds(palette: WorkspacePalette): string[] {
  return [
    "sidebar-bg",
    "content-bg",
    "chat-bg",
    "panel-bg",
    "panel-raised-bg",
    "input-bg",
    "overlay-bg",
    "control-bg",
    "hover-bg",
    "selected-bg",
  ].map((name) => palette[`--workspace-${name}`]);
}

function accentPalette(
  colors: ThemeColors,
  palette: WorkspacePalette,
): WorkspacePalette {
  const foreground = contrastingText(colors.accent);
  const hoverTarget = foreground === "#FFFFFF" ? "#000000" : "#FFFFFF";
  return {
    "--workspace-accent-fill": colors.accent,
    "--workspace-accent-foreground": foreground,
    "--workspace-accent-hover": readableColor(
      mixColor(colors.accent, hoverTarget, 0.14),
      [foreground],
    ),
    "--workspace-accent-active": readableColor(
      mixColor(colors.accent, hoverTarget, 0.24),
      [foreground],
    ),
    "--workspace-native-accent": readableColor(
      colors.accent,
      contentBackgrounds(palette),
    ),
  };
}

function statusPalette(palette: WorkspacePalette): WorkspacePalette {
  const backgrounds = contentBackgrounds(palette);
  const source =
    palette["--workspace-color-scheme"] === "dark" ? darkPalette : plumPalette;
  const result: WorkspacePalette = {};
  for (const status of ["success", "info", "approval", "danger"] as const) {
    const color = readableColor(
      source[`--workspace-native-${status}`],
      backgrounds,
    );
    const soft = readableColor(
      mixColor(palette["--workspace-content-bg"], color, 0.1),
      [palette["--workspace-native-text"]],
    );
    result[`--workspace-${status}-soft`] = soft;
    result[`--workspace-native-${status}`] = readableColor(color, [
      ...backgrounds,
      soft,
    ]);
  }
  result["--workspace-danger-foreground"] = contrastingText(
    result["--workspace-native-danger"],
  );
  return result;
}

function contrastPalette(palette: WorkspacePalette): WorkspacePalette {
  const backgrounds = contentBackgrounds(palette);
  return {
    "--workspace-notification-indicator": readableColor(
      palette["--workspace-notification-bg"],
      backgrounds,
      3,
    ),
    "--workspace-nav-notification-indicator": readableColor(
      palette["--workspace-notification-bg"],
      [
        ...gradientSamples([
          palette["--workspace-gradient-start"],
          palette["--workspace-gradient-middle"],
          palette["--workspace-gradient-end"],
        ]),
        ...["nav-bg", "nav-control", "nav-selected"].map(
          (name) => palette[`--workspace-${name}`],
        ),
      ],
      3,
    ),
    "--workspace-contrast-divider": readableColor(
      palette["--workspace-divider"],
      backgrounds,
      3,
    ),
    "--workspace-contrast-divider-strong": readableColor(
      palette["--workspace-divider-strong"],
      backgrounds,
      3,
    ),
    "--workspace-contrast-text-muted": readableColor(
      palette["--workspace-native-text-muted"],
      backgrounds,
      7,
    ),
    "--workspace-contrast-text-faint": readableColor(
      palette["--workspace-native-text-faint"],
      backgrounds,
      7,
    ),
  };
}

function readablePalette(palette: WorkspacePalette): WorkspacePalette {
  const result = { ...palette };
  const backgrounds = contentBackgrounds(result);
  for (const name of [
    "native-text",
    "native-text-muted",
    "native-text-faint",
    "native-accent",
  ] as const) {
    const token = `--workspace-${name}` as const;
    result[token] = readableColor(result[token], backgrounds);
  }
  return { ...result, ...contrastPalette(result) };
}

export function resolveWorkspacePalette(
  preferences: AppearancePreferences,
  mode: "light" | "dark",
): WorkspacePalette {
  if (mode === "dark") return readablePalette(darkPalette);

  const colors = getThemeColors(preferences);
  const preset = getThemePreset(
    preferences.themeId === "custom"
      ? (preferences.customTheme?.basePresetId ?? defaultThemePresetId)
      : preferences.themeId,
  );
  const custom = preferences.themeId === "custom";
  let stops: GradientStops = preset.gradient;
  if (
    custom &&
    (preferences.customTheme?.gradient ||
      colors.navigation !== preset.colors.navigation)
  ) {
    const { start, end } = getCustomThemeGradient(preferences);
    stops = [start, mixColor(start, end, 0.5), end];
  }
  const bases: Partial<Record<typeof preset.id, WorkspacePalette>> = {
    neutral: neutralPalette,
    "warm-paper": paperPalette,
    "zenith-sky": zenithPalette,
    "warm-studio": warmPalette,
    "plum-studio": plumPalette,
  };
  const palette: WorkspacePalette = {
    ...(bases[preset.id] ?? plumPalette),
  };
  Object.assign(palette, surfacePalette(colors, palette));
  Object.assign(palette, accentPalette(colors, palette));
  palette["--workspace-color-scheme"] = mode;
  Object.assign(palette, navigationPalette(colors, stops));
  Object.assign(palette, statusPalette(palette));
  palette["--workspace-shell-shadow"] =
    "0 2px 6px rgb(15 23 42 / 6%), 0 12px 32px rgb(15 23 42 / 10%)";
  palette["--workspace-composer-shadow"] = "0 2px 8px rgb(0 0 0 / 4%)";
  if (preferences.themeId === "neutral") {
    Object.assign(palette, {
      "--workspace-app-bg": "#F0F0F0",
      "--workspace-titlebar-bg": "#F0F0F0",
      "--workspace-rail-bg": "#F0F0F0",
      "--workspace-shell-gradient": "none",
      "--workspace-sidebar-bg": "#F9F9F9",
      "--workspace-control-bg": "#F3F3F3",
      "--workspace-hover-bg": "#F0F0F0",
      "--workspace-selected-bg": "#EAEAEA",
      "--workspace-accent-soft": "#F3F3F3",
      "--workspace-divider": "#E2E2E2",
      "--workspace-divider-strong": "#D9D9D9",
      "--workspace-nav-bg": "#F9F9F9",
      "--workspace-nav-control": "#F3F3F3",
      "--workspace-nav-hover": "#F0F0F0",
      "--workspace-nav-selected": "#EAEAEA",
      "--workspace-nav-text": "#0D0D0D",
      "--workspace-nav-muted": "#6E6E6E",
      "--workspace-nav-divider": "#EBEBEB",
      "--workspace-artifact-bg": "#F3F3F3",
      "--workspace-artifact-text": "#0D0D0D",
    });
  }
  palette["--workspace-notification-bg"] = colors.notifications;
  palette["--workspace-notification-foreground"] = contrastingText(
    colors.notifications,
  );
  return readablePalette(palette);
}
