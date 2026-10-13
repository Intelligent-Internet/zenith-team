import { DocumentHistoryDiff } from "./document-history-diff";
import { useState } from "react";
import { Link } from "react-router";
import { skipToken } from "@reduxjs/toolkit/query";
import { Clock3, Undo2 } from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { MarkdownView } from "../previews/markdown-view";
import {
  useDocumentHistoryQuery,
  useDocumentHistoryStateQuery,
  useRestoreDocumentHistoryMutation,
  useNameDocumentHistoryMutation,
  type HistoryScope,
  type HistoryEntry,
} from "../api/document-history-api";
import "./document-history.css";

function timestamp(value: string) {
  return new Date(value).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}
function iso(value: string) {
  return value && !Number.isNaN(Date.parse(value))
    ? new Date(value).toISOString()
    : undefined;
}
function entryTitle(entry: HistoryEntry) {
  if (entry.label) return entry.label;
  if (entry.suggestionReview)
    return entry.suggestionReview.action === "apply"
      ? "Accepted suggestions"
      : "Undid accepted suggestions";
  if (entry.changeType === "initial") return "Initial document";
  if (entry.changeType === "restore") return "Document restored";
  return entry.actorName;
}
export function DocumentHistoryPanel({
  scope,
  versionId,
  disabled,
  onRestored,
  onReviewSuggestion,
}: {
  scope: HistoryScope;
  versionId: string;
  disabled: boolean;
  onRestored: () => void;
  onReviewSuggestion?: () => void;
}) {
  const target = { scope, versionId };
  const [actorId, setActor] = useState("");
  const [after, setAfter] = useState("");
  const [before, setBefore] = useState("");
  const [group, setGroup] = useState("day");
  const [cursor, setCursor] = useState(0);
  const [selected, setSelected] = useState<HistoryEntry>();
  const [compare, setCompare] = useState(-1);
  const [selectedHead, setSelectedHead] = useState(0);
  const [label, setLabel] = useState("");
  const [confirmRestore, setConfirmRestore] = useState(false);
  const [error, setError] = useState("");
  const [restore, restoreState] = useRestoreDocumentHistoryMutation();
  const [name, nameState] = useNameDocumentHistoryMutation();
  const invalidRange = !!after && !!before && after >= before;
  const history = useDocumentHistoryQuery(
    {
      ...target,
      actorId,
      after: iso(after),
      before: iso(before),
      beforeSequence: cursor,
    },
    {
      skip: invalidRange,
      pollingInterval: 10000,
      refetchOnMountOrArgChange: true,
    },
  );
  const state = useDocumentHistoryStateQuery(
    selected ? { ...target, sequence: selected.sequence } : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const applicationCapture =
    compare === -1 ? state.currentData?.suggestionCapture : undefined;
  const comparisonSequence =
    compare === -1
      ? (selected?.suggestionReview?.beforeSequence ??
        state.currentData?.previousSequence ??
        0)
      : compare;
  const comparison = useDocumentHistoryStateQuery(
    selected && comparisonSequence && !applicationCapture
      ? { ...target, sequence: comparisonSequence }
      : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const groups = new Map<string, HistoryEntry[]>();
  for (const entry of history.currentData?.items ?? []) {
    const date = new Date(entry.savedAt);
    const key =
      group === "minute"
        ? timestamp(entry.savedAt)
        : date.toLocaleDateString(undefined, { dateStyle: "long" });
    groups.set(key, [...(groups.get(key) ?? []), entry]);
  }
  const select = (entry: HistoryEntry) => {
    setSelected(entry);
    setCompare(-1);
    setSelectedHead(history.currentData?.currentSequence ?? 0);
    setLabel(entry.label);
    setConfirmRestore(false);
    setError("");
  };
  return (
    <section className="pc-document-history" aria-label="Document history">
      <p className="pc-muted">
        Changes save automatically. Times show when changes reached the server.
      </p>
      <div className="pc-history-filters">
        <label>
          Contributor
          <select
            value={actorId}
            onChange={(e) => {
              setActor(e.target.value);
              setCursor(0);
            }}
          >
            <option value="">Everyone</option>
            {history.currentData?.contributors.map((person) => (
              <option
                key={`${person.actorType}:${person.actorId}`}
                value={person.actorId}
              >
                {person.actorName}
                {person.actorType === "agent" ? " (Agent)" : ""}
              </option>
            ))}
          </select>
        </label>
        <label>
          From
          <input
            type="datetime-local"
            value={after}
            onChange={(e) => {
              setAfter(e.target.value);
              setCursor(0);
            }}
          />
        </label>
        <label>
          Before
          <input
            type="datetime-local"
            value={before}
            onChange={(e) => {
              setBefore(e.target.value);
              setCursor(0);
            }}
          />
        </label>
        <label>
          Group by
          <select value={group} onChange={(e) => setGroup(e.target.value)}>
            <option value="day">Day</option>
            <option value="minute">Minute</option>
          </select>
        </label>
      </div>
      {invalidRange ? (
        <p role="alert">Choose an end time after the start time.</p>
      ) : history.isError ? (
        <div role="alert">
          History could not be loaded.{" "}
          <button onClick={() => void history.refetch()}>Retry</button>
        </div>
      ) : !history.currentData ? (
        <p role="status">Loading history…</p>
      ) : (
        <>
          {groups.size === 0 && <p>No saved changes match these filters.</p>}
          {[...groups].map(([date, entries]) => (
            <details key={date} className="pc-history-group">
              <summary>
                {date}
                <small>{entries.length} saved changes</small>
              </summary>
              {entries.map((entry) => (
                <button
                  key={entry.sequence}
                  className="pc-history-entry"
                  onClick={() => select(entry)}
                >
                  <Clock3 size={16} />
                  <span>
                    <strong>{entryTitle(entry)}</strong>
                    <small>
                      {new Date(entry.savedAt).toLocaleTimeString(undefined, {
                        hour: "2-digit",
                        minute: "2-digit",
                        second: "2-digit",
                      })}
                      {entry.label || entry.suggestionReview
                        ? ` · ${entry.actorName}`
                        : ""}
                      {entry.actorType === "agent" ? " · Agent" : ""}
                      {entry.suggestionReview
                        ? ` · Proposed by ${entry.suggestionReview.agentName}`
                        : ""}
                    </small>
                  </span>
                </button>
              ))}
            </details>
          ))}
          <div className="pc-history-pagination">
            {cursor > 0 && (
              <button onClick={() => setCursor(0)}>Latest changes</button>
            )}
            {!!history.currentData.nextBeforeSequence && (
              <button
                onClick={() =>
                  setCursor(history.currentData!.nextBeforeSequence)
                }
              >
                Older changes
              </button>
            )}
          </div>
        </>
      )}
      <Dialog
        open={!!selected}
        onOpenChange={(open) => {
          if (!open && !restoreState.isLoading) setSelected(undefined);
        }}
      >
        <DialogContent
          className="pc-history-dialog"
          overlayClassName="pc-history-overlay"
        >
          <DialogTitle>
            {selected?.label ||
              (selected ? timestamp(selected.savedAt) : "Saved document")}
          </DialogTitle>
          <DialogDescription>
            Shared document saved by {selected?.actorName}. Contributor filters
            select saves, not an isolated copy of that person's edits.
          </DialogDescription>
          {selected?.suggestionReview && (
            <div className="pc-muted">
              <p>
                {selected.suggestionReview.action === "apply"
                  ? "Accepted"
                  : "Undid"}{" "}
                suggestions proposed by {selected.suggestionReview.agentName}.
                {selected.suggestionReview.beforeTheme !==
                  selected.suggestionReview.resultTheme && (
                  <>
                    {" "}
                    Theme: {selected.suggestionReview.beforeTheme} →{" "}
                    {selected.suggestionReview.resultTheme}.
                  </>
                )}
              </p>
              <Link
                className="underline"
                onClick={() => {
                  setSelected(undefined);
                  onReviewSuggestion?.();
                }}
                to={`/projects/${selected.suggestionReview.projectId}/pages?pageId=${selected.suggestionReview.pageId}&changeId=${selected.suggestionReview.changeId}`}
              >
                View suggestion and review decisions
              </Link>
            </div>
          )}
          <div className="pc-history-preview-tools">
            <label>
              Compare with
              <select
                value={compare}
                onChange={(e) => setCompare(Number(e.target.value))}
              >
                <option value={-1}>
                  Previous save (changes in this entry)
                </option>
                <option value={0}>No comparison</option>
                {history.currentData?.items
                  .filter((entry) => entry.sequence !== selected?.sequence)
                  .map((entry) => (
                    <option key={entry.sequence} value={entry.sequence}>
                      {entry.label || timestamp(entry.savedAt)} ·{" "}
                      {entry.actorName}
                    </option>
                  ))}
              </select>
            </label>
            {history.currentData?.canEdit && (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  if (selected)
                    void name({ ...target, sequence: selected.sequence, label })
                      .unwrap()
                      .then(() => setSelected({ ...selected, label }))
                      .catch(() =>
                        setError("The checkpoint name could not be saved."),
                      );
                }}
              >
                <label>
                  Checkpoint name
                  <input
                    value={label}
                    maxLength={200}
                    placeholder="e.g. Approved proposal"
                    onChange={(e) => setLabel(e.target.value)}
                  />
                </label>
                <button className="pc-button" disabled={nameState.isLoading}>
                  Save name
                </button>
              </form>
            )}
          </div>
          {error && (
            <p role="alert">
              {error}{" "}
              <button
                onClick={() => {
                  setSelected(undefined);
                  void history.refetch();
                }}
              >
                Return to history
              </button>
            </p>
          )}
          {state.isError || (!applicationCapture && comparison.isError) ? (
            <p role="alert">
              The saved content could not be loaded.{" "}
              <button
                onClick={() => {
                  void state.refetch();
                  if (comparisonSequence) void comparison.refetch();
                }}
              >
                Retry
              </button>
            </p>
          ) : state.isFetching ||
            !state.currentData ||
            (!applicationCapture &&
              comparisonSequence &&
              (comparison.isFetching || !comparison.currentData)) ? (
            <p role="status">Loading saved content…</p>
          ) : applicationCapture ? (
            <div className="pc-history-preview is-comparing">
              <p className="pc-muted">
                The exact before and after content retained with this review
                decision. Sources you can no longer access are withheld.{" "}
                <button className="underline" onClick={() => setCompare(0)}>
                  View the shared save
                </button>
              </p>
              <DocumentHistoryDiff
                before={applicationCapture.beforeMarkdown}
                after={applicationCapture.resultMarkdown}
              />
              <article>
                <h4>Before the decision</h4>
                <MarkdownView>{applicationCapture.beforeMarkdown}</MarkdownView>
              </article>
              <article>
                <h4>After the decision</h4>
                <MarkdownView>{applicationCapture.resultMarkdown}</MarkdownView>
              </article>
            </div>
          ) : (
            <div
              className={`pc-history-preview ${comparisonSequence ? "is-comparing" : ""}`}
            >
              {comparisonSequence > 0 && comparison.currentData && (
                <DocumentHistoryDiff
                  before={comparison.currentData.markdown}
                  after={state.currentData.markdown}
                />
              )}
              {comparisonSequence > 0 && comparison.currentData && (
                <article>
                  <h4>Comparison</h4>
                  <MarkdownView>{comparison.currentData.markdown}</MarkdownView>
                </article>
              )}
              <article>
                <h4>
                  {comparisonSequence
                    ? "Selected state"
                    : "Document at this time"}
                </h4>
                <MarkdownView>{state.currentData.markdown}</MarkdownView>
              </article>
            </div>
          )}
          {history.currentData?.canEdit && !applicationCapture && (
            <footer>
              {confirmRestore ? (
                <>
                  <p>
                    Restore this content for everyone? Current edits will remain
                    in history.
                  </p>
                  <button
                    className="pc-button"
                    disabled={disabled || restoreState.isLoading}
                    onClick={() => {
                      if (!selected || !history.currentData) return;
                      void restore({
                        ...target,
                        sequence: selected.sequence,
                        expectedSequence: selectedHead,
                      })
                        .unwrap()
                        .then(() => {
                          setSelected(undefined);
                          onRestored();
                        })
                        .catch(() => {
                          setError(
                            "The document changed or access was revoked. Refresh history before restoring.",
                          );
                          setConfirmRestore(false);
                        });
                    }}
                  >
                    Restore document
                  </button>
                  <button
                    className="pc-button"
                    onClick={() => setConfirmRestore(false)}
                  >
                    Cancel
                  </button>
                </>
              ) : (
                <button
                  className="pc-button"
                  disabled={
                    disabled ||
                    !state.currentData ||
                    selected?.sequence === history.currentData.currentSequence
                  }
                  onClick={() => setConfirmRestore(true)}
                >
                  <Undo2 size={16} />
                  Restore this state
                </button>
              )}
              {disabled && (
                <small>
                  Wait for your current edits to finish saving before restoring.
                </small>
              )}
            </footer>
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
}
