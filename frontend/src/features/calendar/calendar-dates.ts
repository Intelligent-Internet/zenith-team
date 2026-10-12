import type { CalendarOccurrence } from "./calendar-api";

export type CalendarView = "Month" | "Week" | "Day" | "Agenda";
export function dateKey(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}
export function dayAt(key: string) {
  return new Date(`${key}T00:00:00`);
}
export function addDays(date: Date, count: number) {
  const next = new Date(date);
  next.setDate(next.getDate() + count);
  return next;
}
export function monthDays(date: Date) {
  const first = new Date(date.getFullYear(), date.getMonth(), 1);
  const start = addDays(first, -(first.getDay() + 6) % 7);
  return Array.from({ length: 42 }, (_, index) => addDays(start, index));
}
export function visibleDays(date: Date, view: CalendarView) {
  if (view === "Month") return monthDays(date);
  const start =
    view === "Week" ? addDays(date, -(date.getDay() + 6) % 7) : date;
  return Array.from(
    { length: view === "Day" ? 1 : view === "Week" ? 7 : 30 },
    (_, index) => addDays(dayAt(dateKey(start)), index),
  );
}
export function onDay(event: CalendarOccurrence, day: Date) {
  if (event.allDay)
    return event.startLocal <= dateKey(day) && event.endLocal > dateKey(day);
  return (
    new Date(event.startAt) < addDays(day, 1) && new Date(event.endAt) > day
  );
}
export function timeLabel(event: CalendarOccurrence) {
  return event.allDay
    ? "All day"
    : new Date(event.startAt).toLocaleTimeString([], {
        hour: "numeric",
        minute: "2-digit",
      });
}

export const calendarColors = [
  "rose",
  "lavender",
  "sage",
  "sky",
  "sand",
] as const;

export function periodLabel(date: Date, view: CalendarView, days: Date[]) {
  if (view === "Month")
    return date.toLocaleDateString([], { month: "long", year: "numeric" });
  const format = new Intl.DateTimeFormat([], {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  return view === "Day"
    ? format.format(date)
    : format.formatRange(days[0], days[days.length - 1]);
}

export function timeRangeLabel(event: CalendarOccurrence) {
  if (event.allDay) return "All day";
  const start = new Date(event.startAt);
  const end = new Date(event.endAt);
  if (dateKey(start) !== dateKey(end)) {
    return new Intl.DateTimeFormat([], {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    }).formatRange(start, end);
  }
  const format = new Intl.DateTimeFormat([], {
    hour: "numeric",
    minute: "2-digit",
  });
  return `${format.format(start)} – ${format.format(end)}`;
}
