import { SocialAccountsSection } from "@/features/social/social-accounts-section";
import {
  ArrowLeft,
  ChevronDown,
  ExternalLinkIcon,
  Loader2,
  Monitor,
  MoreHorizontal,
  Plus,
  Puzzle,
  RefreshCwIcon,
  SearchIcon,
  Settings,
} from "@/shared/ui/icons";
import * as Popover from "@radix-ui/react-popover";
import {
  type ReactNode,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import { toast } from "sonner";
import { useListAgentRuntimesQuery } from "@/shared/api/agent-runtime-api";
import {
  runtimeDisplayName,
  runtimeMachineName,
} from "@/features/agent-setup/model/ceo-agent-setup";

import {
  openDesktopExternal,
  shouldUseDesktopHashRouter,
} from "@/shared/desktop/desktop-environment.service";
import { readSessionSnapshot } from "@/shared/session/session";
import { PluginInstallProgressLabel } from "@/features/settings/components/plugin-install-flow";
import { PluginUninstallDialog } from "@/features/settings/components/plugin-uninstall-dialog";
import { AgentSetupReturn } from "../components/agent-setup-return";
import { WorkflowAppConnectPanel } from "@/features/settings/components/workflow-app-connect-panel";
import {
  reconnectableConnector,
  useListConnectorReviewsQuery,
  useReconnectConnectorMutation,
} from "@/features/settings/api/connector-reviews-api";
import {
  useLazyListPluginsQuery,
  useListPluginsQuery,
  useRefreshPluginsMutation,
} from "@/features/settings/api/plugins-api";
import { usePluginInstallation } from "@/features/settings/hooks/use-plugin-installation";
import {
  type PluginInstallProgress,
  pluginInstallBusy,
  pluginInstallProgress,
  startPluginInstall,
  usePluginInstallSession,
} from "@/features/settings/model/plugin-install-session";
import {
  syncedAgo,
  useMinuteClock,
} from "@/features/settings/model/plugin-sync";
import {
  getSettingsTab,
  pluginCatalogLinkState,
  pluginDetailPath,
} from "@/features/settings/model/settings-navigation";
import {
  readWorkflowAppLink,
  workflowConnectorDisplayFor,
} from "@/features/workflows/model/workflow-catalog";
import type {
  ConnectorItemV1,
  PluginCatalogResponseV1,
  PluginItemV1,
  PluginProvider,
  PluginStatus,
  WorkflowAppPlugin,
} from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import type { LocalPluginIconContext } from "@/shared/lib/plugin-icons";
import { useCan } from "@/shared/permissions";
import { AppAvatar, pluginAvatarProps } from "@/shared/ui/app-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import "@/features/settings/styles/plugins-page.css";

const PLUGIN_PAGE_SIZE = 100;
// A section of the All view shows six rows; its preview also reads the next
// three plugins so the "See …" row can name and show what it opens.
const PLUGIN_SECTION_SIZE = 6;
const PLUGIN_PREVIEW_SIZE = PLUGIN_SECTION_SIZE + 3;
const PLUGIN_STRIP_SIZE = 24;
const PLUGIN_SEARCH_DELAY_MS = 250;
// The reconnect line shows this many logos, then "+N".
const RECONNECT_AVATAR_COUNT = 4;
const CLAUDE_DIRECTORY_URL = "https://claude.com/connectors";

const PLUGIN_CATEGORIES = [
  "Productivity",
  "Creativity",
  "Developer Tools",
  "Business & Operations",
  "Data & Analytics",
  "Communication",
  "Education & Research",
  "Security",
  "Finance",
  "Healthcare",
  "Travel",
  "Entertainment",
  "Other",
] as const;

type PluginCategory = (typeof PLUGIN_CATEGORIES)[number];
type PluginFilter =
  | { kind: "all" }
  | { kind: "installed" }
  | { kind: "featured" }
  | { kind: "category"; category: PluginCategory };

type PluginCategoryGroup = {
  category: PluginCategory;
  plugins: PluginItemV1[];
};

// One section of rows. `overflow` is what follows the shown rows and `total`
// the section's size when known; together they word the "See …" row, which
// opens `view`.
type PluginSectionModel = {
  key: string;
  label: string;
  showHeading: boolean;
  plugins: PluginItemV1[];
  overflow: PluginItemV1[];
  total?: number;
  view?: PluginFilter;
};

type NormalizedPluginCategoryCount = {
  category: PluginCategory;
  count: number;
};

const PLUGIN_STATUS_LABELS: Partial<Record<PluginStatus, string>> = {
  disabled: "Disabled",
  error: "Unavailable",
};

const PLUGINS_TAB = getSettingsTab("plugins");

// A link to one plugin's page, remembering the catalog it was opened from.
type PluginLinkProps = {
  to: string;
  state: ReturnType<typeof pluginCatalogLinkState>;
  onClick: () => void;
};

// The catalog as the person left it for a plugin's page, so the breadcrumb
// or Back returns to the same rows, loaded pages and scroll position. The
// view and search themselves live in the URL (`view`, `q`); this keeps what
// the URL cannot. One entry, used once: one catalog is on screen at a time.
type CatalogMemory = {
  key: string;
  catalogBinding: string;
  categoryPreviewBinding: string;
  categoryPreviewPlugins: PluginItemV1[];
  categoryPreviewRevision: number;
  cursor?: string;
  featuredPreviewHasMore: boolean;
  featuredPreviewPlugins: PluginItemV1[];
  plugins: PluginItemV1[];
  scrollTop: number;
};

let catalogMemory: CatalogMemory | undefined;

export function PluginsSettingsPage() {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const requestedRuntimeId = params.get("runtimeId") ?? "";
  // A Workflow chip sent the viewer here to connect one app on their own
  // computer for one Agent (`app`, `workflow`, `agent`); malformed values
  // show the plain page.
  const workflowAppLink = readWorkflowAppLink(params);
  const runtimesQuery = useListAgentRuntimesQuery(companyId, {
    skip: !companyId,
  });
  const runtimes = [...(runtimesQuery.currentData?.items ?? [])].sort(
    (a, b) =>
      runtimeDisplayName(a).localeCompare(runtimeDisplayName(b)) ||
      a.provider.localeCompare(b.provider),
  );
  const requestedRuntime = runtimes.find(
    (runtime) => runtime.id === requestedRuntimeId,
  );
  const location = useLocation();
  const catalogTopRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const viewTitleRef = useRef<HTMLHeadingElement>(null);
  const isDesktopRuntime = shouldUseDesktopHashRouter();
  // The view and search come back from the URL. A Workflow app link seeds
  // the search with its app the first time; `q=` then keeps an emptied one.
  const [initialSearch] = useState(
    () =>
      params.get("q") ??
      (workflowAppLink
        ? workflowConnectorDisplayFor(workflowAppLink.app).label
        : ""),
  );
  const [initialFilter] = useState(() => readPluginFilter(params.get("view")));
  const [restored] = useState(() =>
    catalogMemory?.key ===
    catalogMemoryKey(
      companyId,
      requestedRuntimeId,
      initialFilter,
      initialSearch,
    )
      ? catalogMemory
      : undefined,
  );
  const [search, setSearch] = useState(initialSearch);
  const [debouncedSearch, setDebouncedSearch] = useState(initialSearch);
  const [filter, setFilter] = useState<PluginFilter>(initialFilter);
  const [pendingFocus, setPendingFocus] = useState<"all" | "view" | null>(null);
  const [cursor, setCursor] = useState(restored?.cursor);
  const [plugins, setPlugins] = useState(restored?.plugins ?? []);
  const [categoryPreviewPlugins, setCategoryPreviewPlugins] = useState(
    restored?.categoryPreviewPlugins ?? [],
  );
  const [featuredPreviewPlugins, setFeaturedPreviewPlugins] = useState(
    restored?.featuredPreviewPlugins ?? [],
  );
  const [featuredPreviewHasMore, setFeaturedPreviewHasMore] = useState(
    restored?.featuredPreviewHasMore ?? false,
  );
  const [categoryPreviewRevision, setCategoryPreviewRevision] = useState(
    restored?.categoryPreviewRevision ?? 0,
  );
  const [pendingConnectorId, setPendingConnectorId] = useState("");
  // A reconnect of one app or of all of them; the list stays locked between
  // the apps of Reconnect all.
  const reconnecting = useRef(false);
  const [reconnectBusy, setReconnectBusy] = useState(false);
  const [operationError, setOperationError] = useState("");
  const [uninstallTarget, setUninstallTarget] = useState<PluginItemV1 | null>(
    null,
  );
  // The row control that asked to uninstall, and its row, for focus to
  // return to once the confirmation closes.
  const uninstallOpener = useRef<{
    control: HTMLElement | null;
    row: HTMLElement | null;
  }>(undefined);
  // A plugin the Workflow panel asked to open that no loaded list holds yet.
  const [awaitedPluginId, setAwaitedPluginId] = useState<string | null>(null);
  const catalogBinding = useRef(restored?.catalogBinding ?? "");
  const categoryPreviewBinding = useRef(restored?.categoryPreviewBinding ?? "");
  // The binding whose previews are on screen, as opposed to requested.
  const shownPreviewBinding = useRef(restored?.categoryPreviewBinding ?? "");
  const pendingScrollTop = useRef(restored?.scrollTop);
  const viewParam = pluginFilterParam(filter);
  const viewKey = `${viewParam ?? ""}\n${debouncedSearch}`;
  const shownViewKey = useRef<string>(undefined);
  const catalogOwner = `${companyId}\n${requestedRuntimeId}`;
  const shownCatalogOwner = useRef(catalogOwner);
  // The `q` and `view` the page last read from the URL or wrote to it. A
  // change it did not make itself (the sidebar's Plugins link, or a link to
  // this page while it is open) seeds the search and the view again.
  const urlSearch = params.get("q");
  const urlView = params.get("view");
  const syncedUrl = useRef({ q: urlSearch, view: urlView });

  // Kept for one return only: whatever this page does next replaces it.
  useEffect(() => {
    catalogMemory = undefined;
  }, []);

  useEffect(() => {
    const timeout = window.setTimeout(
      () => setDebouncedSearch(search.trim()),
      PLUGIN_SEARCH_DELAY_MS,
    );
    return () => window.clearTimeout(timeout);
  }, [search]);

  useEffect(() => {
    const synced = syncedUrl.current;
    if (synced.q === urlSearch && synced.view === urlView) return;
    syncedUrl.current = { q: urlSearch, view: urlView };
    const nextSearch =
      urlSearch ??
      (workflowAppLink
        ? workflowConnectorDisplayFor(workflowAppLink.app).label
        : "");
    setAwaitedPluginId(null);
    setSearch(nextSearch);
    setDebouncedSearch(nextSearch);
    setFilter(readPluginFilter(urlView));
  }, [urlSearch, urlView]);

  // The view and the committed search are kept in the URL (replacing the
  // entry), so the breadcrumb and Back from a plugin's page reopen them.
  useEffect(() => {
    const next = new URLSearchParams(params);
    if (viewParam) next.set("view", viewParam);
    else next.delete("view");
    if (debouncedSearch || workflowAppLink) next.set("q", debouncedSearch);
    else next.delete("q");
    syncedUrl.current = { q: next.get("q"), view: next.get("view") };
    if (next.toString() !== params.toString()) {
      setParams(next, { replace: true });
    }
  }, [debouncedSearch, viewParam]);

  // A new view or search starts at its first page, at the top. On arrival
  // the page opens at the top too, unless it was restored.
  useEffect(() => {
    if (shownViewKey.current === viewKey) return;
    const arriving = shownViewKey.current === undefined;
    shownViewKey.current = viewKey;
    if (arriving && restored) return;
    setCursor(undefined);
    catalogTopRef.current?.scrollIntoView({ block: "start" });
  }, [restored, viewKey]);

  useEffect(() => {
    if (shownCatalogOwner.current === catalogOwner) return;
    shownCatalogOwner.current = catalogOwner;
    catalogBinding.current = "";
    categoryPreviewBinding.current = "";
    shownPreviewBinding.current = "";
    setCursor(undefined);
    setPlugins([]);
    setCategoryPreviewPlugins([]);
    setFeaturedPreviewPlugins([]);
    setFeaturedPreviewHasMore(false);
    setCategoryPreviewRevision(0);
    setOperationError("");
    setUninstallTarget(null);
    setAwaitedPluginId(null);
  }, [catalogOwner]);

  // Moving between views removes the control that moved, so focus follows
  // to the new view's title, or back to the search field.
  useEffect(() => {
    if (!pendingFocus) return;
    if (pendingFocus === "view") viewTitleRef.current?.focus();
    else searchInputRef.current?.focus({ preventScroll: true });
    setPendingFocus(null);
  }, [filter, pendingFocus]);

  const catalogQueryOptions = {
    refetchOnFocus: true,
    refetchOnMountOrArgChange: 10,
    skip: !companyId || Boolean(requestedRuntimeId && runtimesQuery.isLoading),
  };
  const query = useListPluginsQuery(
    {
      runtimeId: requestedRuntimeId || undefined,
      provider: requestedRuntime?.provider,
      category: filter.kind === "category" ? filter.category : undefined,
      companyId,
      cursor,
      featured: filter.kind === "featured" ? true : undefined,
      limit: PLUGIN_PAGE_SIZE,
      query: debouncedSearch || undefined,
      status: filter.kind === "installed" ? "installed" : undefined,
    },
    catalogQueryOptions,
  );
  // The Installed strip. Without a search this is the Installed view's own
  // first page, so the two share one cache entry.
  const installedQuery = useListPluginsQuery(
    {
      runtimeId: requestedRuntimeId || undefined,
      provider: requestedRuntime?.provider,
      companyId,
      limit: PLUGIN_PAGE_SIZE,
      status: "installed",
    },
    catalogQueryOptions,
  );
  const [loadCategoryPreview] = useLazyListPluginsQuery();
  const [refreshPlugins, refreshMutation] = useRefreshPluginsMutation();
  // Every member installs and connects here, on their own computer; only
  // owners and admins accept an app's changed tools for the Company, so the
  // reconnect card is theirs alone (owner decision, 2026-09-29).
  const canReview = useCan("connectors.manage");
  const connectorReviewsQuery = useListConnectorReviewsQuery(
    {
      companyId,
      runtimeId: requestedRuntimeId || undefined,
      provider: requestedRuntime?.provider,
    },
    { ...catalogQueryOptions, skip: catalogQueryOptions.skip || !canReview },
  );
  const [reconnectConnector] = useReconnectConnectorMutation();

  // While Load more reads the next page, the last page keeps the page's
  // provider, computer and rows (`data` outlives an argument change).
  const catalog = query.currentData ?? (cursor ? query.data : undefined);
  const catalogFailed = query.isError && !query.currentData;
  const activeProvider = catalog?.activeProvider;
  const activeRuntimeId = catalog?.runtime?.id;
  const catalogRuntime = runtimes.find(
    (runtime) => runtime.id === activeRuntimeId,
  );
  const targetLabel = `${catalogRuntime ? runtimeMachineName(catalogRuntime) : (catalog?.runtime?.name ?? "No computer selected")} · ${catalog?.activeProvider ? providerDisplayName(catalog.activeProvider) : "No provider"}`;
  const runtimeIsOnline = catalog?.runtime?.online === true;
  const detailRuntimeId = activeRuntimeId || requestedRuntimeId || undefined;
  const localPluginIconContext = useMemo<LocalPluginIconContext | undefined>(
    () =>
      isDesktopRuntime && runtimeIsOnline && activeRuntimeId
        ? { companyId, runtimeId: activeRuntimeId }
        : undefined,
    [activeRuntimeId, companyId, isDesktopRuntime, runtimeIsOnline],
  );
  const installation = usePluginInstallation({
    activeProvider,
    companyId,
    runtimeId: activeRuntimeId,
    runtimeOnline: runtimeIsOnline,
    targetLabel,
  });
  const { applyOverrides, clearMessages, pendingPluginId } = installation;
  const installSession = usePluginInstallSession();
  const installBusy = pluginInstallBusy(installSession);
  const shownError = installation.error || operationError;
  const shownNotice = installation.notice;

  useEffect(() => {
    clearMessages();
  }, [clearMessages, companyId, requestedRuntimeId]);

  // Row actions sit far below the page's message line, so their outcome is
  // also announced where the viewer is looking.
  useEffect(() => {
    if (installation.error) toast.error(installation.error);
  }, [installation.error]);
  useEffect(() => {
    if (installation.notice) toast(installation.notice);
  }, [installation.notice]);

  useEffect(() => {
    const page = query.currentData;
    if (!page) return;

    const nextBinding = `${companyId}:${page.activeProvider}:${page.runtime?.id ?? "offline"}`;
    const providerPlugins = applyOverrides(
      page.plugins.filter((plugin) => plugin.provider === page.activeProvider),
    );
    if (catalogBinding.current && catalogBinding.current !== nextBinding) {
      catalogBinding.current = nextBinding;
      setCursor(undefined);
      setPlugins(providerPlugins);
      return;
    }
    catalogBinding.current = nextBinding;
    setPlugins((current) =>
      cursor ? mergePluginPages(current, providerPlugins) : providerPlugins,
    );
  }, [companyId, cursor, query.currentData]);

  // A change that succeeds here, or on a plugin's own page, shows on the rows
  // already loaded until discovery reports it.
  useEffect(() => {
    setPlugins((current) => applyOverrides(current));
    setCategoryPreviewPlugins((current) => applyOverrides(current));
    setFeaturedPreviewPlugins((current) => applyOverrides(current));
  }, [applyOverrides]);

  const categoryCounts = useMemo(
    () => normalizeCategoryCounts(catalog?.categoryCounts ?? []),
    [catalog?.categoryCounts],
  );
  const categoryCountByName = useMemo(
    () => new Map(categoryCounts.map((entry) => [entry.category, entry.count])),
    [categoryCounts],
  );
  const isAllCategoryPreview =
    filter.kind === "all" && !search.trim() && !debouncedSearch;
  const catalogObservedAt = catalog?.plugins[0]?.observedAt ?? "";
  useEffect(() => {
    if (
      !companyId ||
      !activeProvider ||
      !isAllCategoryPreview ||
      categoryCounts.length === 0
    ) {
      return;
    }

    const binding = [
      companyId,
      activeProvider,
      activeRuntimeId ?? "offline",
      catalogObservedAt,
      categoryPreviewRevision,
      ...categoryCounts.map((entry) => `${entry.category}:${entry.count}`),
    ].join(":");
    if (categoryPreviewBinding.current === binding) return;
    categoryPreviewBinding.current = binding;
    let canceled = false;

    void Promise.allSettled([
      loadCategoryPreview({
        runtimeId: activeRuntimeId,
        provider: activeProvider,
        companyId,
        featured: true,
        limit: PLUGIN_PREVIEW_SIZE,
      }).unwrap(),
      ...categoryCounts.map((entry) =>
        loadCategoryPreview({
          runtimeId: activeRuntimeId,
          provider: activeProvider,
          category: entry.category,
          companyId,
          limit: PLUGIN_PREVIEW_SIZE,
        }).unwrap(),
      ),
    ]).then(([featuredResult, ...categoryResults]) => {
      if (canceled || categoryPreviewBinding.current !== binding) return;
      shownPreviewBinding.current = binding;

      if (featuredResult?.status === "fulfilled") {
        setFeaturedPreviewPlugins(
          applyOverrides(
            featuredResult.value.plugins.filter(
              (plugin) => plugin.provider === activeProvider,
            ),
          ),
        );
        setFeaturedPreviewHasMore(Boolean(featuredResult.value.nextCursor));
      }

      setCategoryPreviewPlugins((current) =>
        reconcileCategoryPreviews({
          activeProvider,
          applyOverrides,
          categoryCounts,
          current,
          results: categoryResults,
        }),
      );
    });

    return () => {
      canceled = true;
    };
  }, [
    activeProvider,
    activeRuntimeId,
    catalogObservedAt,
    categoryCounts,
    categoryPreviewRevision,
    companyId,
    isAllCategoryPreview,
    loadCategoryPreview,
  ]);
  const visiblePlugins = useMemo(
    () =>
      isAllCategoryPreview
        ? mergePluginPages(categoryPreviewPlugins, plugins)
        : plugins,
    [categoryPreviewPlugins, isAllCategoryPreview, plugins],
  );
  const sections = useMemo(
    () =>
      buildPluginSections({
        activeProvider,
        categoryCountByName,
        featuredHasMore: featuredPreviewHasMore,
        featuredPlugins: featuredPreviewPlugins,
        filter,
        plugins: visiblePlugins,
        preview: isAllCategoryPreview,
      }),
    [
      activeProvider,
      categoryCountByName,
      featuredPreviewHasMore,
      featuredPreviewPlugins,
      filter,
      isAllCategoryPreview,
      visiblePlugins,
    ],
  );
  const loadedPlugins = useMemo(
    () =>
      mergePluginPages(plugins, categoryPreviewPlugins, featuredPreviewPlugins),
    [categoryPreviewPlugins, featuredPreviewPlugins, plugins],
  );
  // The strip holds the Installed page plus anything a change just installed
  // on the rows already loaded; a plugin just uninstalled drops out at once.
  const installedPlugins = useMemo(() => {
    const page = installedQuery.currentData;
    const listed =
      page &&
      page.activeProvider === activeProvider &&
      page.runtime?.id === activeRuntimeId
        ? applyOverrides(page.plugins)
        : [];
    return mergePluginPages(listed, loadedPlugins).filter(
      (plugin) => plugin.provider === activeProvider && plugin.installed,
    );
  }, [
    activeProvider,
    activeRuntimeId,
    applyOverrides,
    installedQuery.currentData,
    loadedPlugins,
  ]);
  const isInitialLoading = query.isFetching && !catalog;
  const isLoadingMore = query.isFetching && Boolean(cursor);

  // A restored catalog returns to where it was scrolled once its rows are
  // back on screen.
  useLayoutEffect(() => {
    if (pendingScrollTop.current === undefined || !catalog) return;
    catalogTopRef.current
      ?.closest(".workspace-page-frame__content")
      ?.scrollTo({ top: pendingScrollTop.current });
    pendingScrollTop.current = undefined;
  }, [catalog]);

  const rememberCatalog = () => {
    catalogMemory = {
      key: catalogMemoryKey(
        companyId,
        requestedRuntimeId,
        filter,
        debouncedSearch,
      ),
      catalogBinding: catalogBinding.current,
      // Previews still loading when the page closes load again on return.
      categoryPreviewBinding: shownPreviewBinding.current,
      categoryPreviewPlugins,
      categoryPreviewRevision,
      cursor,
      featuredPreviewHasMore,
      featuredPreviewPlugins,
      plugins,
      scrollTop:
        catalogTopRef.current?.closest(".workspace-page-frame__content")
          ?.scrollTop ?? 0,
    };
  };
  const catalogLinkState = pluginCatalogLinkState(
    `${location.pathname}${location.search}`,
  );
  const pluginLink = (pluginId: string): PluginLinkProps => ({
    to: pluginDetailPath(pluginId, detailRuntimeId),
    state: catalogLinkState,
    onClick: rememberCatalog,
  });
  const openPlugin = (pluginId: string) => {
    rememberCatalog();
    void navigate(pluginDetailPath(pluginId, detailRuntimeId), {
      state: catalogLinkState,
    });
  };

  // The Workflow panel's plugin opens once a loaded list holds it; see
  // showPluginDetails.
  useEffect(() => {
    if (!awaitedPluginId) return;
    if (!loadedPlugins.some((plugin) => plugin.id === awaitedPluginId)) return;
    setAwaitedPluginId(null);
    openPlugin(awaitedPluginId);
  }, [awaitedPluginId, detailRuntimeId, loadedPlugins, navigate]);

  const changeView = (next: PluginFilter) => {
    setAwaitedPluginId(null);
    setFilter(next);
    setPendingFocus(next.kind === "all" ? "all" : "view");
  };

  // "+" starts the install flow, which connects a plugin's app before
  // installing it when Codex would (PluginInstallFlow).
  const install = (plugin: PluginItemV1) => {
    if (pendingPluginId || installBusy) return;
    if (!installation.canChange || !activeRuntimeId) {
      toast(installation.unavailableReason);
      return;
    }
    setOperationError("");
    startPluginInstall({
      avatar: pluginAvatarProps(plugin, localPluginIconContext),
      companyId,
      computer: catalogRuntime
        ? runtimeMachineName(catalogRuntime)
        : (catalog?.runtime?.name ?? "this computer"),
      plugin,
      provider: plugin.provider,
      runtimeId: activeRuntimeId,
    });
  };

  const requestUninstall = (
    plugin: PluginItemV1,
    control: HTMLElement | null,
  ) => {
    if (pendingPluginId) return;
    if (!installation.canChange) {
      toast(installation.unavailableReason);
      return;
    }
    uninstallOpener.current = { control, row: control?.closest("li") ?? null };
    setUninstallTarget(plugin);
  };

  // Back to the row's "…"; after an uninstall that became a "+", so to the
  // row's name instead; failing both, the search field.
  const uninstallReturnFocus = () => {
    const opener = uninstallOpener.current;
    uninstallOpener.current = undefined;
    if (opener?.control?.isConnected) return opener.control;
    const rowLink = opener?.row?.isConnected
      ? opener.row.querySelector<HTMLElement>(".plugins-row__link")
      : null;
    return rowLink ?? searchInputRef.current;
  };

  const confirmUninstall = async () => {
    if (!uninstallTarget) return;
    setOperationError("");
    await installation.change(uninstallTarget);
    setUninstallTarget(null);
  };

  // Reconnects the apps one at a time and stops at the first that fails.
  // The outcome is a toast, since the list that asked may have closed.
  const reconnect = async (connectors: ConnectorItemV1[]) => {
    if (reconnecting.current || connectors.length === 0) return;
    reconnecting.current = true;
    setReconnectBusy(true);
    clearMessages();
    setOperationError("");
    let reconnected = 0;
    try {
      for (const connector of connectors) {
        setPendingConnectorId(connector.id);
        try {
          const operation = await reconnectConnector({
            companyId,
            connectorId: connector.id,
          }).unwrap();
          if (operation.state !== "complete") {
            toast.error(`${connector.name} did not finish reconnecting.`);
            break;
          }
          reconnected += 1;
        } catch (error) {
          toast.error(
            errorMessage(error, `${connector.name} could not be reconnected.`),
          );
          break;
        }
      }
    } finally {
      reconnecting.current = false;
      setReconnectBusy(false);
      setPendingConnectorId("");
    }
    if (reconnected > 0) {
      toast(
        reconnected === 1
          ? `${connectors[0].name} is ready to use again.`
          : `${reconnected} apps are ready to use again.`,
      );
    }
    void connectorReviewsQuery.refetch();
  };

  // The Workflow app panel opens its app's pinned plugin on the plugin's own
  // page. A plugin the catalog hasn't listed (the seeded search is the app's
  // name, not always the plugin's) is searched for by its own name, and its
  // page opens once it is listed.
  const showPluginDetails = (plugin: WorkflowAppPlugin) => {
    if (loadedPlugins.some((candidate) => candidate.id === plugin.id)) {
      openPlugin(plugin.id);
      return;
    }
    setFilter({ kind: "all" });
    setSearch(plugin.name);
    setAwaitedPluginId(plugin.id);
  };

  const refresh = async () => {
    if (!activeProvider) return;
    clearMessages();
    setOperationError("");
    try {
      await refreshPlugins({
        companyId,
        provider: activeProvider,
        runtimeId:
          isDesktopRuntime && runtimeIsOnline ? activeRuntimeId : undefined,
      }).unwrap();
      setCategoryPreviewRevision((current) => current + 1);
      setCursor(undefined);
      void query.refetch();
    } catch (error) {
      setOperationError(
        errorMessage(error, "Plugin discovery could not be refreshed."),
      );
    }
  };

  if (!companyId) {
    return (
      <div className="plugins-page [container:plugins-page_/_inline-size] [scroll-margin-top:var(--workspace-page-gutter-block,_24px)] flex w-full min-w-0 flex-col pb-[24px] text-workspace-text">
        <PluginsHeader />
        <PluginState
          detail="Choose a Company before managing provider plugins."
          title="Company context is unavailable"
        />
      </div>
    );
  }

  const reviewConnectors =
    canReview &&
    connectorReviewsQuery.currentData?.activeProvider === activeProvider
      ? (connectorReviewsQuery.currentData?.connectors ?? [])
      : [];
  const viewTitle =
    filter.kind === "installed"
      ? "Installed"
      : filter.kind === "featured"
        ? "Featured"
        : filter.kind === "category"
          ? filter.category
          : "";
  const viewCount =
    filter.kind === "installed"
      ? catalog?.installedCount
      : filter.kind === "category"
        ? categoryCountByName.get(filter.category)
        : undefined;
  const isRefreshing = refreshMutation.isLoading || query.isFetching;
  const rowProps = {
    pluginLink,
    iconContext: localPluginIconContext,
    installBlocked:
      Boolean(pendingPluginId) ||
      installBusy ||
      !installation.canChange ||
      !activeProvider,
    installBlockedReason:
      pendingPluginId || installBusy
        ? "Another plugin change is in progress."
        : installation.unavailableReason,
    installProgress: (pluginId: string) =>
      pluginInstallProgress(installSession, pluginId),
    menuBlocked: Boolean(pendingPluginId),
    pendingPluginId,
    onInstall: install,
    onUninstall: requestUninstall,
  };

  const socialSection =
    filter.kind === "all" || filter.kind === "installed" ? (
      <SocialAccountsSection
        key="social-accounts"
        search={debouncedSearch}
        connectedOnly={filter.kind === "installed"}
      />
    ) : null;
  const socialAfterProductivity =
    !catalogFailed &&
    !isInitialLoading &&
    plugins.length > 0 &&
    sections.some((section) => section.key === "category:Productivity");

  return (
    <div
      className="plugins-page [container:plugins-page_/_inline-size] [scroll-margin-top:var(--workspace-page-gutter-block,_24px)] flex w-full min-w-0 flex-col pb-[24px] text-workspace-text"
      ref={catalogTopRef}
    >
      <AgentSetupReturn />
      <PluginsHeader>
        <label className="plugins-computer">
          <Monitor aria-hidden="true" className="plugins-computer__icon" />
          <select
            aria-label="Plugin installation target"
            disabled={
              Boolean(pendingPluginId) || installBusy || runtimesQuery.isLoading
            }
            title="Plugins install on this computer for this provider. Connection access is managed separately."
            value={requestedRuntimeId || activeRuntimeId || ""}
            onChange={(event) => {
              const next = new URLSearchParams(params);
              next.set("runtimeId", event.target.value);
              setParams(next);
            }}
          >
            {!activeRuntimeId && !requestedRuntimeId ? (
              <option value="">Choose a computer</option>
            ) : null}
            {runtimes.map((runtime) => (
              <option key={runtime.id} value={runtime.id}>
                {providerDisplayName(runtime.provider)} ·{" "}
                {runtimeMachineName(runtime)}
                {runtime.status !== "online" ? " · Offline" : ""}
              </option>
            ))}
          </select>
          <ChevronDown
            aria-hidden="true"
            className="plugins-computer__chevron"
          />
        </label>
        <PluginsSyncStatus
          observedAt={catalog?.observedAt}
          syncing={refreshMutation.isLoading}
        />
        <button
          aria-label="Refresh plugins"
          className="plugins-icon-button plugins-icon-button--framed"
          disabled={refreshMutation.isLoading || !runtimeIsOnline}
          title={
            runtimeIsOnline
              ? "Refresh plugins"
              : "Bring this computer online to refresh its plugins"
          }
          type="button"
          onClick={() => void refresh()}
        >
          <RefreshCwIcon
            aria-hidden="true"
            className={cx(isRefreshing && "animate-spin")}
            size={18}
          />
        </button>
      </PluginsHeader>

      <div className="flex min-w-0 mt-[20px]">
        <label className="plugins-search">
          <SearchIcon
            aria-hidden="true"
            className="w-[18px] h-[18px] flex-none text-workspace-muted"
          />
          <input
            aria-label="Search plugins"
            placeholder="Search plugins"
            ref={searchInputRef}
            type="search"
            value={search}
            onChange={(event) => {
              setAwaitedPluginId(null);
              setSearch(event.target.value);
            }}
          />
        </label>
      </div>

      {workflowAppLink ? (
        <WorkflowAppConnectPanel
          key={`${workflowAppLink.workflowKey}:${workflowAppLink.app}:${workflowAppLink.agentId}`}
          agentId={workflowAppLink.agentId}
          app={workflowAppLink.app}
          companyId={companyId}
          workflowKey={workflowAppLink.workflowKey}
          onShowPluginDetails={showPluginDetails}
        />
      ) : null}

      <div className="plugins-notices">
        {shownError ? (
          <PluginNotice role="alert" tone="danger">
            {shownError}
          </PluginNotice>
        ) : null}
        {shownNotice ? (
          <PluginNotice role="status" tone="info">
            {shownNotice}
          </PluginNotice>
        ) : null}
        <ReconnectCard
          busy={reconnectBusy}
          connectors={reviewConnectors}
          pendingConnectorId={pendingConnectorId}
          onReconnect={reconnect}
        />
        {/* An offline computer already reads "Offline" in its select, and
            each blocked control says why. */}
        {catalog &&
        installation.unavailableReason &&
        (runtimeIsOnline || !isDesktopRuntime || !activeRuntimeId) ? (
          <PluginNotice tone="neutral">
            {installation.unavailableReason}
          </PluginNotice>
        ) : null}
        {activeProvider === "claude" ? (
          <PluginNotice
            action={
              <button
                className="plugins-text-button"
                type="button"
                onClick={() => void openDesktopExternal(CLAUDE_DIRECTORY_URL)}
              >
                Browse more in Claude
                <ExternalLinkIcon aria-hidden="true" size={14} />
              </button>
            }
            tone="info"
          >
            Zenith Team lists plugins reported by the local Claude CLI.
            Anthropic does not publish its complete directory through a
            documented API.
          </PluginNotice>
        ) : null}
      </div>

      {filter.kind !== "all" ? (
        <div className="plugins-view-header flex min-w-0 flex-col items-start gap-[6px] mt-[24px] [border-bottom:1px_solid_var(--workspace-divider)] pb-[12px]">
          <button
            className="plugins-back"
            type="button"
            onClick={() => changeView({ kind: "all" })}
          >
            <ArrowLeft aria-hidden="true" size={16} />
            All plugins
          </button>
          <h2 ref={viewTitleRef} tabIndex={-1}>
            {viewTitle}
            {viewCount !== undefined ? (
              <span className="text-workspace-muted text-workspace-label font-[400] leading-workspace-label">
                {viewCount} {viewCount === 1 ? "plugin" : "plugins"}
              </span>
            ) : null}
          </h2>
        </div>
      ) : null}

      {isAllCategoryPreview && installedPlugins.some(showsInInstalledStrip) ? (
        <InstalledStrip
          pluginLink={pluginLink}
          iconContext={localPluginIconContext}
          installedCount={Math.max(
            catalog?.installedCount ?? 0,
            installedPlugins.length,
          )}
          plugins={installedPlugins}
          onManage={() => changeView({ kind: "installed" })}
        />
      ) : null}

      {catalogFailed ? (
        <PluginState
          compact
          actionLabel="Try again"
          detail={errorMessage(
            query.error,
            "Plugins could not be loaded. Choose another computer or try again.",
          )}
          title="Plugin catalog is unavailable"
          onAction={() => void query.refetch()}
        />
      ) : isInitialLoading ? (
        <PluginState
          compact
          detail="Reading the selected provider runtime…"
          title="Loading plugin catalog"
        />
      ) : plugins.length === 0 ? (
        <PluginState
          compact
          detail={
            debouncedSearch || filter.kind !== "all"
              ? "No plugins match the current search and filter."
              : "The selected provider has not reported any plugins yet."
          }
          title={
            debouncedSearch
              ? "No matching computer plugins"
              : "No computer plugins yet"
          }
        />
      ) : (
        sections.flatMap((section) => {
          const pluginSection = (
            <PluginSection
              key={section.key}
              rowProps={rowProps}
              section={section}
              onOpenView={changeView}
            />
          );
          return section.key === "category:Productivity"
            ? [pluginSection, socialSection]
            : [pluginSection];
        })
      )}
      {!socialAfterProductivity ? socialSection : null}

      {catalog?.nextCursor && !catalogFailed && !isAllCategoryPreview ? (
        <button
          className="plugins-load-more"
          disabled={isLoadingMore}
          type="button"
          onClick={() => setCursor(catalog.nextCursor ?? undefined)}
        >
          {isLoadingMore ? "Loading…" : "Load more"}
        </button>
      ) : null}

      <PluginUninstallDialog
        pending={Boolean(
          uninstallTarget && pendingPluginId === uninstallTarget.id,
        )}
        plugin={uninstallTarget}
        returnFocus={uninstallReturnFocus}
        targetLabel={targetLabel}
        onCancel={() => setUninstallTarget(null)}
        onConfirm={() => void confirmUninstall()}
      />
    </div>
  );
}

// The page's own header, where the settings frame's would be: the title,
// and at its right the computer the catalog is read from and refresh.
function PluginsHeader({ children }: { children?: ReactNode }) {
  return (
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-[12px_24px]">
      <div className="plugins-header__heading min-w-0 [flex:1_1_240px]">
        <h1>{PLUGINS_TAB?.title ?? "Plugins"}</h1>
        {PLUGINS_TAB?.description ? <p>{PLUGINS_TAB.description}</p> : null}
      </div>
      {children ? (
        <div className="plugins-header__actions">{children}</div>
      ) : null}
    </header>
  );
}

type PluginRowProps = {
  pluginLink: (pluginId: string) => PluginLinkProps;
  iconContext?: LocalPluginIconContext;
  installBlocked: boolean;
  installBlockedReason?: string;
  /** The install flow's inline progress for a plugin, while it has one. */
  installProgress: (pluginId: string) => PluginInstallProgress | undefined;
  menuBlocked: boolean;
  pendingPluginId?: string;
  onInstall: (plugin: PluginItemV1) => void;
  onUninstall: (plugin: PluginItemV1, control: HTMLElement | null) => void;
};

function PluginSection({
  onOpenView,
  rowProps,
  section,
}: {
  onOpenView: (view: PluginFilter) => void;
  rowProps: PluginRowProps;
  section: PluginSectionModel;
}) {
  const headingId = useId();
  const moreLabel = seeMoreLabel(
    section.overflow,
    section.plugins.length,
    section.total,
  );
  const view = section.view;

  return (
    <section
      aria-label={section.showHeading ? undefined : section.label}
      aria-labelledby={section.showHeading ? headingId : undefined}
      className="plugins-section"
    >
      {section.showHeading ? (
        <header className="plugins-section__header">
          <h2 id={headingId}>{section.label}</h2>
        </header>
      ) : null}
      <ul className="plugins-grid">
        {section.plugins.map((plugin) => (
          <PluginRow key={plugin.id} plugin={plugin} {...rowProps} />
        ))}
      </ul>
      {moreLabel && view ? (
        <button
          className="plugins-see-more"
          type="button"
          onClick={() => onOpenView(view)}
        >
          {section.overflow.length > 0 ? (
            <span
              aria-hidden="true"
              className="plugins-see-more__avatars inline-flex flex-none"
            >
              {section.overflow.slice(0, 3).map((plugin) => (
                <AppAvatar
                  key={plugin.id}
                  {...pluginAvatarProps(plugin, rowProps.iconContext)}
                  size={28}
                />
              ))}
            </span>
          ) : null}
          <span>
            {moreLabel}
            <span className="sr-only"> in {section.label}</span>
          </span>
        </button>
      ) : null}
    </section>
  );
}

function PluginRow({
  pluginLink,
  iconContext,
  installBlocked,
  installBlockedReason,
  installProgress,
  menuBlocked,
  pendingPluginId,
  plugin,
  onInstall,
  onUninstall,
}: PluginRowProps & { plugin: PluginItemV1 }) {
  const descriptionId = useId();
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const isPending = pendingPluginId === plugin.id;
  const progress = installProgress(plugin.id);
  const link = pluginLink(plugin.id);
  const description =
    plugin.description || `${providerDisplayName(plugin.provider)} plugin`;
  const statusLabel =
    PLUGIN_STATUS_LABELS[plugin.status] ??
    (plugin.installed ? "Installed" : undefined);
  const spinner = (
    <Loader2 aria-hidden="true" className="animate-spin" size={18} />
  );

  return (
    <li className="plugins-row" data-pending={isPending || undefined}>
      <AppAvatar {...pluginAvatarProps(plugin, iconContext)} size={40} />
      <span className="flex min-w-0 flex-1 flex-col gap-[2px]">
        <Link
          {...link}
          aria-describedby={descriptionId}
          className="plugins-row__link"
          title={plugin.name}
        >
          {plugin.name}
        </Link>
        <span
          className="overflow-hidden text-workspace-muted text-workspace-label leading-workspace-label text-ellipsis whitespace-nowrap"
          id={descriptionId}
          title={description}
        >
          {description}
        </span>
      </span>
      <span className="relative z-[1] flex min-w-[56px] flex-none flex-col items-center">
        {progress ? (
          <PluginInstallProgressLabel name={plugin.name} progress={progress} />
        ) : plugin.action === "install" ? (
          <button
            aria-busy={isPending || undefined}
            aria-disabled={installBlocked || undefined}
            aria-label={
              isPending ? `Installing ${plugin.name}` : `Install ${plugin.name}`
            }
            className="plugins-icon-button"
            title={
              isPending
                ? `Installing ${plugin.name}…`
                : (installBlockedReason ?? `Install ${plugin.name}`)
            }
            type="button"
            onClick={() => onInstall(plugin)}
          >
            {isPending ? spinner : <Plus aria-hidden="true" size={20} />}
          </button>
        ) : (
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <button
                aria-busy={isPending || undefined}
                aria-label={`${plugin.name} actions`}
                className="plugins-icon-button"
                ref={menuButtonRef}
                type="button"
              >
                {isPending ? (
                  spinner
                ) : (
                  <MoreHorizontal aria-hidden="true" size={20} />
                )}
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" sideOffset={6}>
              <DropdownMenuItem asChild>
                <Link {...link}>View details</Link>
              </DropdownMenuItem>
              {plugin.action === "uninstall" ? (
                <DropdownMenuItem
                  className="plugins-menu-item--danger"
                  disabled={menuBlocked}
                  variant="destructive"
                  onSelect={() => onUninstall(plugin, menuButtonRef.current)}
                >
                  Uninstall
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        {statusLabel ? (
          <small className="plugins-row__status" data-status={plugin.status}>
            {statusLabel}
          </small>
        ) : null}
      </span>
    </li>
  );
}

function InstalledStrip({
  pluginLink,
  iconContext,
  installedCount,
  onManage,
  plugins,
}: {
  pluginLink: (pluginId: string) => PluginLinkProps;
  iconContext?: LocalPluginIconContext;
  installedCount: number;
  onManage: () => void;
  plugins: PluginItemV1[];
}) {
  const headingId = useId();
  const tiles = plugins.filter(showsInInstalledStrip);
  const shown = tiles.slice(0, PLUGIN_STRIP_SIZE);
  // "+N" counts the installed plugins past the shown tiles, not the ones
  // the strip leaves out.
  const hidden =
    installedCount - (plugins.length - tiles.length) - shown.length;

  return (
    <section aria-labelledby={headingId} className="plugins-section">
      <header className="plugins-section__header">
        <h2 id={headingId}>Installed</h2>
        <button
          aria-label="Manage installed plugins"
          className="plugins-icon-button"
          title="Manage installed plugins"
          type="button"
          onClick={onManage}
        >
          <Settings aria-hidden="true" size={18} />
        </button>
      </header>
      <ul className="grid grid-cols-[repeat(auto-fill,_minmax(68px,_1fr))] [justify-items:start] gap-[8px_0] m-[14px_0_0_-4px] p-0 [list-style:none]">
        {shown.map((plugin) => {
          const statusLabel = PLUGIN_STATUS_LABELS[plugin.status];
          const label = statusLabel
            ? `${plugin.name} · ${statusLabel}`
            : plugin.name;
          return (
            <li key={plugin.id}>
              <Link
                {...pluginLink(plugin.id)}
                aria-label={label}
                className="plugins-strip__tile"
                data-status={plugin.status}
                title={label}
              >
                <AppAvatar
                  {...pluginAvatarProps(plugin, iconContext)}
                  size={52}
                />
              </Link>
            </li>
          );
        })}
        {hidden > 0 ? (
          <li>
            <button
              aria-label={`Show all ${installedCount} installed plugins`}
              className="plugins-strip__more"
              title={`Show all ${installedCount} installed plugins`}
              type="button"
              onClick={onManage}
            >
              +{hidden}
            </button>
          </li>
        ) : null}
      </ul>
    </section>
  );
}

// Apps whose tools changed since they were reviewed: their logos, a count
// and Reconnect, which lists them to reconnect one by one or all at once.
// Reconnect accepts the new tools of an app that is still connected; one
// that lost its connection is connected again from its plugin's page or its
// Workflow chip instead, so it is not listed.
function ReconnectCard({
  busy,
  connectors,
  onReconnect,
  pendingConnectorId,
}: {
  busy: boolean;
  connectors: ConnectorItemV1[];
  onReconnect: (connectors: ConnectorItemV1[]) => Promise<void>;
  pendingConnectorId: string;
}) {
  const titleId = useId();
  const listTitleId = useId();
  const pendingReviews = connectors.filter(reconnectableConnector);
  if (pendingReviews.length === 0) return null;
  const count = pendingReviews.length;
  const shown = pendingReviews.slice(0, RECONNECT_AVATAR_COUNT);
  const hidden = count - shown.length;

  return (
    <section
      aria-labelledby={titleId}
      className="flex min-w-0 items-center gap-[12px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] p-[8px_8px_8px_12px] [background:var(--workspace-panel-bg)]"
    >
      <span
        aria-hidden="true"
        className="plugins-reconnect-card__avatars inline-flex flex-none items-center"
      >
        {shown.map((connector) => (
          <AppAvatar
            key={connector.id}
            iconUrl={connector.iconUrl}
            name={connector.name}
            size={28}
          />
        ))}
        {hidden > 0 ? (
          <span className="inline-grid min-w-[28px] h-[28px] place-items-center rounded-[999px] p-[0_6px] [background:var(--workspace-control-bg)] text-workspace-muted text-workspace-caption font-[600] leading-[1]">
            +{hidden}
          </span>
        ) : null}
      </span>
      <p
        className="min-w-0 flex-1 m-0 text-workspace-text text-workspace-body font-[500] leading-workspace-body wrap-anywhere"
        id={titleId}
      >
        {count === 1
          ? "1 app needs reconnecting"
          : `${count} apps need reconnecting`}
      </p>
      <Popover.Root>
        <Popover.Trigger className="plugins-reconnect-card__button">
          Reconnect
          <ChevronDown aria-hidden="true" size={14} />
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="end"
            aria-labelledby={listTitleId}
            className="plugins-reconnect-popover"
            collisionPadding={12}
            sideOffset={8}
            // The list takes focus, not its first Reconnect, so opening it
            // does not look like a choice; Tab reaches the buttons.
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              (event.currentTarget as HTMLElement | null)?.focus();
            }}
          >
            <div className="plugins-reconnect-popover__header p-[12px_14px_6px]">
              <p id={listTitleId}>Reconnect apps</p>
              <p>Their tools changed</p>
            </div>
            <ul className="plugins-reconnect-popover__list min-h-0 [flex:1_1_auto] m-0 overflow-y-auto p-[2px_6px_6px] [list-style:none]">
              {pendingReviews.map((connector) => {
                const isPending = pendingConnectorId === connector.id;
                return (
                  <li key={connector.id}>
                    <AppAvatar
                      iconUrl={connector.iconUrl}
                      name={connector.name}
                      size={28}
                    />
                    <span className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap">
                      {connector.name}
                    </span>
                    <button
                      aria-busy={isPending || undefined}
                      aria-disabled={busy || undefined}
                      className="plugins-reconnect-popover__action"
                      type="button"
                      onClick={() => {
                        if (!busy) void onReconnect([connector]);
                      }}
                    >
                      {isPending ? (
                        <Loader2 aria-hidden="true" className="animate-spin" />
                      ) : null}
                      {isPending ? "Reconnecting…" : "Reconnect"}
                      <span className="sr-only"> {connector.name}</span>
                    </button>
                  </li>
                );
              })}
            </ul>
            {count > 1 ? (
              <div className="[border-top:1px_solid_var(--workspace-divider)] p-[6px]">
                <button
                  aria-disabled={busy || undefined}
                  className="plugins-reconnect-popover__all"
                  type="button"
                  onClick={() => {
                    if (!busy) void onReconnect(pendingReviews);
                  }}
                >
                  Reconnect all
                </button>
              </div>
            ) : null}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
    </section>
  );
}

// When the selected computer last reported its catalog, beside refresh.
function PluginsSyncStatus({
  observedAt,
  syncing,
}: {
  observedAt?: string | null;
  syncing: boolean;
}) {
  const now = useMinuteClock();
  if (syncing)
    return (
      <span className="flex-none text-workspace-faint text-workspace-caption leading-workspace-caption whitespace-nowrap">
        Syncing…
      </span>
    );
  const synced = observedAt ? syncedAgo(observedAt, now) : undefined;
  if (!observedAt || !synced) return null;
  return (
    <span
      className="flex-none text-workspace-faint text-workspace-caption leading-workspace-caption whitespace-nowrap"
      title={`Plugins synced ${new Date(observedAt).toLocaleString()}`}
    >
      Synced {synced}
    </span>
  );
}

function PluginNotice({
  action,
  children,
  role,
  tone,
}: {
  action?: ReactNode;
  children: ReactNode;
  role?: "alert" | "status";
  tone: "danger" | "info" | "neutral";
}) {
  return (
    <div className="plugins-notice" data-tone={tone} role={role}>
      <div className="flex min-w-0 items-center gap-[10px]">
        <span
          aria-hidden="true"
          className="w-[8px] h-[8px] flex-none rounded-[999px] [background:var(--plugins-notice-tone)]"
        />
        <p className="plugins-notice__text">{children}</p>
        {action}
      </div>
    </div>
  );
}

function PluginState({
  actionLabel,
  compact = false,
  detail,
  onAction,
  title,
}: {
  actionLabel?: string;
  compact?: boolean;
  detail: string;
  onAction?: () => void;
  title: string;
}) {
  return (
    <div className={cx("plugins-state", compact && "is-compact")}>
      <Puzzle aria-hidden="true" size={28} />
      <strong>{title}</strong>
      <p>{detail}</p>
      {actionLabel && onAction ? (
        <button type="button" onClick={onAction}>
          {actionLabel}
        </button>
      ) : null}
    </div>
  );
}

// "See HubSpot, Linear, and 38 more": names the first plugins past the shown
// rows and counts the rest when the section's size is known.
function seeMoreLabel(
  overflow: PluginItemV1[],
  shown: number,
  total: number | undefined,
) {
  const names = overflow.slice(0, 2).map((plugin) => plugin.name);
  const remaining =
    total === undefined ? undefined : Math.max(0, total - shown - names.length);
  if (names.length === 0) {
    return remaining ? `See ${remaining} more` : undefined;
  }
  const [first, second] = names;
  if (!second) {
    if (remaining === undefined) return `See ${first} and more`;
    return remaining ? `See ${first} and ${remaining} more` : `See ${first}`;
  }
  if (remaining === undefined) return `See ${first}, ${second}, and more`;
  return remaining
    ? `See ${first}, ${second}, and ${remaining} more`
    : `See ${first} and ${second}`;
}

function buildPluginSections({
  activeProvider,
  categoryCountByName,
  featuredHasMore,
  featuredPlugins,
  filter,
  plugins,
  preview,
}: {
  activeProvider: PluginProvider | undefined;
  categoryCountByName: Map<PluginCategory, number>;
  featuredHasMore: boolean;
  featuredPlugins: PluginItemV1[];
  filter: PluginFilter;
  plugins: PluginItemV1[];
  preview: boolean;
}): PluginSectionModel[] {
  const groups = groupPluginsByCategory(plugins, activeProvider);
  if (!preview) {
    // A category view is titled by its header, so its one section is not.
    return groups.map((group) => ({
      key: `category:${group.category}`,
      label: group.category,
      showHeading: filter.kind !== "category",
      plugins: group.plugins,
      overflow: [],
    }));
  }

  const sections: PluginSectionModel[] = groups.map((group) => ({
    key: `category:${group.category}`,
    label: group.category,
    showHeading: true,
    plugins: group.plugins.slice(0, PLUGIN_SECTION_SIZE),
    overflow: group.plugins.slice(PLUGIN_SECTION_SIZE),
    total: Math.max(
      categoryCountByName.get(group.category) ?? 0,
      group.plugins.length,
    ),
    view: { kind: "category", category: group.category },
  }));
  const featured = featuredPlugins.filter(
    (plugin) =>
      (!activeProvider || plugin.provider === activeProvider) &&
      plugin.featured,
  );
  if (!featured.length) return sections;
  return [
    {
      key: `${activeProvider ?? "provider"}:featured`,
      label: "Featured",
      showHeading: true,
      plugins: featured.slice(0, PLUGIN_SECTION_SIZE),
      overflow: featured.slice(PLUGIN_SECTION_SIZE),
      total: featuredHasMore ? undefined : featured.length,
      view: { kind: "featured" },
    },
    ...sections,
  ];
}

function groupPluginsByCategory(
  plugins: PluginItemV1[],
  activeProvider: PluginProvider | undefined,
): PluginCategoryGroup[] {
  const pluginsByCategory = new Map<PluginCategory, PluginItemV1[]>();
  for (const plugin of plugins) {
    if (activeProvider && plugin.provider !== activeProvider) continue;
    const category = pluginCategory(plugin.category);
    const categoryPlugins = pluginsByCategory.get(category) ?? [];
    categoryPlugins.push(plugin);
    pluginsByCategory.set(category, categoryPlugins);
  }

  return PLUGIN_CATEGORIES.flatMap((category) => {
    const categoryPlugins = pluginsByCategory.get(category);
    return categoryPlugins?.length
      ? [{ category, plugins: categoryPlugins }]
      : [];
  });
}

function reconcileCategoryPreviews({
  activeProvider,
  applyOverrides,
  categoryCounts,
  current,
  results,
}: {
  activeProvider: PluginProvider;
  applyOverrides: (plugins: PluginItemV1[]) => PluginItemV1[];
  categoryCounts: NormalizedPluginCategoryCount[];
  current: PluginItemV1[];
  results: PromiseSettledResult<PluginCatalogResponseV1>[];
}) {
  const previousByCategory = new Map<PluginCategory, PluginItemV1[]>();
  for (const plugin of current) {
    if (plugin.provider !== activeProvider) continue;
    const category = pluginCategory(plugin.category);
    const categoryPlugins = previousByCategory.get(category) ?? [];
    categoryPlugins.push(plugin);
    previousByCategory.set(category, categoryPlugins);
  }

  return categoryCounts.flatMap((entry, index) => {
    const result = results[index];
    if (result?.status !== "fulfilled") {
      return previousByCategory.get(entry.category) ?? [];
    }
    return applyOverrides(
      result.value.plugins.filter(
        (plugin) =>
          plugin.provider === activeProvider &&
          pluginCategory(plugin.category) === entry.category,
      ),
    );
  });
}

function normalizeCategoryCounts(
  counts: Array<{ category: string; count: number }>,
): NormalizedPluginCategoryCount[] {
  const totals = new Map<PluginCategory, number>();
  for (const entry of counts) {
    if (!Number.isSafeInteger(entry.count) || entry.count <= 0) continue;
    const category = pluginCategory(entry.category);
    totals.set(category, (totals.get(category) ?? 0) + entry.count);
  }
  return PLUGIN_CATEGORIES.flatMap((category) => {
    const count = totals.get(category) ?? 0;
    return count > 0 ? [{ category, count }] : [];
  });
}

function pluginCategory(category: string | undefined): PluginCategory {
  return (
    PLUGIN_CATEGORIES.find((candidate) => candidate === category) ?? "Other"
  );
}

// Codex leaves out of its Installed strip the internal plugins it gives no
// name or logo of their own (the name is only the id's slug). They still
// show in their category and in the Installed view. The rule is Codex's
// alone: most Claude plugins are named by their slug and many have no logo,
// so the same test would hide installed Claude plugins.
function showsInInstalledStrip(plugin: PluginItemV1) {
  return (
    plugin.provider !== "codex" ||
    Boolean(plugin.iconUrl) ||
    plugin.name !== plugin.nativeId.split("@")[0]
  );
}

// `view` in the URL: absent for All, else installed, featured or
// category:<name>. Anything else opens All.
function readPluginFilter(value: string | null): PluginFilter {
  if (value === "installed") return { kind: "installed" };
  if (value === "featured") return { kind: "featured" };
  const category = PLUGIN_CATEGORIES.find(
    (candidate) => value === `category:${candidate}`,
  );
  return category ? { kind: "category", category } : { kind: "all" };
}

function pluginFilterParam(filter: PluginFilter) {
  if (filter.kind === "all") return undefined;
  if (filter.kind === "category") return `category:${filter.category}`;
  return filter.kind;
}

function catalogMemoryKey(
  companyId: string,
  runtimeId: string,
  filter: PluginFilter,
  search: string,
) {
  return [companyId, runtimeId, pluginFilterParam(filter) ?? "", search].join(
    "\n",
  );
}

function mergePluginPages(...pages: PluginItemV1[][]) {
  const merged = new Map<string, PluginItemV1>();
  for (const page of pages) {
    for (const plugin of page) {
      merged.set(plugin.id, plugin);
    }
  }
  return [...merged.values()];
}

function providerDisplayName(provider: PluginProvider | undefined) {
  if (provider === "claude") return "Claude";
  if (provider === "codex") return "Codex";
  return "Provider";
}

function errorMessage(error: unknown, fallback: string) {
  if (!error || typeof error !== "object") return fallback;
  if ("message" in error && typeof error.message === "string") {
    return error.message;
  }
  if (
    "data" in error &&
    error.data &&
    typeof error.data === "object" &&
    "message" in error.data &&
    typeof error.data.message === "string"
  ) {
    return error.data.message;
  }
  return fallback;
}

export default PluginsSettingsPage;
