import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";
import { useId, useState } from "react";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  milestoneLabel,
  milestoneTypes,
} from "@/features/company-plan/model/milestone-catalog";

import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
import type { TaskDto } from "@/shared/api/types";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/shared/ui/core/dialog";
import { useSubmitHuddleProposalMutation } from "./huddle-ai-api";

type Proposal = components["schemas"]["HuddleProposal"];
type Props = {
  companyId: string;
  huddleId: string;
  proposal: Proposal;
  stale: boolean;
  onClose: () => void;
  onRefresh: () => void;
};

export function HuddleProposalSubmitDialog(props: Props) {
  const task = useGetWorkspaceTaskQuery(
    { companyId: props.companyId, taskId: props.proposal.taskId ?? "" },
    { skip: !props.proposal.taskId, refetchOnMountOrArgChange: true },
  );
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose();
      }}
    >
      <DialogContent className="huddle-dialog huddle-proposal-submit-dialog">
        <DialogHeader>
          <DialogTitle>Submit proposal</DialogTitle>
          <DialogDescription>
            Review and save the Task. Work starts only when you start it from
            the Task page.
          </DialogDescription>
        </DialogHeader>
        {props.proposal.taskId && !task.currentData ? (
          <p role={task.isError ? "alert" : "status"}>
            {task.isError
              ? "Could not load this Task. Close and try again."
              : "Loading Task…"}
          </p>
        ) : (
          <SubmissionReview
            {...props}
            stale={
              props.stale || (Boolean(props.proposal.taskId) && task.isFetching)
            }
            task={task.currentData}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function SubmissionReview({
  companyId,
  huddleId,
  proposal,
  stale,
  onClose,
  onRefresh,
  task,
}: Props & { task?: TaskDto }) {
  const [review] = useState(() => ({ proposal, task }));
  const fieldId = useId();
  const session = useSessionSnapshot();
  const agents = useListCompanyAgentsQuery(companyId, {
    refetchOnMountOrArgChange: true,
  });
  const agent = agents.currentData?.items.find(
    (item) => item.id === proposal.agentId,
  );
  const [milestone, setMilestone] = useState<
    components["schemas"]["TaskMilestoneTypeDto"] | ""
  >("");
  const [error, setError] = useState("");
  const [submit, mutation] = useSubmitHuddleProposalMutation();
  const changed =
    review.proposal.revision !== proposal.revision ||
    review.task?.revision !== task?.revision;
  const wrongTask =
    task &&
    (task.assignee.id !== session.userId ||
      task.executedBy.id !== proposal.agentId);
  const ready =
    Boolean(proposal.projectId) &&
    (!proposal.taskKey || Boolean(proposal.taskId)) &&
    !stale &&
    !changed &&
    !wrongTask &&
    proposal.canEdit &&
    Date.parse(proposal.expiresAt) > Date.now() &&
    !agent?.archivedAt &&
    Boolean(agent) &&
    (Boolean(task) || Boolean(milestone));

  async function confirm() {
    if (!ready || mutation.isLoading) return;
    setError("");
    try {
      await submit({
        companyId,
        huddleId,
        proposalId: review.proposal.id,
        body: {
          expectedRevision: review.proposal.revision,
          confirm: true,
          ...(review.task
            ? { expectedTaskRevision: review.task.revision }
            : {
                milestoneType:
                  milestone as components["schemas"]["TaskMilestoneTypeDto"],
              }),
        },
      }).unwrap();
      onClose();
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    } finally {
      onRefresh();
    }
  }

  return (
    <>
      <div className="huddle-proposal-review">
        <div className="huddle-proposal-summary">
          <p className="huddle-proposal-target">
            {review.proposal.projectTitle}
            {task ? ` · ${task.key}` : " · New Task"}
          </p>
          <h3>{review.proposal.title}</h3>
          <p className="huddle-proposal-brief">{review.proposal.brief}</p>
        </div>
        <dl className="huddle-proposal-ownership">
          <div>
            <dt>Assignee</dt>
            <dd>You</dd>
          </div>
          <div>
            <dt>Agent</dt>
            <dd>{review.proposal.agentName}</dd>
          </div>
        </dl>
        {!task && (
          <>
            <label htmlFor={fieldId}>Milestone</label>
            <select
              id={fieldId}
              value={milestone}
              disabled={mutation.isLoading}
              onChange={(event) =>
                setMilestone(event.target.value as typeof milestone)
              }
            >
              <option value="">Choose a milestone</option>
              {milestoneTypes.map((type) => (
                <option key={type} value={type}>
                  {milestoneLabel(type)}
                </option>
              ))}
            </select>
          </>
        )}
        <p className="huddle-proposals-disclosure">
          Confirmation does not start a Run.
        </p>
        {wrongTask && (
          <p role="alert">
            This Task must be assigned to you and use {proposal.agentName}.
            Update it from the Task page first.
          </p>
        )}
        {changed && (
          <p role="alert">
            The proposal or Task changed. Close and review the latest version
            before submitting.
          </p>
        )}
        {(!proposal.projectId || (proposal.taskKey && !proposal.taskId)) && (
          <p role="alert">
            The target Project or Task was removed. Ask for a new proposal.
          </p>
        )}
        {!agent && (
          <p role="status">
            {agents.isError
              ? "Agent setup could not be loaded. Close and try again."
              : agents.isSuccess
                ? "This Agent is no longer available."
                : "Loading Agent setup…"}
          </p>
        )}
        {agent?.archivedAt && (
          <p role="alert">
            This Agent is archived. Restore it before starting work.
          </p>
        )}
        {stale && (
          <p role="alert">Connection unavailable. Submission is paused.</p>
        )}
        {error && <p role="alert">{error}</p>}
      </div>
      <DialogFooter>
        <Button variant="ghost" disabled={mutation.isLoading} onClick={onClose}>
          Cancel
        </Button>
        <Button
          className="huddle-action-primary"
          disabled={!ready || mutation.isLoading}
          onClick={() => void confirm()}
        >
          {mutation.isLoading
            ? "Saving…"
            : task
              ? "Confirm proposal"
              : "Confirm and create Task"}
        </Button>
      </DialogFooter>
    </>
  );
}
