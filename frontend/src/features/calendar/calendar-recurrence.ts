import type { CalendarInput } from "./calendar-api";
import { dayAt } from "./calendar-dates";

export type CalendarRepeat = Pick<
  CalendarInput,
  | "recurrence"
  | "recurrenceInterval"
  | "recurrenceWeekdays"
  | "recurrenceMonthlyMode"
  | "recurrenceUntil"
  | "recurrenceCount"
>;

export const repeatWeekdays = [
  { value: 1, short: "M", name: "Monday" },
  { value: 2, short: "T", name: "Tuesday" },
  { value: 3, short: "W", name: "Wednesday" },
  { value: 4, short: "T", name: "Thursday" },
  { value: 5, short: "F", name: "Friday" },
  { value: 6, short: "S", name: "Saturday" },
  { value: 0, short: "S", name: "Sunday" },
];

export function monthlyWeekdayLabel(startLocal: string) {
  const date = dayAt(startLocal.slice(0, 10));
  const ordinal = ["first", "second", "third", "fourth", "last"][
    Math.floor((date.getDate() - 1) / 7)
  ];
  const weekday = repeatWeekdays.find((day) => day.value === date.getDay())!;
  return `the ${ordinal} ${weekday.name}`;
}

export function recurrenceSummary(rule: CalendarRepeat, startLocal: string) {
  if (rule.recurrence === "none") return "Does not repeat";
  const unit = {
    daily: "day",
    weekly: "week",
    monthly: "month",
    yearly: "year",
  }[rule.recurrence];
  const interval = rule.recurrenceInterval ?? 1;
  let summary = interval === 1 ? `Every ${unit}` : `Every ${interval} ${unit}s`;
  if (rule.recurrence === "weekly") {
    const days = rule.recurrenceWeekdays?.length
      ? rule.recurrenceWeekdays
      : [dayAt(startLocal.slice(0, 10)).getDay()];
    summary += ` on ${repeatWeekdays
      .filter((day) => days.includes(day.value))
      .map((day) => day.name.slice(0, 3))
      .join(", ")}`;
  }
  if (rule.recurrence === "monthly") {
    summary +=
      rule.recurrenceMonthlyMode === "day_of_week"
        ? ` on ${monthlyWeekdayLabel(startLocal)}`
        : ` on day ${dayAt(startLocal.slice(0, 10)).getDate()}`;
  }
  if (rule.recurrenceUntil) {
    summary += `, until ${dayAt(rule.recurrenceUntil).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}`;
  } else if (rule.recurrenceCount) {
    summary += `, ${rule.recurrenceCount} ${rule.recurrenceCount === 1 ? "occurrence" : "occurrences"}`;
  }
  return summary;
}
