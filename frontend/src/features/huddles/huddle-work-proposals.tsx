import { useId, useState } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import { Button } from "@/shared/ui/core/button";
import { HuddleWorkProgress } from "./huddle-work-progress";
import { HuddleProposalSubmitDialog } from "./huddle-proposal-submit-dialog";
import { useUpdateHuddleProposalMutation } from "./huddle-ai-api";
import "./huddle-work-proposals.css";

type Proposal = components["schemas"]["HuddleProposal"];
type Scope = {
  companyId: string;
  huddleId: string;
  stale: boolean;
  onRefresh: () => void;
};

const stateLabels: Record<Proposal["state"], string> = {
  pending: "Draft · not started",
  rejected: "Rejected",
  expired: "Expired",
  submitted: "Submitted",
};

export function HuddleWorkProposals({
  proposals,
  ...scope
}: Scope & { proposals: Proposal[] }) {
  return (
    <section
      className="huddle-work-proposals grid [align-content:start] gap-[14px] min-h-0 p-[16px] overflow-y-auto text-[length:12px] leading-[1.6]"
      aria-label="Work proposals"
    >
      <p className="huddle-proposals-disclosure">
        Review drafts and follow submitted work. Leaving the call does not
        cancel it. With shared context enabled, the Agent can announce results
        or review requests when the conversation is quiet.
      </p>
      {scope.stale && (
        <p role="alert">
          Proposal status is unavailable. Editing is paused until the connection
          returns.
        </p>
      )}
      {!scope.stale && proposals.length === 0 && (
        <p>
          Ask your invited Agent to prepare a work proposal. Include the Project
          and the outcome you want.
        </p>
      )}
      {proposals.map((proposal) => (
        <ProposalCard key={proposal.id} proposal={proposal} {...scope} />
      ))}
    </section>
  );
}

function ProposalCard({
  proposal,
  companyId,
  huddleId,
  stale,
  onRefresh,
}: Scope & { proposal: Proposal }) {
  const fieldId = useId();
  const [draft, setDraft] = useState<{
    title: string;
    brief: string;
    revision: number;
  } | null>(null);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [update, mutation] = useUpdateHuddleProposalMutation();
  const editable =
    proposal.canEdit && !stale && Date.parse(proposal.expiresAt) > Date.now();
  const changed = draft !== null && draft.revision !== proposal.revision;

  async function save(action: "edit" | "reject") {
    if (!editable || mutation.isLoading) return;
    if (action === "edit" && !draft) return;
    setError("");
    try {
      await update({
        companyId,
        huddleId,
        proposalId: proposal.id,
        body:
          action === "edit" && draft
            ? {
                action,
                expectedRevision: draft.revision,
                title: draft.title.trim(),
                brief: draft.brief.trim(),
              }
            : { action, expectedRevision: proposal.revision },
      }).unwrap();
      setDraft(null);
    } catch (cause) {
      setError(getZenithTeamQueryErrorMessage(cause));
    } finally {
      onRefresh();
    }
  }

  return (
    <article className="huddle-proposal-card">
      <header>
        <strong>{proposal.agentName}</strong>
        <span>{stateLabels[proposal.state]}</span>
      </header>
      <p className="huddle-proposal-target">
        {proposal.projectTitle}
        {!proposal.projectId && " (removed)"}
        {proposal.taskKey
          ? ` · ${proposal.taskKey}${!proposal.taskId ? " (removed)" : ""}`
          : " · New Task proposal"}
      </p>
      {draft ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void save("edit");
          }}
        >
          <label htmlFor={`${fieldId}-title`}>Title</label>
          <input
            id={`${fieldId}-title`}
            value={draft.title}
            maxLength={200}
            required
            disabled={mutation.isLoading || !editable}
            onChange={(event) =>
              setDraft({ ...draft, title: event.target.value })
            }
          />
          <label htmlFor={`${fieldId}-brief`}>Requested outcome</label>
          <textarea
            id={`${fieldId}-brief`}
            value={draft.brief}
            maxLength={4000}
            rows={5}
            required
            disabled={mutation.isLoading || !editable}
            onChange={(event) =>
              setDraft({ ...draft, brief: event.target.value })
            }
          />
          {changed && (
            <p role="alert">
              Someone updated this draft. Your edits are kept here; cancel to
              load their version.
            </p>
          )}
          {!editable && <p>This draft is no longer editable.</p>}
          <div className="huddle-proposal-actions">
            <Button
              className="huddle-action-primary"
              type="submit"
              size="sm"
              disabled={
                !editable ||
                changed ||
                mutation.isLoading ||
                !draft.title.trim() ||
                !draft.brief.trim()
              }
            >
              {mutation.isLoading ? "Saving…" : "Save draft"}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={mutation.isLoading}
              onClick={() => {
                setDraft(null);
                setError("");
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <>
          <h3>{proposal.title}</h3>
          <p className="huddle-proposal-brief">{proposal.brief}</p>
          {editable && (
            <div className="huddle-proposal-actions">
              <Button
                className="huddle-action-primary"
                type="button"
                size="sm"
                disabled={mutation.isLoading}
                onClick={() => setSubmitting(true)}
              >
                Submit proposal
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={mutation.isLoading}
                onClick={() => {
                  setDraft({
                    title: proposal.title,
                    brief: proposal.brief,
                    revision: proposal.revision,
                  });
                  setError("");
                }}
              >
                Edit draft
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={mutation.isLoading}
                onClick={() => void save("reject")}
              >
                Reject
              </Button>
            </div>
          )}
          {proposal.state === "pending" && !proposal.canEdit && (
            <p className="huddle-proposals-disclosure">
              A call moderator can review and submit this draft.
            </p>
          )}
        </>
      )}
      {error && <p role="alert">{error}</p>}
      {proposal.state === "submitted" && (
        <HuddleWorkProgress proposal={proposal} stale={stale} />
      )}
      {submitting && (
        <HuddleProposalSubmitDialog
          companyId={companyId}
          huddleId={huddleId}
          proposal={proposal}
          stale={stale}
          onClose={() => setSubmitting(false)}
          onRefresh={onRefresh}
        />
      )}
    </article>
  );
}
