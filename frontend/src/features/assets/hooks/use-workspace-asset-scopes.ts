import { useMemo } from "react";
import { useListProjectsQuery } from "@/features/company-plan/api/project-api";

import { readSessionSnapshot } from "@/shared/session/session";

export type WorkspaceAssetScope = {
  label: string;
  scope: WorkspaceAssetScopeKey;
  type: "company" | "project";
};

/** Scope paths use immutable Project IDs. */
export type WorkspaceAssetScopeKey = "company" | `projects/${string}`;

export type WorkspaceAssetViewMode = "grid" | "list";

const defaultLabels: Record<string, string> = {
  company: "Company",
};

export function useWorkspaceAssetScopes() {
  const session = readSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const projects = useListProjectsQuery(companyId, { skip: !companyId });
  const scopes = useMemo<WorkspaceAssetScope[]>(
    () => [
      { label: defaultLabels.company, scope: "company", type: "company" },
      ...(projects.data?.items.map((p) => ({
        label: p.title,
        scope: `projects/${p.id}` as const,
        type: "project" as const,
      })) ?? []),
    ],
    [projects.data],
  );

  return { companyId, scopes, isLoading: projects.isLoading };
}

export function workspaceAssetScopeLabel(scope: string) {
  return defaultLabels[scope as WorkspaceAssetScopeKey] ?? scope;
}

export function formatWorkspaceAssetCount(count: number, truncated: boolean) {
  return truncated ? "20,000+" : count.toLocaleString();
}

export function formatWorkspaceAssetBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; value >= 1024 && index < units.length; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${unit}`;
}
