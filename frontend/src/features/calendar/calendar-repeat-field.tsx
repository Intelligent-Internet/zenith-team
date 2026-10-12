import { Repeat2 } from "@/shared/ui/icons";
import { useState, type FormEvent } from "react";
import { PersonalToolsDialog } from "@/features/common/components/personal-tools-dialog";
import type { CalendarInput } from "./calendar-api";
import { addDays, dateKey, dayAt } from "./calendar-dates";
import { CalendarDatePicker, CalendarOptionPicker } from "./calendar-pickers";
import { useCalendarTheme } from "./calendar-theme";
import {
  monthlyWeekdayLabel,
  recurrenceSummary,
  repeatWeekdays,
  type CalendarRepeat,
} from "./calendar-recurrence";

const repeatOptions = [
  { value: "none", label: "Does not repeat" },
  { value: "daily", label: "Every day" },
  { value: "weekly", label: "Every week" },
  { value: "monthly", label: "Every month" },
  { value: "yearly", label: "Every year" },
];

export function CalendarRepeatField({
  input,
  onChange,
}: {
  input: CalendarInput;
  onChange: (rule: CalendarRepeat) => void;
}) {
  const [open, setOpen] = useState(false);
  const custom =
    (input.recurrenceInterval ?? 1) > 1 ||
    Boolean(
      input.recurrenceWeekdays?.length ||
      input.recurrenceCount ||
      input.recurrenceUntil,
    ) ||
    input.recurrenceMonthlyMode === "day_of_week";
  return (
    <div className="calendar-schedule-field">
      <Repeat2 size={17} className="calendar-field-icon" aria-hidden="true" />
      <CalendarOptionPicker
        label="Repeat"
        value={custom ? "custom-current" : input.recurrence}
        options={[
          ...repeatOptions,
          ...(custom
            ? [
                {
                  value: "custom-current",
                  label: "Custom repeat",
                  description: recurrenceSummary(input, input.startLocal),
                },
              ]
            : []),
          { value: "custom", label: "Custom…" },
        ]}
        onChange={(value) => {
          if (value.startsWith("custom")) {
            setOpen(true);
            return;
          }
          onChange({
            recurrence: value as CalendarRepeat["recurrence"],
            recurrenceInterval: 1,
            recurrenceWeekdays: [],
            recurrenceMonthlyMode: "day_of_month",
            recurrenceCount: null,
            recurrenceUntil: null,
          });
        }}
      />
      {custom && (
        <span className="calendar-recurrence-summary">
          {recurrenceSummary(input, input.startLocal)}
        </span>
      )}
      {open && (
        <CustomRepeatDialog
          input={input}
          onClose={() => setOpen(false)}
          onApply={(rule) => {
            onChange(rule);
            setOpen(false);
          }}
        />
      )}
    </div>
  );
}

function CustomRepeatDialog({
  input,
  onClose,
  onApply,
}: {
  input: CalendarInput;
  onClose: () => void;
  onApply: (rule: CalendarRepeat) => void;
}) {
  const theme = useCalendarTheme();
  const startDate = input.startLocal.slice(0, 10);
  const [frequency, setFrequency] = useState<
    Exclude<CalendarRepeat["recurrence"], "none">
  >(input.recurrence === "none" ? "weekly" : input.recurrence);
  const [interval, setInterval] = useState(
    String(input.recurrenceInterval ?? 1),
  );
  const [weekdays, setWeekdays] = useState(
    input.recurrenceWeekdays?.length
      ? input.recurrenceWeekdays
      : [dayAt(startDate).getDay()],
  );
  const [monthlyMode, setMonthlyMode] = useState(
    input.recurrenceMonthlyMode ?? "day_of_month",
  );
  const [ending, setEnding] = useState(
    input.recurrenceUntil ? "date" : input.recurrenceCount ? "count" : "never",
  );
  const [until, setUntil] = useState(
    input.recurrenceUntil ?? dateKey(addDays(dayAt(startDate), 90)),
  );
  const [count, setCount] = useState(String(input.recurrenceCount ?? 13));
  const [error, setError] = useState("");
  const rule: CalendarRepeat = {
    recurrence: frequency,
    recurrenceInterval: Number(interval),
    recurrenceWeekdays:
      frequency === "weekly" ? [...weekdays].sort((a, b) => a - b) : [],
    recurrenceMonthlyMode:
      frequency === "monthly" ? monthlyMode : "day_of_month",
    recurrenceUntil: ending === "date" ? until : null,
    recurrenceCount: ending === "count" ? Number(count) : null,
  };
  function apply(event: FormEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (
      !Number.isInteger(rule.recurrenceInterval) ||
      rule.recurrenceInterval! < 1 ||
      rule.recurrenceInterval! > 999
    ) {
      setError("Choose a repeat interval from 1 to 999.");
      return;
    }
    if (frequency === "weekly" && !weekdays.length) {
      setError("Choose at least one day of the week.");
      return;
    }
    if (ending === "date" && until < startDate) {
      setError("The end date must be on or after the event starts.");
      return;
    }
    if (
      ending === "count" &&
      (!Number.isInteger(rule.recurrenceCount) ||
        rule.recurrenceCount! < 1 ||
        rule.recurrenceCount! > 1000)
    ) {
      setError("Choose from 1 to 1,000 occurrences.");
      return;
    }
    onApply(rule);
  }
  return (
    <PersonalToolsDialog
      style={theme}
      title="Custom repeat"
      description="Choose how often this event repeats and when it ends."
      onClose={onClose}
      className="calendar-repeat-dialog"
      overlayClassName="calendar-repeat-overlay"
    >
      <form className="personal-form calendar-repeat-form" onSubmit={apply}>
        <div className="calendar-repeat-interval">
          <label htmlFor="calendar-repeat-interval">Repeat every</label>
          <input
            id="calendar-repeat-interval"
            type="number"
            min={1}
            max={999}
            step={1}
            required
            value={interval}
            onChange={(event) => setInterval(event.target.value)}
          />
          <CalendarOptionPicker
            label="Repeat unit"
            value={frequency}
            options={[
              { value: "daily", label: "day" },
              { value: "weekly", label: "week" },
              { value: "monthly", label: "month" },
              { value: "yearly", label: "year" },
            ]}
            onChange={(value) => setFrequency(value as typeof frequency)}
          />
        </div>
        {frequency === "weekly" && (
          <fieldset className="calendar-repeat-section">
            <legend>Repeat on</legend>
            <div className="calendar-repeat-weekdays">
              {repeatWeekdays.map((day) => (
                <button
                  key={day.value}
                  type="button"
                  aria-label={day.name}
                  aria-pressed={weekdays.includes(day.value)}
                  onClick={() =>
                    setWeekdays((current) =>
                      current.includes(day.value)
                        ? current.filter((value) => value !== day.value)
                        : [...current, day.value],
                    )
                  }
                >
                  {day.short}
                </button>
              ))}
            </div>
          </fieldset>
        )}
        {frequency === "monthly" && (
          <div className="calendar-schedule-field">
            <span className="calendar-field-label">Repeat on</span>
            <CalendarOptionPicker
              label="Monthly pattern"
              value={monthlyMode}
              options={[
                {
                  value: "day_of_month",
                  label: `Day ${dayAt(startDate).getDate()}`,
                },
                { value: "day_of_week", label: monthlyWeekdayLabel(startDate) },
              ]}
              onChange={(value) => setMonthlyMode(value as typeof monthlyMode)}
            />
          </div>
        )}
        <fieldset className="calendar-repeat-section calendar-repeat-end">
          <legend>Ends</legend>
          <label className="calendar-repeat-end-choice">
            <input
              type="radio"
              name="calendar-repeat-ending"
              value="never"
              checked={ending === "never"}
              onChange={() => setEnding("never")}
            />
            Never
          </label>
          <div className="calendar-repeat-end-row">
            <label className="calendar-repeat-end-choice">
              <input
                type="radio"
                name="calendar-repeat-ending"
                value="date"
                checked={ending === "date"}
                onChange={() => setEnding("date")}
              />
              On date
            </label>
            <CalendarDatePicker
              label="Repeat end date"
              value={until}
              min={startDate}
              disabled={ending !== "date"}
              onChange={setUntil}
            />
          </div>
          <div className="calendar-repeat-end-row">
            <label className="calendar-repeat-end-choice">
              <input
                type="radio"
                name="calendar-repeat-ending"
                value="count"
                checked={ending === "count"}
                onChange={() => setEnding("count")}
              />
              After
            </label>
            <label className="calendar-repeat-count">
              <input
                aria-label="Number of occurrences"
                type="number"
                min={1}
                max={1000}
                step={1}
                required
                disabled={ending !== "count"}
                value={count}
                onChange={(event) => setCount(event.target.value)}
              />
              <span>occurrences</span>
            </label>
          </div>
        </fieldset>
        {error && (
          <p className="personal-error" role="alert">
            {error}
          </p>
        )}
        <footer className="personal-dialog-actions">
          <button type="button" className="personal-button" onClick={onClose}>
            Cancel
          </button>
          <button
            type="submit"
            className="personal-button personal-button--primary"
          >
            Done
          </button>
        </footer>
      </form>
    </PersonalToolsDialog>
  );
}
