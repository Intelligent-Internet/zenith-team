import {
  useGetWorkspaceAnalyticsBreakdownQuery,
  useGetWorkspaceAnalyticsHeatmapQuery,
  useGetWorkspaceAnalyticsSummaryQuery,
} from "@/shared/api/analytics-api";
import { useMemo, useState } from "react";
import type { EChartsOption } from "echarts";

import { count, countLabel, cost, rate, tokens } from "../model/format";
import { metricLabel } from "../model/metrics";
import { useAnalyticsPalette } from "../model/palette";
import { AnalyticsEChart } from "./analytics-echart";
import { AnalyticsOutcomeBar } from "./analytics-outcome-bar";
import type { AnalyticsCalendarQuery } from "../model/calendar-query";
import type {
  AnalyticsBreakdown,
  AnalyticsHeatmap,
  AnalyticsSummary,
} from "@/shared/api/types";

export function AnalyticsDayDetail({
  query,
  date,
}: {
  query: AnalyticsCalendarQuery;
  date: string;
}) {
  const palette = useAnalyticsPalette();
  const requestedStart = new Date(date);
  const end = new Date(requestedStart);
  end.setDate(end.getDate() + 1);
  const dayQuery = {
    ...query,
    startAt: requestedStart.toISOString(),
    endAt: end.toISOString(),
  };
  const summary = useGetWorkspaceAnalyticsSummaryQuery(dayQuery);
  const heatmap = useGetWorkspaceAnalyticsHeatmapQuery(dayQuery);
  const agents = useGetWorkspaceAnalyticsBreakdownQuery({
    ...dayQuery,
    by: "agent",
    sort: "tokens",
    limit: 3,
  });
  // Commit the three responses together. Keep the previous dated detail visible
  // while a new day loads, instead of collapsing its chart and Agent rows.
  const [detail, setDetail] = useState<{
    date: string;
    summary: AnalyticsSummary;
    heatmap: AnalyticsHeatmap;
    agents: AnalyticsBreakdown;
  } | null>(null);
  if (
    summary.currentData &&
    heatmap.currentData &&
    agents.currentData &&
    (detail?.date !== date ||
      detail.summary !== summary.currentData ||
      detail.heatmap !== heatmap.currentData ||
      detail.agents !== agents.currentData)
  ) {
    setDetail({
      date,
      summary: summary.currentData,
      heatmap: heatmap.currentData,
      agents: agents.currentData,
    });
  }
  const updating =
    summary.isFetching || heatmap.isFetching || agents.isFetching;
  const start = new Date(detail?.date ?? date);
  const metrics = detail?.summary.current;
  const hours = useMemo(() => {
    const counts = Array<number>(24).fill(0);
    for (const cell of detail?.heatmap.cells ?? [])
      counts[cell.hour] += Number(cell.runs);
    return counts;
  }, [detail?.heatmap]);
  const option = useMemo<EChartsOption>(
    () => ({
      grid: { top: 12, bottom: 24, left: 25, right: 12 },
      tooltip: {
        trigger: "axis",
        renderMode: "richText",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          const item = (params as { dataIndex: number; value: number }[])[0];
          return `${String(item.dataIndex).padStart(2, "0")}:00\n${countLabel(item.value, "Run")}`;
        },
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: hours.map((_, hour) => hour),
        axisTick: { show: false },
        axisLine: { show: false },
        axisLabel: {
          color: palette.muted,
          fontSize: 9,
          interval: 5,
          formatter: (hour: string) => `${hour.padStart(2, "0")}:00`,
        },
      },
      yAxis: {
        type: "value",
        minInterval: 1,
        splitNumber: 2,
        axisLabel: { color: palette.muted, fontSize: 9 },
        splitLine: { lineStyle: { color: palette.grid, type: "dashed" } },
      },
      series: [
        {
          type: "line",
          data: hours,
          smooth: false,
          showSymbol: false,
          lineStyle: { width: 1.5, color: palette.accent },
          itemStyle: { color: palette.accent },
          areaStyle: { color: palette.areaTop },
          emphasis: { focus: "series" },
        },
      ],
    }),
    [hours, palette],
  );
  const hasError = summary.isError || heatmap.isError || agents.isError;
  return (
    <aside
      className="analytics-day-detail"
      aria-label="Selected day"
      aria-busy={updating}
    >
      <span className="analytics-day-update" role="status">
        {updating
          ? `Loading ${requestedStart.toLocaleDateString(undefined, { month: "short", day: "numeric" })}…`
          : ""}
      </span>
      <header className="analytics-day-heading">
        <strong>{start.getDate()}</strong>
        <span>
          {start.toLocaleDateString(undefined, { weekday: "long" })}
          <small>
            {start.toLocaleDateString(undefined, {
              month: "long",
              year: "numeric",
            })}
          </small>
        </span>
      </header>
      {hasError ? (
        <p role="alert">
          Day details could not be loaded.{" "}
          <button
            type="button"
            onClick={() => {
              void summary.refetch();
              void heatmap.refetch();
              void agents.refetch();
            }}
          >
            Retry day
          </button>
        </p>
      ) : null}
      <div className="analytics-day-count">
        <strong>{count(metrics?.runs?.total)}</strong>
        <span>{metrics?.runs?.total === "1" ? "Run" : "Runs"}</span>
      </div>
      {detail ? (
        <AnalyticsEChart
          option={option}
          height={108}
          ariaLabel={`Hourly Runs on ${start.toLocaleDateString()}. ${hours.map((value, hour) => `${hour}:00 ${value}`).join(", ")}`}
        />
      ) : (
        <div className="analytics-day-chart-loading" role="status">
          Loading hourly activity…
        </div>
      )}
      {metrics ? (
        <div className="analytics-day-outcomes">
          <AnalyticsOutcomeBar metrics={metrics} />
          <div className="analytics-day-outcome-labels">
            <span>{count(metrics.runs?.completed)} completed</span>
            <span>{count(metrics.runs?.failed)} failed</span>
            <span>{count(metrics.runs?.cancelled)} cancelled</span>
          </div>
        </div>
      ) : null}
      <div className="analytics-day-agents">
        <h3>Top Agents</h3>
        {detail?.agents.rows?.length ? (
          detail.agents.rows.map((row, index) => (
            <div className="analytics-day-agent" key={row.key?.id ?? index}>
              <span className="analytics-initial">
                {(row.key?.label ?? "?").slice(0, 1)}
              </span>
              <span>
                <strong>{row.key?.label}</strong>
                <small>
                  {rate(row.runs?.completed, row.runs?.total)} completed
                </small>
              </span>
              <b>{metricLabel(row, "tokens")}</b>
            </div>
          ))
        ) : (
          <p>{agents.isFetching ? "Loading Agents…" : "No Runs this day"}</p>
        )}
      </div>
      <dl className="analytics-day-totals">
        <div>
          <dt>Cost</dt>
          <dd>{cost(metrics?.cost, metrics?.runs?.withoutUsage)}</dd>
        </div>
        <div>
          <dt>Tokens</dt>
          <dd title={tokens(metrics)}>
            {metrics ? metricLabel(metrics, "tokens") : "—"}
          </dd>
        </div>
      </dl>
    </aside>
  );
}
