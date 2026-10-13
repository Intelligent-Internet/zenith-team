import { useState } from "react";
import {
  BookOpen,
  ChevronRight,
  FileText,
  FolderOpen,
  Plus,
  Search,
} from "@/shared/ui/icons";
import {
  useListWikiPagesQuery,
  WIKI_PAGE_LIMIT,
  type WikiCollection,
  type WikiPageSummary,
} from "./wiki-api";

export type WikiFilter = "active" | "archived" | "favorites";

const filterLabels: Record<WikiFilter, string> = {
  active: "All pages",
  favorites: "Favorites",
  archived: "Archived",
};

type WikiLibraryProps = {
  companyId: string;
  canPublish: boolean;
  filter: WikiFilter;
  collectionId: string | null;
  search: string;
  /** Debounced `search`, used for the request. */
  query: string;
  onSearch: (value: string) => void;
  onShowFilter: (filter: WikiFilter) => void;
  onOpenCollection: (collectionId: string) => void;
  onOpenPage: (pageId: string) => void;
  onNewPage: () => void;
  collections: WikiCollection[];
  collectionName: (collectionId: string | null) => string;
  /** Every active page (from the graph), used for collection counts. */
  navigationPages: WikiPageSummary[];
  navigationTruncated: boolean;
};

export function WikiLibrary({
  companyId,
  canPublish,
  filter,
  collectionId,
  search,
  query,
  onSearch,
  onShowFilter,
  onOpenCollection,
  onOpenPage,
  onNewPage,
  collections,
  collectionName,
  navigationPages,
  navigationTruncated,
}: WikiLibraryProps) {
  // Any change to what is listed starts again from the first page.
  const listKey = `${filter}:${collectionId ?? ""}:${query}`;
  const [paging, setPaging] = useState({ key: listKey, offset: 0 });
  const offset = paging.key === listKey ? paging.offset : 0;
  const setOffset = (next: number) =>
    setPaging({ key: listKey, offset: Math.max(0, next) });
  const pages = useListWikiPagesQuery(
    {
      companyId,
      search: query,
      status: filter === "archived" ? "archived" : "active",
      offset,
      favorites_only: filter === "favorites",
      collection_id: collectionId ?? undefined,
    },
    { skip: !companyId },
  );
  const items = pages.data?.items ?? [];
  const total = pages.data?.total ?? 0;

  return (
    <>
      <div className="zen-wiki__library-tools">
        <div className="zen-wiki__filter">
          {(Object.keys(filterLabels) as WikiFilter[]).map((option) => (
            <button
              key={option}
              aria-pressed={filter === option}
              onClick={() => onShowFilter(option)}
            >
              {filterLabels[option]}
            </button>
          ))}
        </div>
        <label className="zen-wiki__library-search">
          <Search size={16} />
          <input
            aria-label="Search library"
            placeholder="Search pages…"
            value={search}
            onChange={(event) => onSearch(event.target.value)}
          />
        </label>
      </div>
      <div className="zen-wiki__collections">
        {collections.map((item, index) => {
          const count = navigationPages.filter(
            (page) => page.collectionId === item.id,
          ).length;
          return (
            <button
              className={`zen-wiki__collection-card ${collectionId === item.id ? "is-current" : ""}`}
              key={item.id}
              onClick={() => onOpenCollection(item.id)}
            >
              <span
                className={`zen-wiki__card-icon zen-wiki__card-icon--${index % 4}`}
              >
                <FolderOpen size={20} />
              </span>
              <strong>{item.name}</strong>
              <span>
                {navigationTruncated
                  ? "Browse collection"
                  : `${count} ${count === 1 ? "page" : "pages"}`}
                <ChevronRight size={15} />
              </span>
            </button>
          );
        })}
      </div>
      <div className="zen-wiki__list-head flex justify-between p-[0_0_9px] [border-bottom:1px_solid_var(--wk-border)] text-[length:13px]">
        <strong>
          {filter === "archived" ? "Archived pages" : filterLabels[filter]}
        </strong>
        <span>Last edited</span>
      </div>
      {pages.isLoading && <p role="status">Loading pages…</p>}
      {pages.isError && (
        <p role="alert">
          Pages could not be loaded.{" "}
          <button
            className="zen-wiki__retry"
            onClick={() => void pages.refetch()}
          >
            Retry
          </button>
        </p>
      )}
      {items.map((item) => (
        <button
          className="zen-wiki__list-row"
          key={item.id}
          onClick={() => onOpenPage(item.id)}
        >
          <FileText size={19} />
          <span>
            <strong>
              {item.title}
              {!item.published && (
                <span className="ml-[8px] text-[length:10px] font-[400] text-[var(--wk-muted)] whitespace-nowrap">
                  Draft
                </span>
              )}
            </strong>
            <small>{collectionName(item.collectionId)}</small>
          </span>
          <time>{new Date(item.updatedAt).toLocaleDateString()}</time>
          <ChevronRight size={17} />
        </button>
      ))}
      {!pages.isLoading && !pages.isError && items.length === 0 && (
        <div className="zen-wiki__empty-state grid [justify-items:center] gap-[7px] p-[60px_20px] text-center text-[var(--wk-muted)]">
          <BookOpen size={32} />
          <h2>
            {search
              ? "No matching pages"
              : collectionId
                ? "No pages in this collection"
                : "No pages yet"}
          </h2>
          <p>
            {search
              ? "Try another search."
              : collectionId
                ? "Add a page here, or choose another collection."
                : "Create the first page to build your company handbook."}
          </p>
          {canPublish && !search && (
            <button className="zen-wiki__primary" onClick={onNewPage}>
              <Plus size={16} /> New page
            </button>
          )}
        </div>
      )}
      {total > WIKI_PAGE_LIMIT && (
        <div className="zen-wiki__pagination">
          <button
            disabled={offset === 0}
            onClick={() => setOffset(offset - WIKI_PAGE_LIMIT)}
          >
            Previous
          </button>
          <span>
            {offset + 1}–{Math.min(offset + WIKI_PAGE_LIMIT, total)}
          </span>
          <button
            disabled={offset + WIKI_PAGE_LIMIT >= total}
            onClick={() => setOffset(offset + WIKI_PAGE_LIMIT)}
          >
            Next
          </button>
        </div>
      )}
    </>
  );
}
