import { Link } from "react-router";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import type { components } from "@/shared/api/generated/api.generated";
import { Button } from "@/shared/ui/core/button";

type Proposal = components["schemas"]["HuddleProposal"];
type Progress = components["schemas"]["HuddleWorkProgress"];

const progressLabels: Record<
  Progress["state"],
  { label: string; detail: string }
> = {
  confirmed: {
    label: "Task confirmed",
    detail: "Open the Task to review it and start work when ready.",
  },
  unavailable: {
    label: "Progress unavailable",
    detail:
      "The submitted Run is no longer available. Check the Task for current work.",
  },
  queued: {
    label: "Queued",
    detail: "Submitted successfully. Waiting to start.",
  },
  waiting_for_runtime: {
    label: "Waiting for computer",
    detail: "Work stays queued until an execution machine is available.",
  },
  preparing: {
    label: "Preparing",
    detail: "The Agent is preparing its workspace.",
  },
  running: { label: "Working", detail: "The submitted work is running." },
  waiting_for_input: {
    label: "Needs human input",
    detail: "Review the question or approval in the Task to continue.",
  },
  resuming: {
    label: "Resuming",
    detail: "A response is saved. A continuation is being prepared.",
  },
  waiting_local_directory: {
    label: "Waiting for workspace",
    detail: "Another execution is using the local workspace.",
  },
  deferred: {
    label: "Scheduled to resume",
    detail: "The Run is waiting for its scheduled start.",
  },
  completed: {
    label: "Run completed",
    detail:
      "Execution finished. Check the result and Task status before accepting the work.",
  },
  failed: {
    label: "Run failed",
    detail: "Open the Task to inspect the failure and recovery options.",
  },
  cancelled: {
    label: "Run cancelled",
    detail:
      "This execution was cancelled. Open the Task for its current status.",
  },
};

export function HuddleWorkProgress({
  proposal,
  stale,
}: {
  proposal: Proposal;
  stale: boolean;
}) {
  const work = proposal.work;
  const presentation = work ? progressLabels[work.state] : undefined;
  const taskId = work?.taskId ?? proposal.submittedTaskId;
  const taskHref = taskId
    ? createChatWorkspaceHref({ kind: "task", taskId })
    : undefined;
  const resultHref =
    taskId &&
    work?.hasResult &&
    work.resultConversationId &&
    work.resultMessageId &&
    work.resultMessageSeq
      ? createChatWorkspaceHref(
          { kind: "task", taskId },
          {
            focusedMessage: {
              conversationId: work.resultConversationId,
              messageId: work.resultMessageId,
              messageSeq: work.resultMessageSeq,
            },
          },
        )
      : undefined;

  return (
    <section
      className="huddle-work-progress"
      aria-label="Submitted work progress"
      data-state={stale ? "stale" : work?.state}
    >
      <strong>
        {stale
          ? "Updates disconnected"
          : (presentation?.label ?? "Waiting for progress")}
      </strong>
      <p>
        {stale
          ? "Showing the last confirmed update. Reconnecting does not resubmit work."
          : (presentation?.detail ??
            "Your submission is saved. Open the Task to check its progress.")}
      </p>
      {work?.taskStatus && (
        <p className="huddle-work-task-state">
          Task: {work.taskStatus.replaceAll("_", " ")}
        </p>
      )}
      {work?.newerTaskExecution && (
        <p>
          The Task has newer work. This card follows only this submission and
          its continuations.
        </p>
      )}
      {work?.state === "completed" && !work.hasResult && (
        <p>No verified final answer is available for this Run.</p>
      )}
      {work && (
        <small>
          Checked{" "}
          <time dateTime={work.checkedAt}>
            {new Date(work.checkedAt).toLocaleTimeString()}
          </time>
        </small>
      )}
      {!taskHref && <p>The submitted Task is no longer available.</p>}
      {taskHref && (
        <div className="huddle-proposal-actions">
          <Button asChild size="sm" variant="outline">
            <Link to={taskHref} target="_blank" rel="noopener noreferrer">
              {!stale && work?.state === "waiting_for_input"
                ? "Review request"
                : `Open ${proposal.submittedTaskKey || "Task"}`}
            </Link>
          </Button>
          {resultHref && (
            <Button asChild size="sm" className="huddle-action-primary">
              <Link to={resultHref} target="_blank" rel="noopener noreferrer">
                View result
              </Link>
            </Button>
          )}
        </div>
      )}
    </section>
  );
}
