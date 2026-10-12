import type { Metrics } from "../model/format";
import { outcomeDescription, runOutcomes } from "../model/metrics";
import { Tooltip } from "@/shared/ui/tooltip";

export function AnalyticsOutcomeBar({
  metrics,
  focusable = true,
}: {
  metrics: Metrics;
  focusable?: boolean;
}) {
  const total = Number(metrics.runs?.total ?? 0);
  const description = outcomeDescription(metrics);
  return (
    <Tooltip label={description}>
      <span
        className="analytics-outcome-bar"
        role="img"
        aria-label={description}
        tabIndex={focusable ? 0 : undefined}
      >
        {runOutcomes(metrics).map((part) => (
          <i
            key={part.name}
            style={{
              width: `${total ? (part.value / total) * 100 : 0}%`,
              background: part.color,
            }}
          />
        ))}
      </span>
    </Tooltip>
  );
}
