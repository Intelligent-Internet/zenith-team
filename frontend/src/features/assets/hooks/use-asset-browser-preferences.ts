import { useState } from "react";
import type { DesktopWorkspaceAssetSort } from "@/app/desktop/desktop-environment.service";
import type { WorkspaceAssetViewMode } from "./use-workspace-asset-scopes";

function useAssetPreference<T extends string>(
  key: string,
  values: readonly T[],
  fallback: T,
) {
  const [value, setValue] = useState<T>(() => {
    try {
      const stored = window.localStorage.getItem(key);
      return values.find((option) => option === stored) ?? fallback;
    } catch {
      return fallback;
    }
  });
  const update = (next: T) => {
    setValue(next);
    try {
      window.localStorage.setItem(key, next);
    } catch {
      /* Keep the preference for this view when storage is unavailable. */
    }
  };
  return [value, update] as const;
}

export function useAssetViewMode() {
  return useAssetPreference<WorkspaceAssetViewMode>(
    "zenith.assets.view",
    ["grid", "list"],
    "grid",
  );
}

export function useAssetSort() {
  return useAssetPreference<DesktopWorkspaceAssetSort>(
    "zenith.assets.sort",
    ["latest", "oldest", "name"],
    "latest",
  );
}
