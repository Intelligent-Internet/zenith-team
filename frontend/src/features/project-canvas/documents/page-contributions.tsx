import { useLocalSuggestions } from "./use-local-suggestions";
import { hasTextDiff, SuggestionTextDiff } from "./suggestion-text-diff";
import { useRef, useState } from "react";
import type { Editor } from "@tiptap/react";
import type { components } from "@/shared/api/generated/api.generated";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import { BlockDocument } from "./block-editor-schema";
import {
  InlinePageSuggestion,
  suggestionPreview,
} from "./inline-page-suggestion";
import {
  usePageChangesQuery,
  usePageChangeDetailQuery,
  useReviewPageChangeMutation,
  type PageReviewScope,
} from "../api/page-api";
import "./page-suggestions.css";

type ReviewInput = components["schemas"]["PageChangeReviewInput"];
const reviewStatuses = new Set([
  "pending",
  "needs_update",
  "already_reflected",
]);

export function PageContributions({
  editor,
  scope,
  canEdit,
  dirty = false,
  initialChangeId = "",
}: {
  scope: PageReviewScope;
  editor?: Editor | null;
  canEdit: boolean;
  dirty?: boolean;
  initialChangeId?: string;
}) {
  const feed = usePageChangesQuery(scope);
  const pending = feed.currentData?.pending ?? [];
  const [busyId, setBusyId] = useState("");
  const visibleIds = [
    ...new Set([
      ...pending.map((change) => change.id),
      ...(initialChangeId ? [initialChangeId] : []),
    ]),
  ];
  if (feed.isError)
    return (
      <p className="po-error" role="alert">
        Suggestions couldn’t be loaded.{" "}
        <button onClick={() => void feed.refetch()}>Retry</button>
      </p>
    );
  if (feed.isLoading)
    return (
      <p
        className="flex items-center gap-[8px] p-[16px_0] text-[var(--pc-muted,_var(--muted-foreground))]"
        role="status"
      >
        Loading suggestions…
      </p>
    );
  if (!visibleIds.length) return null;
  return (
    <>
      {visibleIds.map((id) => (
        <PageProposal
          key={id}
          scope={scope}
          changeId={id}
          canEdit={canEdit}
          dirty={dirty}
          busyId={busyId}
          onBusyChange={setBusyId}
          editor={editor}
        />
      ))}
    </>
  );
}

function PageProposal({
  scope,
  changeId,
  canEdit,
  dirty,
  busyId,
  onBusyChange,
  editor,
}: {
  scope: PageReviewScope;
  changeId: string;
  canEdit: boolean;
  dirty: boolean;
  busyId: string;
  onBusyChange: (id: string) => void;
  editor?: Editor | null;
}) {
  const comparison = usePageChangeDetailQuery(
    { ...scope, id: changeId },
    { refetchOnMountOrArgChange: true },
  );
  const [review, reviewing] = useReviewPageChangeMutation();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [revisionIndex, setRevisionIndex] = useState<number | null>(null);
  const [feedback, setFeedback] = useState("");
  const receipt = useRef<ReviewInput | null>(null);
  const detail = comparison.currentData;
  const localSuggestions = useLocalSuggestions(editor, detail?.suggestions);
  const disabled = dirty || !!busyId || comparison.isFetching || !!error;
  const send = async (input: ReviewInput) => {
    if (dirty || busyId || reviewing.isLoading) return;
    setError("");
    setNotice("");
    receipt.current = input;
    onBusyChange(changeId);
    try {
      await review({ ...scope, id: changeId, input }).unwrap();
      receipt.current = null;
      setRevisionIndex(null);
      setFeedback("");
      setNotice(
        input.action === "apply"
          ? "Changes accepted."
          : input.action === "reject"
            ? "Suggestions rejected."
            : input.action === "request_changes"
              ? "Revision requested. The Agent will reply in the page comments."
              : "Suggestions accepted.",
      );
      await comparison.refetch();
    } catch (cause) {
      setError(
        getZenithTeamQueryErrorMessage(
          cause,
          "Your decision couldn’t be confirmed. Retry it or refresh before continuing.",
        ),
      );
    } finally {
      onBusyChange("");
    }
  };
  const act = (action: ReviewInput["action"], indexes: number[]) => {
    if (!detail || disabled || !indexes.length) return;
    void send({
      requestKey: crypto.randomUUID(),
      fingerprint: detail.fingerprint,
      action,
      indexes,
    });
  };
  if (comparison.isError)
    return (
      <p className="po-error" role="alert">
        This proposal couldn’t be loaded.{" "}
        <button onClick={() => void comparison.refetch()}>Retry</button>
      </p>
    );
  if (!detail)
    return (
      <p className="pc-review-notice" role="status">
        Loading changes…
      </p>
    );
  const suggestions = localSuggestions.filter((suggestion) =>
    reviewStatuses.has(suggestion.status),
  );
  return (
    <article
      className="pc-review-proposal"
      aria-label={`${detail.agentName || "Agent"} proposal`}
      aria-busy={reviewing.isLoading}
    >
      {notice && (
        <p className="pc-review-notice" role="status">
          {notice}
        </p>
      )}
      {error && (
        <ReviewError
          message={error}
          disabled={dirty || !!busyId}
          onRetry={() => receipt.current && void send(receipt.current)}
          onRefresh={() => {
            receipt.current = null;
            setError("");
            void comparison.refetch();
          }}
        />
      )}
      <div className="pc-review-changes">
        {suggestions.map((suggestion) => (
          <InlinePageSuggestion
            key={suggestion.index}
            editor={editor}
            suggestion={suggestion}
            after={detail.after}
            agentId={detail.agentId || detail.agentName || "agent"}
            agentName={detail.agentName || "Agent"}
            priority={`${detail.createdAt}:${detail.id}`}
            busy={!!busyId}
          >
            {(originalVisible) => (
              <article className="pc-inline-review">
                {(suggestion.status === "needs_update" ||
                  suggestion.status === "already_reflected" ||
                  suggestion.competingChangeIds.length > 0) && (
                  <div className="flex items-center flex-wrap gap-[6px] mb-[4px] text-[var(--pc-muted,_var(--muted-foreground))] [font:400_11px/1.5_var(--font-sans,_sans-serif)]">
                    <span className="ml-auto">
                      {suggestion.status === "needs_update"
                        ? detail.status === "needs_update"
                          ? "Revision requested"
                          : suggestion.type === "update" && !suggestion.current
                            ? "Target removed—no changes to apply. Reject or ask the Agent to revise."
                            : "This suggestion no longer fits the page. Ask the Agent to revise it."
                        : suggestion.status === "already_reflected"
                          ? "Already in page"
                          : "Other suggestions for this passage"}
                    </span>
                  </div>
                )}
                <div className="pc-review-diff">
                  {hasTextDiff(
                    suggestion.current ?? suggestion.before,
                    suggestion.after,
                  ) ? (
                    <SuggestionTextDiff
                      before={(suggestion.current ?? suggestion.before)!}
                      after={suggestion.after!}
                    />
                  ) : (
                    <>
                      {(suggestion.current ?? suggestion.before) &&
                        !originalVisible && (
                          <section
                            className="pc-review-before"
                            aria-label="Current content"
                          >
                            <BlockDocument
                              scope={scope}
                              content={{
                                type: "doc",
                                content: [
                                  suggestionPreview(
                                    (suggestion.current ?? suggestion.before)!,
                                  ),
                                ],
                              }}
                              mode="comparison"
                            />
                          </section>
                        )}
                      {(suggestion.after ||
                        (suggestion.type === "move" && suggestion.before)) && (
                        <section
                          className="[background:color-mix(in_srgb,_#398454_8%,_transparent)]"
                          aria-label="Suggested content"
                        >
                          <BlockDocument
                            scope={scope}
                            content={{
                              type: "doc",
                              content: [
                                suggestionPreview(
                                  suggestion.after ?? suggestion.before!,
                                ),
                              ],
                            }}
                            mode="comparison"
                          />
                        </section>
                      )}
                    </>
                  )}
                  {suggestion.type === "theme" && (
                    <p>
                      {detail.beforeTheme} → {detail.afterTheme}
                    </p>
                  )}
                </div>
                {error && (
                  <ReviewError
                    message={error}
                    disabled={dirty || !!busyId}
                    onRetry={() =>
                      receipt.current && void send(receipt.current)
                    }
                    onRefresh={() => {
                      receipt.current = null;
                      setError("");
                      void comparison.refetch();
                    }}
                  />
                )}
                {canEdit && revisionIndex === suggestion.index ? (
                  <form
                    className="mt-3 flex flex-col gap-2"
                    onSubmit={(event) => {
                      event.preventDefault();
                      if (disabled || !feedback.trim()) return;
                      void send({
                        requestKey: crypto.randomUUID(),
                        fingerprint: detail.fingerprint,
                        action: "request_changes",
                        indexes: suggestion.group,
                        feedback: feedback.trim(),
                      });
                    }}
                  >
                    <label htmlFor={`revision-${changeId}-${suggestion.index}`}>
                      What should the Agent change?
                    </label>
                    <textarea
                      id={`revision-${changeId}-${suggestion.index}`}
                      autoFocus
                      required
                      maxLength={4000}
                      rows={3}
                      value={feedback}
                      disabled={!!busyId}
                      onChange={(event) => setFeedback(event.target.value)}
                      className="w-full rounded-md border border-border bg-background p-2 text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-foreground"
                    />
                    <div className="pc-review-change-actions">
                      <button
                        className="pc-review-approve"
                        disabled={disabled || !feedback.trim()}
                        type="submit"
                      >
                        Request revision
                      </button>
                      <button
                        type="button"
                        disabled={!!busyId}
                        onClick={() => setRevisionIndex(null)}
                      >
                        Cancel
                      </button>
                    </div>
                  </form>
                ) : (
                  canEdit && (
                    <footer className="pc-review-change-actions">
                      <button
                        className="pc-review-approve"
                        disabled={
                          disabled ||
                          suggestion.group.some(
                            (index) =>
                              detail.suggestions[index]?.status ===
                              "needs_update",
                          )
                        }
                        title={
                          suggestion.group.length > 1
                            ? "Accept this suggestion and its linked changes"
                            : undefined
                        }
                        onClick={() => act("apply", suggestion.group)}
                      >
                        Accept
                      </button>
                      <button
                        disabled={disabled}
                        onClick={() => act("reject", suggestion.group)}
                      >
                        Reject
                      </button>
                      <button
                        disabled={disabled || !detail.agentId}
                        title={
                          !detail.agentId
                            ? "The authoring Agent is unavailable"
                            : undefined
                        }
                        onClick={() => setRevisionIndex(suggestion.index)}
                      >
                        Revise
                      </button>
                    </footer>
                  )
                )}
              </article>
            )}
          </InlinePageSuggestion>
        ))}
      </div>
    </article>
  );
}

function ReviewError({
  message,
  disabled,
  onRetry,
  onRefresh,
}: {
  message: string;
  disabled: boolean;
  onRetry: () => void;
  onRefresh: () => void;
}) {
  return (
    <div
      className="pc-review-error p-[8px_0] text-[var(--destructive)]"
      role="alert"
    >
      <p>{message}</p>
      <button disabled={disabled} onClick={onRetry}>
        Retry decision
      </button>
      <button disabled={disabled} onClick={onRefresh}>
        Refresh suggestions
      </button>
    </div>
  );
}
