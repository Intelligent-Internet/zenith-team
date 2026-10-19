import { useMemo, useState } from "react";
import { Link } from "react-router";
import type { RunDto } from "@/shared/api/types";
import {
  BookOpen,
  ChevronRight,
  RefreshCcw,
  Search,
  ShieldCheck,
} from "@/shared/ui/icons";
import { useListRunWikiReadsQuery, type RunWikiRead } from "./agent-reads-api";
import { WikiSourcePreviewDialog } from "./wiki-source-preview-dialog";
import "./agent-reads.css";

export type WikiSource = {
  /** The first read row of this page revision; identifies the source. */
  read: RunWikiRead;
  /** Last read row of the revision: whether the Agent saw the whole page. */
  partial: boolean;
};

const TERMINAL_RUN_STATUSES = new Set<RunDto["status"]>([
  "completed",
  "failed",
  "cancelled",
]);

/**
 * One source per page revision. An Agent that pages through a long page
 * reads the same revision several times; that is still one source, and it is
 * partial only if the last chunk it fetched was truncated.
 */
export function collectSources(items: RunWikiRead[]): WikiSource[] {
  const sources = new Map<string, WikiSource>();
  for (const read of items) {
    if (read.kind !== "read" || !read.pageId || read.revisionNumber === null) {
      continue;
    }
    const key = `${read.pageId}:${read.revisionId ?? read.revisionNumber}`;
    const existing = sources.get(key);
    sources.set(key, {
      read: existing?.read ?? read,
      partial: read.truncated,
    });
  }
  return [...sources.values()];
}

export function RunWikiSources({
  companyId,
  runId,
  agentId,
  runStatus,
}: {
  companyId: string;
  runId: string;
  agentId: string;
  /** Explains whether additional source reads may arrive. */
  runStatus: RunDto["status"] | undefined;
}) {
  const active =
    runStatus !== undefined && !TERMINAL_RUN_STATUSES.has(runStatus);
  const reads = useListRunWikiReadsQuery(
    { companyId, runId },
    {
      skip: !companyId || !runId,
    },
  );
  const [previewSource, setPreviewSource] = useState<WikiSource | null>(null);
  const items = reads.currentData?.items;
  const sources = useMemo(() => collectSources(items ?? []), [items]);
  const lookups = useMemo(
    () =>
      (items ?? []).filter(
        (read) => read.kind === "search" || read.kind === "list",
      ),
    [items],
  );

  return (
    <section
      className="zen-wiki-sources"
      aria-labelledby={`wiki-sources-${runId}`}
    >
      <header className="zen-wiki-sources__head flex items-center justify-between gap-[8px]">
        <h2 id={`wiki-sources-${runId}`}>
          <BookOpen size={17} aria-hidden /> Wiki sources
        </h2>
        {sources.length > 0 ? (
          <span className="min-w-[22px] rounded-[999px] p-[0_7px] [background:var(--wk-soft)] text-[var(--wk-muted)] text-[length:12px] font-[600] text-center">
            {sources.length}
          </span>
        ) : null}
      </header>
      {reads.isError && !items ? (
        <p className="zen-wiki-sources__state" role="alert">
          Wiki sources could not be loaded.{" "}
          <button type="button" onClick={() => void reads.refetch()}>
            <RefreshCcw size={13} aria-hidden /> Retry
          </button>
        </p>
      ) : !items ? (
        <div
          className="zen-wiki-sources__skeleton grid gap-[8px]"
          role="status"
          aria-label="Loading Wiki sources"
        >
          <span />
          <span />
        </div>
      ) : (
        <>
          {sources.length === 0 ? (
            <div className="zen-wiki-sources__empty">
              <strong>No Wiki pages read in this run</strong>
              <p>
                {active
                  ? "Pages the Agent reads will appear here as it works. "
                  : ""}
                You can change the Agent&rsquo;s Company Wiki access in{" "}
                <Link to={`/agents/${agentId}?tab=capabilities`}>
                  its settings
                </Link>
                .
              </p>
            </div>
          ) : (
            <ol className="zen-wiki-sources__list">
              {sources.map((source, index) => (
                <SourceRow
                  key={`${source.read.pageId}:${source.read.revisionId}`}
                  index={index + 1}
                  source={source}
                  onOpen={() => setPreviewSource(source)}
                />
              ))}
            </ol>
          )}
          {lookups.length > 0 ? (
            <div className="zen-wiki-sources__searches">
              <h3>Searches and browsing</h3>
              <ul>
                {lookups.map((lookup) => {
                  const label = lookupLabel(lookup);
                  return (
                    <li key={lookup.id}>
                      {lookup.kind === "list" ? (
                        <BookOpen size={13} aria-hidden />
                      ) : (
                        <Search size={13} aria-hidden />
                      )}
                      <span title={label}>{label}</span>
                      <small>
                        {lookup.kind === "list"
                          ? pageCountLabel(lookup.resultCount)
                          : resultLabel(lookup.resultCount)}
                      </small>
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : null}
          {reads.isError ? (
            <p className="zen-wiki-sources__state" role="alert">
              Sources could not be refreshed.{" "}
              <button type="button" onClick={() => void reads.refetch()}>
                Retry
              </button>
            </p>
          ) : null}
        </>
      )}
      <p className="zen-wiki-sources__note">
        <ShieldCheck size={13} aria-hidden /> Read-only. Agents can read
        published Wiki pages but cannot edit them.
      </p>
      {previewSource ? (
        <WikiSourcePreviewDialog
          companyId={companyId}
          read={previewSource.read}
          onClose={() => setPreviewSource(null)}
        />
      ) : null}
    </section>
  );
}

function SourceRow({
  index,
  source,
  onOpen,
}: {
  index: number;
  source: WikiSource;
  onOpen: () => void;
}) {
  const { read, partial } = source;
  const title = read.title.trim() || "Untitled page";
  const changed =
    read.page !== null &&
    read.revisionNumber !== null &&
    read.page.currentRevision > read.revisionNumber;
  const archived = read.page?.status === "archived";
  return (
    <li>
      <button
        type="button"
        className="zen-wiki-sources__source"
        aria-label={`Source ${index}: ${title}, version ${read.revisionNumber}. Open preview`}
        onClick={onOpen}
      >
        <span
          className="grid flex-none w-[24px] h-[24px] place-items-center rounded-[50%] [background:var(--wk-soft)] text-[var(--wk-accent)] text-[length:12px] font-[650]"
          aria-hidden
        >
          {index}
        </span>
        <span className="zen-wiki-sources__body grid min-w-0 flex-1 gap-[4px]">
          <strong>{title}</strong>
          <span className="flex flex-wrap items-center gap-[4px_6px] text-[var(--wk-muted)] text-[length:11px]">
            <span className="zen-wiki-sources__chip">
              v{read.revisionNumber}
            </span>
            {changed ? (
              <span className="zen-wiki-sources__chip is-warning">
                Changed since · now v{read.page?.currentRevision}
              </span>
            ) : null}
            {archived ? (
              <span className="zen-wiki-sources__chip is-warning">
                Archived
              </span>
            ) : null}
            {partial ? (
              <span
                className="zen-wiki-sources__chip"
                title="The Agent read only the first part of this long page"
              >
                Partly read
              </span>
            ) : null}
            <time dateTime={read.createdAt}>
              {new Date(read.createdAt).toLocaleTimeString([], {
                hour: "numeric",
                minute: "2-digit",
              })}
            </time>
          </span>
        </span>
        <ChevronRight size={15} aria-hidden />
      </button>
    </li>
  );
}

function resultLabel(count: number | null) {
  if (count === null) {
    return "";
  }
  return count === 1 ? "1 result" : `${count} results`;
}

/** A search shows its query; a browse shows the collection it listed. */
function lookupLabel(lookup: RunWikiRead) {
  if (lookup.kind !== "list") {
    return lookup.query;
  }
  return lookup.query ? `Browsed ${lookup.query}` : "Browsed all pages";
}

function pageCountLabel(count: number | null) {
  if (count === null) {
    return "";
  }
  return count === 1 ? "1 page" : `${count} pages`;
}
