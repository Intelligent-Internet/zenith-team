import type { CalendarOccurrence } from "./calendar-api";
import { addDays } from "./calendar-dates";

// Independent overlap groups reclaim the whole column after the group ends.
export function layoutTimedEvents(events: CalendarOccurrence[], day: Date) {
  const entries = events
    .filter((event) => !event.allDay)
    .map((event) => {
      const start = new Date(event.startAt);
      const end = new Date(event.endAt);
      const begin =
        start < day ? 0 : start.getHours() * 60 + start.getMinutes();
      const finish =
        end >= addDays(day, 1) ? 1440 : end.getHours() * 60 + end.getMinutes();
      return {
        event,
        begin,
        finish: Math.min(1440, Math.max(begin + 24, finish)),
        lane: 0,
        columns: 1,
      };
    })
    .sort(
      (a, b) =>
        a.begin - b.begin ||
        b.finish - a.finish ||
        a.event.id.localeCompare(b.event.id),
    );
  let group: typeof entries = [];
  let lanes: number[] = [];
  let groupEnd = 0;
  const completeGroup = () => {
    for (const entry of group) entry.columns = lanes.length;
    group = [];
    lanes = [];
  };
  for (const entry of entries) {
    if (entry.begin >= groupEnd) completeGroup();
    let lane = lanes.findIndex((end) => end <= entry.begin);
    if (lane < 0) lane = lanes.length;
    lanes[lane] = entry.finish;
    entry.lane = lane;
    group.push(entry);
    groupEnd = Math.max(...lanes);
  }
  completeGroup();
  return entries;
}
