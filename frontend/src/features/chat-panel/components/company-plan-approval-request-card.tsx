import { useEffect, useId, useRef, useState } from "react";
import {
  CheckCircle2,
  ChevronRight,
  CircleX,
  Clock3,
  FileText,
  NotebookText,
  Undo2,
} from "@/shared/ui/icons";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  useApproveApprovalMutation,
  useCancelApprovalMutation,
  useRejectApprovalMutation,
} from "@/features/approvals/api/approvals-api";
import {
  approvalStatusLabel,
  formatHitlDate,
} from "@/features/approvals/lib/approval-inbox";
import {
  CompanyPlanBriefs,
  CompanyPlanChangeList,
  CompanyPlanMarkdown,
  CompanyPlanTasks,
} from "@/features/chat-panel/components/company-plan-approval-content";
import {
  companyPlanApplySummary,
  companyPlanChangeCounts,
  companyPlanChangeLines,
  companyPlanChangeSummary,
  companyPlanReview,
} from "@/features/chat-panel/model/company-plan-approval-preview";
import type { ApprovalDto } from "@/shared/api/types";
import { missingProjectPermissionTitle } from "@/shared/permissions";
import { useTaskAccessById } from "@/shared/permissions/use-task-access-by-id";
import { PlanApplySkipped } from "./plan-apply-skipped";
import "@/features/chat-panel/styles/company-plan-approval.css";

// Bound from docs/api/spec/approvals.yaml decisionNote.maxLength. HTML
// maxLength counts UTF-16 code units, so it never admits more runes than the server.
const DECISION_NOTE_MAX_LENGTH = 2048;

export function CompanyPlanApprovalRequestCard({
  approval,
  compact = false,
  onResolved,
  onSubmittingChange,
}: {
  approval: ApprovalDto;
  compact?: boolean;
  onResolved?: (approval: ApprovalDto) => void;
  onSubmittingChange?: (approvalId: string, pending: boolean) => void;
}) {
  const [decisionNote, setDecisionNote] = useState(approval.decisionNote ?? "");
  const [noteOpen, setNoteOpen] = useState(Boolean(approval.decisionNote));
  const [feedback, setFeedback] = useState<string | null>(null);
  const noteId = useId();
  const noteRef = useRef<HTMLTextAreaElement>(null);
  const [approve, approveState] = useApproveApprovalMutation();
  const [reject, rejectState] = useRejectApprovalMutation();
  const [cancel, cancelState] = useCancelApprovalMutation();
  const isSubmitting =
    approveState.isLoading || rejectState.isLoading || cancelState.isLoading;
  // A plan raised in a Task chat is decided on that Task's Project alone
  // (ADR 0017): deciding needs "Answer approvals and questions" there, and
  // approving also "Create and edit tasks", because it applies the plan.
  // Until the Task is read, or when it is gone, the server decides.
  const taskAccess = useTaskAccessById(approval.companyId, approval.taskId);
  const canDecide =
    !taskAccess.known || taskAccess.can("project.approvals.resolve");
  const canApprove =
    canDecide && (!taskAccess.known || taskAccess.can("project.tasks.write"));
  const pending = approval.status === "pending";
  const presentation = approval.presentation;
  const review = presentation ? companyPlanReview(presentation) : null;
  const agentsQuery = useListCompanyAgentsQuery(approval.companyId, {
    skip: !review?.tasks.some((task) => task.agentId),
  });
  const agentNames = new Map(
    (agentsQuery.data?.items ?? []).map((agent) => [agent.id, agent.name]),
  );
  agentNames.set(approval.requestedByAgent.id, approval.requestedByAgent.name);
  const counts =
    presentation && review ? companyPlanChangeCounts(presentation) : null;
  const changeLines =
    presentation && review
      ? companyPlanChangeLines(presentation, agentNames)
      : [];
  const project = review?.projects.length === 1 ? review.projects[0] : null;

  useEffect(() => {
    setDecisionNote(approval.decisionNote ?? "");
    setNoteOpen(Boolean(approval.decisionNote));
    setFeedback(null);
  }, [approval.decisionNote, approval.id, approval.revision]);

  useEffect(() => {
    if (noteOpen) noteRef.current?.focus();
  }, [noteOpen]);

  const resolve = async (action: "approve" | "reject" | "cancel") => {
    if (!pending || isSubmitting) return;
    setFeedback(null);
    onSubmittingChange?.(approval.id, true);
    try {
      const argument = {
        approvalId: approval.id,
        companyId: approval.companyId,
        expectedRevision: approval.revision,
      };
      const note = decisionNote.trim()
        ? { decisionNote: decisionNote.trim() }
        : {};
      const updated =
        action === "approve"
          ? await approve({ ...argument, ...note }).unwrap()
          : action === "reject"
            ? await reject({ ...argument, ...note }).unwrap()
            : await cancel(argument).unwrap();
      onResolved?.(updated);
    } catch (error) {
      setFeedback(
        errorStatus(error) === 409
          ? "This approval changed elsewhere. Its latest state is loading."
          : errorStatus(error) === 403
            ? ((error as { message?: string }).message ??
              "You can't decide this approval.")
            : "The approval could not be updated. Try again.",
      );
    } finally {
      onSubmittingChange?.(approval.id, false);
    }
  };

  return (
    <section
      aria-label="Company Plan approval"
      aria-busy={isSubmitting}
      className="company-plan-approval"
      data-compact={compact}
      id={`approval-${approval.id}`}
    >
      <header className="company-plan-approval__header">
        <span className="company-plan-approval__eyebrow">Company plan</span>
        <span
          className="company-plan-approval__status"
          data-status={approval.status}
        >
          {pending ? (
            <Clock3 aria-hidden size={14} />
          ) : approval.status === "approved" ? (
            <CheckCircle2 aria-hidden size={14} />
          ) : null}
          {pending ? "For your review" : approvalStatusLabel(approval.status)}
        </span>
      </header>
      <h2 className="company-plan-approval__title">
        {project?.title ?? (review ? "Review company plan" : approval.title)}
      </h2>
      {project?.description ? (
        <p className="company-plan-approval__description">
          {project.description}
        </p>
      ) : !review && approval.summary ? (
        <p className="company-plan-approval__description">{approval.summary}</p>
      ) : null}
      {counts ? (
        <p className="company-plan-approval__summary">
          {companyPlanChangeSummary(counts)}
        </p>
      ) : null}

      {review && presentation ? (
        <CompanyPlanTasks
          key={`${approval.id}:${approval.revision}`}
          review={review}
          presentation={presentation}
          agentNames={agentNames}
          agentsLoading={agentsQuery.isLoading}
          pending={pending}
        />
      ) : (
        <CompanyPlanMarkdown>{approval.previewMarkdown}</CompanyPlanMarkdown>
      )}
      {agentsQuery.isError ? (
        <p className="company-plan-approval__feedback" role="status">
          Some agent names could not be loaded. The tasks are still shown.
        </p>
      ) : null}
      <CompanyPlanChangeList
        lines={changeLines.filter((line) => line.destructive)}
        destructive
      />

      <details
        className="company-plan-approval__details"
        key={`details:${approval.id}:${approval.revision}`}
      >
        <summary>
          <FileText aria-hidden size={18} />
          Plan details
          <ChevronRight
            aria-hidden
            size={16}
            className="company-plan-approval__chevron"
          />
        </summary>
        <div className="company-plan-approval__details-body">
          {review ? <CompanyPlanBriefs review={review} /> : null}
          <CompanyPlanChangeList lines={changeLines} />
          <dl className="company-plan-approval__metadata">
            <dt>Requested by</dt>
            <dd>{approval.requestedByAgent.name}</dd>
            <dt>Expires</dt>
            <dd>{formatHitlDate(approval.expiresAt)}</dd>
          </dl>
          {pending && canDecide ? (
            <button
              className="company-plan-approval__button company-plan-approval__button--quiet"
              disabled={isSubmitting}
              type="button"
              onClick={() => void resolve("cancel")}
            >
              <Undo2 aria-hidden size={16} />
              {cancelState.isLoading ? "Cancelling…" : "Cancel request"}
            </button>
          ) : null}
        </div>
      </details>

      {pending && !canDecide ? (
        <p className="company-plan-approval__readonly">
          {missingProjectPermissionTitle("project.approvals.resolve")}
        </p>
      ) : pending ? (
        <div className="company-plan-approval__decision">
          {noteOpen ? (
            <label className="company-plan-approval__note" htmlFor={noteId}>
              Note to {approval.requestedByAgent.name} <span>(optional)</span>
              <textarea
                id={noteId}
                ref={noteRef}
                className="company-plan-approval__textarea"
                disabled={isSubmitting}
                maxLength={DECISION_NOTE_MAX_LENGTH}
                placeholder="Add feedback or context…"
                value={decisionNote}
                onChange={(event) => setDecisionNote(event.target.value)}
              />
            </label>
          ) : null}
          <div className="company-plan-approval__footer">
            <p className="company-plan-approval__effect">
              {counts
                ? companyPlanApplySummary(counts)
                : "Applies the changes in this plan."}
            </p>
            <div className="company-plan-approval__actions">
              <button
                className="company-plan-approval__button company-plan-approval__button--primary"
                disabled={isSubmitting || !canApprove}
                type="button"
                onClick={() => void resolve("approve")}
              >
                <CheckCircle2 aria-hidden size={16} />
                {approveState.isLoading ? "Applying…" : "Approve plan"}
              </button>
              <button
                className="company-plan-approval__button"
                disabled={isSubmitting}
                type="button"
                onClick={() => void resolve("reject")}
              >
                <CircleX aria-hidden size={16} />
                {rejectState.isLoading ? "Sending…" : "Request changes"}
              </button>
              <button
                className="company-plan-approval__button company-plan-approval__button--quiet"
                aria-expanded={noteOpen}
                aria-controls={noteOpen ? noteId : undefined}
                disabled={isSubmitting}
                type="button"
                onClick={() => setNoteOpen(!noteOpen)}
              >
                <NotebookText aria-hidden size={16} />
                {noteOpen
                  ? "Hide note"
                  : decisionNote.trim()
                    ? "Edit note"
                    : "Add note"}
              </button>
            </div>
          </div>
          {canApprove ? null : (
            <p className="company-plan-approval__readonly">
              Approving applies the plan, so it also needs "Create and edit
              tasks" on this project.
            </p>
          )}
        </div>
      ) : (
        <>
          {approval.decisionNote ? (
            <p className="company-plan-approval__saved-note">
              {approval.decisionNote}
            </p>
          ) : null}
          {approval.execution ? (
            <p className="company-plan-approval__receipt">
              Execution: {approval.execution.status}
              {approval.execution.skippedReason
                ? ` · ${approval.execution.skippedReason}`
                : ""}
            </p>
          ) : null}
          <PlanApplySkipped execution={approval.execution} />
        </>
      )}
      {feedback ? (
        <p className="company-plan-approval__feedback" role="alert">
          {feedback}
        </p>
      ) : null}
    </section>
  );
}

function errorStatus(error: unknown) {
  if (!error || typeof error !== "object" || !("status" in error)) return null;
  return typeof error.status === "number" ? error.status : null;
}
