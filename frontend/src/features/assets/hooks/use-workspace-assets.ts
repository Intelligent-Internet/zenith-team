import type {
  DesktopWorkspaceAssetKind,
  DesktopWorkspaceAssetSort,
} from "../../../../electron/shared/desktop-bridge.types";
import { useCallback, useEffect, useMemo, useState } from "react";

import { canBrowseDesktopWorkspaceAssets } from "@/shared/desktop/desktop-environment.service";
import { useBrowseWorkspaceAssetsInfiniteQuery } from "@/features/assets/api/workspace-assets-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";

type UseWorkspaceAssetsOptions = {
  companyId: string;
  enabled?: boolean;
  kinds?: readonly DesktopWorkspaceAssetKind[];
  query?: string;
  scope: string;
  sort?: DesktopWorkspaceAssetSort;
};

export function useWorkspaceAssets({
  companyId,
  enabled = true,
  kinds = [],
  query = "",
  scope,
  sort = "latest",
}: UseWorkspaceAssetsOptions) {
  const desktopAvailable = canBrowseDesktopWorkspaceAssets();
  const kindsKey = kinds.join(",");
  const [debouncedQuery, setDebouncedQuery] = useState(query);
  const active = Boolean(companyId && desktopAvailable && enabled && scope);
  const assetsQuery = useBrowseWorkspaceAssetsInfiniteQuery(
    {
      companyId,
      kinds: kindsKey
        ? (kindsKey.split(",") as DesktopWorkspaceAssetKind[])
        : [],
      limit: 100,
      query: debouncedQuery,
      scope,
      sort,
    },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 30,
      skip: !active,
    },
  );
  const pages = assetsQuery.currentData?.pages ?? [];
  const items = useMemo(() => pages.flatMap((page) => page.items), [pages]);
  const lastPage = pages.at(-1);
  const refetchAssets = assetsQuery.refetch;

  useEffect(() => {
    const timeout = window.setTimeout(() => setDebouncedQuery(query), 250);
    return () => window.clearTimeout(timeout);
  }, [query]);

  const refresh = useCallback(() => {
    if (active) void refetchAssets();
  }, [active, refetchAssets]);

  useEffect(() => {
    if (!companyId || !desktopAvailable || !enabled) return;
    let refreshTimer: ReturnType<typeof setTimeout> | undefined;
    const scheduleRefresh = () => {
      if (refreshTimer !== undefined) return;
      refreshTimer = setTimeout(() => {
        refreshTimer = undefined;
        refresh();
      }, 500);
    };
    const unsubscribeEvent = boardlyRealtimeClient.onEvent((event) => {
      if (event.companyId !== companyId) return;
      if (
        event.type === "run.message" ||
        event.type === "run.completed" ||
        event.type === "run.failed" ||
        event.type === "run.cancelled"
      ) {
        scheduleRefresh();
      }
    });
    const unsubscribeRecovery =
      boardlyRealtimeClient.onRecovery(scheduleRefresh);
    return () => {
      clearTimeout(refreshTimer);
      unsubscribeEvent();
      unsubscribeRecovery();
    };
  }, [companyId, desktopAvailable, enabled, refresh]);

  useEffect(() => {
    if (!desktopAvailable || !enabled) return;
    const handleFocus = () => refresh();
    window.addEventListener("focus", handleFocus);
    return () => window.removeEventListener("focus", handleFocus);
  }, [desktopAvailable, enabled, refresh]);

  return {
    desktopAvailable,
    error: assetsQuery.error?.message ?? null,
    items,
    loadMore: () => {
      if (lastPage?.nextCursor && !assetsQuery.isFetchingNextPage) {
        void assetsQuery.fetchNextPage();
      }
    },
    loading:
      active &&
      (query !== debouncedQuery ||
        assetsQuery.isLoading ||
        (assetsQuery.isFetching && !assetsQuery.currentData)),
    loadingMore: assetsQuery.isFetchingNextPage,
    nextCursor: lastPage?.nextCursor ?? null,
    refresh,
    totalCount: lastPage?.totalCount ?? 0,
    truncated: lastPage?.truncated ?? false,
  };
}
