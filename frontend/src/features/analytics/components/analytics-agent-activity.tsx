import { useMemo } from "react";
import type { EChartsOption } from "echarts";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { mixColor } from "@/app/appearance/color";
import {
  useAgentSeries,
  type AnalyticsSeriesQuery,
} from "../model/use-agent-series";
import { useAnalyticsPalette } from "../model/palette";
import { seriesKeys } from "../model/series";
import { formatMetricAmount, metricLabel } from "../model/metrics";
import { countLabel, partLabel } from "../model/format";
import { AnalyticsEChart } from "./analytics-echart";
import { AnalyticsSelect } from "./analytics-select";

export function AnalyticsAgentActivity({
  query,
  data,
  onMetricChange,
}: {
  query: AnalyticsSeriesQuery;
  data?: AnalyticsBreakdown;
  onMetricChange: (metric: string) => void;
}) {
  const palette = useAnalyticsPalette();
  const agents = useMemo(
    () => (data?.rows ?? []).filter((row) => row.key?.id).slice(0, 5),
    [data],
  );
  const { results, retry } = useAgentSeries(
    agents.map((row) => ({ ...query, agentId: row.key!.id! })),
  );
  const loading =
    !data || results.some((result) => !result.data && !result.isError);
  const error = results.some((result) => result.isError);
  const lanes = useMemo(
    () =>
      results.map((result, index) => {
        const keys = seriesKeys(result.data);
        return {
          name: agents[index].key?.label ?? "Agent",
          points: (result.data?.points ?? []).map((point) => {
            const known = keys
              .map((key) => point.parts?.[key])
              .filter((value) => value != null);
            return {
              date: point.startAt!,
              value: known.length
                ? known.reduce((total, value) => total + Number(value), 0)
                : null,
            };
          }),
        };
      }),
    [results, agents],
  );
  const height = Math.max(3, agents.length) * 108 + 34;
  const option = useMemo<EChartsOption>(() => {
    const maximum = Math.max(
      1,
      ...lanes.flatMap((lane) => lane.points.map((point) => point.value ?? 0)),
    );
    const magnitude = 10 ** Math.floor(Math.log10(maximum));
    const max = Math.ceil(maximum / magnitude) * magnitude;
    const date = (value: string) =>
      new Date(value).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        timeZone: query.timeZone,
      });
    return {
      animationDuration: 650,
      animationDurationUpdate: 280,
      animationEasing: "cubicOut",
      axisPointer: { link: [{ xAxisIndex: "all" }] },
      tooltip: {
        trigger: "axis",
        confine: true,
        renderMode: "richText",
        ...palette.tooltip,
        axisPointer: {
          type: "line",
          lineStyle: { color: palette.muted, width: 1, type: "dashed" },
        },
        formatter: (params: unknown) => {
          const index = (params as { dataIndex: number }[])[0]?.dataIndex;
          if (index == null) return "";
          const point = lanes[0]?.points[index];
          if (!point) return "";
          return [
            date(point.date),
            ...lanes.map(
              (lane) =>
                `${lane.name}    ${formatMetricAmount(lane.points[index]?.value ?? null, query.metric)}`,
            ),
          ].join("\n");
        },
      },
      grid: lanes.map((_, index) => ({
        left: 106,
        right: 80,
        top: index * 108 + 14,
        height: 76,
      })),
      xAxis: lanes.map((lane, index) => ({
        type: "category",
        gridIndex: index,
        boundaryGap: false,
        data: lane.points.map((point) => point.date),
        axisLine: { lineStyle: { color: palette.grid } },
        axisTick: { show: false },
        axisLabel: {
          show: index === lanes.length - 1,
          interval: Math.max(1, Math.floor((lane.points.length - 1) / 2)),
          showMaxLabel: true,
          hideOverlap: true,
          color: palette.muted,
          fontSize: 11,
          formatter: date,
        },
        axisPointer: { label: { show: false } },
      })),
      yAxis: lanes.map((_, index) => ({
        type: "value",
        gridIndex: index,
        min: 0,
        max,
        interval: max / 2,
        axisLine: { show: true, lineStyle: { color: palette.grid } },
        axisTick: { show: true, lineStyle: { color: palette.grid } },
        splitLine: { show: false },
        axisLabel: {
          color: palette.muted,
          fontSize: 10,
          formatter: (value: number) => formatMetricAmount(value, query.metric),
        },
      })),
      series: lanes.map((lane, index) => ({
        name: lane.name,
        type: "line",
        xAxisIndex: index,
        yAxisIndex: index,
        data: lane.points.map((point) => point.value),
        smooth: 0.3,
        smoothMonotone: "x",
        connectNulls: false,
        showSymbol: false,
        symbol: "circle",
        symbolSize: 7,
        lineStyle: { width: 1.25, color: palette.accent },
        itemStyle: {
          color: palette.accent,
          borderColor: palette.canvas,
          borderWidth: 2,
        },
        areaStyle: {
          color: {
            type: "linear",
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              {
                offset: 0,
                color: mixColor(
                  palette.canvas,
                  palette.accent,
                  0.55 - index * 0.04,
                ),
              },
              {
                offset: 1,
                color: mixColor(palette.canvas, palette.accent, 0.19),
              },
            ],
          },
        },
        emphasis: { focus: "series", lineStyle: { width: 1.75 } },
      })),
    };
  }, [lanes, palette, query.metric, query.timeZone]);
  const period =
    query.bucket === "day"
      ? "Daily"
      : query.bucket === "week"
        ? "Weekly"
        : "Monthly";
  return (
    <section className="analytics-card analytics-rhythm-card">
      <div className="analytics-rhythm-heading">
        <div>
          <h2>Agent activity</h2>
          <p>{partLabel(query.metric)} · same scale</p>
        </div>
        <AnalyticsSelect
          aria-label="Agent activity metric"
          value={query.metric}
          onChange={(event) => onMetricChange(event.target.value)}
        >
          {["tokens", "cost", "runs", "run_time", "failures"].map((metric) => (
            <option key={metric} value={metric}>
              {period} {partLabel(metric).toLowerCase()}
            </option>
          ))}
        </AnalyticsSelect>
        <div
          className="analytics-rhythm-total flex flex-col text-right gap-[4px] [font-variant-numeric:tabular-nums] max-w-[45%]"
          title="All Agents in this window"
        >
          <strong>
            {data?.total ? metricLabel(data.total, query.metric) : "—"}
          </strong>
          <span>{partLabel(query.metric).toLowerCase()}</span>
        </div>
      </div>
      {error ? (
        <p role="alert">
          Activity could not be loaded.{" "}
          <button type="button" onClick={retry}>
            Retry
          </button>
        </p>
      ) : loading ? (
        <div
          className="grid [place-content:center] min-h-[420px] text-[var(--analytics-muted)] text-workspace-caption"
          style={{ height }}
          role="status"
        >
          Loading activity…
        </div>
      ) : !agents.length ? (
        <p>No active Agents in this window.</p>
      ) : (
        <div className="relative mt-[16px]" style={{ height }}>
          <AnalyticsEChart
            option={option}
            height={height}
            ariaLabel={`Activity for ${countLabel(agents.length, "Agent")} on the same ${partLabel(query.metric)} scale. Hover to compare a period.`}
          />
          {agents.map((row, index) => (
            <div
              className="analytics-rhythm-labels absolute left-0 right-0 flex items-center justify-between pointer-events-none [font-variant-numeric:tabular-nums]"
              key={row.key!.id}
              style={{ top: index * 108 + 14, height: 76 }}
            >
              <span title={row.key?.label}>{row.key?.label}</span>
              <strong
                title={`${row.key?.label}: ${metricLabel(row, query.metric)} ${partLabel(query.metric)}`}
              >
                {metricLabel(row, query.metric)}
              </strong>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
