import { useCallback, useState } from "react";

import { getApiErrorCode } from "@/lib/api-client";
import {
  useGetTaskReportPreviewQuery,
  useSendTaskReportsMutation,
} from "@/features/chat-panel/api/task-report-api";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";

export function TaskReportQueueBanner({
  companyId,
  conversationId,
  onRunCreated,
}: {
  companyId: string;
  conversationId: string;
  onRunCreated: (conversationId: string) => Promise<unknown>;
}) {
  const previewQuery = useGetTaskReportPreviewQuery({
    companyId,
    conversationId,
  });
  const [sendReports, sendState] = useSendTaskReportsMutation();
  const preview = previewQuery.currentData;
  const [error, setError] = useState<string | null>(null);
  const isSending = sendState.isLoading;

  const refresh = useCallback(async () => {
    try {
      await previewQuery.refetch().unwrap();
      setError(null);
    } catch {
      setError(
        "Unable to load task updates. Check your connection and try again.",
      );
    }
  }, [previewQuery]);

  const sendAll = useCallback(async () => {
    if (!preview?.pendingCount || preview.agentBusy || isSending) return;
    setError(null);
    try {
      await sendReports({
        companyId,
        conversationId,
        requestKey: generateUuidV7(),
      }).unwrap();
      try {
        await onRunCreated(conversationId);
      } catch {
        setError(
          "Task updates were sent, but the chat could not refresh. Reload the chat to see them.",
        );
      }
    } catch (cause) {
      setError(
        zenithTeamErrorCode(cause) === "task_report_agent_busy"
          ? "CEO Agent started other work. Send these updates when it finishes."
          : "Unable to send task updates. Check your connection and try again.",
      );
    }
  }, [
    companyId,
    conversationId,
    isSending,
    onRunCreated,
    preview,
    sendReports,
  ]);

  const displayError =
    error ??
    (previewQuery.isError
      ? "Unable to load task updates. Check your connection and try again."
      : null);

  if (!preview?.pendingCount && !displayError) return null;

  const taskLabel =
    preview?.pendingCount === 1 ? "task update" : "task updates";
  return (
    <section
      aria-label="Pending task updates"
      className="min-w-0 rounded-2xl border border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[var(--workspace-approval-soft)] px-4 py-3 text-[var(--workspace-native-text)] shadow-sm"
    >
      {preview?.pendingCount ? (
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0 flex-1 basis-64">
            <p className="m-0 text-sm font-semibold text-[var(--workspace-native-approval)]">
              {preview.pendingCount} {taskLabel} ready
            </p>
            <p className="m-0 mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
              {preview.agentBusy
                ? "CEO Agent is working. Send becomes available when it finishes."
                : "Review and send these task updates to CEO Agent."}
            </p>
            <details className="mt-2 text-xs">
              <DisclosureSummary>Review updates</DisclosureSummary>
              <ul className="mb-0 mt-2 grid grid-cols-1 gap-1 pl-4">
                {preview.events.map((event) => (
                  <li key={event.id} className="break-words">
                    <span className="font-semibold">
                      {event.outcome === "failed" ? "Failed" : "Completed"}
                    </span>{" "}
                    — {event.taskKey}: {event.taskTitle}
                    {event.outcome === "failed" && event.resultSummary ? (
                      <span className="mt-0.5 block text-[var(--workspace-native-text-muted)]">
                        {event.resultSummary}
                      </span>
                    ) : null}
                  </li>
                ))}
                {preview.truncated ? (
                  <li>More task updates are included.</li>
                ) : null}
              </ul>
            </details>
          </div>
          <button
            className="shrink-0 rounded-lg bg-[var(--workspace-panel-bg)] px-3 py-2 text-xs font-semibold text-[var(--workspace-native-approval)] transition hover:bg-[var(--workspace-hover-bg)] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={preview.agentBusy || isSending}
            onClick={() => void sendAll()}
            type="button"
          >
            {isSending ? "Sending…" : "Send all to CEO"}
          </button>
        </div>
      ) : null}
      {displayError ? (
        <div className="mt-2 flex items-center gap-2">
          <p
            aria-live="polite"
            className="m-0 flex-1 text-xs text-[var(--workspace-native-danger)]"
          >
            {displayError}
          </p>
          <button
            className="shrink-0 text-xs font-semibold text-[var(--workspace-native-approval)]"
            type="button"
            onClick={() => void refresh()}
          >
            Retry
          </button>
        </div>
      ) : null}
    </section>
  );
}

function zenithTeamErrorCode(error: unknown) {
  const apiCode = getApiErrorCode(error);
  if (apiCode) return apiCode;
  if (error && typeof error === "object" && "code" in error) {
    return typeof error.code === "string" ? error.code : null;
  }
  return null;
}
