import {
  ChevronRight,
  Clock3,
  Plus,
  RefreshCcw,
  Search,
  Sparkles,
} from "@/shared/ui/icons";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";

import { readSessionSnapshot } from "@/app/session/session";
import {
  useLazyListSchedulesQuery,
  useListSchedulesQuery,
} from "@/features/scheduled/api/scheduled-api";
import {
  ScheduledFieldControl,
  scheduledNativeControlClassName,
} from "@/features/scheduled/components/scheduled-field-control";
import { ScheduledGlassPanel } from "@/features/scheduled/components/scheduled-glass-panel";
import { ScheduledPageShell } from "@/features/scheduled/components/scheduled-page-shell";
import {
  formatScheduleDateTime,
  formatScheduleTrigger,
  scheduleStatusLabel,
  scheduleStatusTone,
} from "@/features/scheduled/model/schedule-format";
import type {
  ScheduledTask,
  ScheduledTaskStatus,
} from "@/shared/api/schedule.service";
import { cx } from "@/shared/lib/cx";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";
import { ScheduledStatusPill } from "@/features/scheduled/components/scheduled-status-pill";

type StatusFilter = "all" | ScheduledTaskStatus;

const statusFilters: Array<{ label: string; value: StatusFilter }> = [
  { label: "All", value: "all" },
  { label: "Active", value: "active" },
  { label: "Paused", value: "paused" },
  { label: "Completed", value: "completed" },
  { label: "Cancelled", value: "cancelled" },
];

export function ScheduledTasksPage() {
  const navigate = useNavigate();
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [schedules, setSchedules] = useState<ScheduledTask[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const hasLoadedAdditionalPages = useRef(false);
  const schedulesQuery = useListSchedulesQuery(
    { companyId, limit: 100 },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
      skip: !companyId,
    },
  );
  const [loadSchedulePage] = useLazyListSchedulesQuery();

  useEffect(() => {
    hasLoadedAdditionalPages.current = false;
    setSchedules([]);
    setNextCursor(null);
    setPageError(null);
  }, [companyId]);

  useEffect(() => {
    const firstPage = schedulesQuery.currentData;
    if (!firstPage) return;
    if (hasLoadedAdditionalPages.current) {
      setSchedules((current) =>
        mergeRefreshedSchedules(current, firstPage.schedules),
      );
    } else {
      setSchedules(firstPage.schedules);
      setNextCursor(firstPage.nextCursor);
    }
    setPageError(null);
  }, [schedulesQuery.currentData]);

  const loadMore = async () => {
    if (!nextCursor || loadingMore) return;
    setLoadingMore(true);
    setPageError(null);
    try {
      const page = await loadSchedulePage({
        companyId,
        cursor: nextCursor,
        limit: 100,
      }).unwrap();
      hasLoadedAdditionalPages.current = true;
      setSchedules((current) => mergeSchedules(current, page.schedules));
      setNextCursor(page.nextCursor);
    } catch {
      setPageError("More schedules could not be loaded.");
    } finally {
      setLoadingMore(false);
    }
  };
  const visibleSchedules = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    return schedules.filter((schedule) => {
      const matchesStatus = status === "all" || schedule.status === status;
      const matchesQuery =
        !normalizedQuery ||
        `${schedule.name} ${schedule.instructions} ${schedule.provider}`
          .toLocaleLowerCase()
          .includes(normalizedQuery);
      return matchesStatus && matchesQuery;
    });
  }, [query, schedules, status]);

  return (
    <ScheduledPageShell
      description="Save an agent instruction once, then run it at any future time or recurring cron schedule."
      actions={
        <GradientCtaButton
          className="scheduled-primary-button"
          hideIcon
          size="auto"
          type="button"
          onClick={() => navigate("/scheduled/new")}
        >
          <Plus aria-hidden className="size-4" />
          New schedule
        </GradientCtaButton>
      }
    >
      <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
        <ScheduledGlassPanel contentClassName="p-4">
          <div className="flex flex-wrap items-center gap-3">
            <label className="min-w-0 flex-1 basis-56">
              <span className="sr-only">Search schedules</span>
              <ScheduledFieldControl
                compact
                icon={<Search className="size-4" />}
              >
                <input
                  aria-label="Search schedules"
                  className={scheduledNativeControlClassName}
                  placeholder="Search schedules"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </ScheduledFieldControl>
            </label>
            <button
              aria-label="Refresh schedules"
              className="scheduled-toolbar-refresh inline-flex shrink-0 items-center justify-center gap-2 border border-[var(--workspace-divider-strong)] bg-[var(--workspace-panel-bg)] px-3 text-[var(--workspace-native-text)] transition-colors hover:bg-[var(--workspace-hover-bg)] focus-visible:bg-[var(--workspace-hover-bg)]"
              title="Refresh schedules"
              type="button"
              onClick={() => void schedulesQuery.refetch()}
            >
              <RefreshCcw
                aria-hidden
                className={cx(
                  "size-[18px] shrink-0",
                  schedulesQuery.isFetching && "animate-spin",
                )}
              />
              Refresh
            </button>
          </div>
          <div
            className="mt-3 flex flex-wrap gap-1.5"
            role="group"
            aria-label="Schedule status"
          >
            {statusFilters.map((filter) => (
              <button
                aria-pressed={filter.value === status}
                className={cx(
                  "rounded-lg px-3 py-1.5 text-xs font-bold transition",
                  filter.value === status
                    ? "bg-[var(--workspace-selected-bg)] text-[var(--workspace-native-accent)]"
                    : "text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-control-bg)] hover:text-[var(--workspace-native-text-muted)]",
                )}
                key={filter.value}
                type="button"
                onClick={() => setStatus(filter.value)}
              >
                {filter.label}
              </button>
            ))}
          </div>
        </ScheduledGlassPanel>

        <Link
          className="group min-h-28 rounded-lg outline-none"
          to="/scheduled/new?preset=daily-brief"
        >
          <ScheduledGlassPanel
            density="dense"
            direction="row"
            className="h-full"
            contentClassName="items-center gap-4 bg-[var(--workspace-accent-soft)] p-4 transition group-hover:bg-[var(--workspace-control-bg)]"
          >
            <span className="grid size-11 shrink-0 place-items-center rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-selected-bg)] text-[var(--workspace-native-accent)]">
              <Sparkles aria-hidden className="size-5" />
            </span>
            <span className="min-w-0 flex-1">
              <strong className="block text-sm font-semibold text-[var(--workspace-native-text)]">
                Start with Daily Brief
              </strong>
              <span className="mt-1 block text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                Every morning at 8:00 with your selected sources.
              </span>
            </span>
            <ChevronRight
              aria-hidden
              className="size-4 text-[var(--workspace-native-text-muted)] transition group-hover:translate-x-0.5 group-hover:text-[var(--workspace-native-text)]"
            />
          </ScheduledGlassPanel>
        </Link>
      </section>

      <ScheduleListState
        isError={schedulesQuery.isError}
        isLoading={schedulesQuery.isLoading}
        schedules={visibleSchedules}
        hasFilters={Boolean(query.trim()) || status !== "all"}
        onClearFilters={() => {
          setQuery("");
          setStatus("all");
        }}
        onRetry={() => void schedulesQuery.refetch()}
      />
      {pageError ? (
        <p
          className="rounded-md border border-[var(--workspace-native-danger)] bg-[var(--workspace-danger-soft)] px-4 py-3 text-sm text-[var(--workspace-native-danger)]"
          role="alert"
        >
          {pageError}
        </p>
      ) : null}
      {nextCursor ? (
        <button
          className="mx-auto inline-flex h-10 items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-5 text-sm font-bold text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-control-bg)] hover:text-[var(--workspace-native-text)] disabled:opacity-45"
          disabled={loadingMore}
          type="button"
          onClick={() => void loadMore()}
        >
          <RefreshCcw
            aria-hidden
            className={cx("size-4", loadingMore && "animate-spin")}
          />
          Load more
        </button>
      ) : null}
    </ScheduledPageShell>
  );
}

function ScheduleListState({
  isError,
  isLoading,
  onRetry,
  schedules,
  hasFilters,
  onClearFilters,
}: {
  isError: boolean;
  isLoading: boolean;
  onRetry: () => void;
  schedules: ScheduledTask[];
  hasFilters: boolean;
  onClearFilters: () => void;
}) {
  if (isLoading) {
    return (
      <div className="grid gap-3" aria-label="Loading schedules">
        {[0, 1, 2].map((index) => (
          <div
            className="h-32 animate-pulse rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)]"
            key={index}
          />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <ScheduledGlassPanel
        density="dense"
        contentClassName="grid min-h-56 place-items-center bg-[var(--workspace-danger-soft)] p-8 text-center ring-1 ring-inset ring-[var(--workspace-native-danger)]"
      >
        <div>
          <h2 className="text-lg font-semibold text-[var(--workspace-native-text)]">
            Schedules are unavailable
          </h2>
          <p className="mt-2 text-sm text-[var(--workspace-native-text-muted)]">
            Zenith Team could not load the current schedule state.
          </p>
          <button
            className="mt-5 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 py-2 text-sm font-bold text-[var(--workspace-native-text)] transition hover:bg-[var(--workspace-control-bg)]"
            type="button"
            onClick={onRetry}
          >
            Try again
          </button>
        </div>
      </ScheduledGlassPanel>
    );
  }

  if (schedules.length === 0) {
    return (
      <ScheduledGlassPanel contentClassName="grid min-h-64 place-items-center p-8 text-center">
        <div className="max-w-md">
          <span className="mx-auto grid size-12 place-items-center rounded-lg bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]">
            <Clock3 aria-hidden className="size-5" />
          </span>
          <h2 className="mt-4 text-lg font-semibold text-[var(--workspace-native-text)]">
            {hasFilters ? "No schedules match this view" : "No schedules yet"}
          </h2>
          <p className="mt-2 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
            {hasFilters
              ? "Change the search or status filter to see another schedule."
              : "Create a one-time schedule, use a familiar preset, or enter any five-field cron expression."}
          </p>
          {hasFilters ? (
            <button
              className="mt-5 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 py-2 text-sm font-medium text-[var(--workspace-native-text)]"
              type="button"
              onClick={onClearFilters}
            >
              Clear filters
            </button>
          ) : (
            <Link
              className="mt-5 inline-flex items-center gap-2 rounded-md bg-[var(--workspace-control-bg)] px-4 py-2 text-sm font-bold text-[var(--workspace-native-text)] transition hover:bg-[var(--workspace-control-bg)]"
              to="/scheduled/new"
            >
              <Plus aria-hidden className="size-4" />
              Create schedule
            </Link>
          )}
        </div>
      </ScheduledGlassPanel>
    );
  }

  return (
    <div className="grid gap-3">
      {schedules.map((schedule) => (
        <ScheduleListItem key={schedule.id} schedule={schedule} />
      ))}
    </div>
  );
}

function ScheduleListItem({ schedule }: { schedule: ScheduledTask }) {
  const timeZone =
    schedule.trigger.type === "cron" ? schedule.trigger.timeZone : undefined;

  return (
    <Link
      className="group rounded-lg outline-none"
      to={`/scheduled/${encodeURIComponent(schedule.id)}`}
    >
      <ScheduledGlassPanel contentClassName="grid gap-5 p-5 transition group-hover:bg-[var(--workspace-control-bg)] md:grid-cols-[minmax(0,1.4fr)_minmax(220px,0.8fr)_auto] md:items-center">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="truncate text-base font-semibold text-[var(--workspace-native-text)]">
              {schedule.name}
            </h2>
            <ScheduledStatusPill tone={scheduleStatusTone(schedule.status)}>
              {scheduleStatusLabel(schedule.status)}
            </ScheduledStatusPill>
          </div>
          <p className="mt-2 line-clamp-2 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
            {schedule.instructions}
          </p>
          <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-[var(--workspace-native-text-muted)]">
            <span className="rounded-full border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-2.5 py-1 capitalize">
              {schedule.provider}
            </span>
            <span>
              {schedule.sources.length === 0
                ? "No external sources"
                : `${schedule.sources.length} source${schedule.sources.length === 1 ? "" : "s"}`}
            </span>
          </div>
        </div>
        <div>
          <p className="text-xs font-bold uppercase tracking-[0.12em] text-[var(--workspace-native-text-muted)]">
            Schedule
          </p>
          <p className="mt-1.5 text-sm font-semibold text-[var(--workspace-native-text)]">
            {formatScheduleTrigger(schedule.trigger)}
          </p>
          <p className="mt-2 text-xs text-[var(--workspace-native-text-muted)]">
            Next · {formatScheduleDateTime(schedule.nextRunAt, timeZone)}
          </p>
        </div>
        <ChevronRight
          aria-hidden
          className="hidden size-5 text-[var(--workspace-native-text-muted)] transition group-hover:translate-x-0.5 group-hover:text-[var(--workspace-native-text)] md:block"
        />
      </ScheduledGlassPanel>
    </Link>
  );
}

function mergeSchedules(current: ScheduledTask[], incoming: ScheduledTask[]) {
  const schedulesById = new Map(
    current.map((schedule) => [schedule.id, schedule]),
  );
  incoming.forEach((schedule) => schedulesById.set(schedule.id, schedule));
  return Array.from(schedulesById.values());
}

function mergeRefreshedSchedules(
  current: ScheduledTask[],
  refreshed: ScheduledTask[],
) {
  const refreshedScheduleIds = new Set(
    refreshed.map((schedule) => schedule.id),
  );
  return [
    ...refreshed,
    ...current.filter((schedule) => !refreshedScheduleIds.has(schedule.id)),
  ];
}

export const Component = ScheduledTasksPage;
