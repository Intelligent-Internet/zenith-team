import { useState } from "react";
import { discussionsApi, type DiscussionDetail } from "../api/discussions-api";
import { useDiscussionUpdates } from "./discussion-provider";
import { Search, X } from "@/shared/ui/icons";

export function DiscussionSearch({
  conversationId,
  detail,
  onOpenMessage,
  onClose,
}: {
  conversationId: string;
  detail: DiscussionDetail;
  onOpenMessage: (id: string) => void;
  onClose: () => void;
}) {
  const runtime = useDiscussionUpdates();
  const authority = runtime.state(conversationId);
  const [query, setQuery] = useState("");
  const [authorId, setAuthorId] = useState("");
  const [after, setAfter] = useState("");
  const [before, setBefore] = useState("");
  const [hasFile, setHasFile] = useState(false);
  const [submitted, setSubmitted] = useState<{
    query: string;
    authorId?: string;
    after?: string;
    before?: string;
    hasFile: boolean;
  }>();
  const result = discussionsApi.useSearchDiscussionQuery(
    {
      ...runtime.session,
      conversationId,
      conversationRevision: authority?.conversationRevision ?? 0,
      grantGeneration: authority?.grantGeneration ?? 0,
      query: submitted?.query ?? "",
      authorId: submitted?.authorId,
      after: submitted?.after,
      before: submitted?.before,
      hasFile: submitted?.hasFile ?? false,
    },
    { skip: !submitted || !authority?.capabilities.search },
  );
  return (
    <aside
      className="discussion-message-search"
      aria-label="Search messages"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <div className="discussion-thread-header">
        <div className="discussion-message-search-heading">
          <h3>Search messages</h3>
          <p>In this conversation</p>
        </div>
        <button
          className="discussion-icon-button"
          aria-label="Close search"
          onClick={onClose}
        >
          <X size={18} />
        </button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (query.trim().length < 2) return;
          const end = before ? new Date(`${before}T00:00:00`) : undefined;
          if (end) end.setDate(end.getDate() + 1);
          setSubmitted({
            query: query.trim(),
            authorId: authorId || undefined,
            after: after
              ? new Date(`${after}T00:00:00`).toISOString()
              : undefined,
            before: end?.toISOString(),
            hasFile,
          });
        }}
      >
        <div className="discussion-message-search-query">
          <Search size={18} aria-hidden />
          <input
            aria-label="Search this conversation"
            autoFocus
            value={query}
            minLength={2}
            maxLength={256}
            required
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search messages…"
          />
          <button
            type="submit"
            className="discussion-message-search-submit"
            disabled={query.trim().length < 2}
          >
            Search
          </button>
        </div>
        <details className="discussion-message-search-filter-disclosure">
          <summary>
            Filters{authorId || after || before || hasFile ? " · Active" : ""}
          </summary>
          <div className="discussion-message-search-filters">
            <label>
              From
              <select
                value={authorId}
                onChange={(e) => setAuthorId(e.target.value)}
              >
                <option value="">Anyone</option>
                {detail.members.map((member) => (
                  <option key={member.userId} value={member.userId}>
                    {member.displayName}
                  </option>
                ))}
              </select>
            </label>
            <label>
              After
              <input
                type="date"
                value={after}
                max={before || undefined}
                onChange={(e) => setAfter(e.target.value)}
              />
            </label>
            <label>
              Through
              <input
                type="date"
                value={before}
                min={after || undefined}
                onChange={(e) => setBefore(e.target.value)}
              />
            </label>
            <label className="discussion-message-search-file">
              <input
                type="checkbox"
                checked={hasFile}
                onChange={(e) => setHasFile(e.target.checked)}
              />
              Has file
            </label>
          </div>
          <button className="discussion-button" type="submit">
            Apply filters
          </button>
        </details>
      </form>
      <div className="discussion-message-search-results" aria-live="polite">
        {result.isFetching ? (
          <p>Searching…</p>
        ) : result.isError ? (
          <p role="alert">
            Search is unavailable. Check your access and try again.
          </p>
        ) : result.currentData ? (
          <>
            <p>
              {result.currentData.items.length === 50
                ? "First 50 matches. Narrow your search for more specific results."
                : `${result.currentData.items.length} ${result.currentData.items.length === 1 ? "match" : "matches"}`}
            </p>
            {result.currentData.items.map((item) =>
              item.kind === "message" ? (
                <button
                  className="discussion-message-search-result"
                  key={item.id}
                  onClick={() => onOpenMessage(item.id)}
                >
                  <span className="discussion-message-search-meta">
                    <strong>{item.author.name}</strong>
                    <time dateTime={item.createdAt}>
                      {new Date(item.createdAt).toLocaleDateString(undefined, {
                        month: "short",
                        day: "numeric",
                      })}
                    </time>
                  </span>
                  {item.threadRootId ? <small>Thread reply</small> : null}
                  <span>{item.snippet}</span>
                </button>
              ) : null,
            )}
          </>
        ) : null}
      </div>
    </aside>
  );
}
