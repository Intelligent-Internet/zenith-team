import { useMemo, useState } from "react";
import { Link } from "react-router";

import { canBrowseDesktopWorkspaceAssets } from "@/shared/desktop/desktop-environment.service";
import {
  useListWorkspaceAssetsQuery,
  workspaceAssetsApi,
} from "@/features/assets/api/workspace-assets-api";
import { AssetsWorkspaceShell } from "@/features/assets/components/assets-workspace-shell";
import { useAssetViewMode } from "@/features/assets/hooks/use-asset-browser-preferences";
import {
  formatWorkspaceAssetCount,
  type WorkspaceAssetScope,
  useWorkspaceAssetScopes,
} from "@/features/assets/hooks/use-workspace-asset-scopes";
import { ViewSwitch } from "@/shared/ui/collection-controls/controls";
import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";
import { useAppDispatch } from "@/state";

import "../styles/assets-responsive.css";

export function AssetsPage() {
  const dispatch = useAppDispatch();
  const desktopAvailable = canBrowseDesktopWorkspaceAssets();
  const { companyId, scopes } = useWorkspaceAssetScopes();
  const [query, setQuery] = useState("");
  const [viewMode, setViewMode] = useAssetViewMode();

  const refresh = () => {
    dispatch(
      workspaceAssetsApi.util.invalidateTags([
        { id: companyId, type: "WorkspaceAsset" },
      ]),
    );
  };

  const visibleScopes = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return scopes.filter(
      (scope) =>
        !normalizedQuery || scope.label.toLowerCase().includes(normalizedQuery),
    );
  }, [query, scopes]);

  return (
    <AssetsWorkspaceShell>
      <div className="assets-page-layout">
        <div
          className="assets-page-controls assets-overview-controls"
          aria-label="Workspace Assets controls"
          role="group"
        >
          <label className="assets-page-search">
            <span className="sr-only">Search asset categories</span>
            <Icon
              name="search"
              className="pointer-events-none absolute left-4 top-3 z-10 size-6 text-white"
            />
            <input
              placeholder="Search asset categories…"
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>

          <div className="assets-page-control-actions">
            <button
              className="collection-toolbar-control"
              disabled={!desktopAvailable}
              type="button"
              onClick={refresh}
            >
              <Icon name="refresh" className="size-5" />
              Refresh
            </button>
            <ViewSwitch value={viewMode} onChange={setViewMode} />
          </div>
        </div>

        <div className="assets-page-content">
          {!desktopAvailable ? (
            <WorkspaceAssetsState
              icon="open-external"
              title="Open in Zenith Team Desktop"
              description="Workspace Assets reads files from the local daemon and is not available in the hosted web app."
            />
          ) : visibleScopes.length ? (
            viewMode === "grid" ? (
              <AssetsGrid companyId={companyId} scopes={visibleScopes} />
            ) : (
              <AssetsList companyId={companyId} scopes={visibleScopes} />
            )
          ) : (
            <WorkspaceAssetsState
              description="Try another category name."
              icon="folder"
              title="No matching categories"
            />
          )}
        </div>
      </div>
    </AssetsWorkspaceShell>
  );
}

export const Component = AssetsPage;

function AssetsGrid({
  companyId,
  scopes,
}: {
  companyId: string;
  scopes: WorkspaceAssetScope[];
}) {
  return (
    <div className="assets-page-scroll">
      <div className="assets-overview-grid">
        {scopes.map((scope) => (
          <AssetGridCard
            companyId={companyId}
            key={scope.scope}
            scope={scope}
          />
        ))}
      </div>
    </div>
  );
}

function AssetGridCard({
  companyId,
  scope,
}: {
  companyId: string;
  scope: WorkspaceAssetScope;
}) {
  const query = useWorkspaceAssetSummary(companyId, scope.scope);
  const value = query.currentData;
  return (
    <Link
      aria-label={`${scope.label}, ${value?.totalCount ?? 0} assets`}
      className="assets-overview-grid-card group"
      to={`/assets/${encodeURIComponent(scope.scope)}`}
    >
      <WorkspaceCategoryArtwork />
      <h2 className="assets-overview-grid-title">{scope.label}</h2>
      <span className="assets-overview-grid-count">
        {query.isLoading
          ? "…"
          : query.isError
            ? "Unavailable"
            : formatWorkspaceAssetCount(
                value?.totalCount ?? 0,
                value?.truncated ?? false,
              )}
      </span>
    </Link>
  );
}

function AssetsList({
  companyId,
  scopes,
}: {
  companyId: string;
  scopes: WorkspaceAssetScope[];
}) {
  return (
    <div className="assets-page-scroll">
      <div className="assets-overview-list collection-list">
        <div
          className="assets-overview-list-heading collection-list__header"
          aria-hidden="true"
        >
          <span>Workspace</span>
          <span>Files</span>
        </div>
        {scopes.map((scope) => (
          <AssetListCard
            companyId={companyId}
            key={scope.scope}
            scope={scope}
          />
        ))}
      </div>
    </div>
  );
}

function AssetListCard({
  companyId,
  scope,
}: {
  companyId: string;
  scope: WorkspaceAssetScope;
}) {
  const query = useWorkspaceAssetSummary(companyId, scope.scope);
  const value = query.currentData;
  return (
    <Link
      className="assets-overview-list-card collection-list__row"
      to={`/assets/${encodeURIComponent(scope.scope)}`}
    >
      <WorkspaceCategoryArtwork compact />
      <div className="assets-overview-list-copy min-w-0 flex-1">
        <h2 className="collection-list__primary truncate" title={scope.label}>
          {scope.label}
        </h2>
        <p className="collection-list__secondary">Company main workspace</p>
      </div>
      <span className="assets-overview-list-count">
        {query.isLoading
          ? "…"
          : query.isError
            ? "Unavailable"
            : formatWorkspaceAssetCount(
                value?.totalCount ?? 0,
                value?.truncated ?? false,
              )}
      </span>
    </Link>
  );
}

function useWorkspaceAssetSummary(companyId: string, scope: string) {
  return useListWorkspaceAssetsQuery(
    {
      companyId,
      kinds: [],
      limit: 1,
      query: "",
      scope,
      sort: "latest",
    },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 30,
      skip: !companyId,
    },
  );
}

function WorkspaceCategoryArtwork({ compact = false }: { compact?: boolean }) {
  return (
    <div
      aria-hidden="true"
      className={cx(
        "assets-category-artwork",
        compact && "assets-category-artwork--compact",
      )}
    >
      <Icon
        name="folder"
        className="size-10 text-[var(--workspace-native-accent)]"
      />
    </div>
  );
}

function WorkspaceAssetsState({
  actionLabel,
  description,
  icon,
  onAction,
  title,
}: {
  actionLabel?: string;
  description: string;
  icon: string;
  onAction?: () => void;
  title: string;
}) {
  return (
    <div className="assets-page-state grid place-items-center rounded-3xl border border-dashed border-white/20 bg-white/[0.03] text-center">
      <div className="max-w-lg px-6">
        <Icon name={icon} className="mx-auto size-12 text-white/35" />
        <h2 className="m-0 mt-4 text-lg font-bold text-white">{title}</h2>
        <p className="m-0 mt-2 text-sm leading-6 text-white/50">
          {description}
        </p>
        {actionLabel && onAction ? (
          <button
            className="mt-5 h-10 rounded-xl border border-white/25 bg-white/10 px-5 text-sm font-bold text-white transition hover:bg-white/16"
            type="button"
            onClick={onAction}
          >
            {actionLabel}
          </button>
        ) : null}
      </div>
    </div>
  );
}
