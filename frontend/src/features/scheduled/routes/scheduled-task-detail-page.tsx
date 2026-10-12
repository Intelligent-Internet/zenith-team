import {
  CalendarClock,
  CirclePause,
  History,
  LoaderCircle,
  MessageSquareText,
  Pencil,
  Play,
  RefreshCcw,
  RotateCcw,
  ShieldCheck,
  X,
} from "@/shared/ui/icons";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";

import { readSessionSnapshot } from "@/app/session/session";
import {
  useCancelScheduleMutation,
  useGetScheduleQuery,
  useLazyListScheduleRunsQuery,
  useListScheduleRunsQuery,
  useListScheduleSourcesQuery,
  usePauseScheduleMutation,
  usePreviewScheduleMutation,
  useResumeScheduleMutation,
  useRunScheduleNowMutation,
} from "@/features/scheduled/api/scheduled-api";
import { ScheduledGlassPanel } from "@/features/scheduled/components/scheduled-glass-panel";
import { ScheduledPageShell } from "@/features/scheduled/components/scheduled-page-shell";
import { ScheduledStatusPill } from "@/features/scheduled/components/scheduled-status-pill";
import { getScheduledTaskErrorMessage } from "@/features/scheduled/model/schedule-errors";
import { useScheduleRunChatPanel } from "@/features/scheduled/hooks/use-schedule-run-chat-panel";
import { resolveScheduleRunChatTarget } from "@/features/scheduled/model/schedule-run-chat";
import {
  formatScheduleDateTime,
  formatScheduleTrigger,
  scheduleRunLabel,
  scheduleRunTone,
  scheduleStatusLabel,
  scheduleStatusTone,
} from "@/features/scheduled/model/schedule-format";
import type {
  SchedulePreviewRequest,
  ScheduleRunRecord,
  ScheduleRunNowRequest,
  ScheduledTask,
} from "@/shared/api/schedule.service";
import { cx } from "@/shared/lib/cx";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";

export function ScheduledTaskDetailPage() {
  const { scheduleId = "" } = useParams<{ scheduleId: string }>();
  const companyId = readSessionSnapshot().company?.id ?? "";
  const scheduleQuery = useGetScheduleQuery(
    { companyId, scheduleId },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
      skip: !companyId || !scheduleId,
    },
  );
  const runsQuery = useListScheduleRunsQuery(
    { companyId, limit: 25, scheduleId },
    {
      refetchOnFocus: true,
      skip: !companyId || !scheduleId,
    },
  );
  const [loadRunPage] = useLazyListScheduleRunsQuery();
  const sourcesQuery = useListScheduleSourcesQuery(
    {
      companyId,
      provider: scheduleQuery.currentData?.provider ?? "codex",
    },
    { skip: !companyId || !scheduleId || !scheduleQuery.currentData },
  );
  const [pauseSchedule, pauseState] = usePauseScheduleMutation();
  const [resumeSchedule, resumeState] = useResumeScheduleMutation();
  const [cancelSchedule, cancelState] = useCancelScheduleMutation();
  const [runNow, runNowState] = useRunScheduleNowMutation();
  const [previewSchedule, previewState] = usePreviewScheduleMutation();
  const [runs, setRuns] = useState<ScheduleRunRecord[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const hasLoadedAdditionalRunPages = useRef(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionNotice, setActionNotice] = useState<string | null>(null);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const pendingRunNow = useRef<{
    request: ScheduleRunNowRequest;
    scheduleId: string;
  } | null>(null);
  const schedule = scheduleQuery.currentData;
  const { chatPanel, openRunChat } = useScheduleRunChatPanel({
    companyId,
    runs,
    schedule,
  });
  const isActing =
    pauseState.isLoading ||
    resumeState.isLoading ||
    cancelState.isLoading ||
    runNowState.isLoading ||
    previewState.isLoading;

  useEffect(() => {
    hasLoadedAdditionalRunPages.current = false;
    setRuns([]);
    setNextCursor(null);
  }, [companyId, scheduleId]);

  useEffect(() => {
    const firstPage = runsQuery.currentData;
    if (!firstPage) return;
    if (hasLoadedAdditionalRunPages.current) {
      setRuns((current) => mergeRefreshedScheduleRuns(current, firstPage.runs));
      return;
    }
    setRuns(firstPage.runs);
    setNextCursor(firstPage.nextCursor);
  }, [runsQuery.currentData]);

  const executeAction = async (
    operation: "cancel" | "pause" | "resume" | "run",
  ) => {
    if (!schedule) return;
    setActionError(null);
    setActionNotice(null);
    let attemptedRunNow: ScheduleRunNowRequest | null = null;

    try {
      if (operation === "run") {
        const request =
          pendingRunNow.current?.scheduleId === scheduleId
            ? pendingRunNow.current.request
            : createRunNowRequest();
        attemptedRunNow = request;
        pendingRunNow.current = { request, scheduleId };
        await runNow({ companyId, request, scheduleId }).unwrap();
        if (pendingRunNow.current?.request === request) {
          pendingRunNow.current = null;
        }
        setActionNotice("The scheduled run was queued.");
        return;
      }

      if (operation === "pause") {
        await pauseSchedule({
          companyId,
          request: {
            expectedRevision: schedule.revision,
            schemaVersion: 1,
          },
          scheduleId,
        }).unwrap();
        setActionNotice("Future occurrences are paused.");
        return;
      }

      if (operation === "cancel") {
        await cancelSchedule({
          companyId,
          request: {
            expectedRevision: schedule.revision,
            schemaVersion: 1,
          },
          scheduleId,
        }).unwrap();
        setConfirmCancel(false);
        setActionNotice("The schedule was cancelled.");
        return;
      }

      const preview = await previewSchedule({
        companyId,
        request: createPreviewRequest(schedule),
      }).unwrap();
      await resumeSchedule({
        companyId,
        request: {
          expectedRevision: schedule.revision,
          reviewDigest: preview.reviewDigest,
          schemaVersion: 1,
        },
        scheduleId,
      }).unwrap();
      setActionNotice("The schedule is active again.");
    } catch (error) {
      if (
        attemptedRunNow &&
        pendingRunNow.current?.request === attemptedRunNow &&
        isDefinitiveRunNowFailure(error)
      ) {
        pendingRunNow.current = null;
      }
      setActionError(
        getScheduledTaskErrorMessage(
          error,
          `Scheduled task could not be ${actionFailureVerb(operation)}.`,
        ),
      );
    }
  };

  const loadMoreRuns = async () => {
    if (!nextCursor || loadingMore) return;
    setLoadingMore(true);
    setActionError(null);
    try {
      const page = await loadRunPage({
        companyId,
        cursor: nextCursor,
        limit: 25,
        scheduleId,
      }).unwrap();
      hasLoadedAdditionalRunPages.current = true;
      setRuns((current) => mergeScheduleRuns(current, page.runs));
      setNextCursor(page.nextCursor);
    } catch (error) {
      setActionError(
        getScheduledTaskErrorMessage(
          error,
          "More run history could not be loaded.",
        ),
      );
    } finally {
      setLoadingMore(false);
    }
  };

  if (scheduleQuery.isLoading) {
    return <DetailLoadingState />;
  }

  if (scheduleQuery.isError || !schedule) {
    return <DetailErrorState onRetry={() => void scheduleQuery.refetch()} />;
  }

  const timeZone =
    schedule.trigger.type === "cron" ? schedule.trigger.timeZone : undefined;
  const sourceNames = new Map(
    (sourcesQuery.currentData?.connectors ?? []).map((connector) => [
      connector.id,
      connector.name,
    ]),
  );

  return (
    <ScheduledPageShell
      title={
        <span className="flex flex-wrap items-center gap-3">
          <span className="min-w-0 break-words">{schedule.name}</span>
          <ScheduledStatusPill tone={scheduleStatusTone(schedule.status)}>
            {scheduleStatusLabel(schedule.status)}
          </ScheduledStatusPill>
        </span>
      }
      description={formatScheduleTrigger(schedule.trigger)}
      actions={
        <ScheduleActions
          confirmCancel={confirmCancel}
          isActing={isActing}
          schedule={schedule}
          onCancel={() => void executeAction("cancel")}
          onCancelConfirmationChange={setConfirmCancel}
          onPause={() => void executeAction("pause")}
          onResume={() => void executeAction("resume")}
          onRun={() => void executeAction("run")}
        />
      }
    >
      {actionError || actionNotice ? (
        <div
          className={cx(
            "rounded-lg border px-4 py-3 text-sm font-semibold",
            actionError
              ? "border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] text-[var(--workspace-native-danger)]"
              : "border-[color-mix(in_srgb,var(--workspace-native-success)_25%,transparent)] bg-[var(--workspace-success-soft)] text-[var(--workspace-native-success)]",
          )}
          role={actionError ? "alert" : "status"}
        >
          {actionError ?? actionNotice}
        </div>
      ) : null}

      {schedule.pauseReason ? (
        <p className="rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[var(--workspace-approval-soft)] px-4 py-3 text-sm text-[var(--workspace-native-approval)]">
          Paused because: {schedule.pauseReason.replace(/_/g, " ")}
        </p>
      ) : null}

      <section className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(300px,0.7fr)]">
        <ScheduledGlassPanel contentClassName="p-5 md:p-6">
          <div className="flex items-center gap-2 text-xs font-bold uppercase tracking-[0.16em] text-[var(--workspace-native-text-muted)]">
            <ShieldCheck aria-hidden className="size-4" />
            Agent instruction
          </div>
          <p className="mt-4 whitespace-pre-wrap break-words text-sm leading-7 text-[var(--workspace-native-text)]">
            {schedule.instructions}
          </p>
        </ScheduledGlassPanel>

        <ScheduledGlassPanel contentClassName="p-5 md:p-6">
          <div className="flex items-center gap-2 text-xs font-bold uppercase tracking-[0.16em] text-[var(--workspace-native-text-muted)]">
            <CalendarClock aria-hidden className="size-4" />
            Timing
          </div>
          <dl className="mt-4 grid gap-4">
            <ScheduleFact
              label="Next run"
              value={formatScheduleDateTime(schedule.nextRunAt, timeZone)}
            />
            <ScheduleFact
              label="Last run"
              value={
                schedule.lastRunAt
                  ? formatScheduleDateTime(schedule.lastRunAt, timeZone)
                  : "Not run yet"
              }
            />
            <ScheduleFact label="Provider" value={schedule.provider} />
          </dl>
        </ScheduledGlassPanel>
      </section>

      <ScheduledGlassPanel contentClassName="p-5 md:p-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-[var(--workspace-native-text)]">
              Sources
            </h2>
            <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
              Exact connector access captured by this schedule.
            </p>
          </div>
          <span className="text-xs font-bold text-[var(--workspace-native-text-muted)]">
            {schedule.sources.length} selected
          </span>
        </div>
        {schedule.sources.length > 0 ? (
          <div className="mt-4 flex flex-wrap gap-2">
            {schedule.sources.map((source) => (
              <span
                className="max-w-full break-words rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-xs font-bold text-[var(--workspace-native-text)]"
                key={source.id}
              >
                {sourceNames.get(source.id) ?? source.name}
              </span>
            ))}
          </div>
        ) : (
          <p className="mt-4 rounded-md border border-dashed border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] p-4 text-sm text-[var(--workspace-native-text-muted)]">
            This task does not use an external source.
          </p>
        )}
      </ScheduledGlassPanel>

      <ScheduleRunHistory
        isError={runsQuery.isError}
        isLoading={runsQuery.isLoading}
        loadingMore={loadingMore}
        nextCursor={nextCursor}
        runs={runs}
        timeZone={timeZone}
        onLoadMore={() => void loadMoreRuns()}
        onOpenChat={openRunChat}
        onRetry={() => void runsQuery.refetch()}
      />
      {chatPanel}
    </ScheduledPageShell>
  );
}

function ScheduleActions({
  confirmCancel,
  isActing,
  onCancel,
  onCancelConfirmationChange,
  onPause,
  onResume,
  onRun,
  schedule,
}: {
  confirmCancel: boolean;
  isActing: boolean;
  onCancel: () => void;
  onCancelConfirmationChange: (confirming: boolean) => void;
  onPause: () => void;
  onResume: () => void;
  onRun: () => void;
  schedule: ScheduledTask;
}) {
  if (confirmCancel) {
    return (
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] p-2">
        <span className="px-2 text-xs font-bold text-[var(--workspace-native-danger)]">
          Cancel permanently?
        </span>
        <button
          className="rounded-md border border-[var(--workspace-native-danger)] bg-[var(--workspace-danger-soft)] px-3 py-2 text-xs font-semibold text-[var(--workspace-native-danger)] disabled:cursor-not-allowed disabled:opacity-60"
          disabled={isActing}
          type="button"
          onClick={onCancel}
        >
          Yes, cancel
        </button>
        <button
          aria-label="Keep schedule"
          className="grid size-8 place-items-center rounded-md text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]"
          disabled={isActing}
          type="button"
          onClick={() => onCancelConfirmationChange(false)}
        >
          <X aria-hidden className="size-4" />
        </button>
      </div>
    );
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {schedule.status !== "cancelled" && schedule.status !== "completed" ? (
        <Link
          className="inline-flex h-10 items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 text-sm font-bold text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]"
          to={`/scheduled/${encodeURIComponent(schedule.id)}/edit`}
        >
          <Pencil aria-hidden className="size-4" />
          Edit
        </Link>
      ) : null}
      {schedule.status === "active" ? (
        <GradientCtaButton
          className="scheduled-primary-button !h-10 px-4"
          disabled={isActing}
          hideIcon
          size="auto"
          type="button"
          onClick={onRun}
        >
          {isActing ? (
            <LoaderCircle aria-hidden className="size-4 animate-spin" />
          ) : (
            <Play aria-hidden className="size-4" />
          )}
          Run now
        </GradientCtaButton>
      ) : null}
      {schedule.status === "active" ? (
        <button
          aria-label="Pause future runs"
          className="grid size-10 place-items-center rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)] disabled:opacity-40"
          disabled={isActing}
          title="Pause"
          type="button"
          onClick={onPause}
        >
          <CirclePause aria-hidden className="size-4" />
        </button>
      ) : null}
      {schedule.status === "paused" ? (
        <button
          aria-label="Resume scheduled runs"
          className="grid size-10 place-items-center rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)] disabled:opacity-40"
          disabled={isActing}
          title="Resume"
          type="button"
          onClick={onResume}
        >
          <RotateCcw aria-hidden className="size-4" />
        </button>
      ) : null}
      {schedule.status !== "cancelled" && schedule.status !== "completed" ? (
        <button
          aria-label="Cancel schedule"
          className="grid size-10 place-items-center rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] text-[var(--workspace-native-danger)] transition hover:bg-[var(--workspace-danger-soft)] hover:text-[var(--workspace-native-danger)] disabled:opacity-40"
          disabled={isActing}
          title="Cancel schedule"
          type="button"
          onClick={() => onCancelConfirmationChange(true)}
        >
          <X aria-hidden className="size-4" />
        </button>
      ) : null}
    </div>
  );
}

function ScheduleFact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[10px] font-bold uppercase tracking-[0.12em] text-[var(--workspace-native-text-muted)]">
        {label}
      </dt>
      <dd className="mt-1 break-words text-sm font-bold capitalize text-[var(--workspace-native-text)]">
        {value}
      </dd>
    </div>
  );
}

function ScheduleRunHistory({
  isError,
  isLoading,
  loadingMore,
  nextCursor,
  onLoadMore,
  onOpenChat,
  onRetry,
  runs,
  timeZone,
}: {
  isError: boolean;
  isLoading: boolean;
  loadingMore: boolean;
  nextCursor: string | null;
  onLoadMore: () => void;
  onOpenChat: (run: ScheduleRunRecord) => void;
  onRetry: () => void;
  runs: ScheduleRunRecord[];
  timeZone?: string;
}) {
  const orderedRuns = useMemo(
    () =>
      [...runs].sort(
        (left, right) =>
          new Date(right.createdAt).getTime() -
          new Date(left.createdAt).getTime(),
      ),
    [runs],
  );

  return (
    <ScheduledGlassPanel contentClassName="p-5 md:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <History
              aria-hidden
              className="size-4 text-[var(--workspace-native-text-muted)]"
            />
            <h2 className="text-base font-semibold text-[var(--workspace-native-text)]">
              Run history
            </h2>
          </div>
          <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            Every scheduled or manual occurrence and its materialization state.
          </p>
        </div>
        <button
          aria-label="Refresh run history"
          className="grid size-9 place-items-center rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]"
          type="button"
          onClick={onRetry}
        >
          <RefreshCcw aria-hidden className="size-3.5" />
        </button>
      </div>

      {isLoading ? (
        <div className="mt-5 grid gap-2">
          {[0, 1, 2].map((index) => (
            <div
              className="h-20 animate-pulse rounded-lg bg-[var(--workspace-control-bg)]"
              key={index}
            />
          ))}
        </div>
      ) : isError && orderedRuns.length === 0 ? (
        <p className="mt-5 rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] p-4 text-sm text-[var(--workspace-native-danger)]">
          Run history could not be loaded.
        </p>
      ) : orderedRuns.length === 0 ? (
        <p className="mt-5 rounded-lg border border-dashed border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] p-5 text-sm text-[var(--workspace-native-text-muted)]">
          No occurrences have run yet.
        </p>
      ) : (
        <div className="mt-5 grid gap-3">
          {orderedRuns.map((run) => (
            <ScheduleRunItem
              key={run.firingId}
              run={run}
              timeZone={timeZone}
              onOpenChat={onOpenChat}
            />
          ))}
        </div>
      )}

      {nextCursor ? (
        <button
          className="mt-4 inline-flex h-9 items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 text-xs font-bold text-[var(--workspace-native-text-muted)] hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)] disabled:opacity-45"
          disabled={loadingMore}
          type="button"
          onClick={onLoadMore}
        >
          {loadingMore ? (
            <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
          ) : null}
          Load more
        </button>
      ) : null}
    </ScheduledGlassPanel>
  );
}

function ScheduleRunItem({
  onOpenChat,
  run,
  timeZone,
}: {
  onOpenChat: (run: ScheduleRunRecord) => void;
  run: ScheduleRunRecord;
  timeZone?: string;
}) {
  return (
    <ScheduledGlassPanel density="regular" contentClassName="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <ScheduledStatusPill tone={scheduleRunTone(run)}>
              {scheduleRunLabel(run)}
            </ScheduledStatusPill>
            <span className="text-xs font-bold capitalize text-[var(--workspace-native-text-muted)]">
              {run.trigger}
            </span>
          </div>
          <p className="mt-2 text-sm font-bold text-[var(--workspace-native-text)]">
            {formatScheduleDateTime(run.occurrenceAt, timeZone)}
          </p>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          <span className="text-xs text-[var(--workspace-native-text-muted)]">
            Recorded {formatScheduleDateTime(run.createdAt, timeZone)}
          </span>
          {resolveScheduleRunChatTarget(run) ? (
            <button
              className="inline-flex h-8 items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 text-xs font-bold text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)]"
              type="button"
              onClick={() => onOpenChat(run)}
            >
              <MessageSquareText aria-hidden className="size-3.5" />
              Open chat
            </button>
          ) : null}
        </div>
      </div>
      {run.reason ? (
        <p className="mt-3 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[var(--workspace-approval-soft)] px-3 py-2 text-xs leading-5 text-[var(--workspace-native-approval)]">
          {run.reason.replace(/_/g, " ")}
        </p>
      ) : null}
    </ScheduledGlassPanel>
  );
}

function createPreviewRequest(schedule: ScheduledTask): SchedulePreviewRequest {
  return {
    agentId: schedule.agentId,
    instructions: schedule.instructions,
    name: schedule.name,
    ...(schedule.presetKey ? { presetKey: schedule.presetKey } : {}),
    schemaVersion: 1,
    sourceIds: schedule.sources.map((source) => source.id),
    trigger: schedule.trigger,
  };
}

function createRunNowRequest(): ScheduleRunNowRequest {
  return {
    requestKey: generateUuidV7(),
    schemaVersion: 1,
  };
}

function isDefinitiveRunNowFailure(error: unknown) {
  if (!error || typeof error !== "object" || !("status" in error)) {
    return false;
  }
  return (
    typeof error.status === "number" &&
    error.status >= 400 &&
    error.status < 500
  );
}

function mergeScheduleRuns(
  current: ScheduleRunRecord[],
  incoming: ScheduleRunRecord[],
) {
  const runsById = new Map(current.map((run) => [run.firingId, run]));
  incoming.forEach((run) => runsById.set(run.firingId, run));
  return Array.from(runsById.values());
}

function mergeRefreshedScheduleRuns(
  current: ScheduleRunRecord[],
  refreshed: ScheduleRunRecord[],
) {
  const refreshedRunIds = new Set(refreshed.map((run) => run.firingId));
  return [
    ...refreshed,
    ...current.filter((run) => !refreshedRunIds.has(run.firingId)),
  ];
}

function actionFailureVerb(operation: "cancel" | "pause" | "resume" | "run") {
  return {
    cancel: "cancelled",
    pause: "paused",
    resume: "resumed",
    run: "run",
  }[operation];
}

function DetailLoadingState() {
  return (
    <ScheduledPageShell>
      <div className="h-10 w-72 animate-pulse rounded-md bg-[var(--workspace-control-bg)]" />
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="h-64 animate-pulse rounded-lg bg-[var(--workspace-control-bg)]" />
        <div className="h-64 animate-pulse rounded-lg bg-[var(--workspace-control-bg)]" />
      </div>
    </ScheduledPageShell>
  );
}

function DetailErrorState({ onRetry }: { onRetry: () => void }) {
  return (
    <ScheduledPageShell>
      <ScheduledGlassPanel
        density="dense"
        contentClassName="grid min-h-72 place-items-center bg-[var(--workspace-danger-soft)] p-8 text-center ring-1 ring-inset ring-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)]"
      >
        <div>
          <h2 className="text-xl font-semibold text-[var(--workspace-native-text)]">
            Scheduled task unavailable
          </h2>
          <p className="mt-2 text-sm text-[var(--workspace-native-text-muted)]">
            It may have changed, or you may no longer have access.
          </p>
          <div className="mt-5 flex justify-center gap-2">
            <button
              className="rounded-md bg-[var(--workspace-accent-fill)] px-4 py-2 text-sm font-semibold text-[var(--workspace-accent-foreground)] hover:bg-[var(--workspace-accent-hover)]"
              type="button"
              onClick={onRetry}
            >
              Try again
            </button>
          </div>
        </div>
      </ScheduledGlassPanel>
    </ScheduledPageShell>
  );
}

export const Component = ScheduledTaskDetailPage;
