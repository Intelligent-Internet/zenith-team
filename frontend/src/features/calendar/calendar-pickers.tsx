import * as Popover from "@radix-ui/react-popover";
import { useEffect, useRef, useState } from "react";
import {
  CalendarDays,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Clock3,
} from "@/shared/ui/icons";
import { dateKey, dayAt, monthDays } from "./calendar-dates";
import { useCalendarTheme } from "./calendar-theme";

const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const timeSlots = Array.from({ length: 96 }, (_, index) => {
  const hour = Math.floor(index / 4);
  const minute = (index % 4) * 15;
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
});

function formatDate(value: string) {
  return dayAt(value).toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

function formatCompactDate(value: string) {
  return dayAt(value).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

function formatTime(value: string) {
  const [hour, minute] = value.split(":").map(Number);
  return new Date(2020, 0, 1, hour, minute).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
}

function parseTime(value: string): string | null {
  const match = value.trim().match(/^(\d{1,2})(?::?(\d{2}))?\s*(am|pm)?$/i);
  if (!match) return null;
  let hour = Number(match[1]);
  const minute = Number(match[2] ?? "0");
  const period = match[3]?.toLowerCase();
  if (minute > 59 || hour > (period ? 12 : 23) || (period && hour < 1)) {
    return null;
  }
  if (period) hour = (hour % 12) + (period === "pm" ? 12 : 0);
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

export function CalendarDatePicker({
  label,
  value,
  appearance = "calendar",
  min = "1900-01-01",
  max = "2199-12-31",
  disabled = false,
  onChange,
}: {
  label: string;
  value: string;
  appearance?: "calendar" | "workspace";
  min?: string;
  max?: string;
  disabled?: boolean;
  onChange: (date: string) => void;
}) {
  const theme = useCalendarTheme();
  const [open, setOpen] = useState(false);
  const [visibleMonth, setVisibleMonth] = useState(() => dayAt(value));
  const firstOfMonth = new Date(
    visibleMonth.getFullYear(),
    visibleMonth.getMonth(),
    1,
  );
  const today = dateKey(new Date());
  const days = monthDays(firstOfMonth);
  const previousLastDay = dateKey(
    new Date(firstOfMonth.getFullYear(), firstOfMonth.getMonth(), 0),
  );
  const nextFirstDay = dateKey(
    new Date(firstOfMonth.getFullYear(), firstOfMonth.getMonth() + 1, 1),
  );
  const monthLabel = firstOfMonth.toLocaleDateString(undefined, {
    month: "long",
    year: "numeric",
  });
  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        if (next) setVisibleMonth(dayAt(value));
        setOpen(next);
      }}
    >
      <Popover.Trigger asChild>
        <button
          type="button"
          className="calendar-picker-trigger"
          aria-label={`${label}: ${formatDate(value)}`}
          disabled={disabled}
        >
          <CalendarDays size={17} aria-hidden="true" />
          <span>{formatCompactDate(value)}</span>
        </button>
      </Popover.Trigger>
      <Popover.Portal
        container={
          appearance === "calendar"
            ? document.querySelector(".boardly-workspace-theme")
            : undefined
        }
      >
        <Popover.Content
          style={appearance === "calendar" ? theme : undefined}
          data-appearance={appearance}
          className="calendar-picker-popover calendar-date-popover"
          sideOffset={7}
          collisionPadding={12}
          align="start"
        >
          <div className="calendar-picker-month-heading">
            <strong>{monthLabel}</strong>
            <div>
              {([-1, 1] as const).map((direction) => (
                <button
                  type="button"
                  key={direction}
                  aria-label={
                    direction === -1 ? "Previous month" : "Next month"
                  }
                  disabled={
                    direction === -1
                      ? previousLastDay < min
                      : nextFirstDay > max
                  }
                  onClick={() =>
                    setVisibleMonth(
                      new Date(
                        firstOfMonth.getFullYear(),
                        firstOfMonth.getMonth() + direction,
                        1,
                      ),
                    )
                  }
                >
                  {direction === -1 ? (
                    <ChevronLeft size={17} />
                  ) : (
                    <ChevronRight size={17} />
                  )}
                </button>
              ))}
            </div>
          </div>
          <div className="calendar-picker-weekdays" aria-hidden="true">
            {weekdays.map((day, index) => (
              <span key={index}>{day}</span>
            ))}
          </div>
          <div className="calendar-picker-days">
            {days.map((day) => {
              const key = dateKey(day);
              return (
                <button
                  type="button"
                  key={key}
                  aria-label={formatDate(key)}
                  aria-pressed={key === value}
                  className={
                    day.getMonth() !== firstOfMonth.getMonth()
                      ? "is-outside"
                      : undefined
                  }
                  data-today={key === today || undefined}
                  disabled={key < min || key > max}
                  onClick={() => {
                    onChange(key);
                    setOpen(false);
                  }}
                >
                  {day.getDate()}
                </button>
              );
            })}
          </div>
          <div className="calendar-picker-footer">
            <button
              type="button"
              disabled={today < min || today > max}
              onClick={() => {
                onChange(today);
                setOpen(false);
              }}
            >
              Today
            </button>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export function CalendarTimePicker({
  label,
  value,
  appearance = "calendar",
  onChange,
}: {
  label: string;
  value: string;
  appearance?: "calendar" | "workspace";
  onChange: (time: string) => void;
}) {
  const theme = useCalendarTheme();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [invalid, setInvalid] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open || draft) return;
    const frame = requestAnimationFrame(() => {
      if (listRef.current) {
        const index = timeSlots.findIndex((slot) => slot >= value);
        listRef.current.scrollTop = Math.max(0, index * 38 - 96);
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [open, value, draft]);
  const matches = draft.trim().toLowerCase();
  const slots = matches
    ? timeSlots.filter(
        (slot) =>
          slot.includes(matches) ||
          formatTime(slot).toLowerCase().includes(matches),
      )
    : timeSlots;
  const typedTime = parseTime(draft);
  function choose(time: string) {
    onChange(time);
    setOpen(false);
    setDraft("");
    setInvalid(false);
  }
  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setDraft("");
          setInvalid(false);
        }
      }}
    >
      <Popover.Trigger asChild>
        <button
          type="button"
          className="calendar-picker-trigger calendar-time-trigger"
          aria-label={`${label}: ${formatTime(value)}`}
        >
          <Clock3 size={16} aria-hidden="true" />
          <span>{formatTime(value)}</span>
        </button>
      </Popover.Trigger>
      <Popover.Portal
        container={
          appearance === "calendar"
            ? document.querySelector(".boardly-workspace-theme")
            : undefined
        }
      >
        <Popover.Content
          style={appearance === "calendar" ? theme : undefined}
          data-appearance={appearance}
          className="calendar-picker-popover calendar-time-popover"
          sideOffset={7}
          collisionPadding={12}
          align="start"
        >
          <label className="calendar-time-search">
            <span className="sr-only">Type {label.toLowerCase()}</span>
            <input
              autoFocus
              value={draft}
              placeholder="Type 9:30 or 2 pm"
              aria-invalid={invalid || undefined}
              onChange={(event) => {
                setDraft(event.target.value);
                setInvalid(false);
              }}
              onKeyDown={(event) => {
                if (event.key !== "Enter") return;
                event.preventDefault();
                if (typedTime) choose(typedTime);
                else setInvalid(true);
              }}
            />
          </label>
          {invalid && (
            <p className="calendar-time-error">Use a time like 9:30 or 2 pm.</p>
          )}
          {typedTime && !timeSlots.includes(typedTime) && (
            <button
              type="button"
              className="calendar-time-custom"
              onClick={() => choose(typedTime)}
            >
              Use {formatTime(typedTime)}
            </button>
          )}
          <div
            ref={listRef}
            className="calendar-time-options"
            aria-label={`${label} options`}
          >
            {slots.map((slot) => (
              <button
                type="button"
                key={slot}
                aria-pressed={slot === value}
                onClick={() => choose(slot)}
              >
                {formatTime(slot)}
                {slot === value && <Check size={15} aria-hidden="true" />}
              </button>
            ))}
            {!slots.length && !typedTime && (
              <p className="calendar-time-empty">
                Type a time and press Enter.
              </p>
            )}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export function CalendarOptionPicker({
  label,
  value,
  appearance = "calendar",
  options,
  onChange,
  disabled = false,
}: {
  label: string;
  value: string;
  appearance?: "calendar" | "workspace";
  options: { value: string; label: string; description?: string }[];
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const theme = useCalendarTheme();
  const [open, setOpen] = useState(false);
  const selected = options.find((option) => option.value === value);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          className="calendar-picker-trigger"
          aria-label={`${label}: ${selected?.label ?? "Choose"}`}
          disabled={disabled}
        >
          <span>{selected?.label ?? "Choose"}</span>
          <ChevronDown size={15} aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal
        container={
          appearance === "calendar"
            ? document.querySelector(".boardly-workspace-theme")
            : undefined
        }
      >
        <Popover.Content
          style={appearance === "calendar" ? theme : undefined}
          data-appearance={appearance}
          className="calendar-picker-popover calendar-option-popover"
          sideOffset={7}
          collisionPadding={12}
          align="start"
        >
          <p className="calendar-option-heading">{label}</p>
          {options.map((option) => (
            <Popover.Close asChild key={option.value}>
              <button
                type="button"
                className="calendar-option"
                aria-pressed={option.value === value}
                onClick={() => onChange(option.value)}
              >
                <span>
                  <strong>{option.label}</strong>
                  {option.description && <small>{option.description}</small>}
                </span>
                {option.value === value && (
                  <Check size={17} aria-hidden="true" />
                )}
              </button>
            </Popover.Close>
          ))}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
