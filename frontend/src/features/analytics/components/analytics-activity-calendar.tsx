import { useGetWorkspaceAnalyticsSeriesQuery } from "@/shared/api/analytics-api";
import { useMemo, useState, type CSSProperties } from "react";
import { ChevronLeft, ChevronRight } from "@/shared/ui/icons";
import type { AnalyticsCalendarQuery } from "../model/calendar-query";

import { mixColor, contrastingText } from "@/app/appearance/color";
import { useAnalyticsPalette } from "../model/palette";
import { formatMetricAmount } from "../model/metrics";
import { seriesKeys } from "../model/series";
import { countLabel, partLabel } from "../model/format";
import { AnalyticsDayDetail } from "./analytics-day-detail";

export function AnalyticsActivityCalendar({
  query,
  metric,
}: {
  query: AnalyticsCalendarQuery;
  metric: string;
}) {
  const palette = useAnalyticsPalette();
  const first = new Date(query.startAt);
  const months = useMemo(() => {
    const start = new Date(query.startAt);
    const end = new Date(query.endAt);
    const result: Date[] = [];
    for (
      let date = new Date(start.getFullYear(), start.getMonth(), 1);
      date < end;
      date = new Date(date.getFullYear(), date.getMonth() + 1, 1)
    )
      result.push(date);
    return result;
  }, [query.startAt, query.endAt]);
  const [monthIndex, setMonthIndex] = useState(() => months.length - 1);
  const [chosenDate, setChosenDate] = useState<string | null>(null);
  const [details, setDetails] = useState(false);
  const month = months[monthIndex] ?? months[0];
  const monthEnd = new Date(month.getFullYear(), month.getMonth() + 1, 1);
  const series = useGetWorkspaceAnalyticsSeriesQuery({
    ...query,
    startAt: new Date(Math.max(first.getTime(), month.getTime())).toISOString(),
    endAt: new Date(
      Math.min(new Date(query.endAt).getTime(), monthEnd.getTime()),
    ).toISOString(),
    bucket: "day",
    metric,
  });
  const days = useMemo(() => {
    const keys = seriesKeys(series.currentData);
    return (series.currentData?.points ?? []).map((point) => {
      const values = keys
        .map((key) => point.parts?.[key])
        .filter((value) => value != null);
      return {
        date: point.startAt!,
        value: values.length
          ? values.reduce((total, value) => total + Number(value), 0)
          : null,
      };
    });
  }, [series.currentData]);
  const max = Math.max(0, ...days.map((day) => day.value ?? 0));
  const selected =
    days.find((day) => day.date === chosenDate) ??
    [...days].reverse().find((day) => (day.value ?? 0) > 0) ??
    days.at(-1);
  if (chosenDate === null && selected) setChosenDate(selected.date);
  const selectedDate = chosenDate ?? selected?.date;
  const byDate = new Map(
    days.map((day) => [new Date(day.date).getDate(), day]),
  );
  const offset = (month.getDay() + 6) % 7;
  const cells =
    Math.ceil((offset + new Date(monthEnd.getTime() - 1).getDate()) / 7) * 7;
  const monthTitle = month.toLocaleDateString(undefined, {
    month: "long",
    year: "numeric",
  });
  function moveMonth(index: number) {
    setMonthIndex(index);
    setChosenDate(null);
  }
  return (
    <section className="analytics-card analytics-calendar-card">
      <div className="analytics-card-heading">
        <h2>Activity calendar</h2>
        <div className="analytics-controls">
          <span className="analytics-muted">{partLabel(metric)} by day</span>
          <button
            type="button"
            aria-pressed={details}
            onClick={() => setDetails(!details)}
          >
            {details ? "Calendar" : "Details"}
          </button>
        </div>
      </div>
      <div className="analytics-calendar-layout">
        <div className="min-w-0">
          <div className="analytics-calendar-month flex items-center justify-center gap-[20px] mb-[18px]">
            <button
              type="button"
              aria-label="Previous month"
              disabled={monthIndex === 0}
              onClick={() => moveMonth(monthIndex - 1)}
            >
              <ChevronLeft size={16} />
            </button>
            <h3 aria-live="polite">{monthTitle}</h3>
            <button
              type="button"
              aria-label="Next month"
              disabled={monthIndex === months.length - 1}
              onClick={() => moveMonth(monthIndex + 1)}
            >
              <ChevronRight size={16} />
            </button>
          </div>
          {series.isError ? (
            <div
              className="mt-[16px] p-[12px_16px] [border:1px_solid_color-mix(in_srgb,_var(--analytics-danger)_35%,_transparent)] rounded-workspace-panel text-[var(--analytics-danger)] text-workspace-caption"
              role="alert"
            >
              Calendar could not be loaded.{" "}
              <button type="button" onClick={() => void series.refetch()}>
                Retry calendar
              </button>
            </div>
          ) : null}
          {details ? (
            <div className="w-full max-w-full overflow-x-auto [scrollbar-width:thin] max-h-[415px]">
              <table>
                <caption className="sr-only">
                  {monthTitle} daily {partLabel(metric)}
                </caption>
                <thead>
                  <tr>
                    <th>Date</th>
                    <th>{partLabel(metric)}</th>
                  </tr>
                </thead>
                <tbody>
                  {days.map((day) => (
                    <tr key={day.date}>
                      <th>
                        <button
                          type="button"
                          onClick={() => {
                            setChosenDate(day.date);
                            setDetails(false);
                          }}
                        >
                          {new Date(day.date).toLocaleDateString()}
                        </button>
                      </th>
                      <td>
                        {day.value == null
                          ? "—"
                          : formatMetricAmount(day.value, metric, false)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div
              className="grid grid-cols-[repeat(7,_minmax(0,_1fr))] gap-[5px]"
              aria-label={monthTitle}
              aria-busy={series.isFetching}
            >
              {["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((day) => (
                <span
                  className="pb-[7px] text-[var(--analytics-muted)] text-center text-workspace-caption"
                  key={day}
                >
                  {day}
                </span>
              ))}
              {Array.from({ length: cells }, (_, index) => {
                const date = new Date(
                  month.getFullYear(),
                  month.getMonth(),
                  index - offset + 1,
                );
                const sameMonth = date.getMonth() === month.getMonth();
                const day = sameMonth ? byDate.get(date.getDate()) : undefined;
                const enabled = !!day;
                const countUnit =
                  metric === "runs"
                    ? "Run"
                    : metric === "tokens"
                      ? "Token"
                      : metric === "failures"
                        ? "Failure"
                        : null;
                const amountLabel =
                  day?.value != null && countUnit
                    ? countLabel(day.value, countUnit)
                    : `${formatMetricAmount(day?.value ?? null, metric, false)} ${partLabel(metric)}`;
                const isSelected = enabled && selectedDate === day.date;
                const intensity =
                  day?.value && max ? 0.13 + (day.value / max) * 0.65 : 0.035;
                const background = mixColor(
                  palette.canvas,
                  palette.accent,
                  isSelected ? 1 : intensity,
                );
                const color = contrastingText(background);
                return (
                  <button
                    key={date.toISOString()}
                    type="button"
                    className={`analytics-calendar-day${!sameMonth ? " is-outside" : ""}`}
                    disabled={!enabled}
                    aria-pressed={isSelected}
                    aria-label={`${date.toLocaleDateString(undefined, { month: "long", day: "numeric", year: "numeric" })}: ${enabled ? amountLabel : "outside selected range"}`}
                    onClick={() => setChosenDate(day!.date)}
                    style={
                      {
                        "--day-bg": background,
                        "--day-fg": color,
                      } as CSSProperties
                    }
                  >
                    <span>{date.getDate()}</span>
                    <strong>
                      {enabled && day.value != null
                        ? formatMetricAmount(day.value, metric)
                        : "—"}
                    </strong>
                  </button>
                );
              })}
            </div>
          )}
          <div className="analytics-calendar-key flex items-center flex-wrap gap-[6px] mt-[15px] text-[length:12px] text-[var(--analytics-muted)]">
            <span>Less</span>
            {palette.heatmap.map((color) => (
              <i key={color} style={{ background: color }} />
            ))}
            <span>More</span>
            <span className="analytics-calendar-zone">
              {query.timeZone}
              {metric === "cost" ? " · Known cost" : ""}
              {days.some((day) => day.value == null)
                ? " · — means unavailable"
                : ""}
            </span>
          </div>
        </div>
        {selectedDate ? (
          <AnalyticsDayDetail
            key={JSON.stringify(query)}
            query={query}
            date={selectedDate}
          />
        ) : (
          <aside className="analytics-day-detail" role="status">
            {series.isFetching
              ? "Loading activity…"
              : "Select a day to explore"}
          </aside>
        )}
      </div>
    </section>
  );
}
