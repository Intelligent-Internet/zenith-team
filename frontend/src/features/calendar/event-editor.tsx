import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  ArrowRight,
  Bell,
  CalendarDays,
  Check,
  ChevronDown,
  Map,
  ListTodo,
  EllipsisVertical,
  Trash2,
} from "@/shared/ui/icons";
import {
  CalendarEventDialog,
  type CalendarEventAnchor,
} from "./calendar-event-dialog";
import {
  useCalendarCatalogQuery,
  useChangeCalendarOccurrenceMutation,
  useDeleteCalendarEventMutation,
  useSaveCalendarEventMutation,
  type CalendarInput,
  type CalendarOccurrence,
} from "./calendar-api";
import { addDays, calendarColors, dateKey, dayAt } from "./calendar-dates";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";

import { CalendarRepeatField } from "./calendar-repeat-field";
import { EventSharingFields } from "./event-participants";
import { useCalendarTheme } from "./calendar-theme";
import {
  CalendarDatePicker,
  CalendarOptionPicker,
  CalendarTimePicker,
} from "./calendar-pickers";

const reminderOptions = [
  { value: "none", label: "No reminder" },
  { value: "0", label: "At start time" },
  { value: "5", label: "5 minutes before" },
  { value: "10", label: "10 minutes before" },
  { value: "30", label: "30 minutes before" },
  { value: "60", label: "1 hour before" },
  { value: "1440", label: "1 day before" },
];

// Shift wall-clock fields without letting the computer's time zone change them.
function shiftTimed(value: string, minutes: number) {
  const timestamp = Date.parse(`${value}:00Z`);
  return new Date(timestamp + minutes * 60_000).toISOString().slice(0, 16);
}

function dayDifference(next: string, previous: string) {
  return (
    (Date.parse(`${next}T00:00:00Z`) - Date.parse(`${previous}T00:00:00Z`)) /
    86_400_000
  );
}

export type CalendarEntryDraft = Pick<
  CalendarInput,
  "title" | "description" | "startLocal"
>;

export type EventEditorSelection = {
  day: Date;
  anchor: CalendarEventAnchor;
  hour?: number;
  minute?: number;
  allDay?: boolean;
  color?: CalendarInput["color"];
  event?: CalendarOccurrence;
  companyId?: string;
  edit?: boolean;
  initialType?: "event" | "reminder";
  draft?: CalendarEntryDraft;
};
function initialInput(
  day: Date,
  hour: number,
  minute: number,
  allDay: boolean,
  event?: CalendarOccurrence,
  color: CalendarInput["color"] = "rose",
): CalendarInput {
  if (event)
    return {
      calendarId: event.calendarId,
      attendeeUserIds: event.attendees.map((guest) => guest.userId),
      type: event.type,
      title: event.title,
      description: event.description,
      location: event.location,
      color: event.color,
      allDay: event.allDay,
      startLocal: event.seriesStartLocal,
      endLocal: event.seriesEndLocal,
      timeZone: event.timeZone,
      recurrence: event.recurrence,
      recurrenceUntil: event.recurrenceUntil,
      recurrenceInterval: event.recurrenceInterval,
      recurrenceWeekdays: event.recurrenceWeekdays,
      recurrenceMonthlyMode: event.recurrenceMonthlyMode,
      recurrenceCount: event.recurrenceCount,
      reminderMinutes: event.reminderMinutes,
      expectedRevision: event.revision,
    };
  const minuteText = String(minute).padStart(2, "0");
  const start = allDay
    ? dateKey(day)
    : `${dateKey(day)}T${String(hour).padStart(2, "0")}:${minuteText}`;
  const end = allDay
    ? dateKey(addDays(day, 1))
    : hour === 23
      ? `${dateKey(addDays(day, 1))}T00:${minuteText}`
      : `${dateKey(day)}T${String(hour + 1).padStart(2, "0")}:${minuteText}`;
  return {
    attendeeUserIds: [],
    type: "event",
    title: "",
    description: "",
    location: "",
    color,
    allDay,
    startLocal: start,
    endLocal: end,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    recurrence: "none",
    recurrenceInterval: 1,
    recurrenceWeekdays: [],
    recurrenceMonthlyMode: "day_of_month",
    recurrenceCount: null,
    recurrenceUntil: null,
    reminderMinutes: 10,
    expectedRevision: 0,
  };
}
export function EventEditor({
  day,
  anchor,
  hour = 9,
  minute = 0,
  allDay = false,
  color,
  event,
  onClose,
  companyId,
  initialType,
  draft,
  onCreateTask,
}: EventEditorSelection & {
  onClose: () => void;
  onCreateTask?: (draft: CalendarEntryDraft) => void;
}) {
  const theme = useCalendarTheme();
  const contextCompanyId = event?.companyId ?? companyId;
  const catalog = useCalendarCatalogQuery(contextCompanyId ?? undefined, {
    refetchOnMountOrArgChange: 60,
  });
  const [input, setInput] = useState(() => ({
    ...initialInput(day, hour, minute, allDay, event, color),
    ...(initialType && !event ? { type: initialType } : {}),
    ...(!event && draft
      ? { title: draft.title, description: draft.description }
      : {}),
  }));
  const [endDateOpen, setEndDateOpen] = useState(false);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const [save, saving] = useSaveCalendarEventMutation();
  const [remove, removing] = useDeleteCalendarEventMutation();
  const [change, changing] = useChangeCalendarOccurrenceMutation();
  const busy = saving.isLoading || removing.isLoading || changing.isLoading;
  const patch = (value: Partial<CalendarInput>) =>
    setInput((current) => ({ ...current, ...value }));
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await save({
        id: event?.id,
        input,
        companyId: contextCompanyId,
      }).unwrap();
      toast.success(
        event
          ? "Calendar updated"
          : input.type === "reminder"
            ? "Reminder created"
            : "Event created",
      );
      onClose();
    } catch (error) {
      setError(getBoardlyQueryErrorMessage(error));
    }
  }
  async function deleteEvent(single: boolean) {
    if (!event) return;
    try {
      if (single)
        await change({
          id: event.id,
          companyId: contextCompanyId,
          input: {
            expectedRevision: event.revision,
            occurrenceDate: event.occurrenceDate,
            action: "cancel",
          },
        }).unwrap();
      else
        await remove({
          id: event.id,
          revision: event.revision,
          companyId: contextCompanyId,
        }).unwrap();
      toast.success(single ? "Occurrence removed" : "Event deleted");
      onClose();
    } catch (error) {
      setError(getBoardlyQueryErrorMessage(error));
    }
  }
  async function toggleComplete() {
    if (!event) return;
    try {
      await change({
        id: event.id,
        companyId: contextCompanyId,
        input: {
          expectedRevision: event.revision,
          occurrenceDate: event.occurrenceDate,
          action: event.completed ? "reopen" : "complete",
        },
      }).unwrap();
      onClose();
    } catch (error) {
      setError(getBoardlyQueryErrorMessage(error));
    }
  }
  function chooseStartDate(value: string) {
    const difference = dayDifference(value, input.startLocal.slice(0, 10));
    patch({
      startLocal: input.allDay
        ? value
        : `${value}${input.startLocal.slice(10)}`,
      endLocal: input.allDay
        ? dateKey(addDays(dayAt(input.endLocal), difference))
        : shiftTimed(input.endLocal, difference * 1440),
    });
  }
  function chooseStartTime(value: string) {
    const previous = input.startLocal.slice(11);
    const difference =
      Number(value.slice(0, 2)) * 60 +
      Number(value.slice(3)) -
      Number(previous.slice(0, 2)) * 60 -
      Number(previous.slice(3));
    patch({
      startLocal: `${input.startLocal.slice(0, 10)}T${value}`,
      endLocal: shiftTimed(input.endLocal, difference),
    });
  }
  function chooseEndDate(value: string) {
    const endLocal = input.allDay
      ? dateKey(addDays(dayAt(value), 1))
      : `${value}${input.endLocal.slice(10)}`;
    patch({
      endLocal:
        !input.allDay && endLocal <= input.startLocal
          ? shiftTimed(input.startLocal, 60)
          : endLocal,
    });
  }
  function chooseEndTime(value: string) {
    let endLocal = `${input.endLocal.slice(0, 10)}T${value}`;
    if (endLocal <= input.startLocal) endLocal = shiftTimed(endLocal, 1440);
    patch({ endLocal });
  }
  function setAllDay(allDay: boolean) {
    const startDay = input.startLocal.slice(0, 10);
    if (allDay) {
      const endDay = input.endLocal.slice(0, 10);
      const lastDay = input.endLocal.endsWith("T00:00")
        ? dateKey(addDays(dayAt(endDay), -1))
        : endDay;
      patch({
        allDay,
        startLocal: startDay,
        endLocal: dateKey(
          addDays(dayAt(lastDay < startDay ? startDay : lastDay), 1),
        ),
      });
      return;
    }
    const lastDay = dateKey(addDays(dayAt(input.endLocal), -1));
    patch({
      allDay,
      startLocal: `${startDay}T09:00`,
      endLocal: `${lastDay}T10:00`,
    });
  }
  const endDate = input.allDay
    ? dateKey(addDays(dayAt(input.endLocal), -1))
    : input.endLocal;
  return (
    <CalendarEventDialog
      anchor={anchor}
      busy={busy}
      style={theme}
      className={`calendar-editor ${deleting ? "calendar-editor--deleting" : ""}`}
      headingActions={
        event && !deleting ? (
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <button
                type="button"
                className="personal-icon-button calendar-editor-menu"
                aria-label="Event actions"
                disabled={busy}
              >
                <EllipsisVertical size={18} />
              </button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal
              container={document.querySelector(".boardly-workspace-theme")}
            >
              <DropdownMenu.Content
                className="calendar-action-menu"
                style={theme}
                align="end"
                sideOffset={6}
              >
                {event.type === "reminder" && (
                  <DropdownMenu.Item onSelect={() => void toggleComplete()}>
                    <Check size={15} />
                    {event.completed ? "Reopen" : "Mark done"}
                  </DropdownMenu.Item>
                )}
                <DropdownMenu.Item
                  className="is-danger"
                  onSelect={() => setDeleting(true)}
                >
                  <Trash2 size={15} />
                  Delete
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        ) : undefined
      }
      title={
        deleting ? "Delete this event?" : event ? "Edit event" : "New event"
      }
      description={
        event?.recurrence !== "none" && event
          ? "Changes apply to the entire series. Completion and occurrence deletion only affect the selected day."
          : "Choose a time, a calendar and the people who should join."
      }
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      {deleting ? (
        <>
          <p className="personal-note">
            {event?.title}
            {event?.recurrence !== "none"
              ? " repeats. Remove this occurrence or the entire series."
              : " will be permanently removed from your calendar."}
          </p>
          {error && (
            <p role="alert" className="personal-error">
              {error}
            </p>
          )}
          <div className="personal-dialog-actions">
            <button
              className="personal-button"
              onClick={() => setDeleting(false)}
              disabled={busy}
            >
              Keep event
            </button>
            {event?.recurrence !== "none" && (
              <button
                className="personal-button"
                onClick={() => void deleteEvent(true)}
                disabled={busy}
              >
                This occurrence
              </button>
            )}
            <button
              className="personal-button personal-button--danger"
              onClick={() => void deleteEvent(false)}
              disabled={busy}
            >
              {busy
                ? "Deleting…"
                : event?.recurrence !== "none"
                  ? "Entire series"
                  : "Delete event"}
            </button>
          </div>
        </>
      ) : (
        <form className="personal-form" onSubmit={(e) => void submit(e)}>
          {event && event.recurrence !== "none" && (
            <p className="personal-note">
              Editing the entire series. Completion and occurrence deletion only
              affect the selected day.
            </p>
          )}
          <div className="personal-tabs" aria-label="Entry type">
            {(["event", "reminder"] as const).map((type) => (
              <button
                type="button"
                key={type}
                aria-pressed={input.type === type}
                onClick={() =>
                  patch({
                    type,
                    reminderMinutes:
                      type === "reminder"
                        ? (input.reminderMinutes ?? 0)
                        : input.reminderMinutes,
                  })
                }
              >
                {type === "event" ? (
                  <CalendarDays size={14} />
                ) : (
                  <Bell size={14} />
                )}
                {type === "event" ? "Event" : "Reminder"}
              </button>
            ))}
            {onCreateTask && (
              <button
                type="button"
                aria-pressed="false"
                onClick={() => onCreateTask(input)}
              >
                <ListTodo size={14} />
                Task
              </button>
            )}
          </div>
          <label>
            <span className="sr-only">Title</span>
            <input
              autoFocus
              required
              maxLength={240}
              className="personal-title-input"
              placeholder={
                input.type === "reminder"
                  ? "What do you need to do?"
                  : "Add a title"
              }
              value={input.title}
              onChange={(e) => patch({ title: e.target.value })}
            />
          </label>
          <section className="calendar-when" aria-label="Date and time">
            <div
              className={`calendar-compact-schedule ${input.allDay ? "is-all-day" : ""}`}
            >
              <CalendarDatePicker
                label="Start date"
                value={input.startLocal.slice(0, 10)}
                onChange={chooseStartDate}
              />
              {!input.allDay && (
                <CalendarTimePicker
                  label="Start time"
                  value={input.startLocal.slice(11, 16)}
                  onChange={chooseStartTime}
                />
              )}
              <ArrowRight
                size={14}
                className="calendar-schedule-arrow"
                aria-hidden="true"
              />
              {input.allDay ? (
                <CalendarDatePicker
                  label="Last day"
                  value={endDate.slice(0, 10)}
                  min={input.startLocal.slice(0, 10)}
                  onChange={chooseEndDate}
                />
              ) : (
                <CalendarTimePicker
                  label="End time"
                  value={input.endLocal.slice(11, 16)}
                  onChange={chooseEndTime}
                />
              )}
              <label className="calendar-all-day-toggle">
                <input
                  type="checkbox"
                  checked={input.allDay}
                  onChange={(e) => setAllDay(e.target.checked)}
                />
                All day
              </label>
            </div>
            {!input.allDay && (
              <div className="calendar-schedule-meta">
                {input.timeZone !==
                  Intl.DateTimeFormat().resolvedOptions().timeZone && (
                  <span className="calendar-timezone-summary">
                    {input.timeZone}
                  </span>
                )}
                <button
                  className="calendar-text-button"
                  type="button"
                  aria-expanded={
                    endDateOpen ||
                    endDate.slice(0, 10) !== input.startLocal.slice(0, 10)
                  }
                  onClick={() => setEndDateOpen((value) => !value)}
                >
                  End date
                  <ChevronDown size={13} />
                </button>
              </div>
            )}
            {!input.allDay &&
              (endDateOpen ||
                endDate.slice(0, 10) !== input.startLocal.slice(0, 10)) && (
                <div className="calendar-end-date">
                  <span className="calendar-field-label">Ends on</span>
                  <CalendarDatePicker
                    label="End date"
                    value={endDate.slice(0, 10)}
                    min={input.startLocal.slice(0, 10)}
                    onChange={chooseEndDate}
                  />
                </div>
              )}
          </section>
          {catalog.currentData ? (
            <EventSharingFields
              input={input}
              patch={patch}
              catalog={catalog.currentData}
              organizerUserId={
                event?.organizerUserId ?? catalog.currentData.userId
              }
              editing={Boolean(event)}
              attendees={event?.attendees ?? []}
            />
          ) : (
            <p
              className="personal-note"
              role={catalog.isError ? "alert" : undefined}
            >
              {catalog.isError
                ? getBoardlyQueryErrorMessage(catalog.error)
                : "Loading calendars and members…"}
            </p>
          )}
          {input.type === "event" && (
            <label className="calendar-location-field">
              <Map size={17} aria-hidden="true" />
              <span className="sr-only">Location</span>
              <input
                placeholder="Add a place or meeting link"
                maxLength={1000}
                value={input.location}
                onChange={(e) => patch({ location: e.target.value })}
              />
            </label>
          )}
          <div className="calendar-option-grid">
            <CalendarRepeatField input={input} onChange={patch} />
            <div className="calendar-schedule-field">
              <Bell
                size={17}
                className="calendar-field-icon"
                aria-hidden="true"
              />
              <CalendarOptionPicker
                label="Remind me"
                value={String(input.reminderMinutes ?? "none")}
                options={
                  input.type === "reminder"
                    ? reminderOptions.filter(
                        (option) => option.value !== "none",
                      )
                    : reminderOptions
                }
                onChange={(value) =>
                  patch({
                    reminderMinutes: value === "none" ? null : Number(value),
                  })
                }
              />
            </div>
          </div>
          <button
            type="button"
            className="calendar-more-toggle"
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((current) => !current)}
          >
            {moreOpen ? "Fewer options" : "More options"}
            <ChevronDown size={15} aria-hidden="true" />
          </button>
          {moreOpen && (
            <div className="calendar-more-fields">
              <label>
                Time zone
                <input
                  required
                  value={input.timeZone}
                  onChange={(e) => patch({ timeZone: e.target.value })}
                  list="calendar-timezones"
                />
                <datalist id="calendar-timezones">
                  {Array.from(
                    new Set([
                      Intl.DateTimeFormat().resolvedOptions().timeZone,
                      "Asia/Ho_Chi_Minh",
                      "UTC",
                      "America/New_York",
                      "America/Los_Angeles",
                      "Europe/London",
                      "Asia/Singapore",
                      "Asia/Tokyo",
                    ]),
                  ).map((zone) => (
                    <option key={zone} value={zone} />
                  ))}
                </datalist>
              </label>
              <label>
                Notes
                <textarea
                  placeholder="Add a little context…"
                  maxLength={16000}
                  value={input.description}
                  onChange={(e) => patch({ description: e.target.value })}
                />
              </label>
              <span className="calendar-field-label">Event color</span>
              <div className="calendar-colors" aria-label="Event color">
                {calendarColors.map((color) => (
                  <button
                    type="button"
                    key={color}
                    aria-label={color}
                    aria-pressed={input.color === color}
                    style={{ background: `var(--calendar-${color})` }}
                    onClick={() => patch({ color })}
                  >
                    {input.color === color && <Check size={14} />}
                  </button>
                ))}
              </div>
            </div>
          )}
          {input.allDay && input.reminderMinutes != null && (
            <p className="personal-note">
              All-day reminders use midnight in the selected time zone.
            </p>
          )}
          {error && (
            <p role="alert" className="personal-error">
              {error}
            </p>
          )}
          <footer className="personal-dialog-actions">
            <button
              type="button"
              className="personal-button"
              onClick={onClose}
              disabled={busy}
            >
              Cancel
            </button>
            <button
              className="personal-button personal-button--primary"
              disabled={busy || !catalog.currentData}
            >
              {saving.isLoading ? "Saving…" : "Save"}
            </button>
          </footer>
        </form>
      )}
    </CalendarEventDialog>
  );
}
