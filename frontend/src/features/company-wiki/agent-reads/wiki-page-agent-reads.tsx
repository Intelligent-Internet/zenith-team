import { Link } from "react-router";
import { useListWikiPageAgentReadsQuery } from "./agent-reads-api";
import { formatRelativeTime } from "./relative-time";
import "./agent-reads.css";

/**
 * Recent Runs whose Agents read this page, for the page details panel.
 * Renders nothing while loading, on error and when no Run has read the page,
 * so the panel only shows the section when there is something to open.
 */
export function WikiPageAgentReads({
  companyId,
  pageId,
}: {
  companyId: string;
  pageId: string;
}) {
  const reads = useListWikiPageAgentReadsQuery(
    { companyId, pageId },
    { skip: !companyId || !pageId },
  );
  const items = reads.currentData?.items;
  if (!items?.length) {
    return null;
  }
  const total = reads.currentData?.total ?? items.length;
  return (
    <div className="zen-wiki__detail-section zen-wiki-used-by">
      <div className="zen-wiki__detail-heading">
        <strong>Used by Agents</strong>
        <span>{total}</span>
      </div>
      <ul
        className="zen-wiki-used-by__list"
        aria-label="Runs that read this page"
      >
        {items.map((read) => (
          <li key={read.runId}>
            <Link
              className="zen-wiki-used-by__link"
              to={`/runs/${read.runId}`}
              title={new Date(read.lastReadAt).toLocaleString()}
            >
              <strong>{read.agentName}</strong>
              <small>
                Read revision {read.revisionNumber} ·{" "}
                {formatRelativeTime(read.lastReadAt)}
              </small>
            </Link>
          </li>
        ))}
      </ul>
      {total > items.length ? (
        <p className="zen-wiki__muted">
          Showing the {items.length} most recent Runs.
        </p>
      ) : null}
    </div>
  );
}
