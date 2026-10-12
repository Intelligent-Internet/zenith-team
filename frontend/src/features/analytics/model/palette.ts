import { useMemo, type CSSProperties } from "react";
import { workspaceFontFamily } from "@/app/appearance/fonts";
import {
  contrastingText,
  mixColor,
  readableColor,
} from "@/app/appearance/color";
import { useWorkspaceAppearance } from "@/app/appearance/workspace-appearance-provider";

// Canvas charts need resolved colors; CSS variables alone cannot repaint them.
// Subscribe to the same palette that drives preset, custom and color-mode changes.
export function useAnalyticsPalette() {
  const { palette, preferences, colorMode } = useWorkspaceAppearance();
  return useMemo(() => {
    const canvas = palette["--workspace-panel-bg"];
    const ink = palette["--workspace-native-text"];
    const chartColor = (color: string) => readableColor(color, [canvas], 3);
    // Appearance edits the shell gradient independently of the action accent.
    // Custom charts use both stops and the accent; dark mode follows the app's
    // fixed dark palette, just like the rest of the workspace.
    const custom = preferences.themeId === "custom" && colorMode === "light";
    const action = palette["--workspace-native-accent"];
    const start = custom ? palette["--workspace-gradient-start"] : action;
    const end = custom ? palette["--workspace-gradient-end"] : action;
    const accent = custom
      ? readableColor(mixColor(mixColor(start, end, 0.4), action, 0.18), [
          canvas,
        ])
      : action;
    const fill = custom ? accent : palette["--workspace-accent-fill"];
    const accentBright = chartColor(
      custom ? mixColor(end, action, 0.18) : mixColor(fill, canvas, 0.25),
    );
    const accentSoft = chartColor(mixColor(accent, canvas, 0.5));
    const success = palette["--workspace-native-success"];
    const warning = palette["--workspace-native-approval"];
    const danger = palette["--workspace-native-danger"];
    const alternate = chartColor(
      mixColor(accent, palette["--workspace-native-info"], 0.6),
    );
    return {
      fontFamily: workspaceFontFamily(preferences.fontId),
      ink,
      accent,
      accentBright,
      accentSoft,
      success,
      warning,
      danger,
      alternate,
      canvas,
      muted: palette["--workspace-native-text-muted"],
      grid: palette["--workspace-divider"],
      accentForeground: contrastingText(fill),
      fill,
      fillSoft: mixColor(canvas, accent, 0.08),
      fillStrong: mixColor(canvas, accent, 0.2),
      areaTop: mixColor(canvas, accent, 0.35),
      areaBottom: mixColor(canvas, accent, 0.02),
      dangerSoft: palette["--workspace-danger-soft"],
      shadow: `${ink}33`,
      heatmap: [
        mixColor(canvas, accent, 0.04),
        mixColor(canvas, accent, 0.32),
        mixColor(canvas, accent, 0.65),
        accent,
      ],
      series: custom
        ? [
            accent,
            accentBright,
            chartColor(mixColor(start, end, 0.15)),
            chartColor(mixColor(end, canvas, 0.25)),
            chartColor(mixColor(start, end, 0.75)),
            chartColor(mixColor(action, end, 0.45)),
            chartColor(mixColor(start, canvas, 0.4)),
            chartColor(mixColor(end, ink, 0.3)),
          ]
        : [
            accent,
            accentBright,
            chartColor(mixColor(accent, success, 0.65)),
            alternate,
            chartColor(mixColor(accent, warning, 0.65)),
            ink,
            accentSoft,
            chartColor(mixColor(accent, danger, 0.65)),
          ],
      cssVariables: {
        "--analytics-accent": accent,
        "--analytics-accent-soft": mixColor(canvas, accent, 0.08),
      } as CSSProperties,
      tooltip: {
        backgroundColor: palette["--workspace-overlay-bg"],
        borderColor: palette["--workspace-divider-strong"],
        borderWidth: 1,
        textStyle: { color: ink, fontSize: 12 },
      },
    };
  }, [palette, preferences.fontId, preferences.themeId, colorMode]);
}

export type AnalyticsPalette = ReturnType<typeof useAnalyticsPalette>;
