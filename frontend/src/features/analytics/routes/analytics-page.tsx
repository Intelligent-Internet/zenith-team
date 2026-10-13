import {
  analyticsApi,
  useGetWorkspaceAnalyticsBreakdownQuery,
  useGetWorkspaceAnalyticsFailuresQuery,
  useGetWorkspaceAnalyticsSeriesQuery,
  useGetWorkspaceAnalyticsSummaryQuery,
  useGetWorkspaceAnalyticsHeatmapQuery,
  useGetWorkspaceAnalyticsBudgetsQuery,
  usePutWorkspaceAnalyticsBudgetMutation,
} from "@/shared/api/analytics-api";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Link, useSearchParams } from "react-router";
import type { EChartsOption } from "echarts";
import {
  Chart2,
  RefreshCw,
  SlidersHorizontal,
  ChevronDown,
  X,
} from "@/shared/ui/icons";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { Tooltip } from "@/shared/ui/tooltip";
import { useCompanyPermissions } from "@/shared/permissions";
import {
  boardlyApi,
  getBoardlyQueryErrorMessage,
} from "@/shared/api/boardly-api";
import { useAppDispatch } from "@/state";

import type {
  AnalyticsBreakdown,
  AnalyticsBudget,
  AnalyticsBudgetInput,
  AnalyticsFailures,
  AnalyticsHeatmap,
  AnalyticsSummary,
} from "@/shared/api/types";
import { AnalyticsBreakdownChart } from "../components/analytics-breakdown-chart";
import { AnalyticsActivityCalendar } from "../components/analytics-activity-calendar";
import { AnalyticsAgentActivity } from "../components/analytics-agent-activity";
import { AnalyticsWorkComposition } from "../components/analytics-work-composition";
import { AnalyticsAgentHeatmap } from "../components/analytics-agent-heatmap";
import { AnalyticsWorkFlow } from "../components/analytics-work-flow";
import { format as chartFormat } from "echarts/core";
import { AnalyticsTrend } from "../components/analytics-trend";
import { AnalyticsEChart } from "../components/analytics-echart";
import { AnalyticsSelect } from "../components/analytics-select";
import { contrastingText } from "@/app/appearance/color";
import { useAnalyticsPalette } from "../model/palette";
import {
  change,
  cost,
  count,
  countLabel,
  dateLabel,
  duration,
  finished,
  money,
  rate,
  sum,
  tokens,
  partLabel,
  type Metrics,
} from "../model/format";
import "../styles.css";

const views = [
  "overview",
  "models",
  "agents",
  "members",
  "projects",
  "computers",
  "reliability",
] as const;
type View = (typeof views)[number];
const ranges = ["today", "7", "30", "90", "180", "custom"];
const viewLabel = (view: View) =>
  view === "projects"
    ? "Projects & Tasks"
    : view === "computers"
      ? "Computers"
      : partLabel(view);

export function AnalyticsPage() {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  return <AnalyticsView key={companyId} companyId={companyId} />;
}

function AnalyticsView({ companyId }: { companyId: string }) {
  const dispatch = useAppDispatch();
  const [params, setParams] = useSearchParams();
  const [denied, setDenied] = useState(false);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const palette = useAnalyticsPalette();
  const pageRef = useRef<HTMLElement | null>(null);
  const canCompany = useCompanyPermissions().can("usage.read") && !denied;
  const scope =
    canCompany && params.get("scope") !== "mine" ? "company" : "mine";
  const requestedTab = params.get("tab") as View;
  const requestedSection =
    views.includes(requestedTab) &&
    !(scope === "mine" && requestedTab === "members")
      ? requestedTab
      : "overview";
  const availableViews = useMemo(
    () => views.filter((view) => view !== "members" || scope === "company"),
    [scope],
  );
  const range = ranges.includes(params.get("range") ?? "")
    ? params.get("range")!
    : "30";
  const start = params.get("start"),
    end = params.get("end");
  const period = useMemo(
    () => windowFor(range, start, end),
    [range, start, end],
  );
  const days = period.days;
  const grains =
    days <= 90
      ? days >= 30
        ? ["day", "week"]
        : ["day"]
      : days > 180
        ? ["week", "month"]
        : ["week"];
  const grain = grains.includes(params.get("grain") ?? "")
    ? params.get("grain")!
    : grains[0];
  const metric = ["cost", "tokens", "runs", "run_time"].includes(
    params.get("metric") ?? "",
  )
    ? params.get("metric")!
    : "runs";
  const sort = ["cost", "tokens", "runs", "run_time", "failures"].includes(
    params.get("sort") ?? "",
  )
    ? params.get("sort")!
    : "tokens";
  const update = (key: string, value: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (value) next.set(key, value);
        else next.delete(key);
        if (["agent", "member", "project", "runtime"].includes(key))
          next.delete(`${key}_id`);
        next.delete("cursor");
        if (key === "scope") {
          next.delete("member");
          next.delete("member_id");
          if (value === "mine" && next.get("tab") === "members")
            next.set("tab", "overview");
        }
        return next;
      },
      { replace: true },
    );
  const filters = {
    provider: params.get("provider") || undefined,
    model: params.get("model") || undefined,
    agentId: params.get("agent") || params.get("agent_id") || undefined,
    memberId:
      scope === "company"
        ? params.get("member") || params.get("member_id") || undefined
        : undefined,
    projectId: params.get("project") || params.get("project_id") || undefined,
    taskId: params.get("task") || undefined,
    runType: params.get("run_type") || undefined,
    runtimeId: params.get("runtime") || params.get("runtime_id") || undefined,
  };
  const filterCount = Object.values(filters).filter(Boolean).length;
  const base = {
    companyId,
    scope,
    ...period.window,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    ...filters,
  } as const;
  const skip = !companyId || !!period.error;
  const summary = useGetWorkspaceAnalyticsSummaryQuery(base, { skip });
  const series = useGetWorkspaceAnalyticsSeriesQuery(
    {
      ...base,
      metric,
      bucket: grain,
    },
    { skip: skip || !["overview", "models"].includes(requestedSection) },
  );
  const failureSeries = useGetWorkspaceAnalyticsSeriesQuery(
    {
      ...base,
      metric: "failures",
      bucket: grain,
    },
    { skip: skip || requestedSection !== "reliability" },
  );
  const failures = useGetWorkspaceAnalyticsFailuresQuery(base, {
    skip: skip || requestedSection !== "reliability",
  });
  const heatmap = useGetWorkspaceAnalyticsHeatmapQuery(base, {
    skip: skip || requestedSection !== "computers",
  });
  const budgets = useGetWorkspaceAnalyticsBudgetsQuery(companyId, {
    skip: skip || scope !== "company" || requestedSection !== "overview",
  });
  const [saveBudget, budgetMutation] = usePutWorkspaceAnalyticsBudgetMutation();
  const taskRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "task", sort, limit: 25 },
    { skip: skip || !["projects", "agents"].includes(requestedSection) },
  );
  const agentRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "agent", sort, limit: 5 },
    { skip: skip || !["overview", "agents"].includes(requestedSection) },
  );
  const runTypeRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "run_type", sort, limit: 5 },
    { skip: skip || requestedSection !== "projects" },
  );
  const modelRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "model", sort, limit: 5 },
    { skip: skip || !["overview", "models"].includes(requestedSection) },
  );
  const memberRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "member", sort, limit: 25 },
    { skip: skip || scope !== "company" || requestedSection !== "members" },
  );
  const projectRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "project", sort, limit: 25 },
    { skip: skip || requestedSection !== "projects" },
  );
  const runtimeRows = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...base, by: "runtime", sort, limit: 25 },
    { skip: skip || requestedSection !== "computers" },
  );
  const filterBase = {
    companyId,
    scope,
    ...period.window,
    timeZone: base.timeZone,
    sort: "tokens",
    limit: 100,
  } as const;
  const agentOptions = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...filterBase, by: "agent" },
    { skip },
  );
  const projectOptions = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...filterBase, by: "project" },
    { skip },
  );
  const memberOptions = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...filterBase, by: "member" },
    { skip: skip || scope !== "company" },
  );
  const runtimeOptions = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...filterBase, by: "runtime" },
    { skip },
  );
  const filterLabels: Record<string, string | undefined> = {
    agentId: agentOptions.currentData?.rows?.find(
      (row) => row.key?.id === filters.agentId,
    )?.key?.label,
    memberId: memberOptions.currentData?.rows?.find(
      (row) => row.key?.id === filters.memberId,
    )?.key?.label,
    projectId: projectOptions.currentData?.rows?.find(
      (row) => row.key?.id === filters.projectId,
    )?.key?.label,
    runtimeId: runtimeOptions.currentData?.rows?.find(
      (row) => row.key?.id === filters.runtimeId,
    )?.key?.label,
  };
  const data =
    summary.currentData ??
    (summary.isFetching && summary.data?.scope === scope
      ? summary.data
      : undefined);
  const error =
    summary.error ||
    heatmap.error ||
    agentOptions.error ||
    projectOptions.error ||
    memberOptions.error ||
    failures.error ||
    series.error ||
    failureSeries.error ||
    taskRows.error ||
    runTypeRows.error ||
    agentRows.error ||
    modelRows.error ||
    memberRows.error ||
    projectRows.error ||
    runtimeRows.error ||
    runtimeOptions.error;
  useEffect(() => {
    if (
      summary.error &&
      "status" in summary.error &&
      summary.error.status === 403 &&
      scope === "company"
    )
      setDenied(true);
  }, [summary.error, scope]);
  const refresh = () => {
    dispatch(
      boardlyApi.util.invalidateTags([{ type: "Analytics", id: companyId }]),
    );
    if (!agentOptions.isUninitialized) void agentOptions.refetch();
    if (!projectOptions.isUninitialized) void projectOptions.refetch();
    if (!memberOptions.isUninitialized) void memberOptions.refetch();
    if (!runtimeOptions.isUninitialized) void runtimeOptions.refetch();
  };
  const selectView = (view: View) =>
    setParams((current) => {
      const next = new URLSearchParams(current);
      next.set("tab", view);
      next.delete("cursor");
      return next;
    });
  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const index = availableViews.indexOf(requestedSection);
    const nextIndex =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? availableViews.length - 1
          : (index +
              (event.key === "ArrowRight" ? 1 : -1) +
              availableViews.length) %
            availableViews.length;
    const view = availableViews[nextIndex];
    selectView(view);
    event.currentTarget.parentElement
      ?.querySelector<HTMLButtonElement>(`#analytics-tab-${view}`)
      ?.focus();
  };
  useEffect(() => {
    pageRef.current?.scrollTo({ top: 0, behavior: "instant" });
  }, [requestedSection]);
  const clearFilters = () =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        for (const key of [
          "project",
          "project_id",
          "agent",
          "agent_id",
          "runtime",
          "runtime_id",
          "member",
          "member_id",
          "task",
          "model",
          "provider",
          "run_type",
          "cursor",
        ])
          next.delete(key);
        return next;
      },
      { replace: true },
    );
  return (
    <main
      ref={pageRef}
      className="analytics-page"
      aria-busy={summary.isFetching}
      data-testid="analytics-page"
      style={palette.cssVariables}
    >
      <header className="analytics-header">
        <div>
          <h1>
            <Chart2 size={23} />
            Analytics
          </h1>
          <p>{base.timeZone}</p>
        </div>
        <div className="analytics-controls">
          {canCompany ? (
            <div role="group" aria-label="Analytics scope">
              {(["company", "mine"] as const).map((value) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={scope === value}
                  onClick={() => update("scope", value)}
                >
                  {partLabel(value)}
                </button>
              ))}
            </div>
          ) : null}
          <span
            className="analytics-updated"
            title={data ? dateLabel(data.asOf) : undefined}
          >
            <i aria-hidden="true" />
            {summary.isFetching ? "Updating…" : "Up to date"}
          </span>
          <button
            type="button"
            aria-label="Refresh analytics"
            disabled={skip || summary.isFetching}
            onClick={refresh}
          >
            <RefreshCw size={15} />
          </button>
        </div>
      </header>
      {denied ? (
        <p role="status">Company access changed. Showing your work.</p>
      ) : null}
      <section
        className="[border:0] [background:transparent]"
        aria-label="Analytics filters"
      >
        <div className="flex items-center justify-between gap-[12px] flex-wrap">
          <div className="analytics-period-controls">
            <label>
              Range
              <AnalyticsSelect
                aria-label="Analytics range"
                value={range}
                onChange={(event) => update("range", event.target.value)}
              >
                {ranges.map((value) => (
                  <option key={value} value={value}>
                    {value === "today"
                      ? "Today"
                      : value === "custom"
                        ? "Custom"
                        : `Last ${value} days`}
                  </option>
                ))}
              </AnalyticsSelect>
            </label>
            {range === "custom" ? (
              <>
                <label>
                  Start
                  <input
                    type="date"
                    value={start ?? ""}
                    onChange={(event) => update("start", event.target.value)}
                  />
                </label>
                <label>
                  End (inclusive)
                  <input
                    type="date"
                    value={end ?? ""}
                    onChange={(event) => update("end", event.target.value)}
                  />
                </label>
              </>
            ) : null}
            {["overview", "models"].includes(requestedSection) ? (
              <label>
                Metric
                <AnalyticsSelect
                  aria-label="Analytics metric"
                  value={metric}
                  onChange={(event) => update("metric", event.target.value)}
                >
                  {["runs", "tokens", "run_time", "cost"].map((value) => (
                    <option key={value} value={value}>
                      {partLabel(value)}
                    </option>
                  ))}
                </AnalyticsSelect>
              </label>
            ) : null}
            {["overview", "models", "agents", "reliability"].includes(
              requestedSection,
            ) ? (
              <label>
                Group by
                <AnalyticsSelect
                  value={grain}
                  onChange={(event) => update("grain", event.target.value)}
                >
                  {grains.map((value) => (
                    <option key={value} value={value}>
                      {value === "day"
                        ? "Daily"
                        : value === "week"
                          ? "Weekly"
                          : "Monthly"}
                    </option>
                  ))}
                </AnalyticsSelect>
              </label>
            ) : null}
          </div>
          <button
            type="button"
            className="inline-flex items-center gap-[8px]"
            aria-expanded={filtersOpen}
            aria-controls="analytics-advanced-filters"
            onClick={() => setFiltersOpen(!filtersOpen)}
          >
            <SlidersHorizontal size={16} />
            Filters
            {filterCount ? (
              <span className="grid min-w-[18px] h-[18px] place-items-center rounded-[4px] [background:var(--analytics-control)] text-workspace-caption">
                {filterCount}
              </span>
            ) : null}
          </button>
        </div>
        <div id="analytics-advanced-filters" hidden={!filtersOpen}>
          <div className="analytics-toolbar">
            <DimensionFilter
              label="Project"
              value={filters.projectId}
              rows={projectOptions.currentData}
              onChange={(value) => update("project", value)}
            />
            <DimensionFilter
              label="Agent"
              value={filters.agentId}
              rows={agentOptions.currentData}
              onChange={(value) => update("agent", value)}
            />
            <DimensionFilter
              label="Runtime"
              value={filters.runtimeId}
              rows={runtimeOptions.currentData}
              onChange={(value) => update("runtime", value)}
            />
            {scope === "company" ? (
              <DimensionFilter
                label="Member"
                value={filters.memberId}
                rows={memberOptions.currentData}
                onChange={(value) => update("member", value)}
              />
            ) : null}
            <label>
              Provider
              <AnalyticsSelect
                value={filters.provider ?? ""}
                onChange={(event) => update("provider", event.target.value)}
              >
                <option value="">All providers</option>
                <option value="claude">Claude</option>
                <option value="codex">Codex</option>
              </AnalyticsSelect>
            </label>
            <label>
              Model
              <input
                key={filters.model ?? ""}
                defaultValue={filters.model ?? ""}
                placeholder="Exact model name"
                title="Press Enter or leave this field to apply"
                onBlur={(event) => update("model", event.target.value.trim())}
                onKeyDown={(event) => {
                  if (event.key === "Enter") event.currentTarget.blur();
                }}
              />
            </label>
            <label>
              Run type
              <AnalyticsSelect
                value={filters.runType ?? ""}
                onChange={(event) => update("run_type", event.target.value)}
              >
                <option value="">All types</option>
                <option value="task">Task</option>
                <option value="direct_chat">Direct chat</option>
                <option value="schedule">Schedule</option>
                <option value="channel_agent">Channel chat</option>
              </AnalyticsSelect>
            </label>
          </div>
        </div>
      </section>
      <nav
        className="analytics-section-nav"
        role="tablist"
        aria-label="Analytics sections"
      >
        {availableViews.map((view) => (
          <button
            key={view}
            type="button"
            role="tab"
            id={`analytics-tab-${view}`}
            aria-controls={`analytics-${view}`}
            aria-selected={view === requestedSection}
            tabIndex={view === requestedSection ? 0 : -1}
            onClick={() => selectView(view)}
            onKeyDown={onTabKeyDown}
          >
            {viewLabel(view)}
          </button>
        ))}
      </nav>
      {filterCount ? (
        <div className="analytics-chips flex gap-[7px] flex-wrap mt-[12px]">
          {Object.entries(filters)
            .filter(([, value]) => value)
            .map(([key, value]) => (
              <button
                type="button"
                key={key}
                aria-label={`Remove ${partLabel(key.replace("Id", ""))} filter`}
                onClick={() => {
                  const canonical = key.replace("Id", "");
                  setParams(
                    (current) => {
                      const next = new URLSearchParams(current);
                      next.delete(canonical);
                      next.delete(key.replace("Id", "_id"));
                      if (key === "runType") next.delete("run_type");
                      return next;
                    },
                    { replace: true },
                  );
                }}
              >
                {partLabel(key.replace("Id", ""))}: {filterLabels[key] ?? value}
                <X size={12} aria-hidden="true" />
              </button>
            ))}
          <button type="button" onClick={clearFilters}>
            Clear filters
          </button>
        </div>
      ) : null}
      {period.error ? <p role="alert">{period.error}</p> : null}
      {error ? (
        <p
          role="alert"
          className="mt-[16px] p-[12px_16px] [border:1px_solid_color-mix(in_srgb,_var(--analytics-danger)_35%,_transparent)] rounded-workspace-panel text-[var(--analytics-danger)] text-workspace-caption"
        >
          {getBoardlyQueryErrorMessage(error)}{" "}
          {"body" in error && error.body?.requestId
            ? `Request ${error.body.requestId}`
            : ""}{" "}
          <button type="button" onClick={refresh}>
            Retry
          </button>
        </p>
      ) : null}
      {!skip && !data ? (
        <div
          className="grid h-[240px] place-items-center text-[var(--analytics-muted)] text-workspace-caption"
          role="status"
        >
          Loading analytics…
        </div>
      ) : null}
      {data ? (
        <>
          <Notices metrics={data.current} />
          {data.current?.runs?.total === "0" ? (
            <section className="analytics-card">
              <h2>
                {Object.values(filters).some(Boolean)
                  ? "No Runs match these filters"
                  : "Analytics fills in as your Agents work"}
              </h2>
              <Link to="/app">Start a chat</Link>{" "}
              <button type="button" onClick={clearFilters}>
                Clear filters
              </button>
            </section>
          ) : null}

          <AnalyticsSection
            view="overview"
            title="Overview"
            active={requestedSection === "overview"}
          >
            <MetricTiles data={data} />
            {data.current?.runs?.total !== "0" ? (
              <>
                <AnalyticsTrend
                  title={`${partLabel(metric)} over time`}
                  series={series.currentData}
                  height={260}
                />
                <div className="analytics-overview-grid">
                  <AnalyticsAgentActivity
                    query={{ ...base, metric: sort, bucket: grain }}
                    data={agentRows.currentData}
                    onMetricChange={(value) => update("sort", value)}
                  />
                  <BreakdownTable
                    title="Top models"
                    metric={sort}
                    data={modelRows.currentData}
                    query={{ ...base, by: "model", sort, limit: 5 }}
                    apply={update}
                  />
                </div>
                <AnalyticsActivityCalendar
                  key={`${companyId}:${scope}:${base.startAt}:${base.endAt}:${base.timeZone}`}
                  query={base}
                  metric={metric}
                />
              </>
            ) : null}
            {scope === "company" && canCompany ? (
              <BudgetCard
                companyId={companyId}
                budgets={budgets.currentData?.items ?? []}
                agentRows={agentOptions.currentData}
                saving={budgetMutation.isLoading}
                saveError={budgetMutation.error}
                onSave={(payload) => void saveBudget({ companyId, payload })}
              />
            ) : null}
          </AnalyticsSection>

          <AnalyticsSection
            view="models"
            controls={
              <SectionSortControl
                sort={sort}
                onChange={(value) => update("sort", value)}
              />
            }
            title="Models"
            active={requestedSection === "models"}
          >
            <div className="analytics-pair-grid analytics-models-grid">
              <BreakdownTable
                title="Models"
                metric={sort}
                data={modelRows.currentData}
                query={{ ...base, by: "model", sort, limit: 5 }}
                apply={update}
              />
              <ModelRelationshipSunburst data={modelRows.currentData} />
            </div>
            <AnalyticsTrend
              title="Model usage over time"
              series={series.currentData}
            />
            <section className="analytics-card analytics-inline-summary">
              <h2>Cache</h2>
              <p>
                Hit rate{" "}
                {rate(
                  data.current?.tokens?.cacheRead,
                  sum(
                    data.current?.tokens?.input,
                    data.current?.tokens?.cacheRead,
                  ),
                )}{" "}
                · {countLabel(data.current?.tokens?.cacheRead, "cache read")} ·{" "}
                {countLabel(data.current?.tokens?.cacheWrite, "cache write")}
              </p>
            </section>
          </AnalyticsSection>

          <AnalyticsSection
            view="agents"
            controls={
              <SectionSortControl
                sort={sort}
                onChange={(value) => update("sort", value)}
              />
            }
            title="Agents"
            active={requestedSection === "agents"}
          >
            <BreakdownTable
              title="Agent performance"
              metric={sort}
              data={agentRows.currentData}
              query={{ ...base, by: "agent", sort, limit: 5 }}
              apply={update}
            >
              <AnalyticsAgentHeatmap
                data={agentRows.currentData}
                query={{ ...base, metric: sort, bucket: grain }}
              />
            </BreakdownTable>
            {filters.agentId ? (
              <>
                <Link to={`/agents/${filters.agentId}`}>
                  Open Agent profile
                </Link>
                <BreakdownTable
                  title="Top Tasks"
                  metric={sort}
                  data={taskRows.currentData}
                  query={{ ...base, by: "task", sort, limit: 25 }}
                  apply={update}
                />
              </>
            ) : null}
          </AnalyticsSection>

          {scope === "company" ? (
            <AnalyticsSection
              view="members"
              title="Members"
              active={requestedSection === "members"}
              controls={
                <SectionSortControl
                  sort={sort}
                  onChange={(value) => update("sort", value)}
                />
              }
            >
              <BreakdownTable
                title="Members"
                metric={sort}
                data={memberRows.currentData}
                query={{ ...base, by: "member", sort, limit: 25 }}
                apply={update}
              />
            </AnalyticsSection>
          ) : null}
          <AnalyticsSection
            view="projects"
            title="Projects & Tasks"
            active={requestedSection === "projects"}
            controls={
              <SectionSortControl
                sort={sort}
                onChange={(value) => update("sort", value)}
              />
            }
          >
            <div className="analytics-pair-grid">
              <BreakdownTable
                title="Projects"
                metric={sort}
                data={projectRows.currentData}
                query={{ ...base, by: "project", sort, limit: 25 }}
                apply={update}
              />
              <AnalyticsWorkComposition
                metric={sort}
                data={taskRows.currentData}
                runTypes={runTypeRows.currentData}
              />
            </div>
            <BreakdownTable
              title="Tasks & other work"
              metric={sort}
              data={taskRows.currentData}
              query={{ ...base, by: "task", sort, limit: 25 }}
              apply={update}
            >
              <AnalyticsWorkFlow
                data={taskRows.currentData}
                runTypes={runTypeRows.currentData}
                metric={sort}
              />
            </BreakdownTable>
          </AnalyticsSection>

          <AnalyticsSection
            view="computers"
            controls={
              <SectionSortControl
                sort={sort}
                onChange={(value) => update("sort", value)}
              />
            }
            title="Computers"
            active={requestedSection === "computers"}
          >
            <div className="analytics-pair-grid analytics-computers-grid">
              <BreakdownTable
                title="Computers"
                metric={sort}
                data={runtimeRows.currentData}
                query={{ ...base, by: "runtime", sort, limit: 25 }}
                apply={update}
              />
              <AnalyticsHeatmapView data={heatmap.currentData} />
            </div>
          </AnalyticsSection>

          <AnalyticsSection
            view="reliability"
            title="Reliability"
            active={requestedSection === "reliability"}
          >
            <AnalyticsTrend
              title="Failures over time"
              series={failureSeries.currentData}
            />
            <Reliability
              failures={failures.currentData}
              metrics={data.current}
            />
          </AnalyticsSection>
        </>
      ) : null}
    </main>
  );
}

function AnalyticsSection({
  view,
  title,
  active,
  controls,
  children,
}: {
  view: View;
  title: string;
  active: boolean;
  controls?: ReactNode;
  children: ReactNode;
}) {
  if (!active) return null;
  const headingId = `analytics-${view}-heading`;
  return (
    <section
      id={`analytics-${view}`}
      role="tabpanel"
      tabIndex={0}
      data-analytics-section={view}
      className="analytics-section"
      aria-labelledby={`analytics-tab-${view}`}
    >
      <header className="analytics-section-heading">
        <h2 id={headingId}>{title}</h2>
        {controls}
      </header>
      {children}
    </section>
  );
}

function SectionSortControl({
  sort,
  onChange,
}: {
  sort: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="analytics-section-controls flex justify-end m-0">
      <label>
        Sort by
        <AnalyticsSelect
          value={sort}
          onChange={(event) => onChange(event.target.value)}
        >
          {["tokens", "cost", "runs", "run_time", "failures"].map((value) => (
            <option key={value} value={value}>
              {partLabel(value)}
            </option>
          ))}
        </AnalyticsSelect>
      </label>
    </div>
  );
}

function DimensionFilter({
  label,
  value,
  rows,
  onChange,
}: {
  label: string;
  value?: string;
  rows?: AnalyticsBreakdown;
  onChange: (value: string) => void;
}) {
  return (
    <label>
      {label}
      <AnalyticsSelect
        value={value ?? ""}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">All {label.toLowerCase()}s</option>
        {value && !rows?.rows?.some((row) => row.key?.id === value) ? (
          <option value={value}>{value}</option>
        ) : null}
        {rows?.rows
          ?.filter((row) => row.key?.id)
          .map((row) => (
            <option key={row.key!.id} value={row.key!.id!}>
              {row.key!.label}
            </option>
          ))}
      </AnalyticsSelect>
    </label>
  );
}

function BudgetCard({
  companyId: _companyId,
  budgets,
  agentRows,
  saving,
  saveError,
  onSave,
}: {
  companyId: string;
  budgets: AnalyticsBudget[];
  agentRows?: AnalyticsBreakdown;
  saving: boolean;
  saveError?: unknown;
  onSave: (payload: AnalyticsBudgetInput) => void;
}) {
  const companyBudget = budgets.find(
    (budget) => budget.ownerKind === "company",
  );
  const [ownerKind, setOwnerKind] = useState<"company" | "agent">("company");
  const [agentId, setAgentId] = useState<string>("");
  const [dollars, setDollars] = useState(
    companyBudget ? ticksToDollars(companyBudget.monthlyLimitUsdTicks) : "400",
  );
  const selected = budgets.find(
    (budget) =>
      budget.ownerKind === ownerKind &&
      (ownerKind === "company" || budget.agentId === agentId),
  );
  useEffect(() => {
    setDollars(
      selected ? ticksToDollars(selected.monthlyLimitUsdTicks) : "400",
    );
  }, [selected?.monthlyLimitUsdTicks, ownerKind, agentId]);
  const validAmount = dollarsToTicks(dollars);
  const unchanged = selected?.monthlyLimitUsdTicks === validAmount;
  const submit = () => {
    const ticks = dollarsToTicks(dollars);
    if (!ticks || (ownerKind === "agent" && !agentId)) return;
    onSave({
      ownerKind,
      agentId: ownerKind === "agent" ? agentId : null,
      monthlyLimitUsdTicks: ticks,
      enabled: selected?.enabled ?? true,
    });
  };
  return (
    <details className="analytics-card analytics-budget-card">
      <summary>
        <span>Budget alerts</span>
        <span className="analytics-muted">
          {companyBudget
            ? `${money(companyBudget.monthlyLimitUsdTicks)} / month`
            : "Set a soft spending limit"}
        </span>
        <ChevronDown size={15} />
      </summary>
      <div className="analytics-card-heading">
        <div>
          <h2>Soft budget alerts</h2>
          <p>Inbox alerts at 80% and 100%. Runs are never blocked.</p>
        </div>
        {saveError ? (
          <span
            role="alert"
            className="mt-[16px] p-[12px_16px] [border:1px_solid_color-mix(in_srgb,_var(--analytics-danger)_35%,_transparent)] rounded-workspace-panel text-[var(--analytics-danger)] text-workspace-caption"
          >
            {getBoardlyQueryErrorMessage(saveError)}
          </span>
        ) : unchanged ? (
          <span className="analytics-muted">Saved</span>
        ) : null}
      </div>
      <div className="analytics-controls">
        <label>
          Owner
          <AnalyticsSelect
            value={ownerKind}
            onChange={(event) =>
              setOwnerKind(event.target.value as "company" | "agent")
            }
          >
            <option value="company">Company</option>
            <option value="agent">Agent</option>
          </AnalyticsSelect>
        </label>
        {ownerKind === "agent" ? (
          <label>
            Agent
            <AnalyticsSelect
              value={agentId}
              onChange={(event) => setAgentId(event.target.value)}
            >
              <option value="">Choose an Agent</option>
              {agentRows?.rows
                ?.filter((row) => row.key?.id)
                .map((row) => (
                  <option key={row.key!.id} value={row.key!.id!}>
                    {row.key!.label}
                  </option>
                ))}
            </AnalyticsSelect>
          </label>
        ) : null}
        <label>
          Monthly limit (USD)
          <input
            inputMode="decimal"
            value={dollars}
            onChange={(event) => setDollars(event.target.value)}
          />
        </label>
        <button
          type="button"
          disabled={
            saving ||
            !validAmount ||
            unchanged ||
            (ownerKind === "agent" && !agentId)
          }
          onClick={submit}
        >
          {saving ? "Saving…" : "Save budget"}
        </button>
      </div>
    </details>
  );
}

function dollarsToTicks(value: string): string | null {
  const normalized = value.trim();
  if (!/^\d+(\.\d{0,10})?$/.test(normalized)) return null;
  const [whole, fraction = ""] = normalized.split(".");
  const ticks =
    BigInt(whole) * 10_000_000_000n +
    BigInt((fraction + "0000000000").slice(0, 10));
  return ticks > 0n ? ticks.toString() : null;
}

function ticksToDollars(value: string): string {
  const ticks = BigInt(value || "0");
  const whole = ticks / 10_000_000_000n;
  const fraction = (ticks % 10_000_000_000n)
    .toString()
    .padStart(10, "0")
    .slice(0, 2);
  return `${whole}.${fraction}`;
}

function AnalyticsHeatmapView({ data }: { data?: AnalyticsHeatmap }) {
  const palette = useAnalyticsPalette();
  const cells = data?.cells ?? [];
  const max = cells.reduce(
    (highest, cell) => Math.max(highest, Number(cell.runs)),
    0,
  );
  const option = useMemo<EChartsOption>(
    () => ({
      animationDuration: 700,
      animationEasing: "cubicOut",
      tooltip: {
        position: "top",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          if (!params || typeof params !== "object") return "";
          const item = params as { value?: [number, number, number] };
          const [hour, day, runs] = item.value ?? [0, 0, 0];
          return `<strong>${weekdayLabel(day)} ${hour}:00</strong><br/>${countLabel(runs, "Run")}`;
        },
      },
      grid: { top: 12, right: 84, bottom: 28, left: 52, containLabel: true },
      xAxis: {
        type: "category",
        data: Array.from({ length: 24 }, (_, hour) => String(hour)),
        splitArea: {
          show: true,
          areaStyle: {
            color: [palette.canvas, palette.fillSoft],
          },
        },
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: palette.muted, fontSize: 10 },
      },
      yAxis: {
        type: "category",
        data: Array.from({ length: 7 }, (_, day) => weekdayLabel(day)),
        splitArea: {
          show: true,
          areaStyle: {
            color: [palette.canvas, palette.fillSoft],
          },
        },
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: palette.muted, fontSize: 10 },
      },
      visualMap: {
        min: 0,
        max: Math.max(max, 1),
        calculable: true,
        orient: "vertical",
        right: 0,
        top: "middle",
        itemHeight: 120,
        textStyle: { color: palette.muted, fontSize: 10 },
        inRange: { color: palette.heatmap },
      },
      series: [
        {
          type: "heatmap",
          data: cells.map((cell, index) => [
            index % 24,
            Math.floor(index / 24),
            Number(cell.runs),
          ]),
          itemStyle: {
            borderColor: palette.canvas,
            borderWidth: 2,
            borderRadius: 4,
          },
          emphasis: {
            itemStyle: { shadowBlur: 14, shadowColor: palette.shadow },
          },
        },
      ],
    }),
    [cells, max, palette],
  );
  return (
    <section className="analytics-card">
      <h2>When Runs happen</h2>
      {!data ? (
        <div
          className="grid place-items-center min-h-[320px] text-[var(--analytics-muted)]"
          role="status"
        >
          Loading heatmap…
        </div>
      ) : (
        <AnalyticsEChart
          option={option}
          ariaLabel="Interactive heatmap showing Runs by weekday and hour. Hover a cell to inspect activity."
          className="analytics-heatmap-chart"
          height={300}
        />
      )}
    </section>
  );
}

function weekdayLabel(day: number) {
  return ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"][day] ?? "";
}

function MetricTiles({ data }: { data: AnalyticsSummary }) {
  const m = data.current,
    p = data.comparison;
  const costValue = cost(m?.cost, m?.runs?.withoutUsage);
  const cards = [
    [
      "Runs",
      count(m?.runs?.total),
      change(m?.runs?.total, p?.runs?.total),
      `${count(m?.runs?.completed)} completed · ${count(data.activeNow)} active now`,
    ],
    [
      "Tokens",
      tokens(m),
      change(
        sum(m?.tokens?.input, m?.tokens?.output),
        sum(p?.tokens?.input, p?.tokens?.output),
      ),
      `Input ${count(m?.tokens?.input)} · Output ${count(m?.tokens?.output)}`,
    ],
    [
      "Run time",
      duration(m?.time?.runTimeMs),
      change(m?.time?.runTimeMs, p?.time?.runTimeMs),
      `p50 ${duration(m?.time?.p50RunTimeMs)} · p90 ${duration(m?.time?.p90RunTimeMs)}`,
    ],
    [
      "Failure rate",
      rate(m?.runs?.failed, finished(m)),
      rateChange(m, p),
      `${count(m?.runs?.failed)} of ${countLabel(finished(m), "finished Run")}`,
    ],
    [
      "Cost",
      costValue === "Unknown" ? "—" : costValue,
      change(
        m?.cost?.reportedUsdTicks == null && m?.cost?.estimatedUsdTicks == null
          ? null
          : sum(m?.cost?.reportedUsdTicks, m?.cost?.estimatedUsdTicks),
        p?.cost?.reportedUsdTicks == null && p?.cost?.estimatedUsdTicks == null
          ? null
          : sum(p?.cost?.reportedUsdTicks, p?.cost?.estimatedUsdTicks),
      ),
      costValue === "Unknown"
        ? "Cost data is not available for this period."
        : `Reported ${money(m?.cost?.reportedUsdTicks)} · Estimated ${money(m?.cost?.estimatedUsdTicks)}`,
    ],
  ];
  return (
    <section className="analytics-kpis" aria-label="Analytics summary">
      {cards.map(([label, value, delta, detail], index) => (
        <Tooltip key={label} label={`${delta}. ${detail}`}>
          <article
            className={`analytics-kpi analytics-kpi--${index}`}
            tabIndex={0}
          >
            <h2>{label}</h2>
            <strong>{value}</strong>
            <span>
              {label === "Cost" && value === "—"
                ? "Cost unavailable"
                : delta.replace(" vs previous", "")}
            </span>
          </article>
        </Tooltip>
      ))}
    </section>
  );
}

function rateChange(m?: Metrics, p?: Metrics) {
  const a = BigInt(finished(m)),
    b = BigInt(finished(p));
  if (!a || !b) return "—";
  const diff =
    Number(
      (BigInt(m?.runs?.failed ?? "0") * 1000n) / a -
        (BigInt(p?.runs?.failed ?? "0") * 1000n) / b,
    ) / 10;
  return `${diff >= 0 ? "+" : ""}${diff.toFixed(1)} pts vs previous`;
}
function Notices({ metrics: m }: { metrics?: Metrics }) {
  const notes: string[] = [];
  if (m?.runs?.withoutUsage !== "0" && m?.runs?.withoutUsage)
    notes.push(`${countLabel(m.runs.withoutUsage, "Run")} reported no usage`);
  if (m?.runs?.withoutTiming !== "0" && m?.runs?.withoutTiming)
    notes.push(`${countLabel(m.runs.withoutTiming, "Run")} without timing`);
  if (m?.cost?.uncostedFactCount !== "0" && m?.cost?.uncostedFactCount)
    notes.push(`${countLabel(m.cost.uncostedFactCount, "cost fact")} unknown`);
  if (BigInt(m?.cost?.planCoveredTokens ?? "0"))
    notes.push(countLabel(m?.cost?.planCoveredTokens, "plan-covered token"));
  return notes.length || m?.cost?.unpricedModels?.length ? (
    <details className="analytics-notice">
      <summary>
        <span>Partial data</span>
        <ChevronDown size={14} />
      </summary>
      <p>{notes.join(" · ")}</p>
      <p>
        Missing usage is excluded from token totals. Historical Runs without
        start times are excluded from durations. Plan-covered usage adds no
        dollars.
      </p>
      {m?.cost?.unpricedModels?.map((model) => (
        <p key={`${model.provider}/${model.model}`}>
          No price for {model.provider} / {model.model}. Its cost remains
          unknown until the dated catalog includes this model.
        </p>
      ))}
    </details>
  ) : null;
}
type BreakdownTableProps = {
  title: string;
  data?: AnalyticsBreakdown;
  query: Parameters<
    typeof analyticsApi.endpoints.getWorkspaceAnalyticsBreakdown.initiate
  >[0];
  apply: (key: string, value: string) => void;
  metric?: string;
  children?: ReactNode;
};

function BreakdownTable({
  title,
  data,
  query,
  apply,
  metric = "tokens",
  children,
}: BreakdownTableProps) {
  const dispatch = useAppDispatch();
  const [details, setDetails] = useState(false);
  const queryKey = JSON.stringify(query);
  const [pagination, setPagination] = useState({
    queryKey,
    cursors: [undefined] as (string | undefined)[],
  });
  if (pagination.queryKey !== queryKey) {
    setPagination({ queryKey, cursors: [undefined] });
  }
  const cursors =
    pagination.queryKey === queryKey ? pagination.cursors : [undefined];
  const page = useGetWorkspaceAnalyticsBreakdownQuery(
    { ...query, cursor: cursors.at(-1) },
    { skip: !details },
  );
  const displayed = details ? page.currentData : data;
  const rows = displayed?.rows ?? [];
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string>();
  const exportGeneration = useRef(0);
  useEffect(() => {
    setExporting(false);
    setExportError(undefined);
    return () => {
      exportGeneration.current++;
    };
  }, [queryKey]);

  const exportAll = async () => {
    const generation = ++exportGeneration.current;
    setExporting(true);
    setExportError(undefined);
    try {
      const exportedRows: NonNullable<AnalyticsBreakdown["rows"]> = [];
      let cursor: string | undefined;
      do {
        const result = await dispatch(
          analyticsApi.endpoints.getWorkspaceAnalyticsBreakdown.initiate(
            { ...query, limit: 100, cursor },
            { subscribe: false, forceRefetch: true },
          ),
        ).unwrap();
        if (generation !== exportGeneration.current) return;
        exportedRows.push(...(result.rows ?? []));
        cursor = result.nextCursor ?? undefined;
      } while (cursor);
      exportBreakdownCsv(title, exportedRows);
    } catch (error) {
      if (generation === exportGeneration.current)
        setExportError(getBoardlyQueryErrorMessage(error));
    } finally {
      if (generation === exportGeneration.current) setExporting(false);
    }
  };

  return (
    <section
      className={`analytics-card analytics-ranking-card${children ? " analytics-detail-visual" : ""}`}
    >
      <div className="analytics-card-heading">
        <div>
          {!children ? (
            <span className="block mb-[5px] text-[var(--analytics-muted)] text-[length:12px] font-[600] tracking-[0.1em] leading-[1.4] uppercase">
              {partLabel(metric)}
            </span>
          ) : null}
          <h2>{title}</h2>
        </div>
        <div className="analytics-controls">
          <button
            type="button"
            disabled={exporting || !data?.rows?.length}
            title="Export all matching rows"
            onClick={() => void exportAll()}
          >
            {exporting ? "Exporting…" : "Export CSV"}
          </button>
          <button
            type="button"
            aria-pressed={details}
            onClick={() => setDetails(!details)}
          >
            {details ? "Chart" : "Details"}
          </button>
        </div>
      </div>
      {exportError ? <p role="alert">{exportError}</p> : null}
      {details && page.error ? (
        <p role="alert">
          {getBoardlyQueryErrorMessage(page.error)}{" "}
          <button type="button" onClick={() => void page.refetch()}>
            Retry
          </button>
        </p>
      ) : !displayed ? (
        <div
          className="grid place-items-center min-h-[320px] text-[var(--analytics-muted)]"
          role="status"
        >
          Loading…
        </div>
      ) : !rows.length ? (
        <p>No matching work in this window.</p>
      ) : (
        <>
          {!details
            ? (children ?? (
                <AnalyticsBreakdownChart data={displayed} metric={metric} />
              ))
            : null}
          {details ? (
            <div className="w-full max-w-full overflow-x-auto [scrollbar-width:thin] mt-[14px]">
              <table>
                <thead>
                  <tr>
                    {[
                      "Name",
                      "Runs",
                      "Tokens",
                      "Cost",
                      "Run time",
                      "Failure rate",
                      "Cache hit",
                    ].map((label) => (
                      <th key={label}>{label}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr
                      key={`${row.key?.kind}/${row.key?.id ?? row.key?.label}`}
                    >
                      <th>{breakdownRowLabel(row, apply)}</th>
                      <td>{count(row.runs?.total)}</td>
                      <td>{tokens(row)}</td>
                      <td>{cost(row.cost, row.runs?.withoutUsage)}</td>
                      <td>{duration(row.time?.runTimeMs)}</td>
                      <td>{rate(row.runs?.failed, finished(row))}</td>
                      <td>
                        {rate(
                          row.tokens?.cacheRead,
                          sum(row.tokens?.input, row.tokens?.cacheRead),
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
        </>
      )}
      {details && (cursors.length > 1 || page.currentData?.nextCursor) ? (
        <nav className="analytics-controls" aria-label={`${title} pages`}>
          <button
            type="button"
            disabled={cursors.length === 1 || page.isFetching}
            onClick={() =>
              setPagination({ queryKey, cursors: cursors.slice(0, -1) })
            }
          >
            Previous
          </button>
          <span>Page {cursors.length}</span>
          <button
            type="button"
            disabled={!page.currentData?.nextCursor || page.isFetching}
            onClick={() => {
              const cursor = page.currentData?.nextCursor;
              if (cursor)
                setPagination({ queryKey, cursors: [...cursors, cursor] });
            }}
          >
            Next
          </button>
        </nav>
      ) : null}
    </section>
  );
}

function ModelRelationshipSunburst({ data }: { data?: AnalyticsBreakdown }) {
  const palette = useAnalyticsPalette();
  const [selected, setSelected] = useState<string | null>(null);
  const rows = data?.rows ?? [];
  const hierarchy = useMemo(() => {
    const grouped = new Map<
      string,
      {
        value: number;
        color: string;
        children: Array<{
          name: string;
          value: number;
          path: string;
          color: string;
        }>;
      }
    >();
    for (const row of rows.slice(0, 12)) {
      const tokens = Number(BigInt(sum(row.tokens?.input, row.tokens?.output)));
      if (tokens <= 0) continue;
      const label = row.key?.label ?? "Unknown model";
      const [provider, ...modelParts] = label.split(" / ");
      const model = modelParts.join(" / ") || label;
      const providerIndex = provider.toLowerCase().includes("claude")
        ? 1
        : provider.toLowerCase().includes("codex")
          ? 0
          : 2;
      const group = grouped.get(provider) ?? {
        value: 0,
        color: palette.series[providerIndex],
        children: [],
      };
      group.value += tokens;
      const modelColors = [
        palette.series[providerIndex],
        palette.series[(providerIndex + 2) % palette.series.length],
        palette.series[(providerIndex + 4) % palette.series.length],
      ];
      group.children.push({
        name: model,
        value: tokens,
        path: label,
        color: modelColors[group.children.length % modelColors.length],
      });
      grouped.set(provider, group);
    }
    return [...grouped.entries()].map(([provider, group]) => ({
      name: provider,
      value: group.value,
      itemStyle: { color: group.color },
      label: { color: contrastingText(group.color) },
      children: group.children.map((child) => ({
        name: child.name,
        value: child.value,
        path: child.path,
        itemStyle: { color: child.color },
        label: { color: contrastingText(child.color) },
      })),
      path: provider,
    }));
  }, [rows, palette]);
  const option = useMemo<EChartsOption>(
    () => ({
      animationDuration: 700,
      animationEasing: "cubicOut",
      aria: { enabled: true },
      tooltip: {
        trigger: "item",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          if (!params || typeof params !== "object") return "";
          const item = params as {
            data?: { name?: string; path?: string; value?: number };
          };
          const label = item.data?.path ?? item.data?.name ?? "Unknown";
          return `${chartFormat.encodeHTML(label)}\n${countLabel(item.data?.value ?? 0, "token")}`;
        },
      },
      series: [
        {
          type: "sunburst",
          radius: ["10%", "86%"],
          nodeClick: false,
          sort: "desc",
          center: ["50%", "50%"],
          emphasis: { focus: "ancestor" },
          labelLayout: { hideOverlap: true },
          label: {
            show: true,
            color: palette.ink,
            fontSize: 11,
            overflow: "truncate",
            width: 70,
            minAngle: 18,
          },
          itemStyle: {
            borderColor: palette.canvas,
            borderWidth: 3,
            borderRadius: 5,
          },
          levels: [
            {},
            {
              r0: "10%",
              r: "42%",
              label: { rotate: 0, fontWeight: 700, minAngle: 30 },
              itemStyle: { color: palette.ink },
            },
            {
              r0: "42%",
              r: "86%",
              label: { rotate: "tangential", fontSize: 10 },
            },
          ],
          data: hierarchy,
        },
      ],
    }),
    [hierarchy, palette],
  );
  const events = useMemo(
    () => ({
      click: (params: unknown) => {
        if (!params || typeof params !== "object") return;
        const item = params as {
          data?: { path?: string; name?: string };
        };
        setSelected(item.data?.path ?? item.data?.name ?? null);
      },
    }),
    [],
  );
  return (
    <section className="analytics-card analytics-model-sunburst-card">
      <div className="analytics-card-heading">
        <div>
          <h2>Provider → model share</h2>
        </div>
      </div>
      {!data ? (
        <div
          className="grid place-items-center min-h-[320px] text-[var(--analytics-muted)]"
          role="status"
        >
          Loading model share…
        </div>
      ) : !hierarchy.length ? (
        <p>No model relationships in this window.</p>
      ) : (
        <>
          <AnalyticsEChart
            option={option}
            ariaLabel="Interactive sunburst chart showing provider and model token share. Hover or click a segment to inspect it."
            className="analytics-model-sunburst"
            height={270}
            onEvents={events}
          />
          <div className="analytics-chart-selection" aria-live="polite">
            {selected ? (
              <strong>{selected}</strong>
            ) : (
              <span>Tokens by provider and model</span>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function breakdownRowLabel(
  row: NonNullable<AnalyticsBreakdown["rows"]>[number],
  apply: (key: string, value: string) => void,
) {
  if (row.key?.id && row.key.kind === "task") {
    return (
      <Link to={`/tasks?taskId=${row.key.id}&taskView=full`}>
        {row.key.label}
      </Link>
    );
  }
  if (row.key?.id) {
    return (
      <button type="button" onClick={() => apply(row.key!.kind!, row.key!.id!)}>
        {row.key.label}
      </button>
    );
  }
  if (row.key?.kind === "model" && !row.key.synthetic) {
    return (
      <button
        type="button"
        onClick={() =>
          apply(
            "model",
            row.key?.label?.split(" / ").slice(1).join(" / ") ?? "",
          )
        }
      >
        {row.key.label}
      </button>
    );
  }
  return row.key?.label;
}

function exportBreakdownCsv(
  title: string,
  rows: NonNullable<AnalyticsBreakdown["rows"]>,
) {
  const header = [
    "name",
    "runs",
    "tokens",
    "costUsdTicks",
    "runTimeMs",
    "failed",
    "finished",
    "inputTokens",
    "outputTokens",
    "cacheReadTokens",
    "cacheWriteTokens",
  ];
  const escape = (value: string) => `"${value.replaceAll('"', '""')}"`;
  const lines = [header.join(",")];
  for (const row of rows) {
    lines.push(
      [
        row.key?.label ?? "",
        row.runs?.total ?? "0",
        sum(row.tokens?.input, row.tokens?.output),
        sum(row.cost?.reportedUsdTicks, row.cost?.estimatedUsdTicks),
        row.time?.runTimeMs ?? "",
        row.runs?.failed ?? "0",
        finished(row),
        row.tokens?.input ?? "0",
        row.tokens?.output ?? "0",
        row.tokens?.cacheRead ?? "0",
        row.tokens?.cacheWrite ?? "0",
      ]
        .map((value) => escape(String(value)))
        .join(","),
    );
  }
  const blob = new Blob([`${lines.join("\n")}\n`], {
    type: "text/csv;charset=utf-8",
  });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `${title.toLowerCase().replaceAll(/[^a-z0-9]+/g, "-")}.csv`;
  link.click();
  URL.revokeObjectURL(url);
}
function Reliability({
  failures,
  metrics: m,
}: {
  failures?: AnalyticsFailures;
  metrics?: Metrics;
}) {
  const palette = useAnalyticsPalette();
  const [sort, setSort] = useState("failures"),
    [codes, setCodes] = useState(false);
  const [selectedOffender, setSelectedOffender] = useState<string | null>(null);
  const agents = useMemo(
    () =>
      [...(failures?.agents ?? [])].sort((a, b) =>
        sort === "rate"
          ? Number(b.rankable) - Number(a.rankable) ||
            Number(b.failed) / Math.max(1, Number(b.finished)) -
              Number(a.failed) / Math.max(1, Number(a.finished))
          : Number(BigInt(b.failed) - BigInt(a.failed)),
      ),
    [failures?.agents, sort],
  );
  const offenderOption = useMemo<EChartsOption>(
    () => ({
      animationDuration: 850,
      animationEasing: "cubicOut",
      tooltip: {
        trigger: "item",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          if (!params || typeof params !== "object") return "";
          const item = params as {
            name?: string;
            data?: { failed?: string; finished?: string };
          };
          return `<strong>${chartFormat.encodeHTML(item.name ?? "Unknown")}</strong><br/>${count(item.data?.failed)} failed · ${count(item.data?.finished)} finished`;
        },
      },
      grid: { left: 20, right: 70, top: 14, bottom: 18, containLabel: true },
      xAxis: {
        type: "value",
        min: 0,
        max: sort === "rate" ? 100 : undefined,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: palette.muted,
          fontSize: 10,
          formatter: sort === "rate" ? "{value}%" : undefined,
        },
        splitLine: {
          lineStyle: { color: palette.grid, type: "dashed" },
        },
      },
      yAxis: {
        type: "category",
        inverse: true,
        data: agents
          .slice(0, 8)
          .map((agent) => agent.key.label ?? "Unknown agent"),
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: palette.ink,
          fontSize: 11,
          fontWeight: 600,
        },
      },
      series: [
        {
          type: "bar",
          barWidth: 15,
          showBackground: true,
          backgroundStyle: {
            color: palette.fillSoft,
            borderRadius: 10,
          },
          itemStyle: {
            color: palette.accent,
            borderRadius: [0, 10, 10, 0],
          },
          label: {
            show: true,
            position: "right",
            color: palette.ink,
            fontWeight: 700,
            formatter: (params: unknown) => {
              if (!params || typeof params !== "object") return "";
              return sort === "rate"
                ? `${Number((params as { value?: number }).value ?? 0).toFixed(1)}%`
                : count(String((params as { value?: number }).value ?? 0));
            },
          },
          data: agents.slice(0, 8).map((agent) => ({
            value:
              sort === "rate"
                ? (Number(agent.failed) / Math.max(1, Number(agent.finished))) *
                  100
                : Number(agent.failed),
            name: agent.key.label,
            failed: agent.failed,
            finished: agent.finished,
          })),
        },
      ],
    }),
    [agents, sort, palette],
  );
  const offenderEvents = useMemo(
    () => ({
      click: (params: unknown) => {
        if (!params || typeof params !== "object") return;
        setSelectedOffender((params as { name?: string }).name ?? null);
      },
    }),
    [],
  );
  return (
    <>
      <section className="analytics-card">
        <h2>Reliability</h2>
        <p>
          {count(m?.runs?.failed)} failed ·{" "}
          {rate(m?.runs?.cancelled, m?.runs?.total)} cancelled ·{" "}
          {countLabel(m?.runs?.retries, "retry", "retries")} ·{" "}
          {countLabel(
            agents.filter((agent) => agent.failed !== "0").length,
            "Agent",
          )}{" "}
          affected
        </p>
      </section>
      <div className="analytics-top-grid">
        <section className="analytics-card">
          <div className="analytics-card-heading">
            <h2>Failure mix</h2>
            <button type="button" onClick={() => setCodes(!codes)}>
              {codes ? "Show classes" : "Show codes"}
            </button>
          </div>
          <FailureMix
            values={(codes ? failures?.byCode : failures?.byClass) ?? {}}
            labelize={!codes}
          />
        </section>
        <section className="analytics-card">
          <div className="analytics-card-heading">
            <h2>Top offenders</h2>
            <AnalyticsSelect
              aria-label="Failure ranking"
              value={sort}
              onChange={(event) => setSort(event.target.value)}
            >
              <option value="failures">Failures</option>
              <option value="rate">Failure rate</option>
            </AnalyticsSelect>
          </div>
          {agents.length ? (
            <>
              <AnalyticsEChart
                option={offenderOption}
                ariaLabel="Interactive horizontal chart of top reliability offenders. Click a row to inspect it."
                className="analytics-offender-chart"
                height={270}
                onEvents={offenderEvents}
              />
              {selectedOffender ? (
                <div className="analytics-chart-selection" aria-live="polite">
                  Selected <strong>{selectedOffender}</strong>
                </div>
              ) : null}
            </>
          ) : (
            <p>No offender data in this window.</p>
          )}
        </section>
      </div>
      <section className="analytics-card">
        <h2>Waiting time</h2>
        <p>
          Queue p50 {duration(m?.time?.p50QueueWaitMs)} · p90{" "}
          {duration(m?.time?.p90QueueWaitMs)} · Preparation p50{" "}
          {duration(m?.time?.p50PreparationMs)}
        </p>
      </section>
    </>
  );
}

function FailureMix({
  values,
  labelize,
}: {
  values: Record<string, string | undefined>;
  labelize: boolean;
}) {
  const palette = useAnalyticsPalette();
  const [selected, setSelected] = useState<string | null>(null);
  const entries = useMemo(
    () =>
      Object.entries(values)
        .filter(([, value]) => Number(value) > 0)
        .sort(([, a], [, b]) => Number(b ?? 0) - Number(a ?? 0)),
    [values],
  );
  const option = useMemo<EChartsOption>(
    () => ({
      animationDuration: 850,
      animationEasing: "cubicOut",
      color: palette.series,
      tooltip: {
        trigger: "item",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          if (!params || typeof params !== "object") return "";
          const item = params as {
            name?: string;
            value?: number;
            percent?: number;
          };
          return `<strong>${chartFormat.encodeHTML(item.name ?? "Unknown")}</strong><br/>${countLabel(item.value ?? 0, "failure")} · ${(item.percent ?? 0).toFixed(1)}%`;
        },
      },
      series: [
        {
          type: "pie",
          radius: ["42%", "72%"],
          center: ["50%", "50%"],
          itemStyle: {
            borderColor: palette.canvas,
            borderWidth: 5,
            borderRadius: 8,
          },
          label: { show: false },
          emphasis: {
            scale: true,
            scaleSize: 8,
            label: {
              show: true,
              color: palette.ink,
              fontWeight: 700,
              formatter: "{b}\n{d}%",
            },
          },
          data: entries.map(([key, value]) => ({
            name: labelize ? partLabel(key) : key,
            value: Number(value ?? 0),
          })),
        },
      ],
    }),
    [entries, labelize, palette],
  );
  const events = useMemo(
    () => ({
      click: (params: unknown) => {
        if (!params || typeof params !== "object") return;
        setSelected((params as { name?: string }).name ?? null);
      },
    }),
    [],
  );
  return (
    <div className="analytics-ranking-list mt-0">
      {entries.length ? (
        <>
          <AnalyticsEChart
            option={option}
            ariaLabel="Interactive failure composition donut chart. Click a segment to inspect the failure class."
            className="analytics-failure-chart"
            height={270}
            onEvents={events}
          />
          <div
            className="analytics-failure-legend flex justify-center gap-[10px_16px] flex-wrap m-[12px_0_0]"
            aria-live="polite"
          >
            {entries.map(([key, value], index) => (
              <span key={key}>
                <i
                  style={{
                    background: palette.series[index % palette.series.length],
                  }}
                />
                {labelize ? partLabel(key) : key} · {count(value)}
              </span>
            ))}
          </div>
          {selected ? (
            <div className="analytics-chart-selection">
              Selected <strong>{selected}</strong>
            </div>
          ) : null}
        </>
      ) : (
        <p>No failures in this window.</p>
      )}
    </div>
  );
}
function windowFor(range: string, start: string | null, end: string | null) {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  let to = new Date(today);
  to.setDate(to.getDate() + 1);
  let from = new Date(today);
  from.setDate(
    from.getDate() -
      (range === "today" ? 0 : Number(range) || 30) +
      (range === "today" ? 0 : 1),
  );
  if (range === "custom") {
    from = new Date(`${start ?? ""}T00:00:00`);
    to = new Date(`${end ?? ""}T00:00:00`);
    to.setDate(to.getDate() + 1);
  }
  const days = Math.round(
    (Date.UTC(to.getFullYear(), to.getMonth(), to.getDate()) -
      Date.UTC(from.getFullYear(), from.getMonth(), from.getDate())) /
      86400000,
  );
  const error =
    !Number.isFinite(days) || days < 1 || days > 367
      ? "Choose a valid range of 1–367 days."
      : null;
  return {
    days: error ? 30 : days,
    error,
    window: {
      startAt: error ? today.toISOString() : from.toISOString(),
      endAt: error ? today.toISOString() : to.toISOString(),
    },
  };
}
