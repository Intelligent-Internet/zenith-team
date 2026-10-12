import type { AnalyticsSummary } from "@/shared/api/types";

export type Metrics = NonNullable<AnalyticsSummary["current"]>;
export type AnalyticsCost = NonNullable<Metrics["cost"]>;

export function sum(...values: (string | null | undefined)[]): string {
  return values
    .reduce<bigint>((total, value) => total + BigInt(value ?? "0"), 0n)
    .toString();
}
export function count(value?: string | null): string {
  return value == null ? "—" : new Intl.NumberFormat().format(BigInt(value));
}
export function countLabel(
  value: string | number | null | undefined,
  singular: string,
  plural = `${singular}s`,
): string {
  return `${count(value?.toString())} ${BigInt(value ?? 0) === 1n ? singular : plural}`;
}
export function tokens(metrics?: Metrics): string {
  if (!metrics) return "—";
  if (
    metrics.runs?.total !== "0" &&
    metrics.runs?.total === metrics.runs?.withoutUsage
  )
    return "No usage reported";
  return `${metrics.runs?.withoutUsage !== "0" ? "≥ " : ""}${count(sum(metrics.tokens?.input, metrics.tokens?.output))}`;
}
export function money(ticks?: string | null): string {
  if (ticks == null) return "Unknown";
  const value = BigInt(ticks);
  if (value > 0n && value < 100_000_000n) return "<$0.01";
  const cents = (value + 50_000_000n) / 100_000_000n;
  return `$${count((cents / 100n).toString())}.${(cents % 100n).toString().padStart(2, "0")}`;
}
export function cost(value?: AnalyticsCost, withoutUsage?: string): string {
  if (!value) return "—";
  if (value.reportedUsdTicks == null && value.estimatedUsdTicks == null) {
    return BigInt(value.planCoveredTokens ?? "0") > 0n &&
      value.uncostedFactCount === "0" &&
      (!withoutUsage || withoutUsage === "0")
      ? "Plan-covered"
      : "Unknown";
  }
  const partial =
    value.uncostedFactCount !== "0" || (!!withoutUsage && withoutUsage !== "0");
  return `${partial ? "≥ " : ""}${money(sum(value.reportedUsdTicks, value.estimatedUsdTicks))}${value.estimatedUsdTicks != null ? " estimate" : ""}`;
}
export function duration(value?: string | number | null): string {
  if (value == null) return "—";
  const seconds = Math.max(0, Math.round(Number(value) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  if (seconds < 86400)
    return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
  return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`;
}
export function rate(numerator?: string, denominator?: string): string {
  const den = BigInt(denominator ?? "0");
  if (!den) return "—";
  return `${Number((BigInt(numerator ?? "0") * 1000n) / den) / 10}%`;
}
export function change(
  current?: string | null,
  previous?: string | null,
): string {
  if (current == null || previous == null) return "—";
  const now = BigInt(current),
    before = BigInt(previous);
  if (!before) return now ? "New" : "—";
  const percent = ((now - before) * 100n) / before;
  return `${percent >= 0 ? "+" : ""}${percent}% vs previous`;
}
export function finished(metrics?: Metrics): string {
  return sum(metrics?.runs?.completed, metrics?.runs?.failed);
}
export function dateLabel(value?: string): string {
  return value ? new Date(value).toLocaleString() : "—";
}
export function partLabel(value: string): string {
  return value
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replaceAll("_", " ")
    .replace(/^./, (c) => c.toUpperCase());
}
