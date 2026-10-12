import { useState } from "react";
import "./task-calendar.css";
import {
  CalendarDatePicker,
  CalendarTimePicker,
  CalendarOptionPicker,
} from "./calendar-pickers";
import {
  COMMON_TIME_ZONES,
  CRON_PRESETS,
  getCronPresetId,
} from "@/features/scheduled/model/schedule-form";
import { useCalendarTheme } from "./calendar-theme";
import "./calendar.css";
import {
  createInitialScheduleForm,
  type ScheduleFormValues,
} from "@/features/scheduled/model/schedule-form";
import type { TaskScheduleTrigger } from "./task-calendar-api";

export function localTaskTime(value: string | Date) {
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}
export function taskScheduleValues(
  trigger?: TaskScheduleTrigger,
  initialTime?: string,
): ScheduleFormValues {
  const defaults = createInitialScheduleForm();
  return {
    ...defaults,
    triggerType: trigger?.type ?? "once",
    runAtLocal:
      trigger?.type === "once"
        ? localTaskTime(trigger.runAt)
        : (initialTime ?? defaults.runAtLocal),
    cronExpression:
      trigger?.type === "cron" ? trigger.expression : defaults.cronExpression,
    timeZone: trigger?.type === "cron" ? trigger.timeZone : defaults.timeZone,
  };
}
export function taskScheduleTrigger(
  values: ScheduleFormValues,
): TaskScheduleTrigger {
  return values.triggerType === "once"
    ? { type: "once", runAt: new Date(values.runAtLocal).toISOString() }
    : {
        type: "cron",
        expression: values.cronExpression,
        timeZone: values.timeZone,
      };
}
export function TaskRunTime({
  value,
  onChange,
  appearance = "calendar",
}: {
  value: string;
  appearance?: "calendar" | "workspace";
  onChange: (value: string) => void;
}) {
  return (
    <div className="task-run-time">
      <CalendarDatePicker
        appearance={appearance}
        label="Run date"
        value={value.slice(0, 10)}
        onChange={(day) => onChange(`${day}T${value.slice(11)}`)}
      />
      <CalendarTimePicker
        appearance={appearance}
        label="Run time"
        value={value.slice(11)}
        onChange={(time) => onChange(`${value.slice(0, 10)}T${time}`)}
      />
    </div>
  );
}

export function TaskScheduleFields({
  values,
  onChange,
  appearance = "workspace",
}: {
  values: ScheduleFormValues;
  appearance?: "calendar" | "workspace";
  onChange: (value: ScheduleFormValues) => void;
}) {
  const theme = useCalendarTheme();
  const [custom, setCustom] = useState(!getCronPresetId(values.cronExpression));
  const preset = custom ? undefined : getCronPresetId(values.cronExpression);
  const repeat = values.triggerType === "once" ? "once" : (preset ?? "custom");
  const fields = values.cronExpression.trim().split(/\s+/u);
  function changeField(index: number, value: string) {
    const next = [...fields];
    next[index] = value;
    onChange({ ...values, cronExpression: next.join(" ") });
  }
  return (
    <div
      className="task-schedule-fields"
      style={appearance === "calendar" ? theme : undefined}
    >
      {values.triggerType === "once" && (
        <TaskRunTime
          appearance={appearance}
          value={values.runAtLocal}
          onChange={(runAtLocal) => onChange({ ...values, runAtLocal })}
        />
      )}
      <CalendarOptionPicker
        appearance={appearance}
        label="Repeat"
        value={repeat}
        options={[
          { value: "once", label: "Does not repeat" },
          ...CRON_PRESETS.map(({ id, label }) => ({ value: id, label })),
          { value: "custom", label: "Custom schedule…" },
        ]}
        onChange={(value) => {
          setCustom(value === "custom");
          if (value === "once") {
            onChange({ ...values, triggerType: "once" });
            return;
          }
          const selected = CRON_PRESETS.find((item) => item.id === value);
          // Keep the selected time when turning a one-time task into a series.
          const expression = (selected?.expression ?? "*/15 * * * *").split(
            " ",
          );
          if (selected && selected.id !== "hourly") {
            const date = new Date(values.runAtLocal);
            const [hour, minute] =
              values.triggerType === "once"
                ? values.runAtLocal.slice(11).split(":").map(Number)
                : [Number(fields[1]), Number(fields[0])];
            if (Number.isInteger(hour) && Number.isInteger(minute)) {
              expression[0] = String(minute);
              expression[1] = String(hour);
            }
            if (values.triggerType === "once") {
              if (selected.id === "weekly")
                expression[4] = String(date.getDay());
              if (selected.id === "monthly")
                expression[2] = String(date.getDate());
            }
          }
          onChange({
            ...values,
            triggerType: "cron",
            cronExpression: expression.join(" "),
          });
        }}
      />
      {values.triggerType === "cron" && (
        <>
          {preset && preset !== "hourly" && (
            <CalendarTimePicker
              appearance={appearance}
              label="Run time"
              value={`${fields[1].padStart(2, "0")}:${fields[0].padStart(2, "0")}`}
              onChange={(time) => {
                const [hour, minute] = time.split(":").map(Number);
                const next = [...fields];
                next[0] = String(minute);
                next[1] = String(hour);
                onChange({ ...values, cronExpression: next.join(" ") });
              }}
            />
          )}
          {preset === "hourly" && (
            <div className="task-run-time">
              <CalendarOptionPicker
                appearance={appearance}
                label="Every"
                value={fields[1]}
                options={[
                  ...new Set([
                    fields[1],
                    "*",
                    "*/2",
                    "*/3",
                    "*/4",
                    "*/6",
                    "*/8",
                    "*/12",
                  ]),
                ].map((value) => ({
                  value,
                  label:
                    value === "*"
                      ? "Every hour"
                      : `Every ${value.slice(2)} hours`,
                }))}
                onChange={(value) => changeField(1, value)}
              />
              <label className="task-schedule-input">
                At minute
                <input
                  aria-label="Minute of the hour"
                  type="number"
                  min={0}
                  max={59}
                  required
                  value={fields[0]}
                  onChange={(event) => {
                    const value = Number(event.target.value);
                    if (
                      event.target.value &&
                      Number.isInteger(value) &&
                      value >= 0 &&
                      value <= 59
                    )
                      changeField(0, String(value));
                  }}
                />
              </label>
            </div>
          )}
          {preset === "weekly" && (
            <div
              className="calendar-repeat-weekdays"
              aria-label="Run on weekdays"
            >
              {[1, 2, 3, 4, 5, 6, 0].map((day, index) => {
                const days = fields[4].split(",");
                const selected = days.includes(String(day));
                return (
                  <button
                    type="button"
                    key={day}
                    aria-label={
                      [
                        "Monday",
                        "Tuesday",
                        "Wednesday",
                        "Thursday",
                        "Friday",
                        "Saturday",
                        "Sunday",
                      ][index]
                    }
                    aria-pressed={selected}
                    onClick={() => {
                      const next = selected
                        ? days.filter((value) => value !== String(day))
                        : [...days, String(day)];
                      if (next.length) changeField(4, next.sort().join(","));
                    }}
                  >
                    {["M", "T", "W", "T", "F", "S", "S"][index]}
                  </button>
                );
              })}
            </div>
          )}
          {preset === "monthly" && (
            <label className="task-schedule-input">
              Day of month
              <input
                aria-label="Day of month"
                type="number"
                min={1}
                max={31}
                required
                value={fields[2]}
                onChange={(event) => {
                  const value = Number(event.target.value);
                  if (
                    event.target.value &&
                    Number.isInteger(value) &&
                    value >= 1 &&
                    value <= 31
                  )
                    changeField(2, String(value));
                }}
              />
            </label>
          )}
          {!preset && (
            <label className="task-schedule-input">
              Cron expression
              <input
                aria-label="Cron expression"
                autoComplete="off"
                spellCheck={false}
                required
                maxLength={256}
                value={values.cronExpression}
                onChange={(event) =>
                  onChange({ ...values, cronExpression: event.target.value })
                }
              />
              <small>
                Minute · hour · day · month · weekday. At least 5 minutes apart.
              </small>
            </label>
          )}
          <CalendarOptionPicker
            appearance={appearance}
            label="Time zone"
            value={values.timeZone}
            options={[...new Set([values.timeZone, ...COMMON_TIME_ZONES])].map(
              (value) => ({ value, label: value.replaceAll("_", " ") }),
            )}
            onChange={(timeZone) => onChange({ ...values, timeZone })}
          />
        </>
      )}
      <p className="task-schedule-note">
        {values.triggerType === "once"
          ? Intl.DateTimeFormat()
              .resolvedOptions()
              .timeZone.replaceAll("_", " ")
          : "Creates a new task each time. Overlapping work is skipped."}
      </p>
    </div>
  );
}
