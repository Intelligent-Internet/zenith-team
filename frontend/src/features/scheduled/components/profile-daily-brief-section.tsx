import { CalendarClock, ChevronRight, RefreshCcw } from "@/shared/ui/icons";
import { Link } from "react-router";

import {
  useListScheduleRunsQuery,
  useListScheduleSourcesQuery,
  useListSchedulesQuery,
} from "@/features/scheduled/api/scheduled-api";
import {
  formatScheduleDateTime,
  formatScheduleTrigger,
  scheduleRunLabel,
  scheduleStatusLabel,
  scheduleStatusTone,
} from "@/features/scheduled/model/schedule-format";
import {
  DAILY_BRIEF_PRESET_KEY,
  type ScheduledTask,
} from "@/shared/api/schedule.service";
import { StatusPill } from "@/shared/ui/status-pill";
import { useCan } from "@/shared/permissions";

export function ProfileDailyBriefSection({ companyId }: { companyId: string }) {
  // Schedules are creator-private and need schedules.manage; a member has
  // no daily brief to show and must not be sent to a route that refuses them.
  const canManageSchedules = useCan("schedules.manage");
  const schedulesQuery = useListSchedulesQuery(
    {
      companyId,
      limit: 1,
      preset_key: DAILY_BRIEF_PRESET_KEY,
      exclude_terminal: true,
    },
    { refetchOnFocus: true, skip: !companyId || !canManageSchedules },
  );
  const dailyBrief = schedulesQuery.currentData?.schedules[0];
  const runsQuery = useListScheduleRunsQuery(
    { companyId, limit: 1, scheduleId: dailyBrief?.id ?? "" },
    {
      refetchOnFocus: true,
      skip: !companyId || !dailyBrief,
    },
  );
  const sourcesQuery = useListScheduleSourcesQuery(
    { companyId, provider: dailyBrief?.provider ?? "codex" },
    { skip: !companyId || !dailyBrief },
  );
  const latestRun = runsQuery.currentData?.runs[0];
  const sourceNames = new Map(
    (sourcesQuery.currentData?.connectors ?? []).map((connector) => [
      connector.id,
      connector.name,
    ]),
  );
  if (!canManageSchedules) return null;

  return (
    <section
      className="my-account-daily [border-top:1px_solid_var(--workspace-divider)]"
      aria-labelledby="daily-brief-title"
    >
      <div className="my-account-setting-row">
        <div>
          <h3 id="daily-brief-title">Daily Brief</h3>
          <p>A morning summary from your connected sources.</p>
        </div>
        {dailyBrief ? (
          <Link
            className="my-account-secondary-link"
            to={`/scheduled/${encodeURIComponent(dailyBrief.id)}`}
          >
            Configure
            <ChevronRight aria-hidden size={14} />
          </Link>
        ) : !schedulesQuery.isLoading && !schedulesQuery.isError ? (
          <Link
            className="my-account-secondary-link"
            to="/scheduled/new?preset=daily-brief"
            aria-label="Set up Daily Brief"
          >
            Set up
          </Link>
        ) : null}
      </div>
      {schedulesQuery.isLoading ? (
        <p className="my-account-feedback" role="status">
          Loading Daily Brief…
        </p>
      ) : schedulesQuery.isError ? (
        <p className="my-account-feedback is-error" role="alert">
          Daily Brief is unavailable.
          <button
            className="my-account-text-action"
            type="button"
            onClick={() => void schedulesQuery.refetch()}
          >
            <RefreshCcw aria-hidden size={14} />
            Retry
          </button>
        </p>
      ) : dailyBrief ? (
        <DailyBriefStateCard
          isLatestRunError={runsQuery.isError}
          isLatestRunLoading={runsQuery.isLoading}
          latestOutput={latestRun?.output}
          latestRunLabel={latestRun ? scheduleRunLabel(latestRun) : undefined}
          schedule={dailyBrief}
          sourceNames={sourceNames}
        />
      ) : null}
    </section>
  );
}

function DailyBriefStateCard({
  isLatestRunError,
  isLatestRunLoading,
  latestOutput,
  latestRunLabel,
  schedule,
  sourceNames,
}: {
  isLatestRunError: boolean;
  isLatestRunLoading: boolean;
  latestOutput?: string | null;
  latestRunLabel?: string;
  schedule: ScheduledTask;
  sourceNames: Map<string, string>;
}) {
  const timeZone =
    schedule.trigger.type === "cron" ? schedule.trigger.timeZone : undefined;
  const statusTone = scheduleStatusTone(schedule.status);
  const statusColors =
    statusTone === "success"
      ? "!bg-[var(--workspace-success-soft,#183e29)] !text-[var(--workspace-native-success,#91eea3)]"
      : statusTone === "paused"
        ? "!bg-[var(--workspace-approval-soft,#483519)] !text-[var(--workspace-native-approval,#ffe6ad)]"
        : statusTone === "draft"
          ? "!bg-[var(--workspace-info-soft,#233751)] !text-[var(--workspace-native-info,#bdd9fa)]"
          : "!bg-[var(--workspace-control-bg,#263542)] !text-[var(--workspace-native-text-muted,#bdc7d1)]";

  return (
    <div className="profile-daily-state mt-4 min-w-0 rounded-md border border-[var(--workspace-divider,#344350)] bg-[var(--workspace-panel-bg,#14212c)] p-5 text-[var(--workspace-native-text,#eef2f5)]">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <span className="grid size-10 shrink-0 place-items-center rounded-md bg-[var(--workspace-info-soft,#233751)] text-[var(--workspace-native-info,#bdd9fa)]">
            <CalendarClock aria-hidden className="size-4.5" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h4 className="min-w-0 break-words text-sm font-bold text-[var(--workspace-native-text,#eef2f5)]">
                {schedule.name}
              </h4>
              <StatusPill
                className={`profile-daily-state__status !border-transparent !shadow-none ${statusColors}`}
                tone={statusTone}
              >
                {scheduleStatusLabel(schedule.status)}
              </StatusPill>
            </div>
            <p className="mt-1 break-words text-xs font-semibold text-[var(--workspace-native-text-muted,#bdc7d1)]">
              {formatScheduleTrigger(schedule.trigger)}
            </p>
          </div>
        </div>
        <div className="min-w-0 text-right max-[479px]:w-full max-[479px]:text-left">
          <span className="block text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--workspace-native-text-muted,#bdc7d1)]">
            Next run
          </span>
          <strong className="mt-1 block break-words text-xs text-[var(--workspace-native-text,#eef2f5)]">
            {formatScheduleDateTime(schedule.nextRunAt, timeZone)}
          </strong>
        </div>
      </div>

      <div className="mt-4 flex flex-wrap gap-2">
        {schedule.sources.length > 0 ? (
          schedule.sources.map((source) => (
            <span
              className="max-w-full break-words rounded-lg border border-[var(--workspace-divider,#344350)] bg-[var(--workspace-control-bg,#263542)] px-2.5 py-1.5 text-xs font-medium text-[var(--workspace-native-text-muted,#bdc7d1)]"
              key={source.id}
            >
              {sourceNames.get(source.id) ?? source.name}
            </span>
          ))
        ) : (
          <span className="text-xs text-[var(--workspace-native-text-muted,#bdc7d1)]">
            No sources selected
          </span>
        )}
      </div>

      {isLatestRunLoading ? (
        <div className="mt-4 h-20 animate-pulse rounded-md border border-[var(--workspace-divider,#344350)] bg-[var(--workspace-control-bg,#263542)]" />
      ) : isLatestRunError ? (
        <p
          className="mt-4 rounded-md border border-[var(--workspace-native-danger,#ffb4b4)] bg-[var(--workspace-danger-soft,#42252d)] p-3 text-xs leading-5 !text-[var(--workspace-native-danger,#ffb4b4)]"
          role="alert"
        >
          Latest run status is unavailable. Open the schedule to retry.
        </p>
      ) : latestOutput ? (
        <div className="mt-4 rounded-md border border-[var(--workspace-divider,#344350)] bg-[var(--workspace-content-bg,#101a24)] p-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--workspace-native-text-muted,#bdc7d1)]">
              Latest result
            </span>
            {latestRunLabel ? (
              <span className="text-[11px] font-medium capitalize text-[var(--workspace-native-text-muted,#bdc7d1)]">
                {latestRunLabel}
              </span>
            ) : null}
          </div>
          <p className="mt-2 line-clamp-5 whitespace-pre-wrap break-words text-sm leading-6 !text-[var(--workspace-native-text,#eef2f5)]">
            {latestOutput}
          </p>
        </div>
      ) : (
        <p className="mt-4 rounded-md border border-dashed border-[var(--workspace-divider,#344350)] bg-[var(--workspace-content-bg,#101a24)] p-3 text-xs leading-5 text-[var(--workspace-native-text-muted,#bdc7d1)]">
          {latestRunLabel
            ? `Latest occurrence: ${latestRunLabel}`
            : "The first briefing will appear here after a successful run."}
        </p>
      )}
    </div>
  );
}
