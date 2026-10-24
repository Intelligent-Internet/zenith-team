import type { components } from "@/shared/api/generated/api.generated";

export function sourceCoverageDescription(
  coverage?: components["schemas"]["IntelligentSourceCoverage"],
): string {
  if (!coverage) return "Result coverage was not recorded.";
  if (coverage.complete)
    return `Complete matching result: ${coverage.rowCount} rows.`;
  return `Bounded sample: ${coverage.rowCount} rows (limit ${coverage.limit}). More matching rows are not included; totals reflect this sample only.`;
}
