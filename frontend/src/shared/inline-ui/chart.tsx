import {
  Bar,
  BarChart,
  Line,
  LineChart,
  Area,
  AreaChart,
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
import { uiColor } from "./style";

type Series = {
  dataKey: string;
  label?: string;
  color?: string;
  valueSuffix?: string;
};
type Config = {
  chartType?: string;
  title?: string;
  xKey?: string;
  nameKey?: string;
  valueKey?: string;
  series?: Series[];
  data?: Record<string, unknown>[];
};
export function InlineChart({ content }: { content: Config }) {
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
    !["bar", "line", "area", "pie", "scatter"].includes(kind) ||
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
  const axes = (
    <>
      <CartesianGrid stroke="var(--workspace-divider)" vertical={false} />
      <XAxis
        dataKey={x}
        tickLine={false}
        axisLine={false}
        type={kind === "scatter" ? "number" : "category"}
      />
      <YAxis
        dataKey={kind === "scatter" ? "y" : undefined}
        tickLine={false}
        axisLine={false}
      />
      <Tooltip
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
      fill={uiColor(s.color ?? colors[i % colors.length])}
      isAnimationActive={false}
      radius={[4, 4, 0, 0]}
    />
  ));
  const lines = series.map((s, i) => (
    <Line
      key={s.dataKey}
      dataKey={s.dataKey}
      name={s.label ?? s.dataKey}
      stroke={uiColor(s.color ?? colors[i % colors.length])}
      isAnimationActive={false}
      strokeWidth={2}
      dot={data.length < 30}
    />
  ));
  return (
    <figure className="min-w-0 space-y-3 rounded-workspace-panel border border-workspace-divider bg-workspace-panel p-4">
      <figcaption className="text-sm font-medium">
        {content.title ?? "Chart"}
      </figcaption>
      <div className="h-64 min-w-0 text-xs text-workspace-muted">
        <ResponsiveContainer width="100%" height="100%">
          {kind === "pie" ? (
            <PieChart>
              <Pie
                data={data}
                nameKey={x}
                dataKey={series[0].dataKey}
                isAnimationActive={false}
              >
                {data.map((_, i) => (
                  <Cell key={i} fill={uiColor(colors[i % colors.length])} />
                ))}
              </Pie>
              <Tooltip />
              <Legend />
            </PieChart>
          ) : kind === "line" ? (
            <LineChart data={data}>
              {axes}
              {lines}
            </LineChart>
          ) : kind === "area" ? (
            <AreaChart data={data}>
              {axes}
              {series.map((s, i) => (
                <Area
                  key={s.dataKey}
                  dataKey={s.dataKey}
                  name={s.label ?? s.dataKey}
                  stroke={uiColor(colors[i % colors.length])}
                  fill={uiColor(colors[i % colors.length])}
                  fillOpacity={0.15}
                  isAnimationActive={false}
                />
              ))}
            </AreaChart>
          ) : kind === "scatter" ? (
            <ScatterChart>
              {axes}
              {series.map((s, i) => (
                <Scatter
                  key={s.dataKey}
                  name={s.label ?? s.dataKey}
                  data={data.map((row) => ({ ...row, y: row[s.dataKey] }))}
                  fill={uiColor(colors[i % colors.length])}
                  isAnimationActive={false}
                />
              ))}
            </ScatterChart>
          ) : (
            <BarChart data={data}>
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
                      {String(row[s.dataKey])}
                      {s.valueSuffix}
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
