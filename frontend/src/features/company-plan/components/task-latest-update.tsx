import { useGetTaskLatestRunQuery } from "../api/task-detail-api";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useCan } from "@/shared/permissions";
import {
  runFailureMessageForViewer,
  runLeftWorkToReview,
  taskAwaitsViewerReview,
} from "../model/task-execution";
import {
  runCancellationCopy,
  runFailureCopy,
} from "@/shared/agent-execution/run-access-reasons";
import { formatTaskEventTime } from "../model/task-detail-presentation";
import { taskWorkStatusLabel } from "../model/task-work-state";
import type { RunStatusDto, TaskDto } from "@/shared/api/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { ArrowRight } from "@/shared/ui/icons";

const runUpdateLabels: Record<RunStatusDto, string> = {
  queued: "Run queued",
  deferred: "Run waiting to retry",
  dispatched: "Run starting",
  waiting_local_directory: "Run needs a workspace",
  running: "Run in progress",
  completed: "Run finished",
  failed: "Run failed",
  cancelled: "Run cancelled",
};

export function TaskLatestUpdate({
  companyId,
  task,
  onOpenChat,
}: {
  companyId: string;
  task: TaskDto;
  onOpenChat: () => void;
}) {
  const query = useGetTaskLatestRunQuery({ companyId, taskId: task.id });
  const run = query.currentData;
  // "Needs your review" only for someone who decides it, and only when the
  // latest Run left work to review; everyone else reads the Task's status.
  const viewerUserId = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  const awaitsReview =
    taskAwaitsViewerReview(task, viewerUserId, canAssign) &&
    runLeftWorkToReview(run);

  if (query.isError) {
    return (
      <div className="task-detail-latest-update" role="status">
        <p className="task-detail-empty">
          Latest update could not be loaded.{" "}
          <button type="button" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      </div>
    );
  }
  if (run === undefined) {
    return (
      <div className="task-detail-latest-update" role="status">
        <p className="task-detail-empty">Loading latest update…</p>
      </div>
    );
  }
  // No Run, so nothing to review: a Task moved into review by hand shows
  // its status in the header alone.
  if (!run) return null;

  // A handoff can change the task's Agent after this Run finished.
  const agentName =
    task.executedBy.kind === "agent" && task.executedBy.id === run.agentId
      ? task.executedBy.name
      : null;
  const title =
    agentName && run.status === "completed"
      ? `${agentName} finished a run`
      : agentName && run.status === "running"
        ? `${agentName} is working`
        : runUpdateLabels[run.status];
  const taskNote = awaitsReview
    ? "Needs your review"
    : task.status === "in_progress" && run.status === "completed"
      ? "The task is still in progress."
      : `Task: ${taskWorkStatusLabel(task)}.`;

  return (
    <section
      className="task-detail-latest-update"
      aria-label="Latest update"
      data-run-status={run.status}
    >
      <FoldAvatar paused agentId={run.agentId} size={32} />
      <div className="task-detail-update-content">
        <div className="task-detail-update-heading">
          <strong>{title}</strong>
          <time
            dateTime={run.updatedAt}
            title={new Date(run.updatedAt).toLocaleString()}
          >
            {formatTaskEventTime(run.updatedAt)}
          </time>
        </div>
        <p>{taskNote}</p>
        {run.status === "failed" ? (
          <div className="task-detail-run-failure" role="alert">
            <strong>
              {runFailureMessageForViewer(
                {
                  message:
                    runFailureCopy(run.failureReason, run.failureMessage) ??
                    "The agent could not finish this run.",
                  reason: run.failureReason,
                },
                run.runtimeId !== null,
              )}
            </strong>
            {run.failureDetail ? <span>{run.failureDetail}</span> : null}
            {run.maxAttempts > 1 ? (
              <span>
                Attempt {run.attempt} of {run.maxAttempts}
              </span>
            ) : null}
          </div>
        ) : runCancellationCopy(run) ? (
          <div className="task-detail-run-failure" role="status">
            <strong>{runCancellationCopy(run)}</strong>
          </div>
        ) : null}
      </div>
      <button
        className="task-detail-update-link"
        type="button"
        onClick={onOpenChat}
      >
        View update <ArrowRight size={16} />
      </button>
    </section>
  );
}
