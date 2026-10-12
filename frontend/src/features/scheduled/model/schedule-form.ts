import type { ScheduleSeed } from "@/features/scheduled/model/schedule-seed";
import {
  DAILY_BRIEF_PRESET_KEY,
  type ScheduleDraft,
  type ScheduledTask,
} from "@/shared/api/schedule.service";

export type ScheduleTriggerType = "cron" | "once";

export type ScheduleFormValues = {
  agentId: string;
  cronExpression: string;
  instructions: string;
  name: string;
  presetKey?: string;
  runAtLocal: string;
  sourceIds: string[];
  timeZone: string;
  triggerType: ScheduleTriggerType;
};

export type CronPreset = {
  description: string;
  expression: string;
  id: "daily" | "hourly" | "monthly" | "weekdays" | "weekly";
  label: string;
};

export const CRON_PRESETS: readonly CronPreset[] = [
  {
    description: "Choose interval and minute",
    expression: "0 * * * *",
    id: "hourly",
    label: "Every hour",
  },
  {
    description: "Choose a time",
    expression: "0 8 * * *",
    id: "daily",
    label: "Every day",
  },
  {
    description: "Monday–Friday · choose time",
    expression: "0 9 * * 1-5",
    id: "weekdays",
    label: "Weekdays",
  },
  {
    description: "Choose days and time",
    expression: "0 9 * * 1",
    id: "weekly",
    label: "Weekly",
  },
  {
    description: "Choose day and time",
    expression: "0 9 1 * *",
    id: "monthly",
    label: "Every month",
  },
] as const;

export const COMMON_TIME_ZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Paris",
  "Asia/Dubai",
  "Asia/Ho_Chi_Minh",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
] as const;

const DAILY_BRIEF_INSTRUCTIONS = `Review the selected sources for information added since the previous briefing and create a concise Markdown briefing.

Include only sections that have useful information:
- Priorities requiring attention
- Today's calendar and deadlines
- Important messages and mentions
- Meaningful KPI changes
- Blockers and risks
- Suggested next actions

Name the source behind each important point and clearly identify any source that could not be read. Read and summarize only; do not send messages, modify records, or take external actions.`;

export function detectScheduleTimeZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function createInitialScheduleForm({
  agentId = "",
  dailyBrief = false,
  timeZone = detectScheduleTimeZone(),
}: {
  agentId?: string;
  dailyBrief?: boolean;
  timeZone?: string;
} = {}): ScheduleFormValues {
  return {
    agentId,
    cronExpression: dailyBrief ? "0 8 * * *" : "0 9 * * *",
    instructions: dailyBrief ? DAILY_BRIEF_INSTRUCTIONS : "",
    name: dailyBrief ? "Daily Brief" : "",
    presetKey: dailyBrief ? DAILY_BRIEF_PRESET_KEY : undefined,
    runAtLocal: createDefaultRunAtLocal(),
    sourceIds: [],
    timeZone,
    triggerType: "cron",
  };
}

// A seed prefills a new cron Schedule. The Agent is chosen once the eligible
// Agents load, and it never carries a preset, so one Workflow may be scheduled
// more than once. Its preselected sources are kept only while the seeded
// Agent is the one chosen; the editor clears them otherwise.
export function createScheduleFormFromSeed(
  seed: ScheduleSeed,
  timeZone?: string,
): ScheduleFormValues {
  return {
    ...createInitialScheduleForm({ timeZone }),
    cronExpression: seed.cronExpression,
    instructions: seed.instructions,
    name: seed.name,
    presetKey: undefined,
    sourceIds: [...new Set(seed.sourceIds ?? [])],
    triggerType: "cron",
  };
}

export function createScheduleFormFromTask(
  schedule: ScheduledTask,
): ScheduleFormValues {
  const trigger = schedule.trigger;

  return {
    agentId: schedule.agentId,
    cronExpression: trigger.type === "cron" ? trigger.expression : "0 9 * * *",
    instructions: schedule.instructions,
    name: schedule.name,
    presetKey: schedule.presetKey ?? undefined,
    runAtLocal:
      trigger.type === "cron"
        ? createDefaultRunAtLocal()
        : formatLocalDateTimeInput(trigger.runAt),
    sourceIds: schedule.sources.map((source) => source.id),
    timeZone:
      trigger.type === "cron" ? trigger.timeZone : detectScheduleTimeZone(),
    triggerType: trigger.type,
  };
}

export function createScheduleDraft(values: ScheduleFormValues): ScheduleDraft {
  return {
    agentId: values.agentId,
    instructions: values.instructions.trim(),
    name: values.name.trim(),
    ...(values.presetKey ? { presetKey: values.presetKey } : {}),
    schemaVersion: 1,
    sourceIds: [...values.sourceIds].sort(),
    trigger:
      values.triggerType === "cron"
        ? {
            expression: values.cronExpression.trim(),
            timeZone: values.timeZone.trim(),
            type: "cron",
          }
        : {
            runAt: new Date(values.runAtLocal).toISOString(),
            type: "once",
          },
  };
}

export function getScheduleStepError(
  step: 1 | 2 | 3,
  values: ScheduleFormValues,
) {
  if (step === 1) {
    if (!values.name.trim()) return "Give this scheduled task a name.";
    if (!values.instructions.trim()) {
      return "Describe what the agent should do on every run.";
    }
    if (!values.agentId) return "Choose the agent that will run this task.";
    return null;
  }

  if (step === 2) {
    if (values.triggerType === "cron") {
      const cronError = getCronExpressionError(values.cronExpression);
      if (cronError) return cronError;
      if (!values.timeZone.trim() || values.timeZone === "Local") {
        return "Choose an IANA time zone, such as Asia/Ho_Chi_Minh.";
      }
      try {
        new Intl.DateTimeFormat(undefined, {
          timeZone: values.timeZone.trim(),
        });
      } catch {
        return "Choose a valid IANA time zone, such as Asia/Ho_Chi_Minh.";
      }
      return null;
    }

    const runAt = new Date(values.runAtLocal);
    if (!values.runAtLocal || Number.isNaN(runAt.getTime())) {
      return "Choose a valid future date and time.";
    }
    if (runAt.getTime() <= Date.now()) {
      return "One-time tasks must be scheduled in the future.";
    }
    return null;
  }

  if (
    values.presetKey === DAILY_BRIEF_PRESET_KEY &&
    values.sourceIds.length === 0
  ) {
    return "Select at least one connected source for the Daily Brief.";
  }

  return null;
}

function getCronExpressionError(expression: string) {
  const fields = expression.trim().split(/\s+/u);
  if (!expression.trim()) return "Enter a recurring cron expression.";
  if (fields.length !== 5 || expression.trim().length > 256) {
    return "Use five cron fields: minute, hour, day, month, weekday. For example: 0 8 * * *.";
  }

  const bounds = [
    ["Minute", 0, 59],
    ["Hour", 0, 23],
    ["Day of month", 1, 31],
    ["Month", 1, 12],
    ["Weekday", 0, 6],
  ] as const;
  const selections: number[][] = [];
  for (const [index, [label, minimum, maximum]] of bounds.entries()) {
    const selection = readCronField(fields[index], minimum, maximum);
    if (!selection) {
      return `${label} must use ${minimum}–${maximum}, *, lists, ranges, or a positive step (for example */5).`;
    }
    selections.push(selection);
  }

  const [minutes, hours, days, months] = selections;
  const times = hours.flatMap((hour) =>
    minutes.map((minute) => hour * 60 + minute),
  );
  if (times.some((time, index) => index > 0 && time - times[index - 1] < 5)) {
    return "Runs must be at least five minutes apart. Use */5 * * * * for every five minutes.";
  }
  // A restricted weekday uses cron's OR rule with the day of month.
  if (fields[4].split(",").some((part) => /^\*(?:\/0*1)?$/u.test(part))) {
    const hasCalendarDate = months.some((month) => {
      const lastDay = new Date(Date.UTC(2000, month, 0)).getUTCDate();
      return days.some((day) => day <= lastDay);
    });
    if (!hasCalendarDate)
      return "This cron expression never runs. Choose a day that exists in the selected month.";
  }
  return null;
}

function readCronField(field: string, minimum: number, maximum: number) {
  const values = new Set<number>();
  for (const part of field.split(",")) {
    const match = /^(\*|\d+(?:-\d+)?)(?:\/(\d+))?$/u.exec(part);
    if (!match) return null;
    const [, range, stepText] = match;
    const step = stepText === undefined ? 1 : Number(stepText);
    const [startText, endText] = range.split("-");
    const start = range === "*" ? minimum : Number(startText);
    const end =
      range === "*" || (stepText !== undefined && endText === undefined)
        ? maximum
        : Number(endText ?? startText);
    if (
      !Number.isSafeInteger(step) ||
      step < 1 ||
      start < minimum ||
      end > maximum ||
      start > end
    ) {
      return null;
    }
    for (let value = start; value <= end; value += step) values.add(value);
  }
  return [...values].sort((left, right) => left - right);
}

export function getCronPresetId(expression: string) {
  const fields = expression.trim().split(/\s+/u);
  if (fields.length !== 5) return undefined;

  const [minute, hour, dayOfMonth, month, dayOfWeek] = fields;
  if (month !== "*" || !isCronNumber(minute, 0, 59)) return undefined;

  if (
    dayOfMonth === "*" &&
    dayOfWeek === "*" &&
    (hour === "*" || isCronStep(hour, 1, 23))
  ) {
    return "hourly";
  }

  if (!isCronNumber(hour, 0, 23)) return undefined;
  if (dayOfMonth === "*" && dayOfWeek === "1-5") return "weekdays";
  if (dayOfMonth === "*" && /^(?:[0-6])(?:,[0-6])*$/u.test(dayOfWeek)) {
    return "weekly";
  }
  if (isCronNumber(dayOfMonth, 1, 31) && dayOfWeek === "*") {
    return "monthly";
  }
  if (dayOfMonth === "*" && dayOfWeek === "*") return "daily";
  return undefined;
}

function isCronNumber(value: string, minimum: number, maximum: number) {
  const number = Number(value);
  return Number.isInteger(number) && number >= minimum && number <= maximum;
}

function isCronStep(value: string, minimum: number, maximum: number) {
  const match = /^\*\/(\d{1,2})$/u.exec(value);
  return match ? isCronNumber(match[1], minimum, maximum) : false;
}

function createDefaultRunAtLocal() {
  const date = new Date();
  date.setDate(date.getDate() + 1);
  date.setHours(9, 0, 0, 0);
  return formatLocalDateTimeInput(date.toISOString());
}

function formatLocalDateTimeInput(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";

  const pad = (part: number) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate(),
  )}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
