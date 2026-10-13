import { useWorkspaceSidebar } from "@/app/shell/workspace-sidebar-context";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import { WorkspacePageSidebar } from "@/app/shell/workspace-page-sidebar";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useCan } from "@/shared/permissions";
import { toast } from "@/shared/ui/sonner";
import { GitBranch, LayoutGrid, PanelLeft, Plus } from "@/shared/ui/icons";
import {
  useGetWikiGraphQuery,
  useGetWikiPageQuery,
  useListWikiCollectionsQuery,
  useListWikiPagesQuery,
} from "./wiki-api";
import { WikiNewCollectionDialog, WikiNewPageDialog } from "./wiki-dialogs";
import { WikiLibrary, type WikiFilter } from "./wiki-library";
import { WikiPageView } from "./wiki-page-view";
import { WikiResearchBanner } from "./wiki-research-banner";
import { WikiSidebar } from "./wiki-sidebar";
import "./wiki-page.css";

const WikiGraph = lazy(() =>
  import("./wiki-graph").then((module) => ({ default: module.WikiGraph })),
);

type WikiView = "library" | "graph";
type WikiNavigationState = {
  wikiBrowse?: {
    search: string;
    filter: WikiFilter;
    collectionId: string | null;
  };
  wikiEditPageId?: string;
};

/** Below this width the page navigation overlays the content. */
const narrowLayoutQuery = "(max-width: 640px)";

export function WikiPage({ sidebarOnly = false }: { sidebarOnly?: boolean }) {
  const workspaceSidebar = useWorkspaceSidebar();
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const canPublish = useCan("wiki.publish");
  const { pageId: routedPageId } = useParams();
  const pageId = sidebarOnly ? undefined : routedPageId;
  const location = useLocation();
  const navigationState = location.state as WikiNavigationState | null;
  const initialBrowse = sidebarOnly ? undefined : navigationState?.wikiBrowse;
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const view: WikiView = params.get("view") === "graph" ? "graph" : "library";
  const returnView: WikiView =
    params.get("from") === "graph" ? "graph" : "library";
  const [search, setSearch] = useState(initialBrowse?.search ?? "");
  const [debouncedSearch, setDebouncedSearch] = useState(
    initialBrowse?.search ?? "",
  );
  const [filter, setFilter] = useState<WikiFilter>(
    initialBrowse?.filter ?? "active",
  );
  const [collectionId, setCollectionId] = useState<string | null>(
    initialBrowse?.collectionId ?? null,
  );
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(
    () => !window.matchMedia(narrowLayoutQuery).matches,
  );
  const sidebarOpen = workspaceSidebar.desktop
    ? !workspaceSidebar.collapsed
    : mobileSidebarOpen;
  const [newPageCollectionId, setNewPageCollectionId] = useState<
    string | null | undefined
  >();
  const [newCollectionOpen, setNewCollectionOpen] = useState(false);
  const collections = useListWikiCollectionsQuery(companyId, {
    skip: !companyId,
  });
  const graph = useGetWikiGraphQuery(companyId, { skip: !companyId });
  const archivedPages = useListWikiPagesQuery(
    { companyId, status: "archived" },
    { skip: !companyId },
  );
  const page = useGetWikiPageQuery(
    { companyId, pageId: pageId ?? "" },
    { skip: !companyId || !pageId },
  );
  // currentData belongs to the routed pageId only; page.data would keep the
  // previous page while the next one loads or after leaving to /wiki.
  const current = page.currentData;
  const pageNotFound =
    page.error && "status" in page.error && page.error.status === 404;
  const [editingPageId, setEditingPageId] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const editing =
    editingPageId === pageId || (!!current && !current.currentRevisionId);
  const editAfterOpen = useCallback((id: string) => setEditingPageId(id), []);
  useEffect(() => setDirty(false), [pageId]);
  useEffect(() => {
    if (!pageId || navigationState?.wikiEditPageId !== pageId) return;
    editAfterOpen(pageId);
    navigate(`${location.pathname}${location.search}`, {
      replace: true,
      state: { ...navigationState, wikiEditPageId: undefined },
    });
  }, [
    pageId,
    navigationState,
    editAfterOpen,
    navigate,
    location.pathname,
    location.search,
  ]);
  const collectionItems = useMemo(
    () => collections.data?.items ?? [],
    [collections.data],
  );
  useEffect(() => {
    if (
      collectionId &&
      collections.isSuccess &&
      !collectionItems.some((item) => item.id === collectionId)
    ) {
      setCollectionId(null);
    }
  }, [collectionId, collectionItems, collections.isSuccess]);
  const navigationPages = useMemo(() => graph.data?.pages ?? [], [graph.data]);
  const collectionName = useMemo(() => {
    const names = new Map(collectionItems.map((item) => [item.id, item.name]));
    return (id: string | null) => (id && names.get(id)) || "Unfiled";
  }, [collectionItems]);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedSearch(search), 220);
    return () => window.clearTimeout(timer);
  }, [search]);

  /** Keeps an unsaved draft on screen instead of navigating away from it. */
  function draftBlocks(message: string) {
    if (!dirty) return false;
    toast.warning(message);
    return true;
  }

  /** The overlaid navigation gives way once the user picks a destination. */
  function closeOverlaySidebar() {
    if (window.matchMedia(narrowLayoutQuery).matches)
      setMobileSidebarOpen(false);
  }

  function openPage(id: string, editAfterOpen = false) {
    if (
      draftBlocks("Wait for your changes to save before opening another page.")
    )
      return;
    if (editAfterOpen && !sidebarOnly) setEditingPageId(id);
    closeOverlaySidebar();
    navigate(`/wiki/pages/${id}?from=${pageId ? returnView : view}`, {
      state: sidebarOnly && editAfterOpen ? { wikiEditPageId: id } : undefined,
    });
  }

  function openOverview(next: WikiView) {
    if (draftBlocks("Wait for your changes to save before leaving this page."))
      return;
    navigate(next === "library" ? "/wiki" : "/wiki?view=graph");
  }

  function showLibrary(
    nextFilter: WikiFilter,
    nextCollectionId: string | null,
  ) {
    if (draftBlocks("Wait for your changes to save before leaving this page."))
      return;
    setFilter(nextFilter);
    setCollectionId(nextCollectionId);
    closeOverlaySidebar();
    if (sidebarOnly) {
      navigate("/wiki", {
        state: {
          wikiBrowse: {
            search,
            filter: nextFilter,
            collectionId: nextCollectionId,
          },
        } satisfies WikiNavigationState,
      });
    } else openOverview("library");
  }

  function showNewPage() {
    if (
      draftBlocks("Wait for your changes to save before creating another page.")
    )
      return;
    setNewPageCollectionId(current?.collectionId ?? collectionId);
  }

  const sidebar = (
    <WikiSidebar
      companyId={companyId}
      canPublish={canPublish}
      editing={editing}
      pageId={pageId}
      pageCollectionId={current?.collectionId}
      libraryCollectionId={collectionId}
      libraryFilter={!pageId && view === "library" ? filter : null}
      collections={collectionItems}
      collectionsError={collections.isError}
      onRetryCollections={() => void collections.refetch()}
      collectionName={collectionName}
      pages={
        workspaceSidebar.collapsed && search.trim()
          ? navigationPages.filter((item) =>
              item.title.toLowerCase().includes(search.trim().toLowerCase()),
            )
          : navigationPages
      }
      pagesLoading={graph.isLoading}
      pagesError={graph.isError}
      onRetryPages={() => void graph.refetch()}
      pagesTruncated={!!graph.data?.truncated}
      archivedTotal={archivedPages.data?.total ?? 0}
      search={search}
      onSearch={(value) => {
        setSearch(value);
        setFilter("active");
        setCollectionId(null);
        if (pageId && !workspaceSidebar.collapsed) openOverview("library");
      }}
      onShowFilter={(next) => showLibrary(next, null)}
      onOpenCollection={(id) => showLibrary("active", id)}
      onOpenPage={openPage}
      onNewPage={showNewPage}
      onNewCollection={() => setNewCollectionOpen(true)}
      onDeleteCollection={(id) => {
        if (collectionId === id) setCollectionId(null);
      }}
      onClose={
        workspaceSidebar.desktop ? undefined : () => setMobileSidebarOpen(false)
      }
    />
  );

  return (
    <main
      className={`zen-wiki ${sidebarOnly ? "zen-wiki--sidebar-preview" : ""} ${sidebarOpen ? "zen-wiki--nav-open" : ""} ${pageId ? "zen-wiki--page" : ""}`}
      aria-label="Company wiki"
      data-tour-id={sidebarOnly ? undefined : "wiki-overview"}
    >
      <div className="zen-wiki__body">
        {!workspaceSidebar.desktop && !sidebarOnly && sidebarOpen && (
          <div
            className="zen-wiki__sidebar-backdrop"
            aria-hidden="true"
            onClick={() => setMobileSidebarOpen(false)}
          />
        )}
        {sidebarOnly ? (
          sidebar
        ) : workspaceSidebar.desktop ? (
          <WorkspacePageSidebar id="wiki" className="zen-wiki">
            {sidebar}
          </WorkspacePageSidebar>
        ) : sidebarOpen ? (
          sidebar
        ) : null}
        {!sidebarOnly && (
          <section className="flex min-w-0 min-h-0 flex-col relative [background:var(--wk-panel)]">
            <WikiResearchBanner
              companyId={companyId}
              currentPageId={pageId}
              onOpenPage={openPage}
            />
            {!pageId && (
              <header className="zen-wiki__top">
                <div
                  className="zen-wiki__brand flex items-center gap-[12px] text-[var(--wk-text)]"
                  data-tour-id="wiki-overview-heading"
                >
                  <div>
                    <span className="zen-wiki__eyebrow">ZENITH TEAM WIKI</span>
                    <strong>Company wiki</strong>
                    <span>
                      {navigationPages.length} pages across{" "}
                      {collectionItems.length} collections
                    </span>
                  </div>
                </div>
                <div className="zen-wiki__top-actions">
                  <div
                    className="zen-wiki__view-tabs"
                    role="group"
                    aria-label="Wiki view"
                  >
                    <button
                      aria-pressed={view === "library"}
                      onClick={() => openOverview("library")}
                    >
                      <LayoutGrid size={16} /> Library
                    </button>
                    <button
                      aria-pressed={view === "graph"}
                      onClick={() => openOverview("graph")}
                    >
                      <GitBranch size={16} /> Graph
                    </button>
                  </div>
                  {canPublish && (
                    <button className="zen-wiki__primary" onClick={showNewPage}>
                      <Plus size={17} /> New page
                    </button>
                  )}
                </div>
              </header>
            )}
            {!workspaceSidebar.desktop && !sidebarOpen && (
              <button
                className="zen-wiki__open-sidebar"
                onClick={() => setMobileSidebarOpen(true)}
                aria-label="Open page navigation"
              >
                <PanelLeft size={18} />
              </button>
            )}
            {pageId ? (
              current ? (
                <WikiPageView
                  key={current.id}
                  companyId={companyId}
                  userId={session.userId}
                  canPublish={canPublish}
                  page={current}
                  editing={editing}
                  dirty={dirty}
                  onEditingChange={(value) =>
                    setEditingPageId(value ? current.id : null)
                  }
                  onDirtyChange={setDirty}
                  collectionName={collectionName}
                  returnView={returnView}
                  onBack={() => openOverview(returnView)}
                  onOpenPage={openPage}
                />
              ) : page.isError ? (
                <p
                  className="p-[40px] text-center text-[var(--wk-muted)]"
                  role="alert"
                >
                  {pageNotFound ? (
                    "This page does not exist or is no longer available."
                  ) : (
                    <>
                      Page could not be loaded.{" "}
                      <button
                        className="zen-wiki__retry"
                        onClick={() => void page.refetch()}
                      >
                        Retry
                      </button>
                    </>
                  )}
                </p>
              ) : (
                <p
                  className="p-[40px] text-center text-[var(--wk-muted)]"
                  role="status"
                >
                  Loading page…
                </p>
              )
            ) : (
              <div className="zen-wiki__overview flex-1 min-h-0 overflow-auto p-[24px_var(--workspace-page-inset)_48px]">
                <div className="zen-wiki__overview-heading flex items-center justify-between mb-[24px]">
                  <div>
                    <h1>
                      {view === "graph"
                        ? "Page connections"
                        : "Explore the library"}
                    </h1>
                    <p>
                      {view === "graph"
                        ? "Hover to trace links. Click a page to read it."
                        : "Browse company knowledge by collection or open a page below."}
                    </p>
                  </div>
                  {view === "graph" && (
                    <span className="text-[var(--wk-muted)] text-workspace-caption">
                      Links come from page content
                    </span>
                  )}
                </div>
                {view === "graph" ? (
                  graph.data ? (
                    <Suspense fallback={<p role="status">Loading graph…</p>}>
                      <WikiGraph
                        graph={graph.data}
                        collections={collectionItems}
                        onOpenPage={(item) => openPage(item.id)}
                        onCreatePage={canPublish ? showNewPage : undefined}
                      />
                    </Suspense>
                  ) : graph.isError ? (
                    <p role="alert">
                      Graph could not be loaded.{" "}
                      <button
                        className="zen-wiki__retry"
                        onClick={() => void graph.refetch()}
                      >
                        Retry
                      </button>
                    </p>
                  ) : (
                    <p role="status">Loading graph…</p>
                  )
                ) : (
                  <WikiLibrary
                    companyId={companyId}
                    collections={collectionItems}
                    canPublish={canPublish}
                    filter={filter}
                    collectionId={collectionId}
                    search={search}
                    query={debouncedSearch}
                    onSearch={setSearch}
                    onShowFilter={(next) => showLibrary(next, null)}
                    onOpenCollection={(id) => showLibrary("active", id)}
                    onOpenPage={openPage}
                    onNewPage={showNewPage}
                    collectionName={collectionName}
                    navigationPages={navigationPages}
                    navigationTruncated={!!graph.data?.truncated}
                  />
                )}
              </div>
            )}
          </section>
        )}
      </div>
      {newPageCollectionId !== undefined && (
        <WikiNewPageDialog
          companyId={companyId}
          collections={collectionItems}
          defaultCollectionId={newPageCollectionId}
          onCreated={(id) => {
            setNewPageCollectionId(undefined);
            openPage(id, true);
          }}
          onClose={() => setNewPageCollectionId(undefined)}
        />
      )}
      {newCollectionOpen && (
        <WikiNewCollectionDialog
          companyId={companyId}
          onClose={() => setNewCollectionOpen(false)}
        />
      )}
    </main>
  );
}
