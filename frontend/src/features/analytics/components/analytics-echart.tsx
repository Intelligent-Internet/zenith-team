import { useEffect, useRef, useState } from "react";
import * as echarts from "echarts/core";
import type { EChartsOption, EChartsType } from "echarts";
import {
  BarChart,
  HeatmapChart,
  LineChart,
  PieChart,
  ScatterChart,
  SunburstChart,
  TreemapChart,
} from "echarts/charts";
import {
  AriaComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TitleComponent,
  TooltipComponent,
  VisualMapComponent,
} from "echarts/components";
import { LabelLayout } from "echarts/features";
import { CanvasRenderer } from "echarts/renderers";
import { useAnalyticsPalette } from "../model/palette";

echarts.use([
  AriaComponent,
  BarChart,
  CanvasRenderer,
  DataZoomComponent,
  GridComponent,
  HeatmapChart,
  LabelLayout,
  LegendComponent,
  LineChart,
  MarkLineComponent,
  PieChart,
  TitleComponent,
  TooltipComponent,
  ScatterChart,
  SunburstChart,
  TreemapChart,
  VisualMapComponent,
]);

export type AnalyticsChartEvent = (params: unknown) => void;

export function AnalyticsEChart({
  option,
  ariaLabel,
  className,
  height = 320,
  onEvents,
}: {
  option: EChartsOption;
  ariaLabel: string;
  className?: string;
  height?: number;
  onEvents?: Record<string, AnalyticsChartEvent>;
}) {
  const palette = useAnalyticsPalette();
  const [visible, setVisible] = useState(false);
  const hostRef = useRef<HTMLDivElement>(null);
  const elementRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | null>(null);
  const rendered = useRef(false);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const root = host.closest(".analytics-page");
    if (!(root instanceof HTMLElement) || !("IntersectionObserver" in window)) {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { root, rootMargin: "360px 0px" },
    );
    observer.observe(host);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const element = elementRef.current;
    if (!visible || !element) return;
    const chart = echarts.init(element, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    rendered.current = false;
    return () => {
      chart.dispose();
      chartRef.current = null;
    };
  }, [visible]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const reducedMotion = window.matchMedia(
      "(prefers-reduced-motion: reduce)",
    ).matches;
    chart.setOption(
      {
        textStyle: { fontFamily: palette.fontFamily, color: palette.ink },
        axisPointer: {
          lineStyle: { color: palette.muted, width: 1 },
          shadowStyle: { color: palette.fillSoft },
          label: {
            backgroundColor: palette.fill,
            color: palette.accentForeground,
          },
        },
        ...option,
        // Replaying the entrance on every filter/selection update blanks the chart.
        animation:
          !rendered.current && !reducedMotion && option.animation !== false,
      },
      { notMerge: true, lazyUpdate: true },
    );
    rendered.current = true;
  }, [option, visible, palette]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !onEvents) return;
    const handlers = Object.entries(onEvents);
    for (const [event, handler] of handlers) chart.on(event, handler);
    return () => {
      if (chart.isDisposed()) return;
      for (const [event, handler] of handlers) chart.off(event, handler);
    };
  }, [onEvents, visible]);

  useEffect(() => {
    const element = hostRef.current;
    const chart = chartRef.current;
    if (!visible || !element || !chart) return;
    let frame = 0;
    const resizeObserver = new ResizeObserver(() => {
      window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => chart.resize());
    });
    resizeObserver.observe(element);
    return () => {
      resizeObserver.disconnect();
      window.cancelAnimationFrame(frame);
    };
  }, [visible]);

  return (
    <div
      ref={hostRef}
      className={`${className ?? ""} analytics-echart-host${visible ? " is-ready" : ""}`}
      role="img"
      aria-label={ariaLabel}
      style={{ height, width: "100%" }}
    >
      <div ref={elementRef} style={{ height: "100%", width: "100%" }} />
    </div>
  );
}
