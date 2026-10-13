import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { EChartsOption } from "echarts";
import { format as chartFormat } from "echarts/core";
import { Tooltip } from "@/shared/ui/tooltip";
import type { AnalyticsSeries } from "@/shared/api/types";
import { AnalyticsEChart } from "./analytics-echart";
import { useAnalyticsPalette, type AnalyticsPalette } from "../model/palette";
import { duration, money, partLabel } from "../model/format";
import { seriesKeys } from "../model/series";
import { chartDataIndex } from "../model/chart";

type ChartPoint = {
  label: string;
  date: string;
  total: number | null;
  trend: number | null;
  [key: string]: string | number | null;
};

export function AnalyticsTrend({
  series,
  title,
  children,
  height = 346,
}: {
  series?: AnalyticsSeries;
  title: string;
  children?: ReactNode;
  height?: number;
}) {
  const palette = useAnalyticsPalette();
  const [table, setTable] = useState(false);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const points = series?.points ?? [];
  const keys = useMemo(() => seriesKeys(series), [series]);
  const metric = series?.metric;
  const hasValues = points.some((point) =>
    keys.some((key) => point.parts?.[key] != null),
  );
  const format = useCallback(
    (value?: string | number | null) =>
      value == null
        ? "Unknown"
        : metric === "cost"
          ? money(String(Math.round(Number(value))))
          : metric === "run_time"
            ? duration(String(Math.round(Number(value))))
            : new Intl.NumberFormat(undefined, {
                maximumFractionDigits: 1,
              }).format(Number(value)),
    [metric],
  );
  const data = useMemo(() => buildChartData(points, keys), [keys, points]);
  const hasTrend = data.some((point) => point.trend != null);
  const insight = useMemo(
    () => summarizeTrend(data, series?.metric),
    [data, series?.metric],
  );
  const selected = activeIndex == null ? undefined : data[activeIndex];
  const option = useMemo(
    () =>
      buildTrendOption(data, keys, insight, series?.metric, format, palette),
    [data, insight, keys, series?.metric, format, palette],
  );
  const events = useMemo(
    () => ({
      click: (params: unknown) => {
        const index = chartDataIndex(params);
        if (index != null) setActiveIndex(index);
      },
      mouseover: (params: unknown) => {
        const index = chartDataIndex(params);
        if (index != null) setActiveIndex(index);
      },
      globalout: () => setActiveIndex(null),
    }),
    [],
  );

  return (
    <section className="analytics-card analytics-trend-card">
      <div className="analytics-card-heading analytics-trend-heading">
        <h2>{title}</h2>
        <div className="analytics-controls">
          {children}
          {hasValues ? (
            <button
              type="button"
              aria-pressed={table}
              onClick={() => setTable(!table)}
            >
              {table ? "Chart" : "Details"}
            </button>
          ) : null}
        </div>
      </div>
      {!series ? (
        <div
          className="grid place-items-center min-h-[320px] text-[var(--analytics-muted)]"
          style={{ height: height + 80 }}
          role="status"
        >
          Loading trend…
        </div>
      ) : !hasValues ? (
        <p className="analytics-chart-empty" role="status">
          {metric === "cost"
            ? "Cost data is not available for this period. Choose Runs or Tokens to explore activity."
            : "No reported values for this period."}
        </p>
      ) : table ? (
        <div className="w-full max-w-full overflow-x-auto [scrollbar-width:thin]">
          <table>
            <caption className="sr-only">{title}</caption>
            <thead>
              <tr>
                <th>Period</th>
                {keys.map((key) => (
                  <th key={key}>{partLabel(key)}</th>
                ))}
                <th>Trend</th>
              </tr>
            </thead>
            <tbody>
              {points.map((point, index) => (
                <tr key={point.startAt}>
                  <td>
                    {new Date(point.startAt ?? "").toLocaleDateString()}
                    {Date.parse(point.endAt ?? "") > Date.now()
                      ? " (partial)"
                      : ""}
                  </td>
                  {keys.map((key) => (
                    <td key={key}>{format(point.parts?.[key])}</td>
                  ))}
                  <td>{format(data[index]?.trend)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <>
          <div className="analytics-trend-insight flex items-center flex-wrap gap-[8px] min-h-[32px] m-[14px_0_4px] text-[var(--analytics-muted)] text-workspace-caption">
            <Tooltip label={insight.detail}>
              <span className="analytics-trend-summary" tabIndex={0}>
                <span className={`analytics-trend-dot ${insight.tone}`} />
                <strong>{insight.headline}</strong>
                <span>{insight.changeLabel}</span>
              </span>
            </Tooltip>
            {selected ? (
              <span className="analytics-trend-selection">
                {selected.label} · {format(selected.total)}
                {selected.complete === 0 ? " · partial period" : ""}
              </span>
            ) : null}
          </div>
          <AnalyticsEChart
            option={option}
            ariaLabel={`${title}. Interactive column chart${hasTrend ? " with trendline" : ""}. Hover or click a period, and drag the timeline below to zoom.`}
            className="analytics-chart analytics-chart--interactive analytics-echart"
            height={height}
            onEvents={events}
          />
          <div
            className="analytics-legend flex items-center gap-[14px] flex-wrap mt-[8px] text-[var(--analytics-muted)] text-[length:12px]"
            aria-label="Chart legend"
          >
            {keys.map((key, index) => (
              <span key={key}>
                <i
                  style={{
                    background: trendSeriesColor(key, index, palette),
                  }}
                />
                {partLabel(key)}
              </span>
            ))}
            {hasTrend ? (
              <span>
                <i className="analytics-legend-line" />
                Trendline
              </span>
            ) : null}
            {insight.average != null ? (
              <Tooltip label="Average across complete periods with reported values only. Missing values are excluded.">
                <span className="analytics-legend-average" tabIndex={0}>
                  Avg. {format(insight.average)}
                </span>
              </Tooltip>
            ) : null}
          </div>
        </>
      )}
    </section>
  );
}

function trendSeriesColor(
  key: string,
  index: number,
  palette: AnalyticsPalette,
) {
  if (key === "completed") return palette.success;
  if (key === "failed") return palette.danger;
  if (key === "cancelled") return palette.muted;
  return palette.series[index % palette.series.length];
}

function buildTrendOption(
  data: ChartPoint[],
  keys: string[],
  insight: ReturnType<typeof summarizeTrend>,
  metric: string | undefined,
  format: (value?: string | number | null) => string,
  palette: AnalyticsPalette,
): EChartsOption {
  const chartColors = keys.map((key, index) =>
    trendSeriesColor(key, index, palette),
  );
  return {
    animationDuration: 700,
    animationDurationUpdate: 360,
    animationEasing: "cubicOut",
    animationEasingUpdate: "cubicOut",
    aria: { enabled: true, decal: { show: false } },
    color: chartColors,
    grid: {
      top: 18,
      right: 22,
      bottom: data.length > 12 ? 56 : 28,
      left: 12,
      containLabel: true,
    },
    tooltip: {
      trigger: "axis",
      confine: true,
      ...palette.tooltip,
      padding: [12, 14],
      axisPointer: {
        type: "cross",
        crossStyle: { color: palette.muted, width: 1 },
        label: {
          backgroundColor: palette.fill,
          color: palette.accentForeground,
        },
      },
      formatter: (params) => {
        const items = Array.isArray(params) ? params : [params];
        const first = items[0] as
          { axisValue?: string; dataIndex?: number } | undefined;
        const partial =
          first?.dataIndex != null && data[first.dataIndex]?.complete === 0;
        const period = `${first?.axisValue ?? "Period"}${partial ? " · partial period" : ""}`;
        const rows = items
          .filter((item) => {
            const name = String(
              (item as { seriesName?: string }).seriesName ?? "",
            );
            return name !== "Average";
          })
          .map((item) => {
            const value = (item as { value?: string | number }).value;
            const name = String(
              (item as { seriesName?: string }).seriesName ?? "",
            );
            return `<div style="display:flex;justify-content:space-between;gap:24px"><span>${chartFormat.encodeHTML(name)}</span><strong>${format(value)}</strong></div>`;
          })
          .join("");
        return `<div style="min-width:180px"><div style="font-weight:700;margin-bottom:7px">${chartFormat.encodeHTML(period)}</div>${rows}</div>`;
      },
    },
    xAxis: {
      type: "category",
      boundaryGap: true,
      data: data.map((point) => point.label),
      axisLine: { lineStyle: { color: palette.grid } },
      axisTick: { show: false },
      axisLabel: {
        color: palette.muted,
        fontSize: 11,
        hideOverlap: true,
      },
    },
    yAxis: {
      type: "value",
      min: 0,
      axisPointer: {
        label: {
          formatter: (params) => formatAxis(Number(params.value), metric),
        },
      },
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: {
        color: palette.muted,
        fontSize: 11,
        formatter: (value: number) => formatAxis(value, metric),
      },
      splitLine: {
        lineStyle: { color: palette.grid, type: "dashed" },
      },
    },
    dataZoom:
      data.length > 12
        ? [
            {
              type: "inside",
              xAxisIndex: 0,
              filterMode: "none",
              throttle: 16,
              minValueSpan: 3,
              zoomOnMouseWheel: true,
              moveOnMouseMove: true,
            },
            {
              type: "slider",
              xAxisIndex: 0,
              height: 18,
              bottom: 6,
              borderColor: palette.accent,
              backgroundColor: palette.fillSoft,
              fillerColor: palette.fillStrong,
              handleStyle: {
                color: palette.accent,
                borderColor: palette.accent,
              },
              moveHandleStyle: { color: palette.accentBright },
              dataBackground: {
                lineStyle: { color: palette.accentSoft },
                areaStyle: { color: palette.fillSoft },
              },
              selectedDataBackground: {
                lineStyle: { color: palette.accent },
                areaStyle: { color: palette.fillStrong },
              },
              emphasis: {
                handleStyle: { color: palette.accent },
                moveHandleStyle: { color: palette.accent },
              },
              labelFormatter: () => "",
              realtime: true,
              throttle: 16,
              minValueSpan: 3,
            },
          ]
        : undefined,
    series: [
      ...keys.map((key, index) => ({
        type: "bar" as const,
        name: partLabel(key),
        stack: "usage",
        barMaxWidth: 34,
        emphasis: {
          focus: "series" as const,
          blurScope: "coordinateSystem" as const,
        },
        itemStyle: {
          color: chartColors[index],
          borderRadius: index === keys.length - 1 ? [6, 6, 0, 0] : [0, 0, 0, 0],
        },
        data: data.map((point) => point[key]),
      })),
      {
        type: "line",
        name: "Trendline",
        smooth: 0.35,
        showSymbol: false,
        connectNulls: false,
        lineStyle: {
          color: palette.ink,
          width: 1.5,
          type: [5, 4],
          opacity: 0.8,
        },
        itemStyle: { color: palette.ink },
        emphasis: { focus: "series", lineStyle: { width: 1.5 } },
        data: data.map((point) => point.trend),
        markLine: {
          silent: true,
          symbol: "none",
          lineStyle: {
            color: palette.muted,
            type: "dotted",
            width: 1,
          },
          label: { show: false },
          data: insight.average == null ? [] : [{ yAxis: insight.average }],
        },
      },
    ],
  };
}

function buildChartData(
  points: NonNullable<AnalyticsSeries["points"]>,
  keys: string[],
): ChartPoint[] {
  const totals = points.map((point) => {
    const values = keys.flatMap((key) =>
      point.parts?.[key] == null ? [] : [Number(point.parts[key])],
    );
    return values.length
      ? values.reduce((total, value) => total + value, 0)
      : null;
  });
  const now = Date.now();
  const complete = points.map((point) => Date.parse(point.endAt ?? "") <= now);
  const regression = linearRegression(
    totals.map((total, index) => (complete[index] ? total : null)),
  );
  return points.map((point, index) => ({
    label: new Date(point.startAt ?? "").toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
    }),
    date: point.startAt ?? "",
    total: totals[index],
    trend:
      complete[index] && totals[index] != null
        ? regression.predict(index)
        : null,
    complete: complete[index] ? 1 : 0,
    ...Object.fromEntries(
      keys.map((key) => [
        key,
        point.parts?.[key] == null ? null : Number(point.parts[key]),
      ]),
    ),
  }));
}

function linearRegression(values: (number | null)[]) {
  const samples = values.flatMap((value, index) =>
    value == null ? [] : [{ value, index }],
  );
  if (samples.length < 2) return { predict: () => null, rSquared: 0 };
  const xMean =
    samples.reduce((sum, sample) => sum + sample.index, 0) / samples.length;
  const yMean =
    samples.reduce((sum, sample) => sum + sample.value, 0) / samples.length;
  const denominator = samples.reduce(
    (sum, sample) => sum + (sample.index - xMean) ** 2,
    0,
  );
  const slope = denominator
    ? samples.reduce(
        (sum, sample) => sum + (sample.index - xMean) * (sample.value - yMean),
        0,
      ) / denominator
    : 0;
  const intercept = yMean - slope * xMean;
  const residual = samples.reduce(
    (sum, sample) =>
      sum + (sample.value - (intercept + slope * sample.index)) ** 2,
    0,
  );
  const total = samples.reduce(
    (sum, sample) => sum + (sample.value - yMean) ** 2,
    0,
  );
  return {
    predict: (index: number) => Math.max(0, intercept + slope * index),
    rSquared: total ? Math.max(0, 1 - residual / total) : 1,
  };
}

function summarizeTrend(allData: ChartPoint[], metric?: string) {
  const data = allData.flatMap((point) =>
    point.complete === 1 && point.total != null
      ? [{ ...point, total: point.total }]
      : [],
  );
  const values = data.map((point) => point.total);
  if (data.length >= 2 && values.every((value) => value === 0)) {
    const hasPartialActivity = allData.some(
      (point) => point.complete === 0 && point.total != null && point.total > 0,
    );
    return {
      headline: hasPartialActivity
        ? "Activity in the current period"
        : "No reported activity",
      detail: hasPartialActivity
        ? "Activity is reported in the current, incomplete period. The trend and average include complete periods with reported values only."
        : "No positive reported values in the complete periods. Missing values are excluded.",
      tone: "is-steady",
      changeLabel: "",
      average: 0,
    };
  }
  const first = values[0] ?? 0;
  const last = values.at(-1) ?? 0;
  const peakIndex = values.reduce(
    (best, value, index) => (value > (values[best] ?? -1) ? index : best),
    0,
  );
  const regression = linearRegression(
    allData.map((point) => (point.complete === 1 ? point.total : null)),
  );
  const delta = first ? ((last - first) / first) * 100 : 0;
  const direction =
    !first && last > 0
      ? "Rising"
      : Math.abs(delta) < 3
        ? "Steady"
        : delta > 0
          ? "Rising"
          : "Cooling";
  const unit =
    metric === "cost" ? "spend" : metric === "run_time" ? "runtime" : "volume";
  const fit =
    regression.rSquared >= 0.6
      ? "strong trend fit"
      : regression.rSquared >= 0.3
        ? "moderate trend fit"
        : "weak trend signal";
  const changeLabel = first
    ? `${delta >= 0 ? "+" : ""}${delta.toFixed(1)}%`
    : last > 0
      ? "New activity"
      : "0.0%";
  return {
    changeLabel: data.length < 2 ? "" : changeLabel,
    headline: data.length < 2 ? "Collecting a trend" : `${direction} ${unit}`,
    detail:
      data.length < 2
        ? "A trend needs at least two complete periods with reported values."
        : `${changeLabel} across complete periods with reported values · peak ${data[peakIndex]?.label ?? "—"} · ${fit}`,
    tone:
      direction === "Cooling"
        ? "is-cooling"
        : direction === "Steady"
          ? "is-steady"
          : "is-rising",
    average: data.length
      ? values.reduce((sum, value) => sum + value, 0) / data.length
      : null,
  };
}

function formatAxis(value: number, metric?: string) {
  if (metric === "cost") return money(String(Math.round(value)));
  if (metric === "run_time") return duration(String(Math.round(value)));
  return new Intl.NumberFormat(undefined, { notation: "compact" }).format(
    value,
  );
}
