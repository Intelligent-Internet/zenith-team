import { useRef, useState } from "react";
import {
  Archive,
  BookOpen,
  ChevronRight,
  FileText,
  FolderOpen,
  LayoutGrid,
  PanelLeft,
  Plus,
  Search,
  Star,
} from "@/shared/ui/icons";
import { useWikiPageMove } from "./use-wiki-page-move";
import { WikiCollectionMenu } from "./wiki-collection-menu";
import type { WikiCollection, WikiPageSummary } from "./wiki-api";
import type { WikiFilter } from "./wiki-library";

type WikiSidebarProps = {
  companyId: string;
  canPublish: boolean;
  editing: boolean;
  /** Routed page, if any. */
  pageId: string | undefined;
  /** Collection of the routed page, expanded by default. */
  pageCollectionId: string | null | undefined;
  /** Collection the Library is filtered to. */
  libraryCollectionId: string | null;
  /** Filter the Library shows, or null when the Library is not on screen. */
  libraryFilter: WikiFilter | null;
  collections: WikiCollection[];
  collectionsError: boolean;
  onRetryCollections: () => void;
  collectionName: (collectionId: string | null) => string;
  pages: WikiPageSummary[];
  pagesLoading: boolean;
  pagesError: boolean;
  onRetryPages: () => void;
  pagesTruncated: boolean;
  archivedTotal: number;
  search: string;
  onSearch: (value: string) => void;
  onShowFilter: (filter: WikiFilter) => void;
  onOpenCollection: (collectionId: string) => void;
  onOpenPage: (pageId: string) => void;
  onNewPage: () => void;
  onNewCollection: () => void;
  onDeleteCollection: (id: string) => void;
  onClose?: () => void;
};

export function WikiSidebar({
  companyId,
  canPublish,
  editing,
  pageId,
  pageCollectionId,
  libraryCollectionId,
  libraryFilter,
  collections,
  collectionsError,
  onRetryCollections,
  collectionName,
  pages,
  pagesLoading,
  pagesError,
  onRetryPages,
  pagesTruncated,
  archivedTotal,
  search,
  onSearch,
  onShowFilter,
  onOpenCollection,
  onOpenPage,
  onNewPage,
  onNewCollection,
  onDeleteCollection,
  onClose,
}: WikiSidebarProps) {
  const createCollectionRef = useRef<HTMLButtonElement>(null);
  const [expandedCollections, setExpandedCollections] = useState<
    Record<string, boolean>
  >({});
  const expand = (collectionId: string, expanded = true) =>
    setExpandedCollections((previous) => ({
      ...previous,
      [collectionId]: expanded,
    }));
  const move = useWikiPageMove({
    companyId,
    enabled: canPublish && !editing,
    pages,
    collectionName,
    onMoved: (collectionId) => {
      if (collectionId) expand(collectionId);
    },
  });
  const firstPopulatedCollectionId = collections.find((collection) =>
    pages.some((item) => item.collectionId === collection.id),
  )?.id;
  /** Marks the filter link the Library currently shows unscoped. */
  const filterCurrent = (filter: WikiFilter) =>
    libraryFilter === filter && libraryCollectionId === null
      ? ("page" as const)
      : undefined;

  function pageLink(item: WikiPageSummary) {
    return (
      <button
        key={item.id}
        className={`zen-wiki__page-link ${item.id === pageId ? "is-current" : ""}`}
        {...move.pageDragProps(item)}
        onClick={() => onOpenPage(item.id)}
      >
        <FileText size={14} />
        <span>{item.title}</span>
        {!item.published && (
          <small className="zen-wiki__draft-label">Draft</small>
        )}
      </button>
    );
  }

  return (
    <aside className="zen-wiki__sidebar" aria-label="Wiki pages">
      <div className="zen-wiki__sidebar-head">
        <BookOpen size={18} />
        <strong>Company wiki</strong>
        {canPublish && (
          <button aria-label="New page" title="New page" onClick={onNewPage}>
            <Plus size={17} />
          </button>
        )}
        {onClose && (
          <button aria-label="Close page navigation" onClick={onClose}>
            <PanelLeft size={18} />
          </button>
        )}
      </div>
      <label className="zen-wiki__search">
        <Search size={16} />
        <input
          aria-label="Search wiki"
          placeholder="Search wiki…"
          value={search}
          disabled={editing}
          onChange={(event) => onSearch(event.target.value)}
        />
      </label>
      <button
        className="zen-wiki__sidebar-link"
        aria-current={filterCurrent("active")}
        onClick={() => onShowFilter("active")}
      >
        <LayoutGrid size={17} /> All pages
        <span className="zen-wiki__nav-count">{pages.length}</span>
      </button>
      <button
        className="zen-wiki__sidebar-link"
        aria-current={filterCurrent("favorites")}
        onClick={() => onShowFilter("favorites")}
      >
        <Star size={17} /> Favorites
        <span className="zen-wiki__nav-count">
          {pages.filter((item) => item.favorite).length}
        </span>
      </button>
      <button
        className="zen-wiki__sidebar-link"
        aria-current={filterCurrent("archived")}
        onClick={() => onShowFilter("archived")}
      >
        <Archive size={17} /> Archived
        <span className="zen-wiki__nav-count">{archivedTotal}</span>
      </button>
      <div className="zen-wiki__sidebar-section zen-wiki__sidebar-section--action">
        <span>Collections</span>
        {canPublish && (
          <button
            aria-label="Create collection"
            ref={createCollectionRef}
            title="Create collection"
            onClick={onNewCollection}
          >
            <Plus size={16} />
          </button>
        )}
      </div>
      {collectionsError && (
        <p className="zen-wiki__error" role="alert">
          Collections could not be loaded.{" "}
          <button className="zen-wiki__retry" onClick={onRetryCollections}>
            Retry
          </button>
        </p>
      )}
      {pagesLoading && <p className="zen-wiki__muted">Loading pages…</p>}
      {pagesError && (
        <p className="zen-wiki__error" role="alert">
          Pages could not be loaded.{" "}
          <button className="zen-wiki__retry" onClick={onRetryPages}>
            Retry
          </button>
        </p>
      )}
      {collections.map((collection) => {
        const children = pages.filter(
          (item) => item.collectionId === collection.id,
        );
        const expanded =
          children.length > 0 &&
          (expandedCollections[collection.id] ??
            (collection.id === pageCollectionId ||
              collection.id === libraryCollectionId ||
              collection.id === firstPopulatedCollectionId));
        return (
          <div
            className="zen-wiki__collection-group"
            key={collection.id}
            {...move.dropTargetProps(collection.id)}
          >
            <div
              className={`zen-wiki__collection-row ${move.dropCollectionId === collection.id ? "is-drop-target" : ""}`}
            >
              {children.length > 0 ? (
                <button
                  className="zen-wiki__collection-toggle"
                  aria-label={`${expanded ? "Collapse" : "Expand"} ${collection.name}`}
                  aria-expanded={expanded}
                  onClick={() => expand(collection.id, !expanded)}
                >
                  <ChevronRight
                    size={13}
                    className={expanded ? "zen-wiki__chevron-expanded" : ""}
                  />
                </button>
              ) : (
                <span
                  className="zen-wiki__collection-toggle zen-wiki__collection-toggle--empty"
                  aria-hidden="true"
                />
              )}
              <button
                className={`zen-wiki__collection ${!pageId && libraryCollectionId === collection.id ? "is-current" : ""}`}
                onClick={() => {
                  expand(collection.id);
                  onOpenCollection(collection.id);
                }}
              >
                <FolderOpen size={16} />
                <span>{collection.name}</span>
                {!pagesTruncated && <small>{children.length}</small>}
              </button>
              {canPublish && (
                <WikiCollectionMenu
                  companyId={companyId}
                  collection={collection}
                  disabled={editing}
                  focusAfterDelete={createCollectionRef}
                  onDeleted={onDeleteCollection}
                />
              )}
            </div>
            {expanded && (
              <div className="zen-wiki__tree-pages">
                {children.map(pageLink)}
              </div>
            )}
          </div>
        );
      })}
      <div
        className={`zen-wiki__collection-group zen-wiki__unfiled-group ${move.draggedPageId ? "is-dragging" : ""}`}
        {...move.dropTargetProps(null)}
      >
        <div
          className={`zen-wiki__sidebar-section zen-wiki__unfiled-target ${move.dropCollectionId === null ? "is-drop-target" : ""}`}
        >
          Unfiled
        </div>
        <div className="zen-wiki__tree-pages">
          {pages.filter((item) => !item.collectionId).map(pageLink)}
        </div>
      </div>
      {pagesTruncated && (
        <p className="zen-wiki__muted">Search to find more pages.</p>
      )}
    </aside>
  );
}
