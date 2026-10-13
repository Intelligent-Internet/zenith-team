import { useId, useMemo, useState } from "react";
import type { AnalyticsBreakdown } from "@/shared/api/types";
import { mixColor } from "@/app/appearance/color";
import { useAnalyticsPalette } from "../model/palette";
import { workComposition } from "../model/work-composition";
import { workFlow, type WorkFlowNode } from "../model/work-flow";
import { formatMetricAmount, metricLabel } from "../model/metrics";
import { partLabel } from "../model/format";

export function AnalyticsWorkFlow({
  data,
  runTypes,
  metric,
}: {
  data?: AnalyticsBreakdown;
  runTypes?: AnalyticsBreakdown;
  metric: string;
}) {
  const palette = useAnalyticsPalette();
  const gradient = useId();
  const [hovered, setHovered] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const flow = useMemo(
    () =>
      workFlow(data && runTypes ? workComposition(runTypes, data, metric) : []),
    [data, runTypes, metric],
  );
  const nodes = [flow.root, ...flow.groups, ...flow.leaves];
  const active = nodes.find((node) => node.id === (hovered ?? selected));
  const percent = (value: number) =>
    `${((value / flow.total) * 100).toFixed(1)}%`;
  const select = (id: string) =>
    setSelected((current) => (current === id ? null : id));
  const related = (source: string, target: string) =>
    !active ||
    active.id === "all_work" ||
    active.id === target ||
    active.id === source ||
    active.parentId === target ||
    (active.id === "task" && source === "task");
  const color = (node: WorkFlowNode) =>
    mixColor(
      palette.canvas,
      palette.accent,
      node.id === "all_work"
        ? 1
        : node.id === "task" || node.parentId === "task"
          ? 0.68
          : 0.36,
    );
  const valueLabel = (node: WorkFlowNode) => {
    if (node.id === "all_work" && runTypes?.total)
      return metricLabel(runTypes.total, metric);
    const row =
      data?.rows?.find(
        (row) => row.key?.id === node.id || row.key?.synthetic === node.id,
      ) ??
      (node.id !== "task"
        ? runTypes?.rows?.find((row) => row.key?.synthetic === node.id)
        : undefined);
    return row
      ? metricLabel(row, metric)
      : formatMetricAmount(node.value, metric);
  };
  const label = (node: WorkFlowNode) =>
    `${node.name} · ${valueLabel(node)} ${partLabel(metric).toLowerCase()} · ${percent(node.value)} of known total`;
  if (!data || !runTypes) return <p role="status">Loading work allocation…</p>;
  if (!flow.total)
    return <p>No reported {partLabel(metric).toLowerCase()} in this window.</p>;
  return (
    <div
      className="analytics-work-flow mt-[20px]"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          setSelected(null);
          setHovered(null);
        }
      }}
    >
      <div className="analytics-flow-scroll">
        <div
          className="analytics-flow-plot relative min-w-[840px]"
          onMouseLeave={() => setHovered(null)}
        >
          <svg
            viewBox={`0 0 1200 ${flow.height}`}
            role="group"
            aria-label={`Work allocation by ${partLabel(metric).toLowerCase()}. Flow width represents known usage.`}
          >
            <defs>
              <linearGradient id={gradient} x1="0" x2="1" y1="0" y2="0">
                <stop
                  stopColor={mixColor(palette.canvas, palette.accent, 0.17)}
                />
                <stop
                  offset="1"
                  stopColor={mixColor(palette.canvas, palette.accent, 0.54)}
                />
              </linearGradient>
            </defs>
            {flow.links.map((link) => (
              <path
                key={`${link.source}/${link.target}`}
                d={link.path}
                className="cursor-pointer [transition:opacity_220ms_ease]"
                fill={`url(#${gradient})`}
                stroke={mixColor(palette.canvas, palette.accent, 0.38)}
                strokeWidth={0.5}
                opacity={related(link.source, link.target) ? 1 : 0.14}
                onMouseEnter={() => setHovered(link.target)}
                onClick={() => select(link.target)}
              />
            ))}
            {nodes.map((node) => {
              const root = node.id === "all_work";
              const leaf = node.parentId === "task";
              const center = node.y + node.height / 2;
              const nodeLabel = label(node);
              return (
                <g
                  key={node.id}
                  className="analytics-flow-node"
                  role="button"
                  tabIndex={0}
                  aria-label={nodeLabel}
                  aria-pressed={selected === node.id}
                  onMouseEnter={() => setHovered(node.id)}
                  onFocus={() => setHovered(node.id)}
                  onBlur={() => setHovered(null)}
                  onClick={() => select(node.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      select(node.id);
                    }
                  }}
                >
                  <title>{nodeLabel}</title>
                  <rect
                    className="analytics-flow-hit"
                    x={root ? 0 : node.x - 5}
                    y={center - Math.max(48, node.height) / 2}
                    width={root ? 123 : leaf ? 310 : 235}
                    height={Math.max(48, node.height)}
                    fill="transparent"
                  />
                  <rect
                    className="analytics-flow-marker"
                    x={node.x}
                    y={center - Math.max(2, node.height) / 2}
                    width={16}
                    height={Math.max(2, node.height)}
                    rx={3}
                    fill={color(node)}
                    stroke={active?.id === node.id ? palette.accent : "none"}
                    strokeWidth={1.5}
                  />
                  {root ? (
                    <foreignObject
                      x={0}
                      y={center - 40}
                      width={90}
                      height={90}
                      pointerEvents="none"
                    >
                      <div className="analytics-flow-root flex h-full flex-col justify-center gap-[5px] text-right wrap-anywhere">
                        <strong>{valueLabel(node)}</strong>
                        <span>{partLabel(metric).toLowerCase()}</span>
                      </div>
                    </foreignObject>
                  ) : (
                    <foreignObject
                      x={node.x + 30}
                      y={center - 30}
                      width={leaf ? 280 : 215}
                      height={68}
                      pointerEvents="none"
                    >
                      <div className="analytics-flow-label w-[fit-content] max-w-full p-[4px] rounded-[4px] [background:color-mix(in_srgb,_var(--analytics-panel)_94%,_transparent)]">
                        <span>{node.name}</span>
                        <div>
                          <strong>
                            {leaf ? valueLabel(node) : percent(node.value)}
                          </strong>
                          <small>
                            {leaf ? percent(node.value) : valueLabel(node)}
                          </small>
                        </div>
                      </div>
                    </foreignObject>
                  )}
                </g>
              );
            })}
          </svg>
          {active ? (
            <div
              className="analytics-flow-tooltip pointer-events-none absolute [transform:translateY(-100%)] grid gap-[5px] max-w-[270px] w-[max-content] p-[12px_15px] text-workspace-caption [border:1px_solid_var(--analytics-line)] rounded-[8px] [background:var(--workspace-overlay-bg)] [box-shadow:0_6px_22px_color-mix(in_srgb,_var(--analytics-text)_10%,_transparent)]"
              role="status"
              style={{
                top: `max(90px, calc(${((active.y + active.height / 2) / flow.height) * 100}% - 14px))`,
                ...(active.id === "all_work"
                  ? { left: `${((active.x + 30) / 1200) * 100}%` }
                  : { right: `${100 - ((active.x - 12) / 1200) * 100}%` }),
              }}
            >
              <strong>{active.name}</strong>
              <span>
                {valueLabel(active)} {partLabel(metric).toLowerCase()}
              </span>
              <span>{percent(active.value)} of known total</span>
            </div>
          ) : null}
        </div>
      </div>
      <div className="analytics-flow-footer">
        <span>Width = {partLabel(metric).toLowerCase()}</span>
        <span>
          Known usage ·{" "}
          {flow.leaves.length ? "Select a path to explore" : "Selected window"}
        </span>
      </div>
    </div>
  );
}
