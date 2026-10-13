import { PagePdfExport } from "../project-canvas/documents/page-pdf";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type SetStateAction,
} from "react";
import { createPortal } from "react-dom";
import type { Editor } from "@tiptap/react";
import { BookOpen } from "@/shared/ui/icons";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { downloadDocument } from "../project-canvas/documents/document-export";
import { LiveDocument } from "../project-canvas/documents/live-document";
import { WikiContent } from "./wiki-content";
import { WikiDialog } from "./wiki-dialogs";
import { DocumentFormatToolbar } from "../project-canvas/documents/document-format-toolbar";
import { toast } from "@/shared/ui/sonner";
import {
  useGetWikiDocumentQuery,
  useGetWikiPageQuery,
  useListWikiCollectionsQuery,
  usePublishWikiPageMutation,
  useUpdateWikiDraftMetadataMutation,
  type WikiDocument,
  type WikiDraftMetadata,
  type WikiPageDetail,
} from "./wiki-api";
import "../project-canvas/canvas/project-canvas.css";
import "../project-canvas/documents/document-workspace.css";
import "./wiki-document-workspace.css";

type WikiDocumentWorkspaceProps = {
  companyId: string;
  pageId: string;
  onDirtyChange: (dirty: boolean) => void;
  onPublished?: () => void;
  /**
   * The Wiki page bar. When given, the editor takes the Wiki's own layout:
   * the save state and draft actions sit in the page bar, and collection,
   * title, status and summary head the document as on the published page.
   * Hosts without one (Canvas review) keep the in-document draft bar.
   */
  actionsContainer?: HTMLElement | null;
};

function readPendingMetadata(key: string): WikiDraftMetadata | null {
  try {
    const value = JSON.parse(window.sessionStorage.getItem(key) ?? "null");
    return value &&
      Number.isInteger(value.expectedSequence) &&
      value.expectedSequence >= 0 &&
      typeof value.title === "string" &&
      typeof value.summary === "string" &&
      (value.collectionId === null || typeof value.collectionId === "string")
      ? value
      : null;
  } catch {
    return null;
  }
}

/** Wiki and Canvas both open this one working draft and publication operation. */
export function WikiDocumentWorkspace(props: WikiDocumentWorkspaceProps) {
  const { userId } = useSessionSnapshot();
  const recoveryKey = `wiki-metadata-draft:${userId}:${props.companyId}:${props.pageId}`;
  return (
    <WikiDocumentEditor
      key={recoveryKey}
      {...props}
      recoveryKey={recoveryKey}
    />
  );
}

function WikiDocumentEditor({
  companyId,
  pageId,
  onDirtyChange,
  onPublished,
  actionsContainer,
  recoveryKey,
}: WikiDocumentWorkspaceProps & { recoveryKey: string }) {
  const scope = { companyId, wikiPageId: pageId };
  const [recoveredDraft, setRecoveredDraft] = useState<string | null>(() => {
    try {
      return window.sessionStorage.getItem(`wiki-draft:${companyId}:${pageId}`);
    } catch {
      return null;
    }
  });
  const draft = useGetWikiDocumentQuery({ companyId, pageId });
  const page = useGetWikiPageQuery({ companyId, pageId });
  const collections = useListWikiCollectionsQuery(companyId);
  const [metadata, updating] = useUpdateWikiDraftMetadataMutation();
  const [publish, publishing] = usePublishWikiPageMutation();
  const [statusContainer, setStatusContainer] = useState<HTMLDivElement | null>(
    null,
  );
  const [editor, setEditor] = useState<Editor | null>(null);
  const [review, setReview] = useState<{
    draft: WikiDocument;
    published: WikiPageDetail;
  } | null>(null);
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const [metadataDraft, setMetadataState] = useState<WikiDraftMetadata | null>(
    () => readPendingMetadata(recoveryKey),
  );
  const pendingMetadata = useRef(metadataDraft);
  const mounted = useRef(true);
  const saveMetadataRef = useRef<() => Promise<void>>(async () => {});
  const metadataFlight = useRef<Promise<void> | null>(null);
  const [recoveryError, setRecoveryError] = useState("");
  // Write before React navigation can unmount the editor and cancel its debounce.
  const setMetadataDraft = useCallback(
    (change: SetStateAction<WikiDraftMetadata | null>) => {
      const previous = pendingMetadata.current;
      const next = typeof change === "function" ? change(previous) : change;
      pendingMetadata.current = next;
      try {
        if (
          !mounted.current &&
          JSON.stringify(readPendingMetadata(recoveryKey)) !==
            JSON.stringify(previous)
        ) {
          // A reopened editor owns the newer draft; the departing save cannot clear it.
          pendingMetadata.current = null;
          return;
        }
        if (next)
          window.sessionStorage.setItem(recoveryKey, JSON.stringify(next));
        else window.sessionStorage.removeItem(recoveryKey);
        if (mounted.current) setRecoveryError("");
      } catch {
        if (mounted.current)
          setRecoveryError(
            "Draft details could not be kept on this browser. Keep this page open until they are saved.",
          );
      }
      if (mounted.current) setMetadataState(next);
    },
    [recoveryKey],
  );
  const [metadataError, setMetadataError] = useState("");

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      void saveMetadataRef.current();
    };
  }, []);
  const data = draft.currentData;
  useEffect(
    () => onDirtyChange(dirty || !!metadataDraft),
    [dirty, metadataDraft, onDirtyChange],
  );
  useEffect(() => {
    if (!metadataDraft) return;
    const protect = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", protect);
    return () => window.removeEventListener("beforeunload", protect);
  }, [metadataDraft]);
  const saveMetadata = (): Promise<void> => {
    if (metadataFlight.current) return metadataFlight.current;
    if (!data?.canEdit || metadataError) return Promise.resolve();
    const job = (async () => {
      while (pendingMetadata.current?.title.trim()) {
        const submitted = pendingMetadata.current;
        try {
          const saved = await metadata({
            companyId,
            pageId,
            request: submitted,
          }).unwrap();
          setMetadataDraft((current) =>
            current === submitted
              ? null
              : current
                ? { ...current, expectedSequence: saved.sequence }
                : null,
          );
        } catch (reason) {
          if (mounted.current) {
            void draft.refetch();
            setMetadataError(
              (reason as { message?: string })?.message ??
                "Draft details changed or could not be saved. Your edits are kept here.",
            );
          } else {
            toast.error(
              "Wiki details haven’t synced. Reopen the draft to retry.",
            );
          }
          break;
        }
      }
    })().finally(() => {
      metadataFlight.current = null;
    });
    metadataFlight.current = job;
    return job;
  };
  saveMetadataRef.current = saveMetadata;
  useEffect(() => {
    if (!metadataDraft || metadataError) return;
    const timer = window.setTimeout(() => void saveMetadataRef.current(), 500);
    return () => window.clearTimeout(timer);
  }, [metadataDraft, metadataError]);
  const editMetadata = (
    changes: Partial<Pick<WikiDocument, "title" | "summary" | "collectionId">>,
  ) => {
    if (!data) return;
    setMetadataDraft((current) => ({
      expectedSequence: data.sequence,
      title: data.title,
      summary: data.summary,
      collectionId: data.collectionId,
      ...current,
      ...changes,
    }));
  };
  const prepare = async () => {
    const [currentDraft, published] = await Promise.all([
      draft.refetch().unwrap(),
      page.refetch().unwrap(),
    ]);
    setReview({ draft: currentDraft, published });
    setError("");
  };
  if (!data)
    return (
      <p role={draft.isError ? "alert" : "status"}>
        {draft.isError
          ? "The working draft is unavailable."
          : "Opening working draft…"}
        {draft.isError && (
          <button onClick={() => void draft.refetch()}>Retry</button>
        )}
      </p>
    );
  const details = metadataDraft ?? data;
  const stale =
    review &&
    (data.sequence !== review.draft.sequence ||
      page.currentData?.revision !== review.published.revision);
  const wikiLayout = actionsContainer !== undefined;
  const published = page.currentData;
  const pdfExport = (
    <PagePdfExport
      title={data.title}
      disabled={dirty || !!metadataDraft || draft.isFetching}
    >
      <WikiContent
        companyId={companyId}
        pageId={pageId}
        content={data.content}
        body={data.markdown}
      />
    </PagePdfExport>
  );
  return (
    <section className="wiki-document-workspace pc-document-workspace pc-page-theme">
      {recoveredDraft && (
        <div className="pc-inline-warning" role="status">
          A draft from the previous editor is available on this browser.
          <button
            onClick={() =>
              downloadDocument(
                recoveredDraft,
                "recovered-wiki-draft.json",
                "application/json",
              )
            }
          >
            Download recovered draft
          </button>
          <button onClick={() => setRecoveredDraft(null)}>Dismiss</button>
        </div>
      )}
      {data.canEdit && <DocumentFormatToolbar editor={editor} />}
      {wikiLayout ? (
        actionsContainer &&
        createPortal(
          <div className="wiki-draft-actions flex items-center gap-[8px]">
            {pdfExport}
            <div ref={setStatusContainer} />
          </div>,
          actionsContainer,
        )
      ) : (
        <>
          <div className="wiki-document-workspace__bar">
            <span className="flex-none text-[var(--pc-ink)] font-[600]">
              Working draft
            </span>
            {pdfExport}
            <div
              className="wiki-document-workspace__status"
              ref={setStatusContainer}
            />
          </div>
          <div className="wiki-document-workspace__details">
            <label>
              <span>Collection</span>
              <select
                aria-label="Draft collection"
                value={details.collectionId ?? ""}
                disabled={!data.canEdit}
                onChange={(event) =>
                  editMetadata({ collectionId: event.target.value || null })
                }
              >
                <option value="">Unfiled</option>
                {collections.currentData?.items.map((collection) => (
                  <option key={collection.id} value={collection.id}>
                    {collection.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              <span>Summary</span>
              <input
                aria-label="Draft summary"
                value={details.summary}
                maxLength={2048}
                disabled={!data.canEdit}
                placeholder="A short description for the library"
                onChange={(event) =>
                  editMetadata({ summary: event.target.value })
                }
              />
            </label>
          </div>
        </>
      )}
      {recoveryError && metadataDraft && (
        <p role="alert" className="pc-inline-error">
          {recoveryError}
        </p>
      )}
      {metadataError && (
        <div role="alert" className="pc-inline-error">
          <p>{metadataError} Your details are still here.</p>
          <p>
            Saved title: {data.title}. Saved summary: {data.summary || "None"}.
            Saved collection:{" "}
            {collections.currentData?.items.find(
              (item) => item.id === data.collectionId,
            )?.name ?? "Unfiled"}
            .
          </p>
          <button
            onClick={() => {
              setMetadataDraft(null);
              setMetadataError("");
            }}
          >
            Use saved details
          </button>
          <button
            onClick={() => {
              setMetadataDraft((current) =>
                current
                  ? { ...current, expectedSequence: data.sequence }
                  : null,
              );
              setMetadataError("");
            }}
          >
            Save my details against this version
          </button>
        </div>
      )}
      {error && (
        <p role="alert" className="pc-inline-error">
          {error}
        </p>
      )}
      <LiveDocument
        scope={scope}
        versionId={pageId}
        title={details.title}
        readOnly={!data.canEdit}
        onDirtyChange={setDirty}
        onEditor={setEditor}
        titleContent={
          wikiLayout ? (
            <div className="grid gap-[8px]">
              <div className="wiki-draft-heading__eyebrow flex items-center gap-[8px] text-[var(--pc-muted)] text-workspace-caption font-[650]">
                <BookOpen size={15} />
                <select
                  aria-label="Collection"
                  value={details.collectionId ?? ""}
                  disabled={!data.canEdit}
                  onChange={(event) =>
                    editMetadata({ collectionId: event.target.value || null })
                  }
                >
                  <option value="">Unfiled</option>
                  {collections.currentData?.items.map((collection) => (
                    <option key={collection.id} value={collection.id}>
                      {collection.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="pc-document-title-field">
                <textarea
                  aria-label="Page title"
                  rows={1}
                  value={details.title}
                  maxLength={240}
                  disabled={!data.canEdit}
                  onChange={(event) =>
                    editMetadata({
                      title: event.target.value.replace(/\s*\n\s*/g, " "),
                    })
                  }
                />
              </div>
              <div className="wiki-draft-heading__meta flex flex-wrap items-center gap-[8px] text-[var(--pc-muted)] text-workspace-caption">
                {published?.currentRevisionId ? (
                  <span className="[border:1px_solid_var(--pc-line)] rounded-[4px] p-[0_5px] [background:var(--pc-raised)] text-[length:10px]">
                    Published · v{published.contentRevision}
                  </span>
                ) : (
                  <span>Not published yet</span>
                )}
                <span className="text-[var(--pc-accent)] font-[600]">
                  Editing
                </span>
              </div>
              <input
                className="wiki-draft-heading__summary"
                aria-label="Page summary"
                value={details.summary}
                maxLength={2048}
                disabled={!data.canEdit}
                placeholder="Add a short summary for the library"
                onChange={(event) =>
                  editMetadata({ summary: event.target.value })
                }
              />
            </div>
          ) : (
            <div className="pc-document-title-field">
              <textarea
                aria-label="Document title"
                rows={1}
                value={details.title}
                maxLength={240}
                disabled={!data.canEdit}
                onChange={(event) =>
                  editMetadata({
                    title: event.target.value.replace(/\s*\n\s*/g, " "),
                  })
                }
              />
            </div>
          )
        }
        onPublish={async () => {
          if (metadataDraft || updating.isLoading)
            throw new Error("Wait for draft details to save before reviewing.");
          await prepare();
        }}
        pendingChanges={!!metadataDraft || updating.isLoading}
        pendingError={metadataError || recoveryError}
        publishLabel="Review and publish"
        statusContainer={statusContainer}
      />
      {review && (
        <WikiDialog
          wide
          title="Publish to Company Wiki"
          description="Review the draft readers will see. Referenced sources keep their own access permissions."
          busy={publishing.isLoading}
          onClose={() => setReview(null)}
        >
          {stale && (
            <p
              className="zen-wiki__restore-stale m-0 rounded-[8px] p-[9px_12px] [background:var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]"
              role="alert"
            >
              The draft or published page changed.{" "}
              <button
                onClick={() =>
                  void prepare().catch(() =>
                    setError("The latest draft could not be loaded."),
                  )
                }
              >
                Review latest
              </button>
            </p>
          )}
          <div className="zen-wiki__restore-comparison">
            <section className="zen-wiki__restore-panel">
              <span className="block p-[9px_14px] [border-bottom:1px_solid_var(--wk-border)] [background:var(--wk-soft)] text-[var(--wk-muted)] text-[length:12px] font-[600]">
                {review.published.currentRevisionId
                  ? `Published · revision ${review.published.contentRevision}`
                  : "Not published yet"}
              </span>
              {review.published.currentRevisionId && (
                <>
                  <h3>{review.published.title}</h3>
                  <WikiContent
                    companyId={companyId}
                    pageId={pageId}
                    content={review.published.content}
                    body={review.published.body}
                  />
                </>
              )}
            </section>
            <section className="zen-wiki__restore-panel">
              <span className="block p-[9px_14px] [border-bottom:1px_solid_var(--wk-border)] [background:var(--wk-soft)] text-[var(--wk-muted)] text-[length:12px] font-[600]">
                Draft to publish
              </span>
              <h3>{review.draft.title}</h3>
              {review.draft.summary && <p>{review.draft.summary}</p>}
              <WikiContent
                companyId={companyId}
                pageId={pageId}
                content={review.draft.content}
                body={review.draft.markdown}
              />
            </section>
          </div>
          <div className="flex justify-end gap-[8px]">
            <button
              className="zen-wiki__button"
              onClick={() => setReview(null)}
              disabled={publishing.isLoading}
            >
              Keep editing
            </button>
            <button
              className="zen-wiki__button zen-wiki__primary"
              disabled={
                !!stale ||
                publishing.isLoading ||
                dirty ||
                !!metadataDraft ||
                updating.isLoading
              }
              onClick={() =>
                void publish({
                  companyId,
                  pageId,
                  request: {
                    expectedRevision: review.published.revision,
                    expectedSequence: review.draft.sequence,
                  },
                })
                  .unwrap()
                  .then(() => {
                    setReview(null);
                    toast.success("Published to Company Wiki.");
                    onPublished?.();
                  })
                  .catch((reason: unknown) => {
                    toast.error(
                      (reason as { message?: string })?.message ??
                        "The page changed. Review the latest draft before publishing.",
                    );
                    void draft.refetch();
                    void page.refetch();
                  })
              }
            >
              {publishing.isLoading ? "Publishing…" : "Publish to Company Wiki"}
            </button>
          </div>
        </WikiDialog>
      )}
    </section>
  );
}
