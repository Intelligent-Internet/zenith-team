import {
  Bar,
  BarChart,
  Line,
  ComposedChart,
  Area,
  Pie,
  PieChart,
  Cell,
  Scatter,
  ScatterChart,
  XAxis,
  YAxis,
  Tooltip,
  Legend,
  CartesianGrid,
  ResponsiveContainer,
} from "recharts";
import { useMemo, useSyncExternalStore } from "react";
import { uiColor } from "./style";

const reducedMotionQuery = "(prefers-reduced-motion: reduce)";
function useReducedMotion() {
  return useSyncExternalStore(
    (notify) => {
      const query = window.matchMedia?.(reducedMotionQuery);
      query?.addEventListener("change", notify);
      return () => query?.removeEventListener("change", notify);
    },
    () => window.matchMedia?.(reducedMotionQuery).matches ?? false,
    () => true,
  );
}

type Series = {
  dataKey: string;
  label?: string;
  valueSuffix?: string;
  valuePrefix?: string;
  valueFormat?: string;
  lowerKey?: string;
  upperKey?: string;
  lower?: number;
  upper?: number;
};
type Config = {
  chartType?: string;
  xKey?: string;
  nameKey?: string;
  valueKey?: string;
  series?: Series[];
  data?: Record<string, unknown>[];
  xAxisLabel?: string;
  axisLabel?: string;
  valueSuffix?: string;
  yAxisMin?: number;
  yAxisMax?: number;
  xAxisScale?: string;
  layout?: "horizontal" | "vertical";
};
// Charts draw in over 1.8s once, when they first appear. Content is memoized by
// value so streaming re-renders never replay the animation.
export function InlineChart({
  content: authored,
  animate = true,
}: {
  content: Config;
  animate?: boolean;
}) {
  const signature = JSON.stringify(authored ?? null);
  const content = useMemo(() => authored, [signature]); // eslint-disable-line react-hooks/exhaustive-deps
  const reduced = useReducedMotion();
  const motion = {
    isAnimationActive: animate && !reduced,
    animationDuration: 1800,
    animationEasing: "ease-out" as const,
  };
  const kind = content?.chartType ?? "bar";
  const data = content?.data;
  const x = content?.xKey ?? content?.nameKey;
  const series =
    content?.series ??
    (content?.valueKey ? [{ dataKey: content.valueKey }] : []);
  if (
    !Array.isArray(data) ||
    data.length > 1000 ||
    !x ||
    !["bar", "line", "pie", "scatter"].includes(kind) ||
    !Array.isArray(series) ||
    !series.length ||
    series.length > 12 ||
    series.some((s) => !s || typeof s.dataKey !== "string") ||
    data.some(
      (row) =>
        !row ||
        typeof row !== "object" ||
        series.some(
          (s) =>
            typeof row[s.dataKey] !== "number" ||
            !Number.isFinite(row[s.dataKey]) ||
            (kind === "pie" && Number(row[s.dataKey]) < 0),
        ),
    )
  ) {
    return (
      <p
        role="status"
        className="rounded-workspace-control border border-workspace-divider p-3 text-sm text-workspace-muted"
      >
        Chart: provide bounded rows, an xKey and numeric series dataKey values.
      </p>
    );
  }
  const colors = ["accent", "info", "success", "warning", "secondary"];
  const vertical = kind === "bar" && content.layout === "vertical";
  const format = (value: unknown, seriesIndex = 0) => {
    const item = series[seriesIndex];
    return `${item?.valuePrefix ?? ""}${typeof value === "number" ? value.toLocaleString(undefined, item?.valueFormat === "compact" ? { notation: "compact" } : undefined) : String(value ?? "")}${item?.valueSuffix ?? content.valueSuffix ?? ""}`;
  };
  const axes = (
    <>
      <CartesianGrid stroke="var(--workspace-divider)" vertical={false} />
      <XAxis
        dataKey={vertical ? undefined : x}
        label={
          content.xAxisLabel
            ? {
                value: content.xAxisLabel,
                position: "insideBottom",
                offset: -5,
              }
            : undefined
        }
        domain={
          vertical
            ? [content.yAxisMin ?? "auto", content.yAxisMax ?? "auto"]
            : undefined
        }
        tickLine={false}
        axisLine={false}
        type={
          vertical || kind === "scatter" || content.xAxisScale === "linear"
            ? "number"
            : "category"
        }
      />
      <YAxis
        dataKey={vertical ? x : kind === "scatter" ? "y" : undefined}
        type={vertical ? "category" : "number"}
        domain={
          vertical
            ? undefined
            : [content.yAxisMin ?? "auto", content.yAxisMax ?? "auto"]
        }
        label={
          content.axisLabel
            ? { value: content.axisLabel, angle: -90, position: "insideLeft" }
            : undefined
        }
        tickLine={false}
        axisLine={false}
      />
      <Tooltip
        formatter={(value, name) => [
          format(
            value,
            Math.max(
              0,
              series.findIndex((s) => (s.label ?? s.dataKey) === name),
            ),
          ),
          name,
        ]}
        contentStyle={{
          background: "var(--workspace-panel-bg)",
          borderColor: "var(--workspace-divider)",
          borderRadius: 8,
        }}
      />
      <Legend />
    </>
  );
  const bars = series.map((s, i) => (
    <Bar
      key={s.dataKey}
      dataKey={s.dataKey}
      name={s.label ?? s.dataKey}
      fill={uiColor(colors[i % colors.length])}
      {...motion}
      radius={[4, 4, 0, 0]}
    />
  ));
  const lines = series.map((s, i) => (
    <Line
      key={s.dataKey}
      dataKey={s.dataKey}
      name={s.label ?? s.dataKey}
      stroke={uiColor(colors[i % colors.length])}
      {...motion}
      strokeWidth={2}
      dot={data.length < 30}
    />
  ));
  return (
    <figure
      aria-label={series.map((s) => s.label ?? s.dataKey).join(", ")}
      className="min-w-0 space-y-3 rounded-workspace-panel border border-workspace-divider bg-workspace-panel p-4"
    >
      <div className="h-64 min-w-0 text-xs text-workspace-muted">
        <ResponsiveContainer width="100%" height="100%">
          {kind === "pie" ? (
            <PieChart>
              <Pie
                data={data}
                nameKey={x}
                dataKey={series[0].dataKey}
                {...motion}
              >
                {data.map((_, i) => (
                  <Cell key={i} fill={uiColor(colors[i % colors.length])} />
                ))}
              </Pie>
              <Tooltip formatter={(value) => format(value)} />
              <Legend />
            </PieChart>
          ) : kind === "line" ? (
            <ComposedChart data={data}>
              {series.map(
                (s, i) =>
                  (s.lowerKey !== undefined || s.lower !== undefined) && (
                    <Area
                      key={`band-${s.dataKey}`}
                      dataKey={(row: Record<string, unknown>) => [
                        s.lowerKey !== undefined
                          ? Number(row[s.lowerKey])
                          : s.lower!,
                        s.upperKey !== undefined
                          ? Number(row[s.upperKey])
                          : s.upper!,
                      ]}
                      stroke="none"
                      fill={uiColor(colors[i % colors.length])}
                      fillOpacity={0.15}
                      {...motion}
                      legendType="none"
                      tooltipType="none"
                    />
                  ),
              )}
              {axes}
              {lines}
            </ComposedChart>
          ) : kind === "scatter" ? (
            <ScatterChart>
              {axes}
              {series.map((s, i) => (
                <Scatter
                  key={s.dataKey}
                  name={s.label ?? s.dataKey}
                  data={data.map((row) => ({ ...row, y: row[s.dataKey] }))}
                  fill={uiColor(colors[i % colors.length])}
                  {...motion}
                />
              ))}
            </ScatterChart>
          ) : (
            <BarChart data={data} layout={vertical ? "vertical" : "horizontal"}>
              {axes}
              {bars}
            </BarChart>
          )}
        </ResponsiveContainer>
      </div>
      <details className="text-xs text-workspace-muted">
        <summary className="cursor-pointer">View data</summary>
        <div className="mt-2 max-h-64 overflow-auto">
          <table className="w-full text-left">
            <thead>
              <tr>
                <th className="p-2">{x}</th>
                {series.map((s) => (
                  <th key={s.dataKey} className="p-2">
                    {s.label ?? s.dataKey}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.map((row, i) => (
                <tr key={i}>
                  <td className="p-2">{String(row[x] ?? "")}</td>
                  {series.map((s) => (
                    <td key={s.dataKey} className="p-2 tabular-nums">
                      {format(row[s.dataKey], series.indexOf(s))}
                      {(s.lowerKey !== undefined || s.lower !== undefined) && (
                        <span className="ml-1 text-workspace-muted">
                          (
                          {format(
                            s.lowerKey !== undefined
                              ? row[s.lowerKey]
                              : s.lower,
                            series.indexOf(s),
                          )}
                          –
                          {format(
                            s.upperKey !== undefined
                              ? row[s.upperKey]
                              : s.upper,
                            series.indexOf(s),
                          )}
                          )
                        </span>
                      )}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </figure>
  );
}
