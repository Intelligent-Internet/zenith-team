import { useMemo } from "react";
import type { EChartsOption } from "echarts";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { contrastingText } from "@/app/appearance/color";
import { useAnalyticsPalette } from "../model/palette";
import { metricAmount, metricLabel } from "../model/metrics";
import { partLabel, rate } from "../model/format";
import { AnalyticsEChart } from "./analytics-echart";
import { AnalyticsSelect } from "./analytics-select";

type Row = NonNullable<AnalyticsBreakdown["rows"]>[number];
type Props = {
  rows: Row[];
  metric: string;
  selectedIndex: number;
  onSelect: (index: number) => void;
};

export function AnalyticsUsageMap(props: Props) {
  const { rows, metric, selectedIndex, onSelect } = props;
  const values = useMemo(
    () => rows.map((row) => metricAmount(row, metric)),
    [rows, metric],
  );
  const total = values.reduce<number>((sum, value) => sum + (value ?? 0), 0);
  const activeIndex = selectedIndex < 0 ? 0 : selectedIndex;
  const active = rows[activeIndex];
  const share = total > 0 ? ((values[activeIndex] ?? 0) / total) * 100 : 0;
  return (
    <div className="analytics-usage-map">
      {total > 0 ? (
        <UsageMosaic {...props} selectedIndex={activeIndex} values={values} />
      ) : (
        <p className="analytics-chart-empty">
          No reported {partLabel(metric).toLowerCase()}
        </p>
      )}
      <div className="analytics-map-caption">
        <span>Area = {partLabel(metric).toLowerCase()}</span>
        <span>Hover or select to explore</span>
      </div>
      {active ? (
        <div className="analytics-map-inspector" aria-live="polite">
          <label>
            <span>Work</span>
            <AnalyticsSelect
              aria-label="Work to inspect"
              value={activeIndex}
              onChange={(event) => onSelect(Number(event.target.value))}
            >
              {rows.map((row, index) => (
                <option
                  key={`${row.key?.kind}/${row.key?.id ?? row.key?.label}`}
                  value={index}
                >
                  {row.key?.label}
                </option>
              ))}
            </AnalyticsSelect>
          </label>
          <dl>
            <div>
              <dt>{partLabel(metric)}</dt>
              <dd>{metricLabel(active, metric)}</dd>
            </div>
            <div>
              <dt>Of shown usage</dt>
              <dd>
                {values[activeIndex] == null
                  ? "Unknown"
                  : `${share.toFixed(1)}%`}
              </dd>
            </div>
            <div>
              <dt>Completed</dt>
              <dd>{rate(active.runs?.completed, active.runs?.total)}</dd>
            </div>
          </dl>
        </div>
      ) : null}
    </div>
  );
}

function UsageMosaic({
  rows,
  metric,
  selectedIndex,
  onSelect,
  values,
}: Props & { values: (number | null)[] }) {
  const palette = useAnalyticsPalette();
  const option = useMemo<EChartsOption>(
    () => ({
      animationDurationUpdate: 250,
      tooltip: {
        trigger: "item",
        renderMode: "richText",
        confine: true,
        ...palette.tooltip,
        formatter: (params: unknown) => {
          const index = (params as { data?: { rowIndex?: number } }).data
            ?.rowIndex;
          const row = index == null ? undefined : rows[index];
          return row
            ? `${row.key?.label}\n${metricLabel(row, metric)} ${partLabel(metric)}`
            : "";
        },
      },
      series: [
        {
          type: "treemap",
          left: 0,
          right: 0,
          top: 8,
          bottom: 8,
          roam: false,
          nodeClick: false,
          breadcrumb: { show: false },
          visibleMin: 0,
          label: {
            show: true,
            position: "insideTopLeft",
            padding: 12,
            overflow: "truncate",
            formatter: (params) => {
              const index = (params.data as { rowIndex: number }).rowIndex;
              const row = rows[index];
              return row
                ? `{name|${params.name}}\n{amount|${metricLabel(row, metric)}}`
                : "";
            },
            rich: {
              name: { fontSize: 12, lineHeight: 23 },
              amount: { fontSize: 23, fontWeight: 600, lineHeight: 32 },
            },
          },
          upperLabel: { show: false },
          itemStyle: {
            borderWidth: 3,
            borderColor: palette.canvas,
            gapWidth: 3,
            borderRadius: 8,
          },
          emphasis: { itemStyle: { borderColor: palette.accent } },
          data: rows.flatMap((row, index) =>
            (values[index] ?? 0) > 0
              ? [
                  {
                    name: row.key?.label,
                    value: values[index]!,
                    rowIndex: index,
                    itemStyle: {
                      color: palette.series[index % palette.series.length],
                      borderColor:
                        index === selectedIndex
                          ? palette.accent
                          : palette.canvas,
                    },
                    label: {
                      color: contrastingText(
                        palette.series[index % palette.series.length],
                      ),
                    },
                  },
                ]
              : [],
          ),
        },
      ],
    }),
    [rows, metric, values, selectedIndex, palette],
  );
  const events = useMemo(
    () => ({
      click: (event: unknown) => {
        const index = (event as { data?: { rowIndex?: number } }).data
          ?.rowIndex;
        if (index != null) onSelect(index);
      },
    }),
    [onSelect],
  );
  return (
    <AnalyticsEChart
      option={option}
      height={320}
      ariaLabel="Work distribution. Each tile's area represents its usage. Hover a tile or use the Work selector for full names and exact values."
      onEvents={events}
    />
  );
}
