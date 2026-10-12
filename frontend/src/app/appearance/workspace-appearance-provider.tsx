import "../fonts/workspace-fonts.css";
import { workspaceFontFamily, type WorkspaceFontId } from "./fonts";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTheme } from "next-themes";

import {
  clearActiveAppearance,
  selectAppearance,
  setActiveAppearance,
} from "@/state/slice/appearance";
import { useAppDispatch, useAppSelector } from "@/state/store";

import { resolveWorkspacePalette, type WorkspacePalette } from "./palette";
import {
  customAppearancePreferences,
  defaultAppearancePreferences,
  getCustomThemeGradient,
  normalizeHexColor,
  randomAppearancePreferences,
  type AppearancePreferences,
  type ThemeColors,
  type ThemeGradient,
  type ThemePresetId,
} from "./preferences";
import {
  appearanceStorageKey,
  lastAppearanceStorageKey,
  readAppearancePreferences,
  receiveAppearancePreferences,
  writeAppearancePreferences,
} from "./storage";

type WorkspaceAppearance = {
  preferences: AppearancePreferences;
  palette: WorkspacePalette;
  selectFont: (id: WorkspaceFontId) => void;
  selectPreset: (id: ThemePresetId) => void;
  selectCustomTheme: () => void;
  randomizeTheme: () => void;
  updateCustomColor: (field: keyof ThemeColors, value: string) => void;
  updateCustomGradient: (field: keyof ThemeGradient, value: string) => void;
  resetTheme: () => void;
  storageError: boolean;
  scopeKey: string | null;
  colorMode: "light" | "dark";
};

const WorkspaceAppearanceContext = createContext<WorkspaceAppearance | null>(
  null,
);

export function useDocumentPalette(
  palette: WorkspacePalette,
  themeId: string,
  colorMode: "light" | "dark",
) {
  const paletteRef = useRef(palette);

  useLayoutEffect(() => {
    const root = document.documentElement;
    const attributes = [
      "data-boardly-workspace-theme",
      "data-boardly-color-mode",
    ];
    const originalAttributes = attributes.map(
      (name) => [name, root.getAttribute(name)] as const,
    );
    const originalTokens = Object.keys(paletteRef.current).map((name) => ({
      name,
      value: root.style.getPropertyValue(name),
      priority: root.style.getPropertyPriority(name),
    }));

    return () => {
      for (const [name, value] of originalAttributes) {
        if (value === null) root.removeAttribute(name);
        else root.setAttribute(name, value);
      }
      for (const { name, value, priority } of originalTokens) {
        if (value) root.style.setProperty(name, value, priority);
        else root.style.removeProperty(name);
      }
    };
  }, []);

  useLayoutEffect(() => {
    const root = document.documentElement;
    root.setAttribute("data-boardly-workspace-theme", themeId);
    root.setAttribute("data-boardly-color-mode", colorMode);
    for (const [name, value] of Object.entries(palette)) {
      root.style.setProperty(name, value);
    }
  }, [palette, themeId, colorMode]);
}

function ScopedWorkspaceAppearance({
  children,
  scopeKey,
}: {
  children: ReactNode;
  scopeKey: string | null;
}) {
  const dispatch = useAppDispatch();
  const activeAppearance = useAppSelector(selectAppearance);
  const [loadedAppearance] = useState(() =>
    readAppearancePreferences(scopeKey),
  );
  const { resolvedTheme, forcedTheme } = useTheme();
  const theme = forcedTheme ?? resolvedTheme;
  const colorMode =
    theme === "light" || theme === "dark"
      ? theme
      : window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  const { preferences, storageError } =
    activeAppearance.scopeKey === scopeKey
      ? activeAppearance
      : loadedAppearance;
  const preferencesRef = useRef(preferences);
  preferencesRef.current = preferences;

  useLayoutEffect(() => {
    dispatch(setActiveAppearance({ scopeKey, ...loadedAppearance }));
    return () => {
      dispatch(clearActiveAppearance(scopeKey));
    };
  }, [dispatch, loadedAppearance, scopeKey]);

  const palette = useMemo(
    () => resolveWorkspacePalette(preferences, colorMode),
    [preferences, colorMode],
  );
  useDocumentPalette(palette, preferences.themeId, colorMode);

  useLayoutEffect(() => {
    if (scopeKey && scopeKey !== lastAppearanceStorageKey) {
      writeAppearancePreferences(lastAppearanceStorageKey, preferences);
    }
  }, [preferences, scopeKey]);

  useLayoutEffect(() => {
    const root = document.documentElement;
    const original = root.style.getPropertyValue("--font-ui");
    const priority = root.style.getPropertyPriority("--font-ui");
    root.style.setProperty(
      "--font-ui",
      workspaceFontFamily(preferences.fontId),
    );
    return () => {
      if (original) root.style.setProperty("--font-ui", original, priority);
      else root.style.removeProperty("--font-ui");
    };
  }, [preferences.fontId]);

  useEffect(() => {
    if (!scopeKey) return;
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== scopeKey && event.key !== null) return;
      // Ignore similarly named sessionStorage entries from other frames.
      try {
        if (event.storageArea && event.storageArea !== window.localStorage)
          return;
      } catch {
        return;
      }
      const nextPreferences = receiveAppearancePreferences(
        scopeKey,
        event.newValue,
      );
      preferencesRef.current = nextPreferences;
      dispatch(
        setActiveAppearance({
          scopeKey,
          preferences: nextPreferences,
          storageError: false,
        }),
      );
    };
    window.addEventListener("storage", handleStorage);
    return () => window.removeEventListener("storage", handleStorage);
  }, [dispatch, scopeKey]);

  const updatePreferences = useCallback(
    (nextPreferences: AppearancePreferences) => {
      preferencesRef.current = nextPreferences;
      const saved = writeAppearancePreferences(scopeKey, nextPreferences);
      dispatch(
        setActiveAppearance({
          scopeKey,
          preferences: nextPreferences,
          storageError: !saved,
        }),
      );
    },
    [dispatch, scopeKey],
  );

  const selectFont = useCallback(
    (fontId: WorkspaceFontId) => {
      updatePreferences({ ...preferencesRef.current, fontId });
    },
    [updatePreferences],
  );

  const selectPreset = useCallback(
    (themeId: ThemePresetId) => {
      updatePreferences({ ...preferencesRef.current, themeId });
    },
    [updatePreferences],
  );

  const selectCustomTheme = useCallback(() => {
    updatePreferences(customAppearancePreferences(preferencesRef.current));
  }, [updatePreferences]);

  const randomizeTheme = useCallback(() => {
    updatePreferences(randomAppearancePreferences(preferencesRef.current));
  }, [updatePreferences]);

  const updateCustomColor = useCallback(
    (field: keyof ThemeColors, value: string) => {
      const color = normalizeHexColor(value);
      if (!color) return;
      const nextPreferences = customAppearancePreferences(
        preferencesRef.current,
      );
      updatePreferences({
        ...nextPreferences,
        customTheme: {
          ...nextPreferences.customTheme,
          colors: { ...nextPreferences.customTheme.colors, [field]: color },
        },
      });
    },
    [updatePreferences],
  );

  const resetTheme = useCallback(() => {
    updatePreferences({
      ...defaultAppearancePreferences(),
      fontId: preferencesRef.current.fontId,
    });
  }, [updatePreferences]);

  const updateCustomGradient = useCallback(
    (field: keyof ThemeGradient, value: string) => {
      const color = normalizeHexColor(value);
      if (!color) return;
      const next = customAppearancePreferences(preferencesRef.current);
      const gradient = { ...getCustomThemeGradient(next), [field]: color };
      updatePreferences({
        ...next,
        customTheme: {
          ...next.customTheme,
          colors: { ...next.customTheme.colors, navigation: gradient.start },
          gradient,
        },
      });
    },
    [updatePreferences],
  );

  const context = useMemo<WorkspaceAppearance>(
    () => ({
      preferences,
      palette,
      selectFont,
      selectPreset,
      selectCustomTheme,
      randomizeTheme,
      updateCustomColor,
      updateCustomGradient,
      resetTheme,
      storageError,
      scopeKey,
      colorMode,
    }),
    [
      preferences,
      palette,
      selectFont,
      selectPreset,
      selectCustomTheme,
      randomizeTheme,
      updateCustomColor,
      updateCustomGradient,
      resetTheme,
      storageError,
      scopeKey,
      colorMode,
    ],
  );

  return (
    <WorkspaceAppearanceContext.Provider value={context}>
      {children}
    </WorkspaceAppearanceContext.Provider>
  );
}

export function WorkspaceAppearanceProvider({
  children,
  userId,
  companyId,
  restoreLastAppearance = false,
}: {
  children: ReactNode;
  userId?: string | null;
  companyId?: string | null;
  restoreLastAppearance?: boolean;
}) {
  const scopeKey =
    appearanceStorageKey(userId, companyId) ??
    (restoreLastAppearance ? lastAppearanceStorageKey : null);
  return (
    <ScopedWorkspaceAppearance
      key={scopeKey ?? "no-company"}
      scopeKey={scopeKey}
    >
      {children}
    </ScopedWorkspaceAppearance>
  );
}

export function useWorkspaceAppearance(): WorkspaceAppearance {
  const appearance = useContext(WorkspaceAppearanceContext);
  if (!appearance)
    throw new Error(
      "Workspace appearance requires WorkspaceAppearanceProvider.",
    );
  return appearance;
}
