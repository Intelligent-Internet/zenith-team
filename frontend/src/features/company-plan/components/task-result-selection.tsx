import { useUpdateWorkspaceTaskMutation } from "@/shared/api/task-api";
import { TaskResultProposal } from "./task-result-proposal";
import { useState } from "react";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useGetTaskResultSelectionQuery } from "../api/task-result-selection-api";

import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import type { TaskDto } from "@/shared/api/types";
import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import type { components } from "@/shared/api/generated/api.generated";
import { hasProjectPermission, useCan } from "@/shared/permissions";
import { Button } from "@/shared/ui/button";

type Decision = NonNullable<
  components["schemas"]["TaskResultSelectionInput"]["acceptChanges"]
>[number];

type Selection = components["schemas"]["TaskResultSelection"];

export function TaskResultSelection({
  task,
  items,
}: {
  task: TaskDto;
  items: ConversationArtifact[];
}) {
  const query = useGetTaskResultSelectionQuery({
    companyId: task.companyId,
    taskId: task.id,
  });
  const viewer = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  const canEditPolicy =
    hasProjectPermission(task.access, "project.tasks.write") &&
    (canAssign || task.assignee.id === viewer);
  const canEdit = canEditPolicy;
  if (query.isError)
    return (
      <p role="alert">
        Result selection could not be loaded.{" "}
        <button type="button" onClick={() => void query.refetch()}>
          Retry
        </button>
      </p>
    );
  if (!query.currentData) return <p role="status">Loading result selection…</p>;
  return (
    <SelectionForm
      key={`${task.id}:${query.currentData.taskRevision}`}
      task={task}
      items={items}
      selection={query.currentData}
      canEdit={canEdit && !query.currentData.hasUnavailable}
      canEditPolicy={canEditPolicy}
    />
  );
}

function SelectionForm({
  task,
  items,
  selection,
  canEdit,
  canEditPolicy,
}: {
  task: TaskDto;
  items: ConversationArtifact[];
  selection: Selection;
  canEdit: boolean;
  canEditPolicy: boolean;
}) {
  const [ids, setIds] = useState(selection.outputIds);
  const [decisions, setDecisions] = useState<
    Record<string, Decision | undefined>
  >({});
  const hasAcceptedChanges = ids.some((id) => decisions[id] !== undefined);
  const [error, setError] = useState("");
  const [proposalOnly, setProposalOnly] = useState(selection.proposalOnly);
  const [reviewRequired, setReviewRequired] = useState(
    selection.reviewRequired,
  );
  async function savePolicy() {
    setError("");
    try {
      await update({
        companyId: task.companyId,
        taskId: task.id,
        request: {
          expectedRevision: selection.taskRevision,
          resultPolicy: { proposalOnly, reviewRequired },
        },
      }).unwrap();
    } catch (cause) {
      setError(
        getZenithTeamQueryErrorMessage(
          cause,
          "The Task has changed or execution has started. Refresh before changing its review policy.",
        ),
      );
    }
  }

  const [update, mutation] = useUpdateWorkspaceTaskMutation();
  const [requestKey] = useState(() => crypto.randomUUID());
  const changed = ids.join(",") !== selection.outputIds.join(",");
  const canApprove = canEdit;
  const eligible = items.filter((item) => item.runStatus === "completed");
  async function save(approve: boolean) {
    setError("");
    try {
      const accepted = approve
        ? consolidateDecisions(
            ids.flatMap((id) => (decisions[id] ? [decisions[id]!] : [])),
          )
        : [];
      await update({
        companyId: task.companyId,
        taskId: task.id,
        request: {
          expectedRevision: selection.taskRevision,
          resultSelection: {
            requestKey,
            outputIds: ids,
            ...(approve && accepted.length ? { acceptChanges: accepted } : {}),
          },
          ...(approve ? { status: "done" as const } : {}),
        },
      }).unwrap();
    } catch (cause) {
      setError(
        getZenithTeamQueryErrorMessage(
          cause,
          "Could not save this selection. Required changes must be accepted before approval; refresh if the Task has changed.",
        ),
      );
    }
  }
  return (
    <section
      className="my-4 rounded-lg border border-[var(--workspace-divider)] p-4"
      aria-label="Selected Task results"
    >
      {selection.hasUnavailable && (
        <p role="alert">
          Some selected deliveries are unavailable with your current access.
          Approval is unavailable until access is restored.
        </p>
      )}
      <h3 className="m-0 text-sm font-semibold">
        {selection.approvedAt && !changed
          ? "Approved results"
          : task.status === "done" && !selection.reviewRequired && !changed
            ? "Completed results"
            : "Select results for review"}
      </h3>
      <p className="my-2 text-sm text-[var(--workspace-native-text-muted)]">
        {selection.proposalOnly
          ? "Approval endorses the proposal. It does not apply its changes."
          : "Accept required document changes before approving these deliveries."}{" "}
        Later deliveries do not replace this selection.
      </p>
      {selection.policyEditable && canEditPolicy && (
        <fieldset
          disabled={mutation.isLoading}
          className="mb-4 grid gap-2 border-0 p-0"
        >
          <legend className="mb-2 text-sm font-semibold">Result policy</legend>
          <label className="flex gap-2 text-sm">
            <input
              type="checkbox"
              checked={proposalOnly}
              onChange={(event) => setProposalOnly(event.target.checked)}
            />
            Proposal only; application is not required
          </label>
          <label className="flex gap-2 text-sm">
            <input
              type="checkbox"
              checked={reviewRequired}
              onChange={(event) => setReviewRequired(event.target.checked)}
            />
            Require Task result approval
          </label>
          <Button
            size="sm"
            variant="outline"
            disabled={
              proposalOnly === selection.proposalOnly &&
              reviewRequired === selection.reviewRequired
            }
            onClick={() => void savePolicy()}
          >
            Save policy
          </Button>
        </fieldset>
      )}
      <fieldset
        disabled={!canEdit || mutation.isLoading}
        className="m-0 grid gap-2 border-0 p-0"
      >
        <legend className="sr-only">Delivered results</legend>
        {eligible.map((item) => (
          <label
            key={item.artifact.id}
            className="flex items-start gap-2 text-sm"
          >
            <input
              type="checkbox"
              checked={ids.includes(item.artifact.id)}
              onChange={(event) =>
                setIds((previous) =>
                  event.target.checked
                    ? [...previous, item.artifact.id]
                    : previous.filter((id) => id !== item.artifact.id),
                )
              }
            />
            <span>
              {item.artifact.title}
              {item.artifact.freshness === "stale" ? " · Stale" : ""}
            </span>
          </label>
        ))}
      </fieldset>
      {ids.length > 1 && (
        <div className="mt-3">
          <p className="mb-2 text-sm font-medium">Result order</p>
          <ol className="m-0 grid list-decimal gap-2 pl-5">
            {ids.map((id, index) => (
              <li key={id} className="text-sm">
                <div className="flex items-center justify-between gap-3">
                  <span>
                    {items.find((item) => item.artifact.id === id)?.artifact
                      .title ?? "Selected delivery"}
                    {index === 0 && (
                      <span className="ml-2 text-[var(--workspace-native-text-muted)]">
                        Primary
                      </span>
                    )}
                  </span>
                  {canEdit && (
                    <div className="flex shrink-0 gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={mutation.isLoading || index === 0}
                        aria-label={`Move result ${index + 1} up`}
                        onClick={() =>
                          setIds((previous) => {
                            const next = [...previous];
                            [next[index - 1], next[index]] = [
                              next[index],
                              next[index - 1],
                            ];
                            return next;
                          })
                        }
                      >
                        Move up
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={
                          mutation.isLoading || index === ids.length - 1
                        }
                        aria-label={`Move result ${index + 1} down`}
                        onClick={() =>
                          setIds((previous) => {
                            const next = [...previous];
                            [next[index], next[index + 1]] = [
                              next[index + 1],
                              next[index],
                            ];
                            return next;
                          })
                        }
                      >
                        Move down
                      </Button>
                    </div>
                  )}
                </div>
              </li>
            ))}
          </ol>
        </div>
      )}
      {ids.some((id) => !items.some((item) => item.artifact.id === id)) && (
        <p className="text-sm">
          Previously selected deliveries outside the loaded list remain
          selected.
        </p>
      )}
      {canEdit &&
        !selection.proposalOnly &&
        eligible
          .filter((item) => ids.includes(item.artifact.id))
          .map((item) => (
            <TaskResultProposal
              key={item.artifact.id}
              companyId={task.companyId}
              output={item.artifact}
              decision={decisions[item.artifact.id]}
              onDecision={(decision) =>
                setDecisions((previous) => ({
                  ...previous,
                  [item.artifact.id]: decision,
                }))
              }
            />
          ))}
      {canEdit && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={
              !ids.length ||
              mutation.isLoading ||
              (!changed && !!selection.selectionId)
            }
            onClick={() => void save(false)}
          >
            Save selection
          </Button>
          {(selection.reviewRequired ? canApprove : canEdit) &&
            (selection.reviewRequired
              ? !selection.approvedAt || changed
              : task.status !== "done" || changed) && (
              <Button
                size="sm"
                disabled={!ids.length || mutation.isLoading}
                onClick={() => void save(true)}
              >
                {selection.reviewRequired
                  ? hasAcceptedChanges
                    ? "Accept changes and approve results"
                    : "Approve selected results"
                  : hasAcceptedChanges
                    ? "Accept changes and complete Task"
                    : "Complete Task with selected results"}
              </Button>
            )}
        </div>
      )}
      {error && (
        <p role="alert" className="mt-2 text-sm">
          {error}
        </p>
      )}
    </section>
  );
}

function consolidateDecisions(decisions: Decision[]): Decision[] {
  const proposals = new Map<string, Decision>();
  for (const decision of decisions) {
    const key = JSON.stringify([
      decision.projectId,
      decision.pageId,
      decision.changeId,
    ]);
    const previous = proposals.get(key);
    if (!previous) {
      proposals.set(key, decision);
      continue;
    }
    if (previous.input.fingerprint !== decision.input.fingerprint) {
      throw new Error("The proposal has changed. Refresh and review it again.");
    }
    const indexes =
      previous.input.indexes?.length && decision.input.indexes?.length
        ? [
            ...new Set([...previous.input.indexes, ...decision.input.indexes]),
          ].sort((a, b) => a - b)
        : undefined;
    proposals.set(key, {
      ...previous,
      input: { ...previous.input, indexes },
    });
  }
  return [...proposals.values()];
}
