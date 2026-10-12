import { useMemo, useState } from "react";
import type { EChartsOption } from "echarts";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { AnalyticsEChart } from "./analytics-echart";
import { AnalyticsOutcomeBar } from "./analytics-outcome-bar";
import { AnalyticsUsageMap } from "./analytics-usage-map";
import { useAnalyticsPalette } from "../model/palette";
import { count, countLabel, duration, partLabel, rate } from "../model/format";
import {
  formatMetricAmount,
  metricAmount,
  metricLabel,
} from "../model/metrics";

export function AnalyticsBreakdownChart({
  data,
  metric,
}: {
  data: AnalyticsBreakdown;
  metric: string;
}) {
  const palette = useAnalyticsPalette();
  const [selected, setSelected] = useState<string | null>(null);
  const kind =
    data.by === "model" || data.by === "member"
      ? "donut"
      : data.by === "project" || data.by === "runtime"
        ? "cards"
        : "distribution";
  const rows = useMemo(() => (data.rows ?? []).slice(0, 8), [data.rows]);
  const values = useMemo(
    () => rows.map((row) => metricAmount(row, metric)),
    [rows, metric],
  );
  const shownTotal = values.reduce<number>(
    (total, value) => total + (value ?? 0),
    0,
  );
  const identity = (index: number) =>
    `${rows[index].key?.kind}/${rows[index].key?.id ?? rows[index].key?.label}`;
  const selectedIndex = rows.findIndex(
    (_, index) => identity(index) === selected,
  );
  const active = rows[selectedIndex];
  const select = (index: number) =>
    setSelected(selected === identity(index) ? null : identity(index));
  const option = useMemo<EChartsOption>(
    () => ({
      animationDuration: 450,
      animationDurationUpdate: 220,
      tooltip: {
        trigger: "item",
        renderMode: "richText",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          const item = params as { data: { rowIndex: number } };
          const row = rows[item.data.rowIndex];
          return `${row.key?.label}\n${metricLabel(row, metric)}`;
        },
      },
      title: {
        text: formatMetricAmount(shownTotal, metric),
        subtext: partLabel(metric),
        left: "center",
        top: "40%",
        itemGap: 7,
        textStyle: { fontSize: 26, color: palette.ink, fontWeight: 600 },
        subtextStyle: { color: palette.muted, fontSize: 11 },
      },
      series: [
        {
          type: "pie",
          radius: ["66%", "84%"],
          label: { show: false },
          labelLine: { show: false },
          itemStyle: {
            borderColor: palette.canvas,
            borderWidth: 3,
            borderRadius: 4,
          },
          emphasis: { scaleSize: 4 },
          data: rows.flatMap((row, index) =>
            (values[index] ?? 0) > 0
              ? [
                  {
                    name: row.key?.label ?? "Unknown",
                    value: values[index]!,
                    rowIndex: index,
                    itemStyle: {
                      color: palette.series[index % palette.series.length],
                      opacity:
                        selectedIndex < 0 || selectedIndex === index ? 1 : 0.28,
                    },
                  },
                ]
              : [],
          ),
        },
      ],
    }),
    [rows, values, shownTotal, metric, palette, selectedIndex],
  );
  const events = useMemo(
    () => ({
      click: (event: unknown) => {
        const index = (event as { data?: { rowIndex?: number } }).data
          ?.rowIndex;
        if (index != null) {
          const row = rows[index];
          setSelected(`${row.key?.kind}/${row.key?.id ?? row.key?.label}`);
        }
      },
    }),
    [rows],
  );
  return (
    <>
      {kind === "distribution" ? (
        <AnalyticsUsageMap
          rows={rows}
          metric={metric}
          selectedIndex={selectedIndex}
          onSelect={select}
        />
      ) : null}
      {kind === "donut" ? (
        <div className="analytics-composition analytics-composition--donut">
          {shownTotal > 0 ? (
            <AnalyticsEChart
              option={option}
              height={252}
              ariaLabel={`${partLabel(metric)} by ${data.by}. Select a segment or a name to inspect it. Exact values in the adjacent list.`}
              onEvents={events}
            />
          ) : (
            <p className="analytics-chart-empty">
              No reported {partLabel(metric).toLowerCase()}
            </p>
          )}
          <div className="analytics-composition-list" aria-label="Chart values">
            {rows.map((row, index) => (
              <button
                key={identity(index)}
                type="button"
                aria-pressed={selectedIndex === index}
                onClick={() => select(index)}
              >
                <i
                  style={{
                    background: palette.series[index % palette.series.length],
                  }}
                />
                <span>
                  <strong>{row.key?.label}</strong>
                  <small>
                    {shownTotal && values[index] != null
                      ? `${((values[index]! / shownTotal) * 100).toFixed(1)}% of shown ${partLabel(metric).toLowerCase()}`
                      : "No reported value"}
                  </small>
                </span>
                <b>{metricLabel(row, metric)}</b>
              </button>
            ))}
          </div>
        </div>
      ) : null}
      {kind === "cards" ? (
        <div className="analytics-resource-grid">
          {rows.map((row, index) => (
            <button
              key={identity(index)}
              type="button"
              aria-pressed={selectedIndex === index}
              onClick={() => select(index)}
            >
              <span className="analytics-resource-name">{row.key?.label}</span>
              <strong>{metricLabel(row, metric)}</strong>
              <span className="analytics-muted">
                {countLabel(row.runs?.total, "Run")} ·{" "}
                {duration(row.time?.runTimeMs)}
              </span>
              <AnalyticsOutcomeBar metrics={row} focusable={false} />
              <span className="analytics-resource-rate">
                {rate(row.runs?.completed, row.runs?.total)} completed
              </span>
            </button>
          ))}
        </div>
      ) : null}
      <div className="analytics-chart-selection" aria-live="polite">
        {active ? (
          <>
            <strong>{active.key?.label}</strong>
            <span>
              {countLabel(active.runs?.total, "Run")} ·{" "}
              {duration(active.time?.runTimeMs)} · {count(active.runs?.failed)}{" "}
              failed
            </span>
          </>
        ) : (
          <span>
            {rows.length < (data.rows?.length ?? 0) || data.nextCursor
              ? `Top ${rows.length} · More in Details`
              : data.by === "model"
                ? countLabel(rows.length, "model")
                : data.by === "agent"
                  ? countLabel(rows.length, "Agent")
                  : countLabel(rows.length, "entry", "entries")}
          </span>
        )}
      </div>
      {metric === "cost" ? (
        <p className="analytics-chart-note">
          Known cost · Unpriced usage in Details
        </p>
      ) : null}
    </>
  );
}
