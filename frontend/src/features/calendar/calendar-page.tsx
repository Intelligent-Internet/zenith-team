import { useSearchParams } from "react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Plus, Search } from "@/shared/ui/icons";
import { CalendarAgendaFolio, CalendarDayFolio } from "./calendar-folio";
import { CalendarUtilities } from "./calendar-utilities";
import { CalendarSidebar } from "./calendar-sidebar";
import { useCalendarTheme } from "./calendar-theme";
import { calendarColors, periodLabel } from "./calendar-dates";
import {
  useCalendarCatalogQuery,
  useCalendarEventsQuery,
  type CalendarOccurrence,
} from "./calendar-api";
import { CalendarEventChip } from "./calendar-event-chip";
import { calendarEventAnchor } from "./calendar-event-dialog";
import { CalendarTimeGrid } from "./calendar-time-grid";
import {
  addDays,
  dateKey,
  dayAt,
  onDay,
  visibleDays,
  type CalendarView,
} from "./calendar-dates";
import { EventEditor, type EventEditorSelection } from "./event-editor";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import "./calendar.css";

import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { EventDetails } from "./event-details";

const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
export function CalendarPage() {
  const theme = useCalendarTheme();
  const [searchParams, setSearchParams] = useSearchParams();
  const targetDate = searchParams.get("date");
  const targetEventId = searchParams.get("eventId");
  const session = useSessionSnapshot();
  const companyId = session.company?.id;
  const catalog = useCalendarCatalogQuery(companyId, { refetchOnFocus: true });
  const [showPersonal, setShowPersonal] = useState(true);
  const [showCompany, setShowCompany] = useState(true);
  const [showDeclined, setShowDeclined] = useState(false);
  const [date, setDate] = useState(() => dayAt(dateKey(new Date())));
  useEffect(() => {
    if (
      targetDate &&
      /^\d{4}-\d{2}-\d{2}$/.test(targetDate) &&
      Number.isFinite(dayAt(targetDate).getTime())
    )
      setDate(dayAt(targetDate));
  }, [targetDate]);
  const [view, setView] = useState<CalendarView>("Week");
  const [search, setSearch] = useState("");
  const [showEvents, setShowEvents] = useState(true);
  const [showReminders, setShowReminders] = useState(true);
  const [editor, setEditor] = useState<EventEditorSelection | null>(null);
  const createButton = useRef<HTMLButtonElement>(null);
  const days = useMemo(() => visibleDays(date, view), [date, view]);
  const query = useCalendarEventsQuery(
    {
      companyId,
      from: addDays(days[0], -2).toISOString(),
      to: addDays(days[days.length - 1], 3).toISOString(),
    },
    { refetchOnFocus: true },
  );
  useEffect(() => {
    if (
      !targetEventId ||
      !targetDate ||
      !query.currentData ||
      query.isFetching ||
      !days.some((day) => dateKey(day) === targetDate)
    )
      return;
    const event = query.currentData.items.find(
      (item) => item.id === targetEventId && item.occurrenceDate === targetDate,
    );
    if (event && createButton.current)
      setEditor({
        day: dayAt(event.occurrenceDate),
        event,
        anchor: calendarEventAnchor(createButton.current),
      });
    const next = new URLSearchParams(searchParams);
    next.delete("eventId");
    next.delete("date");
    setSearchParams(next, { replace: true });
  }, [
    targetEventId,
    targetDate,
    query.currentData,
    query.isFetching,
    days,
    searchParams,
    setSearchParams,
  ]);
  const items = useMemo(
    () =>
      (query.currentData?.items ?? []).filter(
        (event) =>
          days.some((day) => onDay(event, day)) &&
          ((showPersonal &&
            (event.calendarType === "personal" ||
              event.responseStatus !== "")) ||
            (showCompany &&
              event.calendarType === "company" &&
              event.companyId === companyId)) &&
          (showDeclined || event.responseStatus !== "declined") &&
          (event.type === "event" ? showEvents : showReminders) &&
          `${event.title} ${event.description} ${event.location}`
            .toLowerCase()
            .includes(search.toLowerCase()),
      ),
    [
      query.currentData,
      days,
      showPersonal,
      showCompany,
      companyId,
      showDeclined,
      showEvents,
      showReminders,
      search,
    ],
  );
  const eventsByDay = useMemo(
    () =>
      new Map(
        days.map((day) => [
          dateKey(day),
          items.filter((event) => onDay(event, day)),
        ]),
      ),
    [days, items],
  );
  function create(selection: EventEditorSelection) {
    const counts = new Map(calendarColors.map((color) => [color, 0]));
    for (const event of query.currentData?.items ?? [])
      counts.set(event.color, (counts.get(event.color) ?? 0) + 1);
    const color = [...calendarColors].sort(
      (a, b) => counts.get(a)! - counts.get(b)!,
    )[0];
    setDate(selection.day);
    setEditor({ ...selection, color });
  }
  const filtered =
    Boolean(search) ||
    !showPersonal ||
    (Boolean(companyId) && !showCompany) ||
    !showEvents ||
    !showReminders;
  const today = dateKey(new Date());
  const open = (event: CalendarOccurrence, element: HTMLButtonElement) =>
    setEditor({
      day: dayAt(event.occurrenceDate),
      event,
      anchor: calendarEventAnchor(element),
    });
  function navigate(direction: number) {
    setDate(
      view === "Month"
        ? new Date(date.getFullYear(), date.getMonth() + direction, 1)
        : addDays(
            date,
            direction * (view === "Day" ? 1 : view === "Week" ? 7 : 30),
          ),
    );
  }
  return (
    <main className="calendar-page" aria-label="Calendar" style={theme}>
      <CalendarSidebar
        date={date}
        setDate={setDate}
        today={today}
        catalog={catalog.currentData}
        companyId={companyId}
        companyName={session.company?.workspaceName}
        showPersonal={showPersonal}
        setShowPersonal={setShowPersonal}
        showCompany={showCompany}
        setShowCompany={setShowCompany}
        showDeclined={showDeclined}
        setShowDeclined={setShowDeclined}
        showEvents={showEvents}
        setShowEvents={setShowEvents}
        showReminders={showReminders}
        setShowReminders={setShowReminders}
      />
      <section className="calendar-main" aria-label="Calendar entries">
        <header className="calendar-toolbar">
          <div className="calendar-date-navigation">
            <button
              className="personal-button"
              onClick={() => setDate(dayAt(today))}
            >
              Today
            </button>
            <button
              className="personal-icon-button"
              aria-label="Previous period"
              onClick={() => navigate(-1)}
            >
              <ChevronLeft size={18} />
            </button>
            <button
              className="personal-icon-button"
              aria-label="Next period"
              onClick={() => navigate(1)}
            >
              <ChevronRight size={18} />
            </button>
            <h2>{periodLabel(date, view, days)}</h2>
          </div>
          <div className="personal-tabs" aria-label="Calendar view">
            {(["Month", "Week", "Day", "Agenda"] as const).map((item) => (
              <button
                key={item}
                aria-pressed={view === item}
                onClick={() => setView(item)}
              >
                {item}
              </button>
            ))}
          </div>
          <button
            ref={createButton}
            className="personal-button personal-button--primary calendar-create"
            onClick={(click) =>
              create({
                day: date,
                anchor: calendarEventAnchor(click.currentTarget),
              })
            }
          >
            <Plus size={16} />
            Create
          </button>
          <label className="calendar-search">
            <Search size={17} />
            <input
              aria-label="Search this calendar period"
              placeholder="Search this period"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
        </header>
        <div className="calendar-context">
          <span>
            {Intl.DateTimeFormat()
              .resolvedOptions()
              .timeZone.replaceAll("_", " ")}
          </span>
        </div>
        {query.isError ? (
          <div className="personal-empty" role="alert">
            <p>{getBoardlyQueryErrorMessage(query.error)}</p>
            <button
              className="personal-button"
              onClick={() => void query.refetch()}
            >
              Try again
            </button>
          </div>
        ) : null}
        <div
          key={view}
          className={`calendar-view calendar-view--${view.toLowerCase()}`}
          aria-busy={query.isFetching}
        >
          {query.isFetching && (
            <span className="calendar-loading" role="status">
              Updating calendar…
            </span>
          )}
          {view === "Month" && (
            <>
              <div className="calendar-weekdays">
                {weekdays.map((day) => (
                  <span key={day}>{day}</span>
                ))}
              </div>
              <div className="calendar-month-grid">
                {days.map((day) => {
                  const events = eventsByDay.get(dateKey(day)) ?? [];
                  return (
                    <div
                      key={dateKey(day)}
                      className={`calendar-day ${day.getMonth() !== date.getMonth() ? "is-outside" : ""}`}
                    >
                      <button
                        className="calendar-day-create"
                        aria-label={`Create all-day event on ${day.toLocaleDateString([], { dateStyle: "full" })}`}
                        onClick={(click) =>
                          create({
                            day,
                            allDay: true,
                            anchor: calendarEventAnchor(click.currentTarget),
                          })
                        }
                      />
                      <button
                        className={`calendar-day-number ${dateKey(day) === today ? "is-today" : ""}`}
                        aria-label={day.toLocaleDateString([], {
                          dateStyle: "full",
                        })}
                        aria-pressed={dateKey(date) === dateKey(day)}
                        onClick={() => setDate(day)}
                      >
                        {day.getDate()}
                      </button>
                      <div className="calendar-day-entries">
                        {events.slice(0, 3).map((event) => (
                          <CalendarEventChip
                            key={`${event.id}:${event.occurrenceDate}`}
                            event={event}
                            onClick={(click) =>
                              open(event, click.currentTarget)
                            }
                          />
                        ))}
                        {events.length > 3 && (
                          <button
                            className="calendar-more"
                            onClick={() => {
                              setDate(day);
                              setView("Day");
                            }}
                          >
                            +{events.length - 3} more
                          </button>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </>
          )}
          {view === "Week" && (
            <CalendarTimeGrid
              days={days}
              eventsByDay={eventsByDay}
              selectedDate={dateKey(date)}
              onSelectDate={setDate}
              onOpen={open}
              onCreate={create}
            />
          )}
          {view === "Day" && (
            <CalendarDayFolio
              date={date}
              today={today}
              events={eventsByDay.get(dateKey(date)) ?? []}
              onSelectDate={setDate}
              onOpen={open}
              onCreate={(click) =>
                create({
                  day: date,
                  anchor: calendarEventAnchor(click.currentTarget),
                })
              }
              loading={!query.currentData && query.isFetching}
              error={query.isError}
              filtered={filtered}
            />
          )}
          {view === "Agenda" && (
            <CalendarAgendaFolio
              days={days}
              eventsByDay={eventsByDay}
              today={today}
              onOpen={open}
              onCreate={(click) =>
                create({
                  day: date,
                  anchor: calendarEventAnchor(click.currentTarget),
                })
              }
              loading={!query.currentData && query.isFetching}
              error={query.isError}
              filtered={filtered}
            />
          )}
        </div>
      </section>
      <CalendarUtilities />
      {editor?.event && !editor.edit ? (
        <EventDetails
          event={editor.event}
          anchor={editor.anchor}
          onClose={() => setEditor(null)}
          onEdit={
            editor.event.canEdit
              ? () => setEditor({ ...editor, edit: true })
              : undefined
          }
        />
      ) : (
        editor && (
          <EventEditor
            key={
              editor.event?.id ??
              `${dateKey(editor.day)}:${editor.hour ?? 9}:${editor.minute ?? 0}:${Boolean(editor.allDay)}`
            }
            companyId={editor.event?.companyId ?? companyId}
            {...editor}
            onClose={() => setEditor(null)}
          />
        )
      )}
    </main>
  );
}
