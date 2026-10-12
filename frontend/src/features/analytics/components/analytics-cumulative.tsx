import { useMemo, useState } from "react";
import type { EChartsOption } from "echarts";
import type { AnalyticsSeries } from "@/shared/api/types";
import { AnalyticsEChart } from "./analytics-echart";
import { useAnalyticsPalette } from "../model/palette";
import { count, duration, money, partLabel } from "../model/format";
import { seriesKeys } from "../model/series";
import { chartDataIndex } from "../model/chart";

export function AnalyticsCumulative({ series }: { series?: AnalyticsSeries }) {
  const palette = useAnalyticsPalette();
  const [selected, setSelected] = useState<number | null>(null);
  const isCost = series?.metric === "cost";
  const points = useMemo(() => {
    let cumulative: number | null = isCost ? null : 0;
    const keys = seriesKeys(series);
    return (series?.points ?? []).map((point) => {
      const values = keys.map((key) => point.parts?.[key]);
      const value =
        isCost && values.every((amount) => amount == null)
          ? null
          : values.reduce<number>(
              (total, amount) => total + Number(amount ?? 0),
              0,
            );
      if (value !== null) cumulative = (cumulative ?? 0) + value;
      return {
        label: new Date(point.startAt ?? "").toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
        }),
        value,
        cumulative,
      };
    });
  }, [series, isCost]);
  const format = (value: number | null) =>
    isCost
      ? money(value == null ? null : String(Math.round(value)))
      : series?.metric === "run_time"
        ? duration(value)
        : count(String(Math.round(value ?? 0)));
  const total = points.at(-1)?.cumulative ?? null;
  const half = points.find(
    (point) =>
      total != null && total > 0 && (point.cumulative ?? 0) >= total / 2,
  );
  const active =
    (selected == null ? undefined : points[selected]) ?? points.at(-1);
  const option = useMemo<EChartsOption>(
    () => ({
      animationDuration: 650,
      animationDurationUpdate: 250,
      animationEasing: "cubicOut",
      grid: { top: 18, left: 6, right: 18, bottom: 12, containLabel: true },
      tooltip: {
        trigger: "axis",
        renderMode: "richText",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          const item = (params as { dataIndex: number }[])[0];
          const point = points[item?.dataIndex];
          return point
            ? `${point.label}\n${isCost ? "Known cost to date" : "To date"}   ${format(point.cumulative)}\n${isCost ? "Known cost this period" : "This period"}   ${format(point.value)}`
            : "";
        },
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: points.map((point) => point.label),
        axisLine: { lineStyle: { color: palette.grid } },
        axisTick: { show: false },
        axisLabel: { color: palette.muted, fontSize: 10, hideOverlap: true },
      },
      yAxis: {
        type: "value",
        min: 0,
        splitNumber: 3,
        axisLabel: {
          color: palette.muted,
          fontSize: 10,
          formatter: (v: number) =>
            series?.metric === "cost" || series?.metric === "run_time"
              ? format(v)
              : new Intl.NumberFormat(undefined, {
                  notation: "compact",
                }).format(v),
        },
        splitLine: { lineStyle: { color: palette.grid, type: "dashed" } },
      },
      series: [
        {
          type: "line",
          data: points.map((point) => point.cumulative),
          smooth: 0.2,
          smoothMonotone: "x",
          symbol: "circle",
          symbolSize: 7,
          showSymbol: false,
          lineStyle: { width: 2.5, color: palette.accent },
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
                { offset: 0, color: palette.areaTop },
                { offset: 1, color: palette.areaBottom },
              ],
            },
          },
        },
      ],
    }),
    [points, series?.metric, isCost, palette],
  );
  const events = useMemo(
    () => ({
      mouseover: (event: unknown) => setSelected(chartDataIndex(event)),
      globalout: () => setSelected(null),
    }),
    [],
  );
  return (
    <section className="analytics-card analytics-cumulative-card">
      <div className="analytics-card-heading">
        <h2>Cumulative usage</h2>
        <span className="analytics-muted">
          {isCost ? "Known cost" : partLabel(series?.metric ?? "cost")}
        </span>
      </div>
      {!series ? (
        <p role="status">Loading cumulative usage…</p>
      ) : (
        <>
          <div className="analytics-cumulative-total">
            <strong>{format(active?.cumulative ?? (isCost ? null : 0))}</strong>
            <span>through {active?.label ?? "this window"}</span>
          </div>
          <AnalyticsEChart
            option={option}
            ariaLabel={
              isCost
                ? "Cumulative known cost area chart. Unknown costs are excluded. Hover a date to see known costs to date and in that period."
                : "Cumulative usage area chart. Hover a date to see usage to date and usage in that period."
            }
            className="analytics-cumulative-chart"
            height={225}
            onEvents={events}
          />
          <div className="analytics-chart-selection">
            <span>
              {isCost
                ? total == null
                  ? "No reported or estimated cost is available in this window."
                  : half
                    ? `50% of known cost reached by ${half.label}`
                    : "Known cost is zero in this window."
                : half
                  ? `50% reached by ${half.label}`
                  : "No reported usage in this window."}
            </span>
          </div>
          {isCost ? (
            <p className="analytics-muted">
              Includes reported and estimated costs. Unknown costs are excluded.
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}
