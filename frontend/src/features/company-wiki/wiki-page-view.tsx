import { PagePdfExport } from "../project-canvas/documents/page-pdf";
import { useState } from "react";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { webAppUrl } from "@/shared/lib/web-app-url";
import { toast } from "@/shared/ui/sonner";
import {
  ArrowLeft,
  BookOpen,
  ChevronRight,
  History,
  Link2,
  PanelRight,
  Pencil,
  Star,
  StarFilled,
} from "@/shared/ui/icons";
import { useSetWikiFavoriteMutation, type WikiPageDetail } from "./wiki-api";
import { WikiDetailsPanel } from "./wiki-details-panel";
import { WikiRestoreDialog, type WikiRestoreReview } from "./wiki-dialogs";
import { WikiHistoryPanel } from "./wiki-history-panel";
import { WikiContent } from "./wiki-content";
import { wikiAuthorLabel } from "./wiki-author-label";
import { WikiDocumentWorkspace } from "./wiki-document-workspace";

type WikiPageViewProps = {
  companyId: string;
  userId: string | undefined;
  canPublish: boolean;
  page: WikiPageDetail;
  editing: boolean;
  dirty: boolean;
  onEditingChange: (editing: boolean) => void;
  onDirtyChange: (dirty: boolean) => void;
  collectionName: (collectionId: string | null) => string;
  returnView: "library" | "graph";
  onBack: () => void;
  onOpenPage: (pageId: string) => void;
};

/** One Wiki page: breadcrumb bar, reader or editor, and side panels. */
export function WikiPageView({
  companyId,
  userId,
  canPublish,
  page,
  editing,
  dirty,
  onEditingChange,
  onDirtyChange,
  collectionName,
  returnView,
  onBack,
  onOpenPage,
}: WikiPageViewProps) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(true);
  const [restoreReview, setRestoreReview] = useState<WikiRestoreReview | null>(
    null,
  );
  const [prose, setProse] = useState<HTMLDivElement | null>(null);
  const [draftActions, setDraftActions] = useState<HTMLSpanElement | null>(
    null,
  );
  const [setFavorite] = useSetWikiFavoriteMutation();

  async function copyLink() {
    try {
      await copyTextToClipboard(webAppUrl(`/wiki/pages/${page.id}`));
      toast.success("Page link copied.");
    } catch {
      toast.error("Could not copy the page link.");
    }
  }

  async function toggleFavorite() {
    const result = await setFavorite({
      companyId,
      pageId: page.id,
      favorite: !page.favorite,
    });
    if ("error" in result)
      toast.error(result.error?.message ?? "Could not update favorites.");
  }

  const meta = (
    <div className="zen-wiki__meta flex items-center flex-wrap gap-[8px] text-[var(--wk-muted)] text-workspace-caption m-[0_0_24px]">
      {page.currentRevisionId ? (
        <>
          <span>
            {wikiAuthorLabel({
              agentId: page.lastAuthorAgentId,
              agentName: page.lastAuthorAgentName,
              authorUserId: page.lastAuthorUserId,
              viewerUserId: userId,
            })}
          </span>
          <span>{new Date(page.revisionCreatedAt).toLocaleString()}</span>
          <span className="[border:1px_solid_var(--wk-border)] rounded-[4px] p-[0_5px] [background:var(--wk-soft)] text-[length:10px]">
            Published · v{page.contentRevision}
          </span>
        </>
      ) : (
        <span>Not published yet</span>
      )}
    </div>
  );

  return (
    <>
      <div className="zen-wiki__page-bar">
        <button onClick={onBack}>
          <ArrowLeft size={16} /> {returnView === "graph" ? "Graph" : "Library"}
        </button>
        <ChevronRight size={14} />
        <span>{collectionName(page.collectionId)}</span>
        <ChevronRight size={14} />
        <span className="min-w-0 [flex:0_1_auto] text-[var(--wk-text)] overflow-hidden text-ellipsis whitespace-nowrap">
          {page.title}
        </span>
        <div className="zen-wiki__page-actions">
          <button
            aria-label="Favorite"
            aria-pressed={page.favorite}
            title={page.favorite ? "Remove from favorites" : "Add to favorites"}
            onClick={() => void toggleFavorite()}
          >
            {page.favorite ? <StarFilled size={17} /> : <Star size={17} />}
          </button>
          {!editing && (
            <PagePdfExport title={page.title}>
              <WikiContent
                companyId={companyId}
                pageId={page.id}
                content={page.content}
                body={page.body}
              />
            </PagePdfExport>
          )}
          <button
            aria-label="Copy page link"
            title="Copy page link"
            onClick={() => void copyLink()}
          >
            <Link2 size={17} />
          </button>
          <button
            aria-label={
              detailsOpen ? "Close page details" : "Open page details"
            }
            aria-pressed={detailsOpen}
            title="Page details"
            onClick={() => {
              setDetailsOpen(!detailsOpen);
              setHistoryOpen(false);
            }}
          >
            <PanelRight size={17} />
          </button>
          <button
            aria-label="Page history"
            aria-pressed={historyOpen}
            onClick={() => {
              setHistoryOpen(!historyOpen);
              setDetailsOpen(false);
            }}
          >
            <History size={16} />
            <span className="zen-wiki__action-label">History</span>
          </button>
          {canPublish && !editing && page.status === "active" && (
            <button
              className="zen-wiki__primary"
              onClick={() => onEditingChange(true)}
            >
              <Pencil size={16} /> Edit draft
            </button>
          )}
          {editing && page.currentRevisionId && (
            <button disabled={dirty} onClick={() => onEditingChange(false)}>
              View published
            </button>
          )}
          {editing && <span className="contents" ref={setDraftActions} />}
        </div>
      </div>
      <div className="zen-wiki__reader-grid">
        <div className="zen-wiki__document-scroll">
          {editing ? (
            <WikiDocumentWorkspace
              key={`${companyId}:${page.id}`}
              companyId={companyId}
              pageId={page.id}
              onDirtyChange={onDirtyChange}
              onPublished={() => onEditingChange(false)}
              actionsContainer={draftActions}
            />
          ) : (
            <article className="zen-wiki__document">
              <div className="zen-wiki__eyebrow">
                <BookOpen size={15} />
                {collectionName(page.collectionId)}
              </div>
              <h1>{page.title}</h1>
              {meta}
              {page.status === "archived" && (
                <p className="inline-block rounded-[5px] [background:var(--workspace-approval-soft)] text-[var(--workspace-native-approval)] p-[4px_8px]">
                  Archived page
                </p>
              )}
              <div className="zen-wiki__prose" ref={setProse}>
                <WikiContent
                  companyId={companyId}
                  pageId={page.id}
                  content={page.content}
                  body={page.body}
                />
              </div>
              {!page.body.trim() && (
                <p className="text-[var(--wk-muted)]">This page is empty.</p>
              )}
            </article>
          )}
        </div>
        {historyOpen && (
          <WikiHistoryPanel
            companyId={companyId}
            userId={userId}
            page={page}
            canRestore={canPublish && page.status === "active"}
            editing={dirty}
            onRestore={(revision) =>
              setRestoreReview({ revision, snapshot: page })
            }
            onClose={() => setHistoryOpen(false)}
          />
        )}
        {detailsOpen && !historyOpen && (
          <WikiDetailsPanel
            companyId={companyId}
            page={page}
            prose={prose}
            canPublish={canPublish}
            editing={editing}
            onOpenPage={onOpenPage}
            onClose={() => setDetailsOpen(false)}
          />
        )}
      </div>
      {restoreReview && (
        <WikiRestoreDialog
          companyId={companyId}
          page={page}
          review={restoreReview}
          collectionName={collectionName}
          onReviewLatest={() =>
            setRestoreReview({
              revision: restoreReview.revision,
              snapshot: page,
            })
          }
          onRestored={() => onEditingChange(true)}
          onClose={() => setRestoreReview(null)}
        />
      )}
    </>
  );
}
