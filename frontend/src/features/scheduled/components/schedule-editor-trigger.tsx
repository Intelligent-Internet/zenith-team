import {
  CalendarDays,
  Check,
  ChevronDown,
  Clock3,
  Repeat2,
  SlidersHorizontal,
  TriangleAlert,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState } from "react";

import {
  CRON_PRESETS,
  COMMON_TIME_ZONES,
  getCronPresetId,
  type CronPreset,
  type ScheduleFormValues,
} from "@/features/scheduled/model/schedule-form";
import { ScheduledGlassPanel } from "@/features/scheduled/components/scheduled-glass-panel";
import {
  ScheduledFieldControl,
  scheduledNativeControlClassName,
} from "@/features/scheduled/components/scheduled-field-control";
import { cx } from "@/shared/lib/cx";
import { DateTimeInput } from "@/shared/ui/date-time-input";

import { EditorField } from "@/features/scheduled/components/schedule-editor-basics";

export function ScheduleEditorTrigger({
  error,
  onChange,
  values,
}: {
  error: string | null;
  onChange: (next: ScheduleFormValues) => void;
  values: ScheduleFormValues;
}) {
  return (
    <div className="grid gap-6">
      <div>
        <h2 className="text-sm font-semibold text-[var(--workspace-native-text)]">
          Trigger type
        </h2>
        <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
          Choose one exact future time or a recurring five-field cron schedule.
        </p>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <TriggerTypeButton
            active={values.triggerType === "once"}
            description="Run once at an exact future date and time."
            icon={<CalendarDays aria-hidden className="size-5" />}
            label="One time"
            onClick={() => onChange({ ...values, triggerType: "once" })}
          />
          <TriggerTypeButton
            active={values.triggerType === "cron"}
            description="Repeat on any supported cron cadence."
            icon={<Repeat2 aria-hidden className="size-5" />}
            label="Recurring"
            onClick={() => onChange({ ...values, triggerType: "cron" })}
          />
        </div>
      </div>

      {values.triggerType === "once" ? (
        <EditorField
          description={`Time zone: ${describeTimeZone(Intl.DateTimeFormat().resolvedOptions().timeZone)} · Detected from this computer.`}
          label="Run at"
        >
          <ScheduledFieldControl>
            <DateTimeInput
              aria-label="Run at"
              className={scheduledNativeControlClassName}
              type="datetime-local"
              value={values.runAtLocal}
              onChange={(event) =>
                onChange({ ...values, runAtLocal: event.target.value })
              }
            />
          </ScheduledFieldControl>
        </EditorField>
      ) : (
        <RecurringScheduleEditor
          error={error}
          onChange={onChange}
          values={values}
        />
      )}
      {error ? (
        <p
          id="schedule-trigger-error"
          className="text-sm text-[var(--workspace-native-danger)]"
          role="alert"
        >
          {error}
        </p>
      ) : null}
    </div>
  );
}

function RecurringScheduleEditor({
  error,
  onChange,
  values,
}: {
  error: string | null;
  onChange: (next: ScheduleFormValues) => void;
  values: ScheduleFormValues;
}) {
  const activePreset = getCronPresetId(values.cronExpression);
  const showDaylightSavingWarning = shouldWarnAboutDaylightSaving(values);
  const minuteInterval = /^\*\/(5|6|10|12|15|20|30) \* \* \* \*$/u.exec(
    values.cronExpression,
  )?.[1];
  const hourInterval = /^0 \*\/(1|2|3|4|6|8|12) \* \* \*$/u.exec(
    values.cronExpression,
  )?.[1];
  const [customMode, setCustomMode] = useState<"cron" | "interval">(
    minuteInterval || hourInterval ? "interval" : "cron",
  );
  const intervalValue = minuteInterval
    ? `minutes:${minuteInterval}`
    : hourInterval
      ? `hours:${hourInterval}`
      : "";
  const usesCustomSchedule = !activePreset;
  const presetConfiguration = readPresetConfiguration(values.cronExpression);
  const cronInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (error) cronInputRef.current?.focus();
  }, [error]);

  return (
    <>
      <EditorField
        label="Time zone"
        description="All recurring run times use this zone. The UTC offset shown is the current offset; daylight saving changes are applied automatically."
      >
        <ScheduledFieldControl trailing={<ChevronDown className="size-4" />}>
          <select
            aria-label="Time zone"
            className={`${scheduledNativeControlClassName} cursor-pointer appearance-none`}
            value={values.timeZone}
            onChange={(event) =>
              onChange({ ...values, timeZone: event.target.value })
            }
          >
            {[
              ...new Set([
                values.timeZone,
                Intl.DateTimeFormat().resolvedOptions().timeZone,
                ...COMMON_TIME_ZONES,
              ]),
            ]
              .filter(Boolean)
              .map((zone) => (
                <option key={zone} value={zone}>
                  {describeTimeZone(zone)}
                </option>
              ))}
          </select>
        </ScheduledFieldControl>
      </EditorField>
      <div>
        <h2 className="text-sm font-semibold text-[var(--workspace-native-text)]">
          Repeat
        </h2>
        <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
          Pick a common schedule, or choose Custom for intervals and cron.
        </p>
        <div className="mt-3 grid grid-cols-2 gap-2 lg:grid-cols-3">
          {CRON_PRESETS.map((preset) => {
            const selected = activePreset === preset.id;
            return (
              <ScheduledGlassPanel
                density={selected ? "dense" : "regular"}
                className="min-h-[68px]"
                selectionState={selected ? "selected" : "unselected"}
                contentClassName={cx(
                  "p-0 transition",
                  selected && "bg-[var(--workspace-accent-soft)]",
                )}
                key={preset.id}
              >
                <button
                  aria-pressed={selected}
                  className="flex h-full w-full items-center justify-between gap-3 rounded-[20px] px-4 py-3 text-left transition hover:bg-[var(--workspace-hover-bg)]"
                  type="button"
                  onClick={() =>
                    onChange({ ...values, cronExpression: preset.expression })
                  }
                >
                  <span className="min-w-0">
                    <strong className="block text-sm font-semibold text-[var(--workspace-native-text)]">
                      {preset.label}
                    </strong>
                    <span className="mt-0.5 block text-xs text-[var(--workspace-native-text-muted)]">
                      {selected
                        ? describePresetConfiguration(
                            preset.id,
                            presetConfiguration,
                          )
                        : preset.description}
                    </span>
                  </span>
                  {selected ? (
                    <Check
                      aria-hidden
                      className="size-4 shrink-0 text-[var(--workspace-native-accent)]"
                    />
                  ) : null}
                </button>
              </ScheduledGlassPanel>
            );
          })}
          <ScheduledGlassPanel
            density={usesCustomSchedule ? "dense" : "regular"}
            className="min-h-[68px]"
            selectionState={usesCustomSchedule ? "selected" : "unselected"}
            contentClassName={cx(
              "p-0 transition",
              usesCustomSchedule && "bg-[var(--workspace-accent-soft)]",
            )}
          >
            <button
              aria-pressed={usesCustomSchedule}
              className="flex h-full w-full items-center justify-between gap-3 rounded-[20px] px-4 py-3 text-left transition hover:bg-[var(--workspace-hover-bg)]"
              type="button"
              onClick={() => {
                setCustomMode("interval");
                onChange({ ...values, cronExpression: "*/15 * * * *" });
              }}
            >
              <span className="min-w-0">
                <strong className="block text-sm font-semibold text-[var(--workspace-native-text)]">
                  Custom
                </strong>
                <span className="mt-0.5 block text-xs text-[var(--workspace-native-text-muted)]">
                  Interval or cron
                </span>
              </span>
              {usesCustomSchedule ? (
                <Check
                  aria-hidden
                  className="size-4 shrink-0 text-[var(--workspace-native-accent)]"
                />
              ) : (
                <SlidersHorizontal
                  aria-hidden
                  className="size-4 shrink-0 text-[var(--workspace-native-text-muted)]"
                />
              )}
            </button>
          </ScheduledGlassPanel>
        </div>
      </div>

      {activePreset ? (
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          <div
            className={cx(
              "min-w-0",
              (activePreset === "hourly" ||
                activePreset === "weekly" ||
                activePreset === "monthly") &&
                "sm:col-span-2",
            )}
          >
            <PresetScheduleEditor
              configuration={presetConfiguration}
              presetId={activePreset}
              onChange={(configuration) =>
                onChange({
                  ...values,
                  cronExpression: buildPresetCron(activePreset, configuration),
                })
              }
            />
          </div>
        </div>
      ) : (
        <div className="max-w-3xl">
          <div>
            <div className="mb-3 flex w-fit rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] p-1">
              <CustomModeButton
                active={customMode === "interval"}
                label="Interval"
                onClick={() => {
                  setCustomMode("interval");
                  if (!minuteInterval && !hourInterval) {
                    onChange({ ...values, cronExpression: "*/15 * * * *" });
                  }
                }}
              />
              <CustomModeButton
                active={customMode === "cron"}
                label="Cron"
                onClick={() => setCustomMode("cron")}
              />
            </div>

            {customMode === "interval" ? (
              <EditorField label="Repeat every">
                <ScheduledFieldControl
                  icon={<Repeat2 className="size-5" />}
                  trailing={<ChevronDown className="size-4" />}
                >
                  <select
                    aria-label="Repeat interval"
                    className={`${scheduledNativeControlClassName} cursor-pointer appearance-none`}
                    value={intervalValue}
                    onChange={(event) => {
                      const [unit, amount] = event.target.value.split(":");
                      onChange({
                        ...values,
                        cronExpression:
                          unit === "minutes"
                            ? `*/${amount} * * * *`
                            : `0 */${amount} * * *`,
                      });
                    }}
                  >
                    {[5, 6, 10, 12, 15, 20, 30].map((minutes) => (
                      <option
                        key={`minutes:${minutes}`}
                        value={`minutes:${minutes}`}
                      >
                        Every {minutes} minutes
                      </option>
                    ))}
                  </select>
                </ScheduledFieldControl>
              </EditorField>
            ) : (
              <EditorField
                description="Five fields: minute · hour · day · month · weekday. Minimum interval is five minutes."
                label="Cron expression"
              >
                <ScheduledFieldControl
                  icon={<SlidersHorizontal className="size-5" />}
                >
                  <input
                    ref={cronInputRef}
                    aria-label="Cron expression"
                    aria-invalid={Boolean(error)}
                    aria-describedby={
                      error ? "schedule-trigger-error" : undefined
                    }
                    autoCapitalize="off"
                    autoComplete="off"
                    className={`${scheduledNativeControlClassName} font-mono`}
                    maxLength={256}
                    placeholder="0 8 * * *"
                    spellCheck={false}
                    value={values.cronExpression}
                    onChange={(event) =>
                      onChange({
                        ...values,
                        cronExpression: event.target.value,
                      })
                    }
                  />
                </ScheduledFieldControl>
              </EditorField>
            )}
          </div>
        </div>
      )}

      {showDaylightSavingWarning ? (
        <div className="flex gap-3 rounded-lg border border-[var(--workspace-divider-strong)] bg-[var(--workspace-approval-soft)] p-4 text-[var(--workspace-native-approval)]">
          <TriangleAlert
            aria-hidden
            className="mt-0.5 size-4 shrink-0 text-[var(--workspace-native-approval)]"
          />
          <p className="text-xs leading-5 text-[var(--workspace-native-approval)]">
            This time zone changes clocks seasonally. Runs between 01:00 and
            03:00 may be skipped during spring-forward or occur twice during
            fall-back.
          </p>
        </div>
      ) : null}
    </>
  );
}

function describeTimeZone(timeZone: string) {
  try {
    const offset = new Intl.DateTimeFormat(undefined, {
      timeZone,
      timeZoneName: "shortOffset",
    })
      .formatToParts(new Date())
      .find((part) => part.type === "timeZoneName")?.value;
    return `${timeZone.replaceAll("_", " ")} (${offset ?? "UTC"})`;
  } catch {
    return timeZone;
  }
}

type PresetConfiguration = {
  dayOfMonth: number;
  hour: number;
  hourInterval: number;
  minute: number;
  weekdays: number[];
};

const WEEKDAYS = [
  { label: "Sun", value: 0 },
  { label: "Mon", value: 1 },
  { label: "Tue", value: 2 },
  { label: "Wed", value: 3 },
  { label: "Thu", value: 4 },
  { label: "Fri", value: 5 },
  { label: "Sat", value: 6 },
] as const;

function PresetScheduleEditor({
  configuration,
  onChange,
  presetId,
}: {
  configuration: PresetConfiguration;
  onChange: (next: PresetConfiguration) => void;
  presetId: CronPreset["id"];
}) {
  if (presetId === "hourly") {
    return (
      <div className="grid gap-4 sm:grid-cols-2">
        <EditorField label="Repeat every">
          <ScheduledFieldControl
            icon={<Repeat2 className="size-5" />}
            trailing={<ChevronDown className="size-4" />}
          >
            <select
              aria-label="Hour interval"
              className={`${scheduledNativeControlClassName} cursor-pointer appearance-none`}
              value={configuration.hourInterval}
              onChange={(event) =>
                onChange({
                  ...configuration,
                  hourInterval: Number(event.target.value),
                })
              }
            >
              {[1, 2, 3, 4, 6, 8, 12].map((hours) => (
                <option key={hours} value={hours}>
                  {hours} {hours === 1 ? "hour" : "hours"}
                </option>
              ))}
            </select>
          </ScheduledFieldControl>
        </EditorField>
        <EditorField label="At minute">
          <ScheduledFieldControl icon={<Clock3 className="size-5" />}>
            <input
              aria-label="Minute of the hour"
              className={scheduledNativeControlClassName}
              max={59}
              min={0}
              type="number"
              value={configuration.minute}
              onChange={(event) => {
                const minute = Number(event.target.value);
                if (Number.isInteger(minute) && minute >= 0 && minute <= 59) {
                  onChange({ ...configuration, minute });
                }
              }}
            />
          </ScheduledFieldControl>
        </EditorField>
      </div>
    );
  }

  if (presetId === "weekly") {
    return (
      <div className="grid items-start gap-4 sm:grid-cols-2">
        <div>
          <span className="text-sm font-semibold text-[var(--workspace-native-text)]">
            Run on
          </span>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {WEEKDAYS.map((weekday) => {
              const selected = configuration.weekdays.includes(weekday.value);
              return (
                <button
                  aria-pressed={selected}
                  className={cx(
                    "min-w-11 rounded-md border px-3 py-2 text-xs font-bold transition",
                    selected
                      ? "border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]"
                      : "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]",
                  )}
                  key={weekday.value}
                  type="button"
                  onClick={() => {
                    const weekdays = selected
                      ? configuration.weekdays.filter(
                          (value) => value !== weekday.value,
                        )
                      : [...configuration.weekdays, weekday.value].sort();
                    if (weekdays.length > 0) {
                      onChange({ ...configuration, weekdays });
                    }
                  }}
                >
                  {weekday.label}
                </button>
              );
            })}
          </div>
        </div>
        <ScheduleTimeField configuration={configuration} onChange={onChange} />
      </div>
    );
  }

  if (presetId === "monthly") {
    return (
      <div className="grid gap-4 sm:grid-cols-2">
        <EditorField label="Day of month">
          <ScheduledFieldControl icon={<CalendarDays className="size-5" />}>
            <input
              aria-label="Day of month"
              className={scheduledNativeControlClassName}
              max={31}
              min={1}
              type="number"
              value={configuration.dayOfMonth}
              onChange={(event) => {
                const dayOfMonth = Number(event.target.value);
                if (
                  Number.isInteger(dayOfMonth) &&
                  dayOfMonth >= 1 &&
                  dayOfMonth <= 31
                ) {
                  onChange({ ...configuration, dayOfMonth });
                }
              }}
            />
          </ScheduledFieldControl>
        </EditorField>
        <ScheduleTimeField configuration={configuration} onChange={onChange} />
      </div>
    );
  }

  return (
    <ScheduleTimeField configuration={configuration} onChange={onChange} />
  );
}

function ScheduleTimeField({
  configuration,
  onChange,
}: {
  configuration: PresetConfiguration;
  onChange: (next: PresetConfiguration) => void;
}) {
  return (
    <EditorField label="Run at">
      <ScheduledFieldControl>
        <DateTimeInput
          aria-label="Run time"
          className={scheduledNativeControlClassName}
          type="time"
          value={formatScheduleTime(configuration)}
          onChange={(event) => {
            const [hour, minute] = event.target.value.split(":").map(Number);
            if (
              Number.isInteger(hour) &&
              hour >= 0 &&
              hour <= 23 &&
              Number.isInteger(minute) &&
              minute >= 0 &&
              minute <= 59
            ) {
              onChange({ ...configuration, hour, minute });
            }
          }}
        />
      </ScheduledFieldControl>
    </EditorField>
  );
}

function readPresetConfiguration(expression: string): PresetConfiguration {
  const [minuteField, hourField, dayOfMonthField, , dayOfWeekField] = expression
    .trim()
    .split(/\s+/u);
  const hourIntervalMatch = /^\*\/(\d{1,2})$/u.exec(hourField ?? "");
  const weekdays = /^(?:[0-6])(?:,[0-6])*$/u.test(dayOfWeekField ?? "")
    ? [...new Set(dayOfWeekField.split(",").map(Number))].sort()
    : [1];

  return {
    dayOfMonth: readBoundedNumber(dayOfMonthField, 1, 31, 1),
    hour: readBoundedNumber(hourField, 0, 23, 9),
    hourInterval: readBoundedNumber(hourIntervalMatch?.[1], 1, 23, 1),
    minute: readBoundedNumber(minuteField, 0, 59, 0),
    weekdays,
  };
}

function buildPresetCron(
  presetId: CronPreset["id"],
  configuration: PresetConfiguration,
) {
  const { dayOfMonth, hour, hourInterval, minute, weekdays } = configuration;

  switch (presetId) {
    case "hourly":
      return `${minute} ${hourInterval === 1 ? "*" : `*/${hourInterval}`} * * *`;
    case "daily":
      return `${minute} ${hour} * * *`;
    case "weekdays":
      return `${minute} ${hour} * * 1-5`;
    case "weekly":
      return `${minute} ${hour} * * ${weekdays.join(",")}`;
    case "monthly":
      return `${minute} ${hour} ${dayOfMonth} * *`;
  }
}

function describePresetConfiguration(
  presetId: CronPreset["id"],
  configuration: PresetConfiguration,
) {
  const time = formatScheduleTime(configuration);

  switch (presetId) {
    case "hourly":
      return `${configuration.hourInterval === 1 ? "Every hour" : `Every ${configuration.hourInterval} hours`} · minute ${padNumber(configuration.minute)}`;
    case "daily":
      return `At ${time}`;
    case "weekdays":
      return `Monday–Friday · ${time}`;
    case "weekly":
      return `${configuration.weekdays
        .map(
          (value) => WEEKDAYS.find((weekday) => weekday.value === value)?.label,
        )
        .filter(Boolean)
        .join(", ")} · ${time}`;
    case "monthly":
      return `Day ${configuration.dayOfMonth} · ${time}`;
  }
}

function formatScheduleTime(configuration: PresetConfiguration) {
  return `${padNumber(configuration.hour)}:${padNumber(configuration.minute)}`;
}

function padNumber(value: number) {
  return String(value).padStart(2, "0");
}

function readBoundedNumber(
  value: string | undefined,
  minimum: number,
  maximum: number,
  fallback: number,
) {
  const number = Number(value);
  return Number.isInteger(number) && number >= minimum && number <= maximum
    ? number
    : fallback;
}

function shouldWarnAboutDaylightSaving(values: ScheduleFormValues) {
  const fields = values.cronExpression.trim().split(/\s+/u);
  if (fields.length !== 5 || !cronHourIncludesTransitionWindow(fields[1])) {
    return false;
  }
  return timeZoneHasSeasonalOffset(values.timeZone.trim());
}

function cronHourIncludesTransitionWindow(field: string) {
  const included = new Set<number>();
  for (const item of field.split(",")) {
    const [base, rawStep] = item.split("/");
    const step = rawStep === undefined ? 1 : Number(rawStep);
    if (!Number.isInteger(step) || step < 1) return false;
    const bounds = base === "*" ? [0, 23] : base.split("-").map(Number);
    if (
      bounds.length > 2 ||
      bounds.some(
        (value) => !Number.isInteger(value) || value < 0 || value > 23,
      )
    ) {
      return false;
    }
    const start = bounds[0];
    const end = bounds.length === 1 ? start : bounds[1];
    if (start > end) return false;
    for (let hour = start; hour <= end; hour += step) included.add(hour);
  }
  return [1, 2, 3].some((hour) => included.has(hour));
}

function timeZoneHasSeasonalOffset(timeZone: string) {
  if (!timeZone) return false;
  try {
    const year = new Date().getUTCFullYear();
    return (
      timeZoneOffsetMinutes(new Date(Date.UTC(year, 0, 15, 12)), timeZone) !==
      timeZoneOffsetMinutes(new Date(Date.UTC(year, 6, 15, 12)), timeZone)
    );
  } catch {
    return false;
  }
}

function timeZoneOffsetMinutes(date: Date, timeZone: string) {
  const parts = new Intl.DateTimeFormat("en-US", {
    day: "2-digit",
    hour: "2-digit",
    hourCycle: "h23",
    minute: "2-digit",
    month: "2-digit",
    timeZone,
    year: "numeric",
  }).formatToParts(date);
  const values = Object.fromEntries(
    parts.map((part) => [part.type, part.value]),
  );
  const zonedTime = Date.UTC(
    Number(values.year),
    Number(values.month) - 1,
    Number(values.day),
    Number(values.hour),
    Number(values.minute),
  );
  return Math.round((zonedTime - date.getTime()) / 60_000);
}

function TriggerTypeButton({
  active,
  description,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  description: string;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <ScheduledGlassPanel
      density={active ? "dense" : "regular"}
      className="min-h-24"
      selectionState={active ? "selected" : "unselected"}
      contentClassName={cx(
        "p-0 transition",
        active && "bg-[var(--workspace-accent-soft)]",
      )}
    >
      <button
        aria-pressed={active}
        className={cx(
          "flex h-full w-full items-start gap-3 rounded-[20px] p-4 text-left transition hover:bg-[var(--workspace-hover-bg)]",
          active
            ? "text-[var(--workspace-native-accent)]"
            : "text-[var(--workspace-native-text-muted)] hover:text-[var(--workspace-native-text)]",
        )}
        type="button"
        onClick={onClick}
      >
        <span className="mt-0.5">{icon}</span>
        <span>
          <strong className="block text-sm font-semibold text-[var(--workspace-native-text)]">
            {label}
          </strong>
          <span className="mt-1 block text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            {description}
          </span>
        </span>
      </button>
    </ScheduledGlassPanel>
  );
}

function CustomModeButton({
  active,
  label,
  onClick,
}: {
  active: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      aria-pressed={active}
      className={cx(
        "rounded-lg px-3 py-1.5 text-xs font-bold transition",
        active
          ? "bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]"
          : "text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]",
      )}
      type="button"
      onClick={onClick}
    >
      {label}
    </button>
  );
}
