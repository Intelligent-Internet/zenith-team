import { useMemo, type CSSProperties } from "react";
import {
  contrastingText,
  mixColor,
  readableColor,
} from "@/app/appearance/color";
import { useWorkspaceAppearance } from "@/app/appearance/workspace-appearance-provider";

// Apply the same accent to the calendar and its portalled dialogs and pickers.
// Custom gradients use their final stop; presets and dark mode keep their accent.
export function useCalendarTheme(): CSSProperties {
  const { palette, preferences, colorMode } = useWorkspaceAppearance();
  return useMemo(() => {
    const custom = preferences.themeId === "custom" && colorMode === "light";
    const fill = custom
      ? palette["--workspace-gradient-end"]
      : palette["--workspace-accent-fill"];
    const soft = custom
      ? mixColor(palette["--workspace-panel-bg"], fill, 0.08)
      : palette["--workspace-accent-soft"];
    const accent = custom
      ? readableColor(fill, [
          palette["--workspace-panel-bg"],
          palette["--workspace-overlay-bg"],
          palette["--workspace-input-bg"],
          soft,
        ])
      : palette["--workspace-native-accent"];
    const foreground = contrastingText(fill);
    return {
      "--workspace-native-accent": accent,
      "--workspace-accent-fill": fill,
      "--workspace-accent-foreground": foreground,
      "--workspace-accent-hover": custom
        ? readableColor(
            mixColor(
              fill,
              foreground === "#FFFFFF" ? "#000000" : "#FFFFFF",
              0.14,
            ),
            [foreground],
          )
        : palette["--workspace-accent-hover"],
      "--workspace-accent-soft": soft,
      "--personal-accent": accent,
      "--ring": accent,
      "--calendar-pane-shadow":
        colorMode === "dark"
          ? "0 0 4px rgb(0 0 0 / 22.4%), 0 0 24px rgb(0 0 0 / 25.6%)"
          : "0 0 4px rgb(25 30 36 / 8%), 0 0 24px rgb(25 30 36 / 9.6%)",
      "--calendar-divider-shadow":
        colorMode === "dark"
          ? "0 1px 3px rgb(0 0 0 / 24%), 0 4px 8px -2px rgb(0 0 0 / 20.8%)"
          : "0 1px 3px rgb(25 30 36 / 8%), 0 4px 8px -2px rgb(25 30 36 / 9.6%)",
      "--calendar-control-shadow":
        colorMode === "dark"
          ? "0 1px 3px rgb(0 0 0 / 15%), 0 4px 8px -2px rgb(0 0 0 / 13%)"
          : "0 1px 3px rgb(25 30 36 / 5%), 0 4px 8px -2px rgb(25 30 36 / 6%)",
      "--calendar-surface-shadow":
        colorMode === "dark"
          ? "0 2px 4px rgb(0 0 0 / 14%), 0 6px 14px -3px rgb(0 0 0 / 12%)"
          : "0 2px 4px rgb(25 30 36 / 5%), 0 6px 14px -3px rgb(25 30 36 / 6%)",
      "--calendar-hover-shadow":
        colorMode === "dark"
          ? "0 4px 8px rgb(0 0 0 / 16%), 0 10px 22px -4px rgb(0 0 0 / 15%)"
          : "0 4px 8px rgb(25 30 36 / 6%), 0 10px 22px -4px rgb(25 30 36 / 9%)",
      "--calendar-popover-shadow":
        colorMode === "dark"
          ? "0 24px 64px rgb(0 0 0 / 41.6%), 0 8px 20px rgb(0 0 0 / 28.8%), 0 1px 4px rgb(0 0 0 / 22.4%)"
          : "0 24px 64px rgb(25 30 36 / 17.6%), 0 8px 20px rgb(25 30 36 / 9.6%), 0 1px 4px rgb(25 30 36 / 8%)",
    } as CSSProperties;
  }, [palette, preferences.themeId, colorMode]);
}
