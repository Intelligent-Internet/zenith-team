import { cost, duration, money, partLabel, sum, type Metrics } from "./format";

const compact = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});

export function metricAmount(row: Metrics, metric: string): number | null {
  if (metric === "cost") {
    if (
      row.cost?.reportedUsdTicks == null &&
      row.cost?.estimatedUsdTicks == null
    )
      return null;
    return Number(sum(row.cost.reportedUsdTicks, row.cost.estimatedUsdTicks));
  }
  if (metric === "run_time")
    return row.time?.runTimeMs == null ? null : Number(row.time.runTimeMs);
  if (metric === "runs") return Number(row.runs?.total ?? 0);
  if (metric === "failures") return Number(row.runs?.failed ?? 0);
  if (row.runs?.total !== "0" && row.runs?.total === row.runs?.withoutUsage)
    return null;
  return Number(sum(row.tokens?.input, row.tokens?.output));
}

export function formatMetricAmount(
  value: number | null,
  metric: string,
  short = true,
): string {
  if (value == null) return "Unknown";
  if (metric === "cost") return money(String(Math.round(value)));
  if (metric === "run_time") return duration(value);
  return short ? compact.format(value) : value.toLocaleString();
}

export function metricLabel(row: Metrics, metric: string): string {
  if (metric === "cost") return cost(row.cost, row.runs?.withoutUsage);
  const amount = metricAmount(row, metric);
  if (amount == null && metric === "tokens") return "No usage reported";
  const value = formatMetricAmount(amount, metric);
  return metric === "tokens" && Number(row.runs?.withoutUsage ?? 0) > 0
    ? `≥ ${value}`
    : value;
}

export function runOutcomes(row: Metrics) {
  const completed = Number(row.runs?.completed ?? 0);
  const failed = Number(row.runs?.failed ?? 0);
  const cancelled = Number(row.runs?.cancelled ?? 0);
  return [
    { name: "Completed", value: completed, color: "var(--analytics-accent)" },
    { name: "Failed", value: failed, color: "var(--analytics-danger)" },
    {
      name: "Cancelled",
      value: cancelled,
      color: "var(--workspace-native-approval)",
    },
    {
      name: "Other",
      value: Math.max(
        0,
        Number(row.runs?.total ?? 0) - completed - failed - cancelled,
      ),
      color: "var(--analytics-muted)",
    },
  ];
}

export function outcomeDescription(row: Metrics) {
  return (
    runOutcomes(row)
      .filter((part) => part.value > 0)
      .map(
        (part) => `${part.value.toLocaleString()} ${part.name.toLowerCase()}`,
      )
      .join(" · ") || `0 ${partLabel("runs")}`
  );
}
