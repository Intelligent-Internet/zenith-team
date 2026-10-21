import type { AnalyticsSeries } from "@/shared/api/types";

// Cache counters accompany Tokens, and successful Runs accompany failures;
// neither is part of the metric named by these charts.
export function seriesKeys(series?: AnalyticsSeries): string[] {
  if (series?.metric === "tokens") return ["input", "output"];
  if (series?.metric === "failures") return ["failed"];
  return [
    ...new Set(
      (series?.points ?? []).flatMap((point) => Object.keys(point.parts ?? {})),
    ),
  ];
}
