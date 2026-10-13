import type {
  DesktopWorkspaceAssetItem,
  DesktopWorkspaceAssetKind,
  DesktopWorkspaceAssetSort,
} from "../../../../electron/shared/desktop-bridge.types";
import { useEffect, useRef, useState } from "react";
import {
  Link,
  Navigate,
  useLocation,
  useNavigate,
  useParams,
} from "react-router";

import { canBrowseDesktopWorkspaceAssets } from "@/shared/desktop/desktop-environment.service";
import {
  useAssetSort,
  useAssetViewMode,
} from "@/features/assets/hooks/use-asset-browser-preferences";
import { AssetPreviewDialog } from "@/features/assets/components/asset-preview-dialog";
import { AssetsWorkspaceShell } from "@/features/assets/components/assets-workspace-shell";
import { WorkspaceAssetCardPreview } from "@/features/assets/components/workspace-asset-card-preview";
import {
  formatWorkspaceAssetBytes,
  formatWorkspaceAssetCount,
  workspaceAssetScopeLabel,
  type WorkspaceAssetViewMode,
  useWorkspaceAssetScopes,
} from "@/features/assets/hooks/use-workspace-asset-scopes";
import { useWorkspaceAssets } from "@/features/assets/hooks/use-workspace-assets";
import {
  DropdownSelectButton,
  ViewSwitch,
} from "@/shared/ui/collection-controls/controls";
import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";

import "../styles/assets-responsive.css";

type AssetKindFilter = "all" | DesktopWorkspaceAssetKind;

const kindOptions = [
  { label: "All files", value: "all" },
  { label: "Images", value: "image" },
  { label: "Documents", value: "document" },
  { label: "Media", value: "media" },
  { label: "Archives", value: "archive" },
  { label: "Other", value: "other" },
] as const satisfies ReadonlyArray<{ label: string; value: AssetKindFilter }>;

const sortOptions = [
  { label: "Latest first", value: "latest" },
  { label: "Oldest first", value: "oldest" },
  { label: "Name A–Z", value: "name" },
] as const satisfies ReadonlyArray<{
  label: string;
  value: DesktopWorkspaceAssetSort;
}>;

const controlSurfaceClassName =
  "border border-white/30 bg-white/10 text-white shadow-[inset_0_0_20px_rgba(242,242,242,0.12),inset_0_1px_0_rgba(255,255,255,0.16)] backdrop-blur-[10px]";

export function AssetGroupDetailPage() {
  const { groupId = "" } = useParams<{ groupId: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const desktopAvailable = canBrowseDesktopWorkspaceAssets();
  const {
    companyId,
    scopes,
    isLoading: scopesLoading,
  } = useWorkspaceAssetScopes();
  const scope = scopes.find((candidate) => candidate.scope === groupId);
  const [kindFilter, setKindFilter] = useState<AssetKindFilter>("all");
  const [query, setQuery] = useState("");
  const [selectedAsset, setSelectedAsset] =
    useState<DesktopWorkspaceAssetItem | null>(null);
  const [sort, setSort] = useAssetSort();
  const [viewMode, setViewMode] = useAssetViewMode();
  const searchInputRef = useRef<HTMLInputElement | null>(null);
  const previewTriggerRef = useRef<HTMLButtonElement | null>(null);
  const {
    error,
    items,
    loadMore,
    loading,
    loadingMore,
    nextCursor,
    refresh,
    totalCount,
    truncated,
  } = useWorkspaceAssets({
    companyId,
    enabled: Boolean(scope),
    kinds: kindFilter === "all" ? [] : [kindFilter],
    query,
    scope: scope?.scope ?? groupId,
    sort,
  });

  useEffect(() => {
    previewTriggerRef.current = null;
    setSelectedAsset(null);
  }, [companyId, groupId]);

  useEffect(() => {
    const preview = (
      location.state as {
        workspaceAssetPreview?: {
          companyId: string;
          item: DesktopWorkspaceAssetItem;
          scope: string;
        };
      } | null
    )?.workspaceAssetPreview;
    if (
      !preview ||
      preview.companyId !== companyId ||
      preview.scope !== groupId
    ) {
      return;
    }
    setSelectedAsset(preview.item);
    navigate(`${location.pathname}${location.search}`, {
      replace: true,
      state: null,
    });
  }, [
    companyId,
    groupId,
    location.pathname,
    location.search,
    location.state,
    navigate,
  ]);

  const scopeLabel = scope?.label ?? workspaceAssetScopeLabel(groupId);
  const sourceSection = new URLSearchParams(location.search).get("section");
  const projectReturnSection =
    sourceSection &&
    [
      "tasks",
      "approvals",
      "analysis",
      "documents",
      "pages",
      "artifacts",
      "files",
      "canvas",
    ].includes(sourceSection)
      ? `/${sourceSection}`
      : "";
  const countLabel = formatWorkspaceAssetCount(totalCount, truncated);

  if (groupId !== "company" && !scope && !scopesLoading) {
    return <Navigate to="/assets" replace />;
  }

  const closeAssetPreview = () => {
    setSelectedAsset(null);
    window.requestAnimationFrame(() => previewTriggerRef.current?.focus());
  };

  return (
    <>
      <AssetsWorkspaceShell
        breadcrumb={
          groupId.startsWith("projects/") ? (
            <nav
              aria-label="Breadcrumb"
              className="project-files-breadcrumb flex min-w-0 items-center gap-[8px] text-[length:13px]"
            >
              <Link to="/projects">Projects</Link>
              <span aria-hidden="true">/</span>
              <Link
                className="project-files-breadcrumb__project"
                title={scopeLabel}
                to={`/projects/${encodeURIComponent(groupId.slice("projects/".length))}${projectReturnSection}`}
              >
                {scopeLabel}
              </Link>
              <span aria-hidden="true">/</span>
              <span aria-current="page">Files</span>
            </nav>
          ) : (
            <Link to="/assets">All assets</Link>
          )
        }
        title={groupId.startsWith("projects/") ? "Files" : scopeLabel}
        description={`${countLabel} ${totalCount === 1 && !truncated ? "file" : "files"}`}
      >
        <div className="assets-page-layout">
          <div
            className="assets-page-controls assets-detail-controls"
            aria-label={`${scopeLabel} asset controls`}
            role="group"
          >
            <div className="assets-detail-controls-primary">
              <label className="assets-page-search assets-detail-search">
                <span className="sr-only">Search files in {scopeLabel}</span>
                <Icon
                  name="search"
                  className="pointer-events-none absolute left-4 top-3 z-10 size-6 text-white"
                />
                <input
                  ref={searchInputRef}
                  placeholder="Search filename or path…"
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>

              <DropdownSelectButton
                ariaLabel="Filter files by kind"
                className="assets-detail-kind-filter collection-toolbar-control"
                options={kindOptions}
                value={kindFilter}
                onChange={setKindFilter}
              />
            </div>

            <div className="assets-page-control-actions assets-detail-controls-secondary">
              <button
                aria-label="Refresh workspace files"
                className="collection-toolbar-control collection-toolbar-control--icon"
                disabled={loading || !desktopAvailable}
                type="button"
                onClick={refresh}
              >
                <Icon
                  name="refresh"
                  className={cx("size-5", loading && "animate-spin")}
                />
              </button>
              <ViewSwitch value={viewMode} onChange={setViewMode} />
              <DropdownSelectButton
                ariaLabel="Sort workspace files"
                className="assets-detail-sort collection-toolbar-control"
                options={sortOptions}
                value={sort}
                onChange={setSort}
              />
            </div>
          </div>

          <div className="assets-page-content">
            {!desktopAvailable ? (
              <DetailState
                description="This read-only filesystem browser is available only in Zenith Team Desktop."
                icon="open-external"
                title="Open in Zenith Team Desktop"
              />
            ) : error ? (
              <DetailState
                actionLabel="Retry"
                description={error}
                icon="alert"
                title="Local daemon unavailable"
                onAction={refresh}
              />
            ) : loading ? (
              <DetailState
                description={`Finding files in ${scopeLabel}…`}
                icon="refresh"
                title="Scanning workspace files"
              />
            ) : items.length ? (
              <AssetCollection
                companyId={companyId}
                items={items}
                nextCursor={nextCursor}
                scope={scope?.scope ?? groupId}
                viewMode={viewMode}
                loadingMore={loadingMore}
                onLoadMore={loadMore}
                onSelect={(item, trigger) => {
                  previewTriggerRef.current = trigger;
                  setSelectedAsset(item);
                }}
              />
            ) : (
              <DetailState
                actionLabel={
                  query.trim() || kindFilter !== "all"
                    ? "Clear filters"
                    : undefined
                }
                onAction={() => {
                  setQuery("");
                  setKindFilter("all");
                  searchInputRef.current?.focus();
                }}
                description={
                  query.trim() || kindFilter !== "all"
                    ? "Try another filename, path, or file kind."
                    : `Files created in the ${scopeLabel} workspace will appear here.`
                }
                icon="folder"
                title={
                  query.trim() || kindFilter !== "all"
                    ? "No matching files"
                    : "This workspace is empty"
                }
              />
            )}
          </div>
        </div>
      </AssetsWorkspaceShell>

      <AssetPreviewDialog
        asset={selectedAsset}
        companyId={companyId}
        scope={scope?.scope ?? groupId}
        onOpenChange={(open) => {
          if (!open) closeAssetPreview();
        }}
      />
    </>
  );
}

function AssetCollection({
  companyId,
  items,
  loadingMore,
  nextCursor,
  onLoadMore,
  onSelect,
  scope,
  viewMode,
}: {
  companyId: string;
  items: readonly DesktopWorkspaceAssetItem[];
  loadingMore: boolean;
  nextCursor: string | null;
  onLoadMore: () => void;
  onSelect: (
    item: DesktopWorkspaceAssetItem,
    trigger: HTMLButtonElement,
  ) => void;
  scope: string;
  viewMode: WorkspaceAssetViewMode;
}) {
  return (
    <div className="h-full min-w-0 overflow-y-auto [scrollbar-width:thin]">
      <div className={viewMode === "list" ? "collection-list" : undefined}>
        {viewMode === "list" ? (
          <div
            className="assets-file-list-heading min-h-[38px] text-workspace-muted text-workspace-caption leading-workspace-caption font-[500]"
            aria-hidden="true"
          >
            <span>Name</span>
            <span>Kind</span>
            <span>Size</span>
            <span>Modified</span>
          </div>
        ) : null}
        <div
          className={
            viewMode === "grid"
              ? "assets-file-grid grid gap-4 py-3"
              : "assets-file-list grid"
          }
        >
          {items.map((item) =>
            viewMode === "grid" ? (
              <AssetGridItem
                companyId={companyId}
                item={item}
                key={item.path}
                scope={scope}
                onSelect={onSelect}
              />
            ) : (
              <AssetListItem item={item} key={item.path} onSelect={onSelect} />
            ),
          )}
        </div>
      </div>
      {nextCursor ? (
        <div className="flex justify-center py-7">
          <button
            className={cx(
              controlSurfaceClassName,
              "h-11 rounded-xl px-6 text-sm font-bold transition hover:bg-white/[0.14] disabled:opacity-50",
            )}
            disabled={loadingMore}
            type="button"
            onClick={onLoadMore}
          >
            {loadingMore ? "Loading…" : "Load more"}
          </button>
        </div>
      ) : null}
    </div>
  );
}

function AssetGridItem({
  companyId,
  item,
  onSelect,
  scope,
}: {
  companyId: string;
  item: DesktopWorkspaceAssetItem;
  onSelect: (
    item: DesktopWorkspaceAssetItem,
    trigger: HTMLButtonElement,
  ) => void;
  scope: string;
}) {
  return (
    <button
      aria-label={`Open ${item.filename}`}
      className="assets-file-grid-item group flex h-[224px] min-w-0 flex-col items-center rounded-xl pt-3 text-center outline-none transition-colors hover:bg-white/10 focus-visible:bg-white/10"
      type="button"
      onClick={(event) => onSelect(item, event.currentTarget)}
    >
      <AssetGridPreview companyId={companyId} item={item} scope={scope} />
      <span
        className="mb-3 mt-auto block w-full truncate px-3 text-center text-sm font-medium leading-5 text-white"
        title={item.filename}
      >
        {item.filename}
      </span>
    </button>
  );
}

function AssetGridPreview({
  companyId,
  item,
  scope,
}: {
  companyId: string;
  item: DesktopWorkspaceAssetItem;
  scope: string;
}) {
  const preview = item.kind === "image" || item.kind === "media";
  const fallback = (
    <span className="absolute inset-0 grid content-center justify-items-center gap-3">
      <Icon
        name={kindIcon(item.kind)}
        className="size-12 text-[var(--workspace-native-accent)]"
      />
      <span className="text-xs uppercase text-[var(--workspace-native-text-muted)]">
        {item.filename.split(".").pop() || item.kind}
      </span>
    </span>
  );
  return (
    <span className="relative h-40 w-[calc(100%-24px)] shrink-0 overflow-hidden rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)]">
      {preview ? (
        <WorkspaceAssetCardPreview
          companyId={companyId}
          fallback={fallback}
          item={item}
          scope={scope}
          showFilename={false}
        />
      ) : (
        fallback
      )}
    </span>
  );
}

function AssetListItem({
  item,
  onSelect,
}: {
  item: DesktopWorkspaceAssetItem;
  onSelect: (
    item: DesktopWorkspaceAssetItem,
    trigger: HTMLButtonElement,
  ) => void;
}) {
  return (
    <button
      aria-label={`Open ${item.filename}`}
      className="assets-file-list-item collection-list__row"
      type="button"
      onClick={(event) => onSelect(item, event.currentTarget)}
    >
      <span className="assets-file-list-icon">
        <Icon name={kindIcon(item.kind)} />
      </span>
      <span className="assets-file-list-primary">
        <span
          className="text-workspace-text text-workspace-body leading-workspace-body font-[600] block truncate"
          title={item.filename}
        >
          {item.filename}
        </span>
        <span
          className="collection-list__secondary"
          title={item.parentPath || "Workspace root"}
        >
          {item.parentPath || "Workspace root"}
        </span>
      </span>
      <span className="assets-file-list-meta">
        <span className="capitalize">{item.kind}</span>
        <span>{formatWorkspaceAssetBytes(item.sizeBytes)}</span>
        <span>{formatModifiedDate(item.modifiedAt)}</span>
      </span>
    </button>
  );
}

function DetailState({
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
    <div className="h-auto min-h-[220px] grid place-items-center rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] text-center">
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

function kindIcon(kind: DesktopWorkspaceAssetKind) {
  if (kind === "image") return "artboard";
  if (kind === "media") return "play";
  if (kind === "archive") return "box-tick";
  if (kind === "document") return "note";
  return "folder";
}

function formatModifiedDate(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    day: "2-digit",
    month: "short",
    year: "numeric",
  }).format(new Date(value));
}
