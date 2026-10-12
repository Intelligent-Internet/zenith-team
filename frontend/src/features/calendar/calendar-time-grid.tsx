import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import type { CalendarOccurrence } from "./calendar-api";
import { dateKey } from "./calendar-dates";
import { CalendarEventChip } from "./calendar-event-chip";
import { calendarEventAnchor } from "./calendar-event-dialog";
import type { EventEditorSelection } from "./event-editor";

import { layoutTimedEvents } from "./calendar-time-layout";

const hourHeight = 60;

export function CalendarTimeGrid({
  days,
  eventsByDay,
  selectedDate,
  onSelectDate,
  onOpen,
  onCreate,
}: {
  days: Date[];
  eventsByDay: Map<string, CalendarOccurrence[]>;
  selectedDate: string;
  onSelectDate: (date: Date) => void;
  onOpen: (item: CalendarOccurrence, element: HTMLButtonElement) => void;
  onCreate: (selection: EventEditorSelection) => void;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const [now, setNow] = useState(() => new Date());
  const [scrollbarWidth, setScrollbarWidth] = useState(0);
  const firstDay = dateKey(days[0]);
  const today = dateKey(now);
  const nowOffset =
    (now.getHours() + now.getMinutes() / 60 + now.getSeconds() / 3600) *
    hourHeight;

  useEffect(() => {
    const update = () => setNow(new Date());
    const timer = window.setInterval(update, 30000);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);

  useLayoutEffect(() => {
    const element = scroll.current;
    if (!element) return;
    const measure = () =>
      setScrollbarWidth(element.offsetWidth - element.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useLayoutEffect(() => {
    const element = scroll.current;
    if (!element) return;
    const selectedPeriodContainsToday = days.some(
      (day) => dateKey(day) === dateKey(new Date()),
    );
    const current = new Date();
    const currentOffset =
      (current.getHours() + current.getMinutes() / 60) * hourHeight;
    element.scrollTop = selectedPeriodContainsToday
      ? Math.max(0, currentOffset - element.clientHeight / 3)
      : 7 * hourHeight;
  }, [firstDay, days.length]);

  const layouts = useMemo(
    () =>
      new Map(
        days.map((day) => [
          dateKey(day),
          layoutTimedEvents(eventsByDay.get(dateKey(day)) ?? [], day),
        ]),
      ),
    [days, eventsByDay],
  );

  return (
    <div
      className="calendar-time-view"
      style={
        {
          "--day-count": days.length,
          "--calendar-scrollbar-width": `${scrollbarWidth}px`,
        } as CSSProperties
      }
    >
      <div className="calendar-time-head">
        <span />
        <div>
          {days.map((day) => (
            <button
              key={dateKey(day)}
              className={dateKey(day) === today ? "is-today" : ""}
              aria-label={day.toLocaleDateString([], { dateStyle: "full" })}
              aria-pressed={dateKey(day) === selectedDate}
              onClick={() => onSelectDate(day)}
            >
              <span>{day.toLocaleDateString([], { weekday: "short" })}</span>
              <strong className={dateKey(day) === today ? "is-today" : ""}>
                {day.getDate()}
              </strong>
            </button>
          ))}
        </div>
      </div>
      <div className="calendar-all-day">
        <span>All day</span>
        <div>
          {days.map((day) => (
            <div key={dateKey(day)}>
              <button
                className="calendar-all-day-create"
                aria-label={`Create all-day event on ${day.toLocaleDateString([], { dateStyle: "full" })}`}
                onClick={(click) =>
                  onCreate({
                    day,
                    allDay: true,
                    anchor: calendarEventAnchor(click.currentTarget),
                  })
                }
              />
              {(eventsByDay.get(dateKey(day)) ?? [])
                .filter((event) => event.allDay)
                .map((event) => (
                  <CalendarEventChip
                    key={`${event.id}:${event.occurrenceDate}`}
                    event={event}
                    onClick={(click) => onOpen(event, click.currentTarget)}
                  />
                ))}
            </div>
          ))}
        </div>
      </div>
      <div className="calendar-time-scroll" ref={scroll}>
        <div className="calendar-hour-labels">
          {Array.from({ length: 24 }, (_, hour) => (
            <span key={hour}>{String(hour).padStart(2, "0")}:00</span>
          ))}
        </div>
        <div className="calendar-time-columns">
          {days.map((day) => {
            const positions = layouts.get(dateKey(day)) ?? [];
            return (
              <div
                className={`calendar-time-column ${dateKey(day) === today ? "is-today" : ""}`}
                key={dateKey(day)}
              >
                {Array.from({ length: 24 }, (_, hour) => (
                  <button
                    key={hour}
                    className="calendar-hour-slot"
                    aria-label={`Create event ${dateKey(day)} during ${hour}:00 hour`}
                    onClick={(click) => {
                      const position =
                        click.detail === 0
                          ? 0
                          : click.clientY -
                            click.currentTarget.getBoundingClientRect().top;
                      const minute = Math.min(
                        45,
                        Math.max(
                          0,
                          Math.floor((position / hourHeight) * 4) * 15,
                        ),
                      );
                      onCreate({
                        day,
                        hour,
                        minute,
                        anchor: calendarEventAnchor(
                          click.currentTarget,
                          (minute / 60) * hourHeight,
                          hourHeight / 4,
                        ),
                      });
                    }}
                  />
                ))}
                {dateKey(day) === today && (
                  <div
                    className="calendar-now-line"
                    style={{ top: nowOffset }}
                    aria-label="Current time"
                  />
                )}
                {positions.map(({ event, begin, finish, lane, columns }) => (
                  <CalendarEventChip
                    key={`${event.id}:${event.occurrenceDate}`}
                    event={event}
                    compact={finish - begin < 60}
                    onClick={(click) => onOpen(event, click.currentTarget)}
                    style={{
                      position: "absolute",
                      top: `${(begin / 60) * hourHeight}px`,
                      height: `${((finish - begin) / 60) * hourHeight - 2}px`,
                      width: `calc(${100 / columns}% - 5px)`,
                      left: `calc(${(lane * 100) / columns}% + 3px)`,
                    }}
                  />
                ))}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
