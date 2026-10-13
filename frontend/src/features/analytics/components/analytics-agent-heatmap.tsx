import { useMemo, useRef, useState, type CSSProperties } from "react";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { Tooltip } from "@/shared/ui/tooltip";
import { mixColor } from "@/app/appearance/color";
import {
  useAgentSeries,
  type AnalyticsSeriesQuery,
} from "../model/use-agent-series";
import { useAnalyticsPalette } from "../model/palette";
import { seriesKeys } from "../model/series";
import { formatMetricAmount, metricLabel } from "../model/metrics";
import { countLabel, duration, partLabel, rate } from "../model/format";

export function AnalyticsAgentHeatmap({
  data,
  query,
}: {
  data?: AnalyticsBreakdown;
  query: AnalyticsSeriesQuery;
}) {
  const palette = useAnalyticsPalette();
  const gridRef = useRef<HTMLDivElement>(null);
  const [hovered, setHovered] = useState<number | null>(null);
  const [selected, setSelected] = useState<{
    agent: string;
    date: string;
  } | null>(null);
  const agents = useMemo(
    () => (data?.rows ?? []).filter((row) => row.key?.id).slice(0, 5),
    [data],
  );
  const { results, retry } = useAgentSeries(
    agents.map((row) => ({ ...query, agentId: row.key!.id! })),
  );
  const lanes = useMemo(
    () =>
      results.map((result) => {
        const keys = seriesKeys(result.data);
        return new Map(
          (result.data?.points ?? []).map((point) => {
            const known = keys
              .map((key) => point.parts?.[key])
              .filter((value) => value != null);
            return [
              point.startAt!,
              known.length
                ? known.reduce((total, value) => total + Number(value), 0)
                : null,
            ];
          }),
        );
      }),
    [results],
  );
  const dates = useMemo(
    () => [...new Set(lanes.flatMap((lane) => [...lane.keys()]))].sort(),
    [lanes],
  );
  const maximum = Math.max(
    1,
    ...lanes.flatMap((lane) => [...lane.values()].map((value) => value ?? 0)),
  );
  const dateLabel = (date: string) =>
    new Date(date).toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
      timeZone: query.timeZone,
    });
  const selectedColumn = selected ? dates.indexOf(selected.date) : -1;
  const activeColumn = hovered ?? selectedColumn;
  const selectedAgent = agents.findIndex(
    (row) => row.key?.id === selected?.agent,
  );
  const activeRow = agents[selectedAgent];
  const period =
    query.bucket === "week"
      ? "Weekly"
      : query.bucket === "month"
        ? "Monthly"
        : "Daily";
  const cellColor = (value: number | null) =>
    mixColor(
      palette.canvas,
      palette.accent,
      value == null || value === 0 ? 0.045 : 0.16 + (value / maximum) * 0.84,
    );
  if (results.some((result) => result.isError))
    return (
      <p role="alert">
        Activity could not be loaded.{" "}
        <button type="button" onClick={retry}>
          Retry
        </button>
      </p>
    );
  if (!data || results.some((result) => !result.data))
    return (
      <div
        className="grid place-items-center h-[350px] text-[var(--analytics-muted)]"
        role="status"
      >
        Loading activity…
      </div>
    );
  if (!agents.length || !dates.length)
    return <p>No Agent activity in this window.</p>;
  return (
    <div className="analytics-agent-heatmap mt-[18px]">
      <div className="text-[var(--analytics-muted)] text-workspace-caption mb-[16px]">
        {agents.length < (data.rows?.length ?? 0) ? "Top 5 · " : ""}
        {period} {partLabel(query.metric).toLowerCase()}
      </div>
      <div className="analytics-heatmap-scroll">
        <div
          className="min-w-[max(720px,_calc(var(--heatmap-columns)_*_12px_+_340px))]"
          ref={gridRef}
          style={{ "--heatmap-columns": dates.length } as CSSProperties}
          onMouseLeave={() => setHovered(null)}
        >
          <div
            className="analytics-heatmap-row analytics-heatmap-axis"
            aria-hidden="true"
          >
            <span />
            <div className="analytics-heatmap-dates">
              {dates.map((date, index) => (
                <span key={date}>
                  {index === dates.length - 1 ||
                  (index < dates.length - Math.ceil(dates.length / 12) &&
                    index % Math.max(1, Math.ceil(dates.length / 5)) === 0)
                    ? dateLabel(date)
                    : ""}
                </span>
              ))}
            </div>
            <span>{partLabel(query.metric)}</span>
            <span>Completed</span>
          </div>
          {agents.map((row, rowIndex) => {
            const id = row.key!.id!;
            const completion =
              Number(row.runs?.total) > 0
                ? Number(row.runs?.completed ?? 0) / Number(row.runs!.total)
                : 0;
            const detail = `${row.key?.label} · ${countLabel(row.runs?.total, "Run")} · ${duration(row.time?.p50RunTimeMs)} median · ${rate(row.runs?.completed, row.runs?.total)} completed`;
            return (
              <div className="analytics-heatmap-row" key={id}>
                <Tooltip label={detail}>
                  <span
                    className="text-workspace-body font-[550] leading-[1.4] overflow-hidden [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical]"
                    tabIndex={0}
                  >
                    {row.key?.label}
                  </span>
                </Tooltip>
                <div className="analytics-heatmap-cells">
                  {dates.map((date, column) => {
                    const value = lanes[rowIndex]?.get(date) ?? null;
                    const label = `${dateLabel(date)} · ${row.key?.label} · ${formatMetricAmount(value, query.metric)} ${partLabel(query.metric).toLowerCase()}`;
                    return (
                      <Tooltip key={date} label={label}>
                        <button
                          type="button"
                          className={`analytics-heatmap-cell${value == null ? " is-unknown" : ""}${activeColumn === column ? " is-column-active" : ""}`}
                          style={
                            {
                              "--heatmap-cell": cellColor(value),
                            } as CSSProperties
                          }
                          aria-label={label}
                          aria-pressed={
                            selected?.agent === id && selected.date === date
                          }
                          data-row={rowIndex}
                          data-column={column}
                          tabIndex={
                            column === Math.max(0, selectedColumn) ? 0 : -1
                          }
                          onMouseEnter={() => setHovered(column)}
                          onFocus={() => setHovered(column)}
                          onBlur={() => setHovered(null)}
                          onClick={() =>
                            setSelected((current) =>
                              current?.agent === id && current.date === date
                                ? null
                                : { agent: id, date },
                            )
                          }
                          onKeyDown={(event) => {
                            if (event.key === "Escape") {
                              setSelected(null);
                              setHovered(null);
                              return;
                            }
                            const nextRow =
                              event.key === "ArrowDown"
                                ? Math.min(agents.length - 1, rowIndex + 1)
                                : event.key === "ArrowUp"
                                  ? Math.max(0, rowIndex - 1)
                                  : rowIndex;
                            const nextColumn =
                              event.key === "ArrowRight"
                                ? Math.min(dates.length - 1, column + 1)
                                : event.key === "ArrowLeft"
                                  ? Math.max(0, column - 1)
                                  : event.key === "Home"
                                    ? 0
                                    : event.key === "End"
                                      ? dates.length - 1
                                      : column;
                            if (nextRow !== rowIndex || nextColumn !== column) {
                              event.preventDefault();
                              gridRef.current
                                ?.querySelector<HTMLButtonElement>(
                                  `[data-row="${nextRow}"][data-column="${nextColumn}"]`,
                                )
                                ?.focus();
                            }
                          }}
                        />
                      </Tooltip>
                    );
                  })}
                </div>
                <strong
                  className="text-right text-[length:17px] font-[600] tracking-[-0.025em] wrap-anywhere"
                  title={metricLabel(row, query.metric)}
                >
                  {metricLabel(row, query.metric)}
                </strong>
                <Tooltip label={detail}>
                  <span
                    className="analytics-heatmap-completed flex items-center justify-end gap-[8px] text-workspace-body whitespace-nowrap"
                    tabIndex={0}
                  >
                    <svg viewBox="0 0 32 32" aria-hidden="true">
                      <circle
                        cx="16"
                        cy="16"
                        r="12"
                        fill="none"
                        stroke={palette.fillSoft}
                        strokeWidth="4"
                      />
                      <circle
                        cx="16"
                        cy="16"
                        r="12"
                        fill="none"
                        stroke={palette.accent}
                        strokeWidth="4"
                        pathLength="100"
                        strokeDasharray={`${completion * 100} 100`}
                        transform="rotate(-90 16 16)"
                      />
                    </svg>
                    {rate(row.runs?.completed, row.runs?.total)}
                  </span>
                </Tooltip>
              </div>
            );
          })}
        </div>
      </div>
      <div className="analytics-heatmap-footer">
        <span className="analytics-heatmap-selection" aria-live="polite">
          {activeRow && selectedColumn >= 0
            ? `${dateLabel(selected!.date)} · ${activeRow.key?.label} · ${formatMetricAmount(lanes[selectedAgent]?.get(selected!.date) ?? null, query.metric)} ${partLabel(query.metric).toLowerCase()}`
            : ""}
        </span>
        <div
          className="analytics-heatmap-scale flex items-center gap-[3px] ml-auto"
          aria-label={`Color scale: 0 to ${formatMetricAmount(maximum, query.metric)} ${partLabel(query.metric)} per period, shared by all Agents`}
        >
          <span>0</span>
          {[0, 0.2, 0.4, 0.6, 0.8, 1].map((level) => (
            <i key={level} style={{ background: cellColor(maximum * level) }} />
          ))}
          <span>{formatMetricAmount(maximum, query.metric)}</span>
        </div>
      </div>
    </div>
  );
}
