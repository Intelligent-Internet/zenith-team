import { useRef, useState } from "react";
import type { Editor } from "@tiptap/react";
import type { components } from "@/shared/api/generated/api.generated";
import { PageConflictEditor } from "./page-conflict-editor";
import { BlockDocument } from "./block-editor-schema";
import {
  InlinePageSuggestion,
  suggestionPreview,
} from "./inline-page-suggestion";
import {
  usePageChangesQuery,
  usePageChangeDetailQuery,
  useReviewPageChangeMutation,
  type PageScope,
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
  onEditPage,
}: {
  scope: PageScope;
  editor?: Editor | null;
  canEdit: boolean;
  dirty?: boolean;
  initialChangeId?: string;
  onEditPage?: () => void;
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
      <p className="pc-review-empty" role="status">
        Loading suggestions…
      </p>
    );
  if (!visibleIds.length) return null;
  return (
    <>
      {dirty && (
        <p className="pc-review-notice" role="status">
          Saving your edits… Review actions will be available when the page is
          saved.
        </p>
      )}
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
          onEditPage={onEditPage}
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
  onEditPage,
}: {
  scope: PageScope;
  changeId: string;
  onEditPage?: () => void;
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
  const [conflictIndex, setConflictIndex] = useState<number | null>(null);
  const [reviewMode, setReviewMode] = useState<"accept" | "merge">("accept");
  const receipt = useRef<ReviewInput | null>(null);
  const detail = comparison.currentData;
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
      setNotice(
        input.action === "apply"
          ? "Changes merged into your page."
          : input.action === "reject"
            ? "Suggestions dismissed."
            : "Suggestions resolved.",
      );
      await comparison.refetch();
    } catch {
      setError(
        "The page may have changed, or your decision couldn’t be confirmed. Retry your decision or refresh before continuing.",
      );
    } finally {
      onBusyChange("");
    }
  };
  const act = (action: ReviewInput["action"], indexes: number[]) => {
    if (!detail || disabled || !indexes.length) return;
    if (action === "apply") {
      const conflict = detail.suggestions.find(
        (item) =>
          indexes.includes(item.index) &&
          (item.status !== "pending" || item.competingChangeIds.length > 0),
      );
      if (conflict) {
        setReviewMode("accept");
        setConflictIndex(conflict.index);
        return;
      }
    }
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
  const suggestions = detail.suggestions.filter((suggestion) =>
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
                  <div className="pc-review-change-heading">
                    <span className="pc-review-status">
                      {suggestion.status === "needs_update"
                        ? "Needs revision"
                        : suggestion.status === "already_reflected"
                          ? "Already in page"
                          : "Conflicting change"}
                    </span>
                  </div>
                )}
                <div className="pc-review-diff">
                  {suggestion.before && !originalVisible && (
                    <section
                      className="pc-review-before"
                      aria-label="Original content"
                    >
                      <BlockDocument
                        scope={scope}
                        content={{
                          type: "doc",
                          content: [suggestionPreview(suggestion.before)],
                        }}
                        mode="comparison"
                      />
                    </section>
                  )}
                  {suggestion.after && (
                    <section
                      className="pc-review-after"
                      aria-label="Suggested content"
                    >
                      <BlockDocument
                        scope={scope}
                        content={{
                          type: "doc",
                          content: [suggestionPreview(suggestion.after)],
                        }}
                        mode="comparison"
                      />
                    </section>
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
                {dirty && <small role="status">Saving your edits…</small>}
                {canEdit && conflictIndex === suggestion.index && (
                  <PageConflictEditor
                    key={`${suggestion.index}:${reviewMode}`}
                    initialMode={reviewMode}
                    scope={scope}
                    detail={detail}
                    suggestion={suggestion}
                    editor={editor}
                    dirty={dirty}
                    busy={!!busyId}
                    onBusyChange={onBusyChange}
                    onClose={() => setConflictIndex(null)}
                    onEditPage={onEditPage}
                  />
                )}
                <footer className="pc-review-change-actions">
                  {canEdit && (
                    <>
                      <button
                        className="pc-review-merge"
                        disabled={
                          disabled || suggestion.status === "needs_update"
                        }
                        title={
                          suggestion.status === "needs_update"
                            ? "Use Merge to revise this outdated version"
                            : suggestion.group.length > 1
                              ? "Accept this version and its linked changes"
                              : undefined
                        }
                        onClick={() => {
                          if (suggestion.status === "already_reflected") {
                            act("resolve", suggestion.group);
                            return;
                          }
                          const conflict = detail.suggestions.some(
                            (item) =>
                              suggestion.group.includes(item.index) &&
                              (item.status !== "pending" ||
                                item.competingChangeIds.length > 0),
                          );
                          if (conflict) {
                            setReviewMode("accept");
                            setConflictIndex(suggestion.index);
                          } else act("apply", suggestion.group);
                        }}
                      >
                        Accept this version
                      </button>
                      <button
                        disabled={disabled}
                        title={
                          suggestion.group.length > 1
                            ? "Reject this version and its linked changes"
                            : undefined
                        }
                        onClick={() => act("reject", suggestion.group)}
                      >
                        Reject this version
                      </button>
                      {detail.suggestions.some(
                        (item) =>
                          suggestion.group.includes(item.index) &&
                          (item.status === "needs_update" ||
                            item.competingChangeIds.length > 0),
                      ) && (
                        <button
                          disabled={disabled}
                          onClick={() => {
                            setReviewMode("merge");
                            setConflictIndex(suggestion.index);
                          }}
                        >
                          Merge
                        </button>
                      )}
                    </>
                  )}
                </footer>
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
    <div className="pc-review-error" role="alert">
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
