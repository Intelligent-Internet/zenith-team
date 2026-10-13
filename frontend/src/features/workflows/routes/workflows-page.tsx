import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";

import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import { useListWorkflowsQuery } from "@/features/workflows/api/workflows-api";
import {
  WorkflowCardGrid,
  type WorkflowOpenHandler,
} from "@/features/workflows/components/workflow-card";
import { WorkflowFeaturedGrid } from "@/features/workflows/components/workflow-featured-card";
import { WorkflowLaunchDialog } from "@/features/workflows/components/workflow-launch-dialog";
import { WorkflowSection } from "@/features/workflows/components/workflow-section";
import {
  primaryWorkflowCategory,
  workflowCategoryIcon,
  workflowCategoryLabel,
  workflowCategoryOrderFor,
  workflowMatchesQuery,
} from "@/features/workflows/model/workflow-catalog";
import type { WorkflowCategory, WorkflowSummary } from "@/shared/api/types";
import { useCompanyPermissions } from "@/shared/permissions";
import {
  DropdownSelectButton,
  SearchControl,
} from "@/shared/ui/collection-controls/controls";
import { Pin, Search, Zap } from "@/shared/ui/icons";
import {
  selectPinnedWorkflowKeys,
  toggleWorkflowPin,
  useAppDispatch,
  useAppSelector,
} from "@/state";

import "../styles/workflows.css";

type WorkflowsTab = "browse" | "pinned";
type CategoryFilter = WorkflowCategory | "all";

// Marks a dialog entry the catalog pushed itself, so closing it can step
// back through history instead of stacking a second catalog entry.
type WorkflowDialogLocationState = { openedFromWorkflowsCatalog: true };

function openedFromCatalog(state: unknown): boolean {
  return Boolean(
    state &&
    typeof state === "object" &&
    (state as Partial<WorkflowDialogLocationState>)
      .openedFromWorkflowsCatalog === true,
  );
}

const sectionCardLimit = 6;
const noWorkflows: WorkflowSummary[] = [];
const tabs: ReadonlyArray<{ id: WorkflowsTab; label: string }> = [
  { id: "browse", label: "Browse" },
  { id: "pinned", label: "Pinned" },
];

/**
 * The Workflows catalog: ready-to-run templates, each a short form that
 * becomes a Task for an Agent. `/workflows/:workflowKey` renders the same page
 * with that Workflow's launch dialog open. The tab, category and search live
 * in the query string so a deep link and the dialog round trip keep them.
 */
export function WorkflowsPage() {
  const { companyId = "" } = useCompanyPermissions();
  const { workflowKey } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const dispatch = useAppDispatch();
  const [searchParams, setSearchParams] = useSearchParams();
  const panelRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const closedKeyRef = useRef<string | undefined>(undefined);
  // The history entry an open or close was already requested from. Until
  // the location changes, further requests (a double click, a second
  // Escape) are ignored so the page never pushes or steps back twice.
  const navigatingFromRef = useRef<string | null>(null);

  const catalogQuery = useListWorkflowsQuery(companyId, { skip: !companyId });
  const workflows = catalogQuery.data?.items ?? noWorkflows;
  const categoryOrder = useMemo(
    () => workflowCategoryOrderFor(workflows),
    [workflows],
  );

  const tab: WorkflowsTab =
    searchParams.get("tab") === "pinned" ? "pinned" : "browse";
  const categoryParam = searchParams.get("category");
  const category: CategoryFilter =
    categoryOrder.find((item) => item === categoryParam) ?? "all";
  // The input keeps its own state so typing never waits on a navigation;
  // the query string mirrors it for deep links.
  const urlQuery = searchParams.get("q") ?? "";
  const [query, setQuery] = useState(urlQuery);
  // The q the box and the URL last agreed on. A q that differs came from a
  // navigation — Explore › Workflows, Back, or a link to this page while it
  // is open — and replaces what the box holds.
  const syncedQueryRef = useRef(urlQuery);
  useEffect(() => {
    if (urlQuery === syncedQueryRef.current) return;
    syncedQueryRef.current = urlQuery;
    setQuery(urlQuery);
  }, [urlQuery]);
  const pinnedKeyList = useAppSelector((state) =>
    selectPinnedWorkflowKeys(state, companyId),
  );
  const pinnedKeys = useMemo(() => new Set(pinnedKeyList), [pinnedKeyList]);

  const updateParams = useCallback(
    (patch: Record<string, string | null>) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          for (const [key, value] of Object.entries(patch)) {
            if (value) next.set(key, value);
            else next.delete(key);
          }
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const scrollToTop = () =>
    panelRef.current
      ?.closest(".workspace-page-frame__content")
      ?.scrollTo({ top: 0 });

  const setCategory = (next: CategoryFilter) => {
    updateParams({ category: next === "all" ? null : next });
    scrollToTop();
  };
  const setSearch = (next: string) => {
    const written = next.trim() ? next : "";
    syncedQueryRef.current = written;
    setQuery(next);
    updateParams({ q: written || null });
  };
  const clearFilters = () => {
    syncedQueryRef.current = "";
    setQuery("");
    updateParams({ category: null, q: null });
  };
  const selectTab = (next: WorkflowsTab) =>
    updateParams({ tab: next === "browse" ? null : next });

  useEffect(() => {
    navigatingFromRef.current = null;
  }, [location.key]);

  const openWorkflow: WorkflowOpenHandler = (key, opener) => {
    if (navigatingFromRef.current === location.key) return;
    navigatingFromRef.current = location.key;
    openerRef.current = opener;
    const state: WorkflowDialogLocationState = {
      openedFromWorkflowsCatalog: true,
    };
    navigate(`/workflows/${encodeURIComponent(key)}${location.search}`, {
      state,
    });
  };
  const closeWorkflow = () => {
    if (!workflowKey || navigatingFromRef.current === location.key) return;
    navigatingFromRef.current = location.key;
    closedKeyRef.current = workflowKey;
    if (openedFromCatalog(location.state)) {
      // The entry below is this catalog with the same query string.
      navigate(-1);
    } else {
      navigate(`/workflows${location.search}`, { replace: true });
    }
  };
  // Focus returns to the card that opened the dialog, or — after a deep
  // link — to the first card for that Workflow, or to the selected tab when
  // that card is not on screen (another tab, a filter, or past a section's
  // first six).
  const restoreFocus = () => {
    const opener = openerRef.current;
    const key = closedKeyRef.current;
    openerRef.current = null;
    closedKeyRef.current = undefined;
    if (opener?.isConnected) {
      opener.focus();
      return;
    }
    const panel = panelRef.current;
    if (!panel?.isConnected) return;
    const card = key
      ? panel.querySelector<HTMLElement>(
          `[data-workflow-card="${CSS.escape(key)}"]`,
        )
      : null;
    (
      card ??
      panel.parentElement?.querySelector<HTMLElement>(
        '.workflows-tabs [aria-selected="true"]',
      )
    )?.focus();
  };
  // Unpinning on the Pinned tab removes the card whose pin had focus, so
  // focus moves to the pin of the card that takes its place, else the one
  // before it, else the empty notice's action or the selected tab.
  const unpinnedIndexRef = useRef<number | null>(null);
  const togglePin = (key: string) => {
    if (tab === "pinned" && pinnedKeys.has(key)) {
      unpinnedIndexRef.current = pinnedWorkflows
        .filter(matches)
        .findIndex((item) => item.key === key);
    }
    dispatch(toggleWorkflowPin({ companyId, workflowKey: key }));
  };

  const categoryOptions = useMemo(() => {
    const present = new Set(workflows.flatMap((item) => item.categories));
    return [
      { label: "All categories", value: "all" as CategoryFilter },
      ...categoryOrder
        .filter((item) => present.has(item) || item === category)
        .map((item) => ({
          label: workflowCategoryLabel(item),
          value: item as CategoryFilter,
        })),
    ];
  }, [category, categoryOrder, workflows]);

  const filtering = category !== "all" || Boolean(query.trim());
  const matches = useCallback(
    (workflow: WorkflowSummary) =>
      (category === "all" || workflow.categories.includes(category)) &&
      workflowMatchesQuery(workflow, query),
    [category, query],
  );
  const pinnedWorkflows = useMemo(() => {
    const byKey = new Map(workflows.map((item) => [item.key, item]));
    return pinnedKeyList.flatMap((key) => {
      const workflow = byKey.get(key);
      return workflow ? [workflow] : [];
    });
  }, [pinnedKeyList, workflows]);
  useEffect(() => {
    const index = unpinnedIndexRef.current;
    if (index === null) return;
    unpinnedIndexRef.current = null;
    const panel = panelRef.current;
    if (!panel?.isConnected) return;
    const pins = panel.querySelectorAll<HTMLElement>(".workflow-card__pin");
    const target =
      pins[Math.min(Math.max(index, 0), pins.length - 1)] ??
      panel.querySelector<HTMLElement>(".workflows-notice__action button") ??
      panel.parentElement?.querySelector<HTMLElement>(
        '.workflows-tabs [aria-selected="true"]',
      );
    target?.focus();
  }, [pinnedWorkflows]);

  const summary = workflowKey
    ? workflows.find((item) => item.key === workflowKey)
    : undefined;
  const cardHandlers = {
    onOpen: openWorkflow,
    onTogglePin: togglePin,
    pinnedKeys,
  };

  // Roving focus across the two tabs: arrows wrap, Home and End jump.
  function onTabKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    let next: WorkflowsTab;
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      next = tab === "browse" ? "pinned" : "browse";
    } else if (event.key === "Home") {
      next = tabs[0].id;
    } else if (event.key === "End") {
      next = tabs[tabs.length - 1].id;
    } else {
      return;
    }
    event.preventDefault();
    selectTab(next);
    event.currentTarget.parentElement
      ?.querySelector<HTMLButtonElement>(`[data-tab="${next}"]`)
      ?.focus();
  }

  let content;
  if (!catalogQuery.data && catalogQuery.isError) {
    content = (
      <WorkflowsNotice
        tone="error"
        title="Workflows are unavailable"
        description="Zenith Team could not load the workflow catalog."
        action={
          <button
            type="button"
            className="workspace-action-button"
            onClick={() => void catalogQuery.refetch()}
          >
            Try again
          </button>
        }
      />
    );
  } else if (!catalogQuery.data) {
    content = <WorkflowsSkeleton />;
  } else if (!workflows.length) {
    content = (
      <WorkflowsNotice
        icon={<Zap aria-hidden size={20} />}
        title="No workflows yet"
        description="Workflows ship with Zenith Team releases. They will appear here once this release includes them."
      />
    );
  } else if (tab === "pinned") {
    const visible = pinnedWorkflows.filter(matches);
    content = !pinnedWorkflows.length ? (
      <WorkflowsNotice
        icon={<Pin aria-hidden size={20} />}
        title="No pinned workflows yet"
        description="Select the pin on any workflow card in Browse to keep it here for quick access."
        action={
          <button
            type="button"
            className="workspace-action-button"
            onClick={() => selectTab("browse")}
          >
            Browse workflows
          </button>
        }
      />
    ) : (
      <WorkflowResults
        category={category}
        filtering={filtering}
        query={query}
        results={visible}
        noun="pinned workflow"
        onClear={clearFilters}
      >
        <WorkflowCardGrid
          label="Pinned workflows"
          workflows={visible}
          {...cardHandlers}
        />
      </WorkflowResults>
    );
  } else if (filtering) {
    const visible = workflows.filter(matches);
    content = (
      <WorkflowResults
        category={category}
        filtering
        query={query}
        results={visible}
        noun="workflow"
        onClear={clearFilters}
      >
        <WorkflowCardGrid
          label="Matching workflows"
          workflows={visible}
          {...cardHandlers}
        />
      </WorkflowResults>
    );
  } else {
    const featured = workflows.filter((item) => item.featured);
    content = (
      <div className="grid gap-[32px]">
        {featured.length ? (
          <WorkflowSection title="Featured">
            <WorkflowFeaturedGrid workflows={featured} onOpen={openWorkflow} />
          </WorkflowSection>
        ) : null}
        {categoryOrder.map((section) => {
          const primary = workflows.filter(
            (item) => primaryWorkflowCategory(item) === section,
          );
          if (!primary.length) return null;
          const total = workflows.filter((item) =>
            item.categories.includes(section),
          ).length;
          return (
            <WorkflowSection
              key={section}
              title={workflowCategoryLabel(section)}
              icon={workflowCategoryIcon(section)}
              seeAllCount={total}
              onSeeAll={() => setCategory(section)}
            >
              <WorkflowCardGrid
                workflows={primary.slice(0, sectionCardLimit)}
                {...cardHandlers}
              />
            </WorkflowSection>
          );
        })}
      </div>
    );
  }

  return (
    <WorkspacePageFrame
      ariaLabel="Workflows"
      tourId="workflows-overview"
      className="workflows-page"
      contentWidth="contained"
      title="Workflows"
      description="Ready-to-run templates. Fill in a short form and an Agent does the work."
      toolbar={
        <div className="workflows-toolbar">
          <div
            className="workflows-tabs inline-flex flex-none gap-[2px] p-[3px] [border:1px_solid_var(--workspace-divider)] rounded-[8px] [background:var(--workspace-control-bg)]"
            role="tablist"
            aria-label="Workflows"
          >
            {tabs.map((item) => (
              <button
                key={item.id}
                type="button"
                role="tab"
                id={`workflows-tab-${item.id}`}
                data-tab={item.id}
                aria-selected={tab === item.id}
                aria-controls="workflows-tabpanel"
                tabIndex={tab === item.id ? 0 : -1}
                onClick={() => selectTab(item.id)}
                onKeyDown={onTabKeyDown}
              >
                {item.label}
                {item.id === "pinned" && pinnedWorkflows.length ? (
                  <span className="inline-grid min-w-[18px] h-[18px] place-items-center p-[0_5px] rounded-[9px] [background:var(--workspace-accent-soft)] text-workspace-accent text-workspace-micro font-[600] leading-[18px]">
                    {pinnedWorkflows.length}
                  </span>
                ) : null}
              </button>
            ))}
          </div>
          <div className="workflows-toolbar__filters">
            <DropdownSelectButton
              ariaLabel="Filter workflows by category"
              className="workflows-category-filter"
              options={categoryOptions}
              value={category}
              onChange={setCategory}
            />
            <SearchControl
              ariaLabel="Search workflows"
              placeholder="Search workflows"
              value={query}
              onChange={setSearch}
            />
          </div>
        </div>
      }
    >
      <div
        ref={panelRef}
        className="[container:workflows_/_inline-size] min-w-0"
        role="tabpanel"
        id="workflows-tabpanel"
        aria-labelledby={`workflows-tab-${tab}`}
      >
        {content}
      </div>
      <WorkflowLaunchDialog
        companyId={companyId}
        workflowKey={workflowKey}
        summary={summary}
        onClose={closeWorkflow}
        onRestoreFocus={restoreFocus}
      />
    </WorkspacePageFrame>
  );
}

function WorkflowResults({
  category,
  children,
  filtering,
  noun,
  onClear,
  query,
  results,
}: {
  category: CategoryFilter;
  children: ReactNode;
  filtering: boolean;
  noun: string;
  onClear: () => void;
  query: string;
  results: readonly WorkflowSummary[];
}) {
  const scope = [
    category === "all" ? null : `in ${workflowCategoryLabel(category)}`,
    query.trim() ? `matching “${query.trim()}”` : null,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className="grid gap-[14px]">
      {filtering ? (
        <div className="workflows-results__bar flex items-center justify-between gap-[12px] min-w-0">
          <p role="status">
            {results.length} {results.length === 1 ? noun : `${noun}s`}
            {scope ? ` ${scope}` : ""}
          </p>
          <button
            type="button"
            className="workflows-results__clear"
            onClick={onClear}
          >
            Clear
          </button>
        </div>
      ) : null}
      {results.length ? (
        children
      ) : (
        <WorkflowsNotice
          icon={<Search aria-hidden size={20} />}
          title={`No ${noun}s match`}
          description="Try another search or category."
          action={
            <button
              type="button"
              className="workspace-action-button"
              onClick={onClear}
            >
              Clear filters
            </button>
          }
        />
      )}
    </div>
  );
}

function WorkflowsNotice({
  action,
  description,
  icon,
  title,
  tone,
}: {
  action?: ReactNode;
  description: string;
  icon?: ReactNode;
  title: string;
  tone?: "error";
}) {
  return (
    <section
      className="workflows-notice"
      data-tone={tone}
      role={tone === "error" ? "alert" : undefined}
    >
      {icon ? (
        <span className="grid w-[44px] h-[44px] place-items-center rounded-[10px] [background:var(--workspace-accent-soft)] text-workspace-accent">
          {icon}
        </span>
      ) : null}
      <h2>{title}</h2>
      <p>{description}</p>
      {action ? (
        <div className="workflows-notice__action mt-[10px]">{action}</div>
      ) : null}
    </section>
  );
}

function WorkflowsSkeleton() {
  return (
    <div className="grid gap-[14px]" aria-busy="true">
      <span className="sr-only" role="status">
        Loading workflows
      </span>
      <span className="animate-pulse workflow-skeleton w-[160px] h-[20px] mt-[10px]" />
      <div className="workflows-featured-grid" aria-hidden="true">
        {[0, 1, 2].map((index) => (
          <span
            key={index}
            className="animate-pulse workflow-skeleton h-[176px]"
          />
        ))}
      </div>
      <span className="animate-pulse workflow-skeleton w-[160px] h-[20px] mt-[10px]" />
      <div className="workflows-grid" aria-hidden="true">
        {[0, 1, 2, 3].map((index) => (
          <span
            key={index}
            className="animate-pulse workflow-skeleton h-[88px]"
          />
        ))}
      </div>
    </div>
  );
}
