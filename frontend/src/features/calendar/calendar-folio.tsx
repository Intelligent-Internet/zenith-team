import { useState, type CSSProperties, type MouseEventHandler } from "react";
import { Bell, Check, ChevronDown, Map, Plus } from "@/shared/ui/icons";
import type { CalendarOccurrence } from "./calendar-api";
import {
  addDays,
  dateKey,
  timeLabel,
  timeRangeLabel,
  visibleDays,
} from "./calendar-dates";

function DateHeading({ date, today }: { date: Date; today: string }) {
  return (
    <span
      className={`calendar-folio-date ${dateKey(date) === today ? "is-today" : ""}`}
    >
      <strong>{String(date.getDate()).padStart(2, "0")}</strong>
      <span>
        <b>{date.toLocaleDateString([], { weekday: "long" })}</b>
        <small>
          {date.toLocaleDateString([], { month: "long", year: "numeric" })}
        </small>
      </span>
    </span>
  );
}

function FolioEntry({
  event,
  day,
  onOpen,
  timed = false,
  overlapping = false,
}: {
  event: CalendarOccurrence;
  day: Date;
  onOpen: (event: CalendarOccurrence, element: HTMLButtonElement) => void;
  timed?: boolean;
  overlapping?: boolean;
}) {
  const continues = !event.allDay && new Date(event.startAt) < day;
  const endsLater = !event.allDay && new Date(event.endAt) > addDays(day, 1);
  return (
    <button
      className={`calendar-folio-entry ${timed ? "is-timed" : ""} ${event.allDay ? "is-all-day" : ""} ${overlapping ? "is-overlapping" : ""} ${event.completed ? "is-completed" : ""} ${event.responseStatus === "declined" ? "is-declined" : ""} ${event.responseStatus === "needs_action" ? "is-invited" : ""}`}
      style={
        { "--event-color": `var(--calendar-${event.color})` } as CSSProperties
      }
      onClick={(click) => onOpen(event, click.currentTarget)}
      aria-label={`${event.title} · ${timeRangeLabel(event)}${overlapping ? " · Overlaps another event" : ""}`}
    >
      {timed ? (
        <time className="calendar-folio-start">
          {continues ? "Ongoing" : timeLabel(event)}
        </time>
      ) : (
        <span
          className="shrink-0 w-[7px] h-[7px] rounded-[50%] [background:var(--event-color)]"
          aria-hidden="true"
        />
      )}
      <span className="calendar-folio-entry-content grid gap-[5px] min-w-0 flex-1">
        <strong>
          {event.type === "reminder" &&
            (event.completed ? <Check size={14} /> : <Bell size={14} />)}
          {event.title}
        </strong>
        {event.location && (
          <small>
            {timed && <Map size={13} />}
            {event.location}
          </small>
        )}
      </span>
      <time className="calendar-folio-end">
        {timed
          ? endsLater
            ? "Continues tomorrow"
            : new Date(event.endAt).toLocaleTimeString([], {
                hour: "numeric",
                minute: "2-digit",
              })
          : timeRangeLabel(event)}
      </time>
    </button>
  );
}

function FolioEmpty({
  loading,
  error,
  filtered,
  onCreate,
}: {
  loading: boolean;
  error: boolean;
  filtered: boolean;
  onCreate: MouseEventHandler<HTMLButtonElement>;
}) {
  return (
    <div
      className="calendar-folio-empty grid [justify-items:center] gap-[12px] p-[60px_20px] text-[var(--workspace-text-muted)] text-[length:13px]"
      role={loading ? "status" : undefined}
    >
      <p>
        {loading
          ? "Loading events…"
          : error
            ? "Calendar unavailable"
            : filtered
              ? "No matching events"
              : "No events planned"}
      </p>
      {!loading && !error && (
        <button className="personal-button" onClick={onCreate}>
          <Plus size={15} /> Create event
        </button>
      )}
    </div>
  );
}

type FolioProps = {
  date: Date;
  today: string;
  onOpen: (event: CalendarOccurrence, element: HTMLButtonElement) => void;
  onCreate: MouseEventHandler<HTMLButtonElement>;
  loading: boolean;
  error: boolean;
  filtered: boolean;
};

export function CalendarDayFolio({
  date,
  today,
  events,
  onSelectDate,
  onOpen,
  onCreate,
  ...status
}: FolioProps & {
  events: CalendarOccurrence[];
  onSelectDate: (day: Date) => void;
}) {
  const [allDayOpen, setAllDayOpen] = useState(true);
  const allDay = events.filter((event) => event.allDay);
  let latestEnd = -Infinity;
  const timed = events
    .filter((event) => !event.allDay)
    .sort(
      (a, b) =>
        Date.parse(a.startAt) - Date.parse(b.startAt) ||
        a.id.localeCompare(b.id),
    )
    .map((event) => {
      const overlapping = Date.parse(event.startAt) < latestEnd;
      latestEnd = Math.max(latestEnd, Date.parse(event.endAt));
      return { event, overlapping };
    });
  const groups = [
    { title: "Morning", start: 0, end: 12 },
    { title: "Afternoon", start: 12, end: 18 },
    { title: "Evening", start: 18, end: 24 },
  ];
  return (
    <div className="calendar-folio calendar-folio--day">
      <nav
        className="calendar-folio-week [box-shadow:var(--calendar-surface-shadow)] grid grid-cols-[repeat(7,_minmax(0,_1fr))] [border:1px_solid_var(--workspace-divider)] rounded-[12px] overflow-hidden mb-[28px]"
        aria-label="Choose a day"
      >
        {visibleDays(date, "Week").map((day) => (
          <button
            key={dateKey(day)}
            aria-pressed={dateKey(date) === dateKey(day)}
            aria-label={day.toLocaleDateString([], { dateStyle: "full" })}
            onClick={() => onSelectDate(day)}
          >
            <span>{day.toLocaleDateString([], { weekday: "short" })}</span>
            <strong>{day.getDate()}</strong>
            {dateKey(day) === today && <i aria-hidden="true" />}
          </button>
        ))}
      </nav>
      <header className="calendar-folio-heading">
        <DateHeading date={date} today={today} />
        {allDay.length > 0 && (
          <button
            className="personal-button calendar-folio-collapse"
            aria-expanded={allDayOpen}
            onClick={() => setAllDayOpen(!allDayOpen)}
          >
            All day ({allDay.length}) <ChevronDown size={14} />
          </button>
        )}
      </header>
      {allDayOpen && allDay.length > 0 && (
        <div className="calendar-folio-entries">
          {allDay.map((event) => (
            <FolioEntry
              key={`${event.id}:${event.occurrenceDate}`}
              event={event}
              day={date}
              onOpen={onOpen}
            />
          ))}
        </div>
      )}
      {groups.map((group) => {
        const entries = timed.filter(({ event }) => {
          const start = new Date(event.startAt);
          const hour = start < date ? 0 : start.getHours();
          return hour >= group.start && hour < group.end;
        });
        if (!entries.length) return null;
        return (
          <section
            className="calendar-folio-period mt-[28px]"
            key={group.title}
            aria-label={group.title}
          >
            <h3>{group.title}</h3>
            <div className="calendar-folio-entries">
              {entries.map(({ event, overlapping }) => (
                <FolioEntry
                  key={`${event.id}:${event.occurrenceDate}`}
                  event={event}
                  day={date}
                  onOpen={onOpen}
                  timed
                  overlapping={overlapping}
                />
              ))}
            </div>
          </section>
        );
      })}
      {!events.length && <FolioEmpty {...status} onCreate={onCreate} />}
    </div>
  );
}

export function CalendarAgendaFolio({
  days,
  eventsByDay,
  today,
  onOpen,
  onCreate,
  ...status
}: Omit<FolioProps, "date"> & {
  days: Date[];
  eventsByDay: Map<string, CalendarOccurrence[]>;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const populated = days.filter((day) => eventsByDay.get(dateKey(day))?.length);
  return (
    <div className="calendar-folio calendar-folio--agenda">
      {!populated.length && <FolioEmpty {...status} onCreate={onCreate} />}
      {populated.map((day) => {
        const key = dateKey(day);
        const events = [...eventsByDay.get(key)!].sort(
          (a, b) =>
            Number(b.allDay) - Number(a.allDay) ||
            a.startAt.localeCompare(b.startAt) ||
            a.id.localeCompare(b.id),
        );
        const open = !collapsed.has(key);
        return (
          <section className="calendar-folio-journal" key={key}>
            <button
              className="calendar-folio-heading calendar-folio-collapse"
              aria-expanded={open}
              onClick={() =>
                setCollapsed((current) => {
                  const next = new Set(current);
                  if (next.has(key)) next.delete(key);
                  else next.add(key);
                  return next;
                })
              }
            >
              <DateHeading date={day} today={today} />
              <span className="calendar-folio-count">
                {events.length} {events.length === 1 ? "entry" : "entries"}
              </span>
              <ChevronDown size={16} />
            </button>
            {open && (
              <div className="calendar-folio-entries">
                {events.map((event) => (
                  <FolioEntry
                    key={`${event.id}:${event.occurrenceDate}`}
                    event={event}
                    day={day}
                    onOpen={onOpen}
                  />
                ))}
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}
