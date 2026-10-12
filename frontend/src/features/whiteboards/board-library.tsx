import { useState, useEffect } from "react";
import { Link, useNavigate } from "react-router";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useCan } from "@/shared/permissions";
import {
  Plus,
  Search,
  Star,
  StarFilled,
  Archive,
  RotateCcw,
  Pencil,
  LayoutDashboard,
  ArrowUpRight,
  Clock3,
  Crown,
  UserMinus,
  X,
} from "@/shared/ui/icons";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { toast } from "@/shared/ui/sonner";
import {
  useClaimWhiteboardMutation,
  useCreateWhiteboardMutation,
  useFavoriteWhiteboardMutation,
  useListWhiteboardsQuery,
  useUpdateWhiteboardMutation,
  type Board,
} from "./board-api";
import { WhiteboardDetailsDialog } from "./board-details-dialog";

const libraryFilters = [
  {
    value: "recent",
    label: "Recent",
    icon: Clock3,
    emptyIcon: LayoutDashboard,
    emptyTitle: "Room for your next idea",
    emptyDescription: "Start with a note, a sketch, or a question.",
  },
  {
    value: "favorites",
    label: "Favorites",
    icon: Star,
    emptyIcon: Star,
    emptyTitle: "Keep your favorite ideas close",
    emptyDescription: "Star a Whiteboard to find it here.",
  },
  {
    value: "archived",
    label: "Archived",
    icon: Archive,
    emptyIcon: Archive,
    emptyTitle: "No archived Whiteboards",
    emptyDescription: "Archived Boards can be recovered here.",
  },
  {
    // Company Whiteboard managers only.
    value: "unowned",
    label: "Unowned",
    icon: UserMinus,
    emptyIcon: UserMinus,
    emptyTitle: "Every Whiteboard has a manager",
    emptyDescription:
      "Boards that no active member manages appear here, ready to claim.",
  },
] as const;

export function boardHref(companyId: string, boardId: string) {
  return `/whiteboards/${boardId}?companyId=${encodeURIComponent(companyId)}`;
}
export function WhiteboardLibrary() {
  const session = useSessionSnapshot(),
    companyId = session.company?.id ?? "";
  const navigate = useNavigate();
  const [search, setSearch] = useState("");
  // Search the library after typing pauses rather than on every keystroke.
  const [query, setQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 300);
    return () => clearTimeout(timer);
  }, [search]);
  const [filter, setFilter] = useState("recent");
  const [offset, setOffset] = useState(0);
  const [editor, setEditor] = useState<Board | "new" | null>(null);
  const [requestKey, setRequestKey] = useState(() => crypto.randomUUID());
  const canCreate = useCan("whiteboards.create");
  const canClaim = useCan("whiteboards.manage");
  const filters = libraryFilters.filter(
    ({ value }) => value !== "unowned" || canClaim,
  );
  const activeFilter =
    filters.find(({ value }) => value === filter) ?? filters[0];
  const unowned = activeFilter.value === "unowned";
  const boards = useListWhiteboardsQuery(
    {
      companyId,
      search: query,
      archived: activeFilter.value === "archived",
      favorites: activeFilter.value === "favorites",
      unowned: unowned || undefined,
      offset,
    },
    { skip: !companyId },
  );
  const [create, creating] = useCreateWhiteboardMutation();
  const [update, updating] = useUpdateWhiteboardMutation();
  const [claim, claiming] = useClaimWhiteboardMutation();
  const [favorite] = useFavoriteWhiteboardMutation();
  const EmptyIcon = search ? Search : activeFilter.emptyIcon;
  const report = (error: unknown) =>
    toast.error(getBoardlyQueryErrorMessage(error));
  const edit = (board?: Board) => {
    setEditor(board ?? "new");
    setRequestKey(crypto.randomUUID());
  };
  const saveDetails = async ({
    title,
    purpose,
  }: Pick<Board, "title" | "purpose">) => {
    if (!editor || creating.isLoading || updating.isLoading) return;
    try {
      if (editor === "new") {
        const board = await create({
          companyId,
          input: { requestKey, title, purpose },
        }).unwrap();
        navigate(boardHref(companyId, board.id));
      } else
        await update({
          companyId,
          boardId: editor.id,
          input: {
            expectedRevision: editor.revision,
            title,
            purpose,
          },
        }).unwrap();
      setEditor(null);
    } catch (error) {
      report(error);
    }
  };
  return (
    <section className="wb-library" aria-label="Company Whiteboards">
      <header className="wb-library-header">
        <div>
          <h1>Whiteboards</h1>
          <p>A shared space to think, sketch, and connect ideas.</p>
        </div>
        <button
          className="wb-primary"
          disabled={!canCreate}
          onClick={() => edit()}
        >
          <Plus size={18} />
          New whiteboard
        </button>
      </header>
      <div className="wb-library-filters">
        <div
          className="wb-library-tabs"
          role="group"
          aria-label="Whiteboard filters"
        >
          {filters.map(({ value, label, icon: FilterIcon }) => (
            <button
              key={value}
              aria-pressed={activeFilter.value === value}
              onClick={() => {
                setFilter(value);
                setOffset(0);
              }}
            >
              <FilterIcon size={16} aria-hidden="true" />
              {label}
            </button>
          ))}
        </div>
        <div className="wb-library-search" role="search">
          <Search size={18} aria-hidden="true" />
          <input
            aria-label="Search Whiteboards"
            placeholder="Search whiteboards…"
            value={search}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                setSearch("");
                setOffset(0);
              }
            }}
            onChange={(event) => {
              setSearch(event.target.value);
              setOffset(0);
            }}
          />
          {search && (
            <button
              className="wb-library-search-clear"
              aria-label="Clear search"
              onClick={() => {
                setSearch("");
                setOffset(0);
              }}
            >
              <X size={15} aria-hidden="true" />
            </button>
          )}
        </div>
      </div>
      {boards.isError ? (
        <div role="alert" className="wb-message">
          {getBoardlyQueryErrorMessage(boards.error)}
          <button onClick={() => void boards.refetch()}>Retry</button>
        </div>
      ) : boards.isFetching && !boards.currentData ? (
        <div
          className="wb-board-list wb-library-loading"
          role="status"
          aria-label="Loading Whiteboards"
        >
          {Array.from({ length: 6 }, (_, index) => (
            <div
              className="wb-board-placeholder"
              key={index}
              aria-hidden="true"
            >
              <span />
              <span />
              <span />
            </div>
          ))}
        </div>
      ) : !boards.currentData?.boards.length ? (
        <div className="wb-empty">
          <span className="wb-empty-symbol">
            <EmptyIcon size={28} aria-hidden="true" />
          </span>
          <h2>
            {search ? "No matching Whiteboards" : activeFilter.emptyTitle}
          </h2>
          <p>
            {search
              ? "Try a different name or purpose."
              : activeFilter.emptyDescription}
          </p>
          {search ? (
            <button
              onClick={() => {
                setSearch("");
                setOffset(0);
              }}
            >
              Clear search
            </button>
          ) : (
            canCreate &&
            activeFilter.value === "recent" && (
              <button className="wb-primary" onClick={() => edit()}>
                <Plus size={16} />
                New whiteboard
              </button>
            )
          )}
        </div>
      ) : (
        <div className="wb-board-list">
          {boards.currentData.boards.map((board) => (
            <article className="wb-board-card" key={board.id}>
              <Link
                className="wb-board-open"
                to={boardHref(companyId, board.id)}
                aria-label={`Open ${board.title}`}
              >
                <div className="wb-board-identity">
                  <span className="wb-board-symbol" aria-hidden="true">
                    <LayoutDashboard size={22} />
                  </span>
                  <span className="wb-board-type">
                    {board.archivedAt
                      ? "Archived whiteboard"
                      : board.visibility === "private"
                        ? "Private whiteboard"
                        : board.visibility === "public"
                          ? "Public whiteboard"
                          : "Company whiteboard"}
                  </span>
                  <ArrowUpRight
                    className="wb-board-open-arrow"
                    size={17}
                    aria-hidden="true"
                  />
                </div>
                <h2>{board.title}</h2>
                <p>{board.purpose || "Whiteboard"}</p>
              </Link>
              <footer>
                <time
                  dateTime={board.updatedAt}
                  title={new Date(board.updatedAt).toLocaleString()}
                >
                  Updated{" "}
                  {new Date(board.updatedAt).toLocaleDateString(undefined, {
                    month: "short",
                    day: "numeric",
                  })}
                </time>
                <div className="wb-board-actions">
                  {unowned && (
                    <button
                      className="wb-board-claim"
                      title="Take manage access to this whiteboard"
                      aria-label={`Claim ${board.title}`}
                      disabled={claiming.isLoading}
                      onClick={() =>
                        void claim({
                          companyId,
                          boardId: board.id,
                          input: { expectedRevision: board.revision },
                        })
                          .unwrap()
                          .then(() =>
                            toast.success(`You now manage ${board.title}.`),
                          )
                          .catch(report)
                      }
                    >
                      <Crown size={16} aria-hidden="true" />
                      Claim
                    </button>
                  )}
                  <button
                    title={
                      board.favorite
                        ? "Remove from favorites"
                        : "Add to favorites"
                    }
                    aria-label={
                      board.favorite
                        ? `Unfavorite ${board.title}`
                        : `Favorite ${board.title}`
                    }
                    aria-pressed={board.favorite}
                    onClick={() =>
                      void favorite({
                        companyId,
                        boardId: board.id,
                        favorite: !board.favorite,
                      })
                        .unwrap()
                        .catch(report)
                    }
                  >
                    {board.favorite ? (
                      <StarFilled size={18} />
                    ) : (
                      <Star size={18} />
                    )}
                  </button>
                  {board.canShare && !board.archivedAt && (
                    <button
                      title="Edit details"
                      aria-label={`Rename ${board.title}`}
                      onClick={() => edit(board)}
                    >
                      <Pencil size={18} />
                    </button>
                  )}
                  {board.canManage && (
                    <button
                      title={
                        board.archivedAt
                          ? "Recover whiteboard"
                          : "Archive whiteboard"
                      }
                      aria-label={
                        board.archivedAt
                          ? `Recover ${board.title}`
                          : `Archive ${board.title}`
                      }
                      onClick={() =>
                        void update({
                          companyId,
                          boardId: board.id,
                          input: {
                            expectedRevision: board.revision,
                            archived: !board.archivedAt,
                          },
                        })
                          .unwrap()
                          .catch(report)
                      }
                    >
                      {board.archivedAt ? (
                        <RotateCcw size={18} />
                      ) : (
                        <Archive size={18} />
                      )}
                    </button>
                  )}
                </div>
              </footer>
            </article>
          ))}
        </div>
      )}
      {(offset > 0 || boards.currentData?.hasMore) && (
        <div className="wb-pagination">
          <button
            disabled={!offset}
            onClick={() => setOffset(Math.max(0, offset - 100))}
          >
            Previous
          </button>
          <button
            disabled={!boards.currentData?.hasMore}
            onClick={() => setOffset(offset + 100)}
          >
            Next
          </button>
        </div>
      )}
      {editor && (
        <WhiteboardDetailsDialog
          board={editor === "new" ? undefined : editor}
          pending={creating.isLoading || updating.isLoading}
          onClose={() => setEditor(null)}
          onSave={saveDetails}
        />
      )}
    </section>
  );
}
