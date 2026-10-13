import {
  ArrowUpRight,
  ChevronRight,
  File,
  FileCode2,
  FileText,
  FolderOpen,
  ImageIcon,
  Globe2,
  LayoutDashboard,
  Loader2,
  Plus,
  RefreshCw,
  Search,
  X,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  ViewSwitch,
  type CollectionViewMode,
} from "@/shared/ui/collection-controls/controls";

import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import { ArtifactThumbnail } from "@/features/chat-panel/components/artifacts/artifact-thumbnail";
import { BoardPreview } from "@/features/whiteboards/board-preview";
import type { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import {
  conversationArtifactReference,
  artifactReviewStatusLabel,
  type ConversationArtifactReference,
} from "@/features/chat-panel/model/conversation-artifact";
import {
  artifactAppUrl,
  artifactFileSelection,
  artifactUsesLocalAppPreview,
} from "@/features/chat-panel/model/artifact-file-access";

export function ConversationArtifactsPanel({
  companyId,
  artifacts,
  layout = "panel",
  reviewOnly = false,
  linkedArtifactLoading = false,
  linkedArtifactUnavailable = false,
  onRetryLinkedArtifact,
  onShowAll,
  onClose,
  onSelect,
  onCreateWhiteboard,
}: {
  companyId: string;
  artifacts: ReturnType<typeof useConversationArtifacts>;
  layout?: "panel" | "page";
  reviewOnly?: boolean;
  linkedArtifactLoading?: boolean;
  linkedArtifactUnavailable?: boolean;
  onRetryLinkedArtifact?: () => void;
  onShowAll?: () => void;
  onClose?: () => void;
  onSelect: (artifact: ConversationArtifactReference) => void;
  /** Makes a new Artifact here; a Whiteboard is the kind people make. */
  onCreateWhiteboard?: () => void;
}) {
  const [view, setView] = useState<CollectionViewMode>("grid");
  const [search, setSearch] = useState("");
  const searchInput = useRef<HTMLInputElement>(null);
  const clearSearch = () => {
    setSearch("");
    searchInput.current?.focus();
  };
  const searchTerms = useMemo(
    () => search.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean),
    [search],
  );
  const hasSearch = layout === "page" && searchTerms.length > 0;
  const items = useMemo(() => {
    if (!hasSearch) return artifacts.items;
    return artifacts.items.filter(({ artifact }) => {
      const text = [
        artifact.title,
        artifact.summary,
        ...artifact.items.flatMap((item) => [item.displayName, item.label]),
      ]
        .join(" ")
        .toLocaleLowerCase();
      return searchTerms.every((term) => text.includes(term));
    });
  }, [artifacts.items, hasSearch, searchTerms]);
  const searchingEarlier = hasSearch && artifacts.hasMore && !artifacts.error;

  // Search older pages before declaring that no artifacts match.
  useEffect(() => {
    if (!searchingEarlier || artifacts.isFetching) return;
    void artifacts.loadMore();
  }, [
    searchingEarlier,
    artifacts.isFetching,
    artifacts.count,
    artifacts.loadMore,
  ]);

  return (
    <section
      aria-label="Conversation artifacts"
      className={`artifacts-panel${layout === "page" ? " artifacts-panel--page" : ""}`}
    >
      <header className="artifacts-panel__header">
        <div>
          <h2>{reviewOnly ? "Awaiting review" : "Artifacts"}</h2>
        </div>
        {!linkedArtifactLoading &&
        !linkedArtifactUnavailable &&
        artifacts.count > 0 ? (
          <span className="artifacts-count">
            {artifacts.count}
            {artifacts.hasMore ? "+" : ""}
          </span>
        ) : null}
        {onCreateWhiteboard ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className="artifacts-new-button"
                title="New artifact"
                type="button"
              >
                <Plus aria-hidden size={15} />
                New
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-44">
              <DropdownMenuItem onSelect={onCreateWhiteboard}>
                <LayoutDashboard size={17} />
                Whiteboard
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
        <button
          aria-label="Refresh artifacts"
          title="Refresh artifacts"
          className="artifacts-icon-button artifacts-panel__refresh"
          disabled={artifacts.isFetching}
          onClick={() => void artifacts.refresh()}
          type="button"
        >
          <RefreshCw aria-hidden size={15} />
        </button>
        {layout === "page" ? (
          <ViewSwitch
            ariaLabel="Artifact view"
            value={view}
            onChange={setView}
          />
        ) : null}
        {onClose ? (
          <button
            aria-label="Close artifacts"
            title="Close artifacts"
            className="artifacts-icon-button"
            onClick={onClose}
            type="button"
          >
            <X aria-hidden size={18} />
          </button>
        ) : null}
      </header>
      {layout === "page" ? (
        <div className="artifacts-search-toolbar">
          <div className="artifacts-search">
            <Search size={18} aria-hidden />
            <input
              ref={searchInput}
              type="search"
              aria-label="Search artifacts"
              placeholder="Search by title, description, or file name"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              onKeyDown={(event) => {
                if (
                  event.key === "Escape" &&
                  !event.nativeEvent.isComposing &&
                  search
                ) {
                  event.preventDefault();
                  event.stopPropagation();
                  clearSearch();
                }
              }}
            />
            {search ? (
              <button
                className="artifacts-icon-button"
                type="button"
                aria-label="Clear artifact search"
                onClick={clearSearch}
              >
                <X size={16} aria-hidden />
              </button>
            ) : null}
          </div>
          {artifacts.isLoading ||
          artifacts.error ||
          searchingEarlier ||
          hasSearch ? (
            <p className="artifacts-search-summary" role="status">
              {artifacts.isLoading
                ? "Loading artifacts…"
                : artifacts.error
                  ? "Search is unavailable until artifacts reload."
                  : searchingEarlier
                    ? "Searching older artifacts…"
                    : `${items.length} ${items.length === 1 ? "result" : "results"}`}
            </p>
          ) : null}
        </div>
      ) : null}
      <div className="artifacts-panel__body">
        {linkedArtifactLoading ? (
          <ArtifactPanelState
            icon={<Loader2 className="artifact-spinner" size={22} />}
          >
            Opening artifact…
          </ArtifactPanelState>
        ) : linkedArtifactUnavailable ? (
          <div className="artifacts-notice" role="alert">
            <p>
              This artifact could not be opened. It may be unavailable or you
              may no longer have access.
            </p>
            <button
              className="artifact-text-button"
              onClick={onRetryLinkedArtifact}
              type="button"
            >
              Retry
            </button>
            <button
              className="artifact-text-button"
              onClick={onShowAll}
              type="button"
            >
              Show all artifacts
            </button>
          </div>
        ) : artifacts.isLoading ? (
          <ArtifactPanelState
            icon={<Loader2 className="artifact-spinner" size={22} />}
          >
            Loading artifacts…
          </ArtifactPanelState>
        ) : artifacts.error ? (
          <div className="artifacts-notice" role="alert">
            <p>{artifacts.error}</p>
            <button
              className="artifact-text-button"
              disabled={artifacts.isFetching}
              onClick={() => void artifacts.refresh()}
              type="button"
            >
              Try again
            </button>
          </div>
        ) : reviewOnly && artifacts.items.length === 0 && artifacts.hasMore ? (
          <ArtifactPanelState
            icon={<Loader2 className="artifact-spinner" size={22} />}
          >
            Looking for earlier results…
          </ArtifactPanelState>
        ) : hasSearch && items.length === 0 ? (
          <ArtifactPanelState
            icon={
              searchingEarlier ? (
                <Loader2 className="artifact-spinner" size={22} />
              ) : (
                <Search size={27} />
              )
            }
          >
            <strong>
              {searchingEarlier
                ? "Searching artifacts…"
                : "No matching artifacts"}
            </strong>
            <span>
              {searchingEarlier
                ? "Checking earlier saved work in this conversation."
                : "Try a different title, description, or file name."}
            </span>
            {!searchingEarlier ? (
              <button
                className="artifact-text-button"
                type="button"
                onClick={clearSearch}
              >
                Clear search
              </button>
            ) : null}
          </ArtifactPanelState>
        ) : items.length === 0 ? (
          <ArtifactPanelState icon={<FolderOpen size={27} />}>
            <strong>
              {reviewOnly ? "No artifacts awaiting review" : "No artifacts yet"}
            </strong>
            <span>
              {reviewOnly
                ? "Review decisions and task status are tracked separately."
                : "Apps, media, plans, and files will appear here when an Agent saves final work."}
            </span>
          </ArtifactPanelState>
        ) : (
          <ul
            className="artifacts-list"
            data-view={layout === "page" ? view : undefined}
          >
            {items.map((artifact) => (
              <li className="artifacts-list__item" key={artifact.artifact.id}>
                <ArtifactRow
                  artifact={artifact}
                  companyId={companyId}
                  compact={layout === "page" && view === "list"}
                  isLatest={
                    !reviewOnly &&
                    artifact.artifact.id === artifacts.items[0]?.artifact.id
                  }
                  onSelect={() =>
                    onSelect(conversationArtifactReference(artifact))
                  }
                />
              </li>
            ))}
          </ul>
        )}
        {!linkedArtifactLoading &&
        !linkedArtifactUnavailable &&
        !hasSearch &&
        artifacts.hasMore ? (
          <button
            className="artifacts-load-more"
            disabled={artifacts.isFetching}
            onClick={() => void artifacts.loadMore()}
            type="button"
          >
            {artifacts.isLoadingMore ? "Loading…" : "Load earlier artifacts"}
          </button>
        ) : null}
        {reviewOnly && !linkedArtifactLoading && !linkedArtifactUnavailable ? (
          <button
            className="artifacts-load-more"
            onClick={onShowAll}
            type="button"
          >
            Show all artifacts
          </button>
        ) : null}
      </div>
    </section>
  );
}

function ArtifactRow({
  artifact,
  companyId,
  compact = false,
  isLatest,
  onSelect,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  compact?: boolean;
  isLatest: boolean;
  onSelect: () => void;
}) {
  const primary =
    artifact.artifact.items.find((item) => item.role === "primary") ??
    artifact.artifact.items[0];
  const ArtifactIcon = {
    app: Globe2,
    media: ImageIcon,
    text: FileText,
    file: File,
    whiteboard: LayoutDashboard,
  }[artifact.artifact.type];
  return (
    <button
      aria-label={`Open ${artifact.artifact.title}`}
      data-artifact-id={artifact.artifact.id}
      className="artifact-row"
      onClick={onSelect}
      type="button"
    >
      <ArtifactIcon aria-hidden className="artifact-row__icon" size={20} />
      <span className="artifact-row__content">
        <span className="artifact-row__title" title={artifact.artifact.title}>
          {artifact.artifact.title}
        </span>
        {artifact.artifact.summary ? (
          <span className="artifact-row__summary">
            {artifact.artifact.summary}
          </span>
        ) : null}
        {!compact ? (
          <ArtifactRowPreview
            artifact={artifact}
            companyId={companyId}
            primary={primary}
          />
        ) : null}
        <span className="artifact-row__meta">
          <span>{artifactDetail(artifact)}</span>
          <span aria-hidden>·</span>
          <time dateTime={artifact.artifact.createdAt}>
            {formatDate(artifact.artifact.createdAt)}
          </time>
          {isLatest ? (
            <span className="artifact-row__latest">Latest</span>
          ) : null}
          {artifact.deliverable ? (
            <span
              className="artifact-row__review"
              data-status={artifact.deliverable.status}
            >
              {artifactReviewStatusLabel(artifact.deliverable.status)}
            </span>
          ) : null}
        </span>
      </span>
      <ChevronRight aria-hidden className="artifact-row__chevron" size={16} />
    </button>
  );
}

function ArtifactRowPreview({
  artifact,
  companyId,
  primary,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  primary: ConversationArtifact["artifact"]["items"][number] | undefined;
}) {
  if (artifact.artifact.type === "text") return null;
  // A Whiteboard has no files; its card shows a small image of the Board.
  if (artifact.artifact.type === "whiteboard")
    return (
      <BoardPreview companyId={companyId} boardId={artifact.artifact.id} />
    );
  if (artifact.artifact.type === "app") {
    const rawUrl = artifact.publication?.customUrl || artifact.publication?.url;
    const url = rawUrl ? artifactAppUrl(rawUrl) : null;
    return (
      <span className="artifact-row__app">
        <span className="artifact-row__browser-bar">
          <i /> <i /> <i />
          <b>{url ? new URL(url).host : "App preview"}</b>
        </span>
        <span className="artifact-row__app-body">
          <Globe2 aria-hidden size={24} />
          <span>{publicationLabel(artifact)}</span>
          {url ? <ArrowUpRight aria-hidden size={16} /> : null}
        </span>
      </span>
    );
  }
  if (artifact.artifact.type === "media" && primary) {
    const previewImage = artifact.artifact.items.find(
      (item) =>
        item.contentType.startsWith("image/") &&
        /preview|thumbnail|poster|cover/i.test(
          `${item.label} ${item.displayName}`,
        ),
    );
    const preview = primary.contentType.startsWith("image/")
      ? primary
      : (previewImage ?? primary);
    return (
      <span className="artifact-row__media">
        <ArtifactThumbnail
          companyId={companyId}
          file={artifactFileSelection(
            artifact.runId,
            artifact.artifact.id,
            preview,
          )}
        />
      </span>
    );
  }
  return (
    <span className="artifact-row__files">
      {artifact.artifact.items.slice(0, 3).map((item) => (
        <span key={item.id}>
          {/json|javascript|xml|yaml/.test(item.contentType) ? (
            <FileCode2 aria-hidden size={17} />
          ) : item.contentType.startsWith("image/") ? (
            <ImageIcon aria-hidden size={17} />
          ) : (
            <FileText aria-hidden size={17} />
          )}
          <span title={item.displayName}>{item.displayName}</span>
        </span>
      ))}
    </span>
  );
}

function ArtifactPanelState({
  children,
  icon,
}: {
  children: ReactNode;
  icon: ReactNode;
}) {
  return (
    <div className="artifacts-empty" role="status">
      {icon}
      {children}
    </div>
  );
}

function artifactDetail(artifact: ConversationArtifact) {
  const count = artifact.artifact.items.length;
  if (artifact.artifact.type === "app") return "App";
  if (artifact.artifact.type === "media") {
    return `${count} media ${count === 1 ? "item" : "items"}`;
  }
  if (artifact.artifact.type === "text") return "Document";
  if (artifact.artifact.type === "whiteboard") return "Whiteboard";
  return `${count} saved ${count === 1 ? "file" : "files"}`;
}

function publicationLabel(artifact: ConversationArtifact) {
  if (artifactUsesLocalAppPreview(artifact)) return "Local app";
  const status = artifact.publication?.status;
  if (status === "published") return "Published app";
  if (status === "failed") return "Publication failed";
  if (status === "unpublished") return "App offline";
  if (status) return "Publishing…";
  return "Source saved";
}

function formatDate(value: string) {
  return new Date(value).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  });
}
