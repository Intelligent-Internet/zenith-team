import { useEffect } from "react";
import { skipToken } from "@reduxjs/toolkit/query";
import type { components } from "@/shared/api/generated/api.generated";
import {
  useProjectPresentationQuery,
  usePageChangeDetailQuery,
} from "@/features/project-canvas/api/page-api";
import {
  BlockDocument,
  type Block,
} from "@/features/project-canvas/documents/block-editor-schema";
import type { RunOutput } from "@/features/run-deliverables/model/run-deliverable";
import { Link } from "react-router";

type Decision = NonNullable<
  components["schemas"]["TaskResultSelectionInput"]["acceptChanges"]
>[number];

export function TaskResultProposal({
  companyId,
  output,
  decision,
  onDecision,
}: {
  companyId: string;
  output: RunOutput;
  decision?: Decision;
  onDecision: (decision: Decision | undefined) => void;
}) {
  const location = output.presentationUrl
    ? new URL(output.presentationUrl, "https://zenith.invalid")
    : null;
  const projectId = location?.pathname.match(
    /^\/projects\/([^/]+)\/pages$/,
  )?.[1];
  const id = location?.searchParams.get("presentationId");
  const capture = useProjectPresentationQuery(
    projectId && id ? { companyId, projectId, id } : skipToken,
  );
  const pageId = capture.currentData?.pageId;
  const changeId = capture.currentData?.changeId;
  const scope = projectId && pageId ? { companyId, projectId, pageId } : null;
  const detail = usePageChangeDetailQuery(
    scope && changeId ? { ...scope, id: changeId } : skipToken,
  );
  const fingerprint = detail.currentData?.fingerprint;
  useEffect(() => {
    if (decision && fingerprint && decision.input.fingerprint !== fingerprint)
      onDecision(undefined);
  }, [decision, fingerprint, onDecision]);
  if (!projectId || !id) return null;
  if (capture.isError || detail.isError)
    return (
      <p role="alert">
        The proposal cannot be reviewed with your current access.
      </p>
    );
  if (!capture.currentData)
    return <p role="status">Loading delivered proposal…</p>;
  if (!changeId) return null;
  if (!detail.currentData || !scope)
    return <p role="status">Loading changes for review…</p>;
  const value = detail.currentData;
  const requiredIndexes = capture.currentData.requiredSuggestionIndexes;
  const pending = value.suggestions.filter(
    (item) => requiredIndexes.includes(item.index) && item.status !== "applied",
  );
  if (!pending.length) return null;
  const canApply = pending.every((item) => item.status === "pending");
  const needsNewDelivery = pending.some((item) =>
    [
      "rejected",
      "already_reflected",
      "superseded",
      "resolved",
      "undone",
    ].includes(item.status),
  );
  const checked = decision?.input.fingerprint === value.fingerprint;
  return (
    <section
      className="my-3 rounded-lg border border-[var(--workspace-divider)] p-3"
      aria-label={`Review changes to ${output.title}`}
    >
      <h4 className="mb-2 text-sm font-semibold">Changes to {output.title}</h4>
      {pending.map((suggestion) => (
        <div key={suggestion.index} className="my-3 grid gap-3 md:grid-cols-2">
          {(suggestion.current ?? suggestion.before) && (
            <section aria-label="Current content">
              <p className="text-xs font-semibold">Current</p>
              <BlockDocument
                scope={scope}
                content={
                  {
                    type: "doc",
                    content: [suggestion.current ?? suggestion.before],
                  } as Block
                }
                mode="comparison"
              />
            </section>
          )}
          {suggestion.after && (
            <section aria-label="Proposed content">
              <p className="text-xs font-semibold">Proposed</p>
              <BlockDocument
                scope={scope}
                content={{ type: "doc", content: [suggestion.after] } as Block}
                mode="comparison"
              />
            </section>
          )}
          {suggestion.type === "theme" && (
            <p className="text-sm">
              Theme: {value.beforeTheme} → {value.afterTheme}
            </p>
          )}
          {suggestion.status !== "pending" && (
            <p className="text-sm md:col-span-2">
              {suggestion.status === "rejected"
                ? "This delivered suggestion was rejected."
                : suggestion.status === "already_reflected"
                  ? "This change is already in the document, but was not accepted as part of this delivery."
                  : suggestion.status === "superseded"
                    ? "A newer proposal replaces this delivered suggestion."
                    : ["resolved", "undone"].includes(suggestion.status)
                      ? "This proposal no longer supplies an accepted change for this delivery."
                      : "The document has changed. Ask the Agent to revise this suggestion."}
            </p>
          )}
        </div>
      ))}
      {canApply ? (
        <label className="flex gap-2 text-sm">
          <input
            type="checkbox"
            checked={checked}
            onChange={(event) =>
              onDecision(
                event.target.checked
                  ? {
                      projectId: scope.projectId,
                      pageId: scope.pageId,
                      changeId,
                      input: {
                        requestKey: crypto.randomUUID(),
                        fingerprint: value.fingerprint,
                        action: "apply",
                        indexes: pending.map((item) => item.index),
                      },
                    }
                  : undefined,
              )
            }
          />
          Accept these changes with the Task result
        </label>
      ) : (
        <p className="text-sm">
          {needsNewDelivery
            ? "To complete a Task that requires these updates, submit and select a new delivery of the shared document or the revised proposal."
            : "Review these suggestions in the document before accepting them with the Task result."}
        </p>
      )}
      {output.liveUrl && (
        <Link
          className="mt-2 block text-sm underline"
          to={`${output.liveUrl}&changeId=${encodeURIComponent(changeId)}`}
        >
          Review in document
        </Link>
      )}
    </section>
  );
}
