import { useId, useMemo, useState } from "react";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { contrastingText, mixColor } from "@/app/appearance/color";
import { useAnalyticsPalette } from "../model/palette";
import {
  workComposition,
  ringSector,
  type WorkPart,
} from "../model/work-composition";
import { formatMetricAmount, metricLabel } from "../model/metrics";
import { partLabel } from "../model/format";

export function AnalyticsWorkComposition({
  data,
  runTypes,
  metric,
}: {
  data?: AnalyticsBreakdown;
  runTypes?: AnalyticsBreakdown;
  metric: string;
}) {
  const palette = useAnalyticsPalette();
  const gradientId = useId();
  const [hovered, setHovered] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const parts = useMemo(
    () => (data && runTypes ? workComposition(runTypes, data, metric) : []),
    [data, runTypes, metric],
  );
  const total = parts.reduce((sum, part) => sum + part.value, 0);
  const percent = (value: number) => `${((value / total) * 100).toFixed(1)}%`;
  const sectors = useMemo(() => {
    // Start small categories at twelve o'clock, then draw the dominant group.
    let angle = -Math.PI / 2;
    return [...parts].reverse().map((part) => {
      const start = angle;
      angle += (part.value / total) * Math.PI * 2;
      return { part, start, end: angle };
    });
  }, [parts, total]);
  const activeId = hovered ?? selected;
  const activeGroup = parts.find(
    (part) =>
      part.id === activeId ||
      part.children?.some((child) => child.id === activeId),
  );
  const active =
    activeGroup?.children?.find((child) => child.id === activeId) ??
    activeGroup;
  const color = (part: WorkPart) =>
    mixColor(
      palette.canvas,
      palette.accent,
      part.id === parts[0]?.id
        ? 1
        : part.id === "task"
          ? 0.48
          : 0.2 + parts.indexOf(part) * 0.07,
    );
  const select = (id: string) =>
    setSelected((current) => (current === id ? null : id));
  const label = (part: WorkPart) =>
    `${part.name}: ${formatMetricAmount(part.value, metric)} ${partLabel(metric)}, ${percent(part.value)}`;
  return (
    <section
      className="analytics-card analytics-work-composition"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          setSelected(null);
          setHovered(null);
        }
      }}
    >
      <div className="analytics-rhythm-heading">
        <h2>Work composition</h2>
      </div>
      {!data || !runTypes ? (
        <div className="analytics-rhythm-loading" role="status">
          Loading composition…
        </div>
      ) : !total ? (
        <p>No reported {partLabel(metric).toLowerCase()} in this window.</p>
      ) : (
        <>
          <div
            className="analytics-work-ring"
            onMouseLeave={() => setHovered(null)}
          >
            <svg
              viewBox="0 -55 600 655"
              role="group"
              aria-label={`Work composition by ${partLabel(metric).toLowerCase()}`}
            >
              {sectors.map(({ part, start, end }) => {
                const middle = (start + end) / 2;
                const active = activeGroup?.id === part.id;
                const outer = part.children?.length ? 218 : 280;
                const fill = color(part);
                const offset = active ? 4 : 0;
                let childAngle = start;
                return (
                  <g
                    key={part.id}
                    className="analytics-work-sector"
                    style={{
                      transform: `translate(${Math.cos(middle) * offset}px, ${Math.sin(middle) * offset}px)`,
                    }}
                  >
                    <defs>
                      <linearGradient
                        id={`${gradientId}-${part.id}`}
                        x1="0"
                        y1="0"
                        x2="1"
                        y2="1"
                      >
                        <stop
                          stopColor={mixColor(fill, palette.canvas, 0.08)}
                        />
                        <stop offset="1" stopColor={fill} />
                      </linearGradient>
                    </defs>
                    <path
                      d={ringSector(start, end, 103, outer)}
                      fill={`url(#${gradientId}-${part.id})`}
                      stroke={palette.canvas}
                      strokeWidth={2}
                      role="button"
                      tabIndex={0}
                      aria-label={label(part)}
                      aria-pressed={selected === part.id}
                      onMouseEnter={() => setHovered(part.id)}
                      onFocus={() => setHovered(part.id)}
                      onBlur={() => setHovered(null)}
                      onClick={() => select(part.id)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") {
                          event.preventDefault();
                          select(part.id);
                        }
                      }}
                    >
                      <title>{label(part)}</title>
                    </path>
                    {part.children?.map((child, index) => {
                      const childStart = childAngle;
                      childAngle += (child.value / total) * Math.PI * 2;
                      return (
                        <path
                          key={child.id}
                          d={ringSector(childStart, childAngle, 218, 280)}
                          fill={mixColor(
                            palette.canvas,
                            palette.accent,
                            0.32 + (index % 5) * 0.075,
                          )}
                          stroke={palette.canvas}
                          strokeWidth={2}
                          role="button"
                          tabIndex={0}
                          aria-label={label(child)}
                          aria-pressed={selected === child.id}
                          onMouseEnter={() => setHovered(child.id)}
                          onFocus={() => setHovered(child.id)}
                          onBlur={() => setHovered(null)}
                          onClick={() => select(child.id)}
                          onKeyDown={(event) => {
                            if (event.key === "Enter" || event.key === " ") {
                              event.preventDefault();
                              select(child.id);
                            }
                          }}
                        >
                          <title>{label(child)}</title>
                        </path>
                      );
                    })}
                    {part.value / total >= 0.08 ? (
                      <text
                        x={300 + (Math.cos(middle) * (outer + 103)) / 2}
                        y={300 + (Math.sin(middle) * (outer + 103)) / 2 - 4}
                        textAnchor="middle"
                        fill={contrastingText(fill)}
                        pointerEvents="none"
                      >
                        <tspan
                          x={300 + (Math.cos(middle) * (outer + 103)) / 2}
                          fontSize="15"
                        >
                          {part.name}
                        </tspan>
                        <tspan
                          x={300 + (Math.cos(middle) * (outer + 103)) / 2}
                          dy="25"
                          fontSize="22"
                          fontWeight="600"
                        >
                          {percent(part.value)}
                        </tspan>
                      </text>
                    ) : null}
                  </g>
                );
              })}
              {sectors
                .filter(({ part }) => part.value / total < 0.04)
                .slice(0, 2)
                .map(({ part, start, end }, index) => {
                  const angle = (start + end) / 2;
                  const x = 300 + Math.cos(angle) * 273;
                  const y = 300 + Math.sin(angle) * 273;
                  const labelX = index === 0 ? 275 : 335;
                  return (
                    <g key={part.id} pointerEvents="none">
                      <path
                        d={`M ${x} ${y} Q ${x} -3 ${labelX} -20`}
                        stroke={palette.muted}
                        strokeWidth="0.8"
                        fill="none"
                      />
                      <circle cx={x} cy={y} r="2.5" fill={palette.ink} />
                      <text
                        x={labelX}
                        y="-36"
                        textAnchor={index === 0 ? "end" : "start"}
                        fill={palette.ink}
                        fontSize="14"
                      >
                        {part.name}
                        <tspan x={labelX} dy="18" fontWeight="600">
                          {percent(part.value)}
                        </tspan>
                      </text>
                    </g>
                  );
                })}
            </svg>
            <div className="analytics-work-center">
              <strong>
                {runTypes.total
                  ? metricLabel(runTypes.total, metric)
                  : formatMetricAmount(total, metric)}
              </strong>
              <span>{partLabel(metric).toLowerCase()}</span>
            </div>
            {active ? (
              <div className="analytics-work-tooltip" role="status">
                <strong>{active.name}</strong>
                <div>
                  <span>
                    {formatMetricAmount(active.value, metric)}{" "}
                    {partLabel(metric).toLowerCase()}
                  </span>
                  <b>{percent(active.value)}</b>
                </div>
                {active.children?.slice(0, 6).map((child) => (
                  <div key={child.id}>
                    <span title={child.name}>{child.name}</span>
                    <b>{percent(child.value)}</b>
                  </div>
                ))}
                {(active.children?.length ?? 0) > 6 ? (
                  <small>+ {active.children!.length - 6} Tasks</small>
                ) : null}
              </div>
            ) : null}
          </div>
          <div className="analytics-work-key" aria-label="Work categories">
            {parts.map((part) => (
              <button
                key={part.id}
                className={part.value / total < 0.04 ? "is-minor" : undefined}
                type="button"
                aria-pressed={selected === part.id}
                onClick={() => select(part.id)}
                onMouseEnter={() => setHovered(part.id)}
                onMouseLeave={() => setHovered(null)}
              >
                <i style={{ background: color(part) }} />
                <span>
                  {part.name}
                  <strong>{percent(part.value)}</strong>
                </span>
              </button>
            ))}
          </div>
        </>
      )}
    </section>
  );
}
