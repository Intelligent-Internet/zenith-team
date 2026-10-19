import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import { X } from "@/shared/ui/icons";
import {
  useGetWikiDocumentQuery,
  type WikiDocument,
  useCreateWikiCollectionMutation,
  useCreateWikiPageMutation,
  useRestoreWikiRevisionMutation,
  type WikiCollection,
  type WikiPageDetail,
  type WikiRevision,
} from "./wiki-api";
import { WikiContent } from "./wiki-content";
import type { components } from "@/shared/api/generated/api.generated";

/**
 * A restore under review. The Current side and the expected revision are
 * frozen when the dialog opens; realtime refetches must not silently move
 * what the user confirms.
 */
export type WikiRestoreReview = {
  revision: WikiRevision;
  snapshot: WikiPageDetail;
};

export function RestorePreview({
  companyId,
  label,
  title,
  collection,
  summary,
  body,
  content,
  pageId,
}: {
  companyId: string;
  label: string;
  title: string;
  collection: string;
  summary: string;
  body: string;
  content?: components["schemas"]["CanvasBlock"];
  pageId: string;
}) {
  return (
    <section className="zen-wiki__restore-panel">
      <span className="block p-[9px_14px] [border-bottom:1px_solid_var(--wk-border)] [background:var(--wk-soft)] text-[var(--wk-muted)] text-[length:12px] font-[600]">
        {label}
      </span>
      <div className="zen-wiki__restore-document">
        <span className="zen-wiki__eyebrow">{collection}</span>
        <h3>{title}</h3>
        {summary && (
          <p className="m-[0_0_16px] text-[var(--wk-muted)] text-[length:13px]">
            {summary}
          </p>
        )}
        <div className="zen-wiki__prose">
          {body.trim() ? (
            <WikiContent
              companyId={companyId}
              pageId={pageId}
              body={body}
              content={content}
            />
          ) : (
            <p className="text-[var(--wk-muted)]">This page is empty.</p>
          )}
        </div>
      </div>
    </section>
  );
}

/**
 * Shared frame for the Wiki dialogs: a portaled Radix dialog with focus
 * trap, Escape, outside-click dismissal and focus return.
 */
export function WikiDialog({
  title,
  description,
  alert = false,
  busy = false,
  wide = false,
  initialFocus,
  returnFocus,
  onClose,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  /** Confirmation that only closes through its own buttons or Escape. */
  alert?: boolean;
  /** While a request runs the dialog cannot be dismissed. */
  busy?: boolean;
  wide?: boolean;
  initialFocus?: { current: HTMLElement | null };
  returnFocus?: { current: HTMLElement | null };
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        className={`zen-wiki-dialog ${wide ? "zen-wiki-dialog--wide" : ""}`}
        overlayClassName="zen-wiki-dialog__overlay"
        showCloseButton={false}
        role={alert ? "alertdialog" : "dialog"}
        {...(description ? {} : { "aria-describedby": undefined })}
        onOpenAutoFocus={(event) => {
          if (!initialFocus?.current) return;
          event.preventDefault();
          initialFocus.current.focus();
        }}
        onCloseAutoFocus={(event) => {
          if (!returnFocus?.current) return;
          event.preventDefault();
          returnFocus.current.focus();
        }}
        onInteractOutside={(event) => {
          if (alert || busy) event.preventDefault();
        }}
      >
        <div className="flex items-center justify-between gap-[12px]">
          <DialogTitle className="m-0 text-workspace-title leading-workspace-title font-[600]">
            {title}
          </DialogTitle>
          <DialogClose
            className="zen-wiki-dialog__close"
            aria-label="Close"
            disabled={busy}
          >
            <X size={17} />
          </DialogClose>
        </div>
        {description && (
          <DialogDescription className="m-0 text-[var(--wk-muted)]">
            {description}
          </DialogDescription>
        )}
        {children}
      </DialogContent>
    </Dialog>
  );
}

export function WikiRestoreDialog({
  companyId,
  page,
  review,
  collectionName,
  onReviewLatest,
  onRestored,
  onClose,
}: {
  companyId: string;
  /** The live page; compared with the snapshot to detect concurrent saves. */
  page: WikiPageDetail;
  review: WikiRestoreReview;
  collectionName: (collectionId: string | null) => string;
  onReviewLatest: () => void;
  onRestored: () => void;
  onClose: () => void;
}) {
  const [restoreRevision, restoreState] = useRestoreWikiRevisionMutation();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const { revision, snapshot } = review;
  const document = useGetWikiDocumentQuery({ companyId, pageId: page.id });
  const [draft, setDraft] = useState<WikiDocument | null>(null);
  useEffect(() => {
    if (!draft && document.currentData) setDraft(document.currentData);
  }, [draft, document.currentData]);
  const stale =
    page.revision !== snapshot.revision ||
    (!!draft && document.currentData?.sequence !== draft.sequence);

  async function confirm() {
    if (!draft) return;
    const result = await restoreRevision({
      companyId,
      pageId: snapshot.id,
      revisionId: revision.id,
      expectedRevision: snapshot.revision,
      expectedSequence: draft.sequence,
    });
    if ("error" in result) {
      const conflict =
        result.error && "status" in result.error && result.error.status === 409;
      if (conflict) onClose();
      toast.error(
        conflict
          ? "This page changed while you reviewed its history. Review the latest version before restoring."
          : (result.error?.message ?? "Could not restore revision."),
        conflict ? { duration: Infinity } : undefined,
      );
      return;
    }
    onClose();
    onRestored();
    toast.success(`Revision ${revision.number} restored to the working draft.`);
  }

  return (
    <WikiDialog
      alert
      wide
      busy={restoreState.isLoading}
      initialFocus={cancelRef}
      title={`Restore revision ${revision.number}?`}
      description="Replace the working draft with this revision. Readers keep seeing the published page until you review and publish the restored draft."
      onClose={onClose}
    >
      {stale && (
        <p
          className="zen-wiki__restore-stale m-0 rounded-[8px] p-[9px_12px] [background:var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]"
          role="alert"
        >
          This page changed while you were reviewing.{" "}
          <button
            onClick={() => {
              if (document.currentData) setDraft(document.currentData);
              onReviewLatest();
            }}
          >
            Compare with the latest version
          </button>
        </p>
      )}
      <div className="zen-wiki__restore-comparison">
        {draft ? (
          <RestorePreview
            companyId={companyId}
            label="Current working draft"
            title={draft.title}
            collection={collectionName(draft.collectionId)}
            summary={draft.summary}
            body={draft.markdown}
            content={draft.content}
            pageId={page.id}
          />
        ) : (
          <p role={document.isError ? "alert" : "status"}>
            {document.isError
              ? "The current draft could not be loaded."
              : "Loading current draft…"}
          </p>
        )}
        <RestorePreview
          companyId={companyId}
          label={`After restore · from revision ${revision.number}`}
          title={revision.title}
          collection={collectionName(revision.collectionId)}
          summary={revision.summary}
          body={revision.body}
          pageId={page.id}
          content={revision.content}
        />
      </div>
      <div className="flex justify-end gap-[8px]">
        <button
          ref={cancelRef}
          className="zen-wiki__button"
          disabled={restoreState.isLoading}
          onClick={onClose}
        >
          Cancel
        </button>
        <button
          className="zen-wiki__button zen-wiki__primary"
          disabled={restoreState.isLoading || stale || !draft}
          onClick={() => void confirm()}
        >
          {restoreState.isLoading ? "Restoring…" : "Restore to draft"}
        </button>
      </div>
    </WikiDialog>
  );
}

export function WikiNewPageDialog({
  companyId,
  collections,
  defaultCollectionId,
  onCreated,
  onClose,
}: {
  companyId: string;
  collections: WikiCollection[];
  defaultCollectionId: string | null;
  onCreated: (pageId: string) => void;
  onClose: () => void;
}) {
  const [title, setTitle] = useState("");
  const [collectionId, setCollectionId] = useState(defaultCollectionId ?? "");
  const [createPage, createState] = useCreateWikiPageMutation();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = title.trim();
    if (!trimmed) return;
    const result = await createPage({
      companyId,
      request: {
        title: trimmed,
        summary: "",
        collectionId: collectionId || null,
      },
    });
    if ("error" in result) {
      toast.error(result.error?.message ?? "Page could not be created.");
      return;
    }
    onCreated(result.data.id);
  }

  return (
    <WikiDialog title="New page" busy={createState.isLoading} onClose={onClose}>
      <form
        className="zen-wiki-dialog__form"
        onSubmit={(event) => void submit(event)}
      >
        <label>
          Title
          <input
            autoFocus
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="What is this page about?"
            maxLength={240}
          />
        </label>
        <label>
          Collection
          <select
            value={collectionId}
            onChange={(event) => setCollectionId(event.target.value)}
          >
            <option value="">Unfiled</option>
            {collections.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </label>
        <div className="flex justify-end gap-[8px]">
          <button
            className="zen-wiki__button zen-wiki__primary"
            disabled={!title.trim() || createState.isLoading}
          >
            Create and write
          </button>
        </div>
      </form>
    </WikiDialog>
  );
}

export function WikiNewCollectionDialog({
  companyId,
  onClose,
}: {
  companyId: string;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [createCollection, createState] = useCreateWikiCollectionMutation();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    const result = await createCollection({ companyId, name: trimmed });
    if ("error" in result) {
      toast.error(result.error?.message ?? "Collection could not be created.");
      return;
    }
    onClose();
    toast.success("Collection created.");
  }

  return (
    <WikiDialog
      title="New collection"
      busy={createState.isLoading}
      onClose={onClose}
    >
      <form
        className="zen-wiki-dialog__form"
        onSubmit={(event) => void submit(event)}
      >
        <label>
          Name
          <input
            autoFocus
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Collection name"
            maxLength={120}
          />
        </label>
        <div className="flex justify-end gap-[8px]">
          <button
            className="zen-wiki__button zen-wiki__primary"
            disabled={!name.trim() || createState.isLoading}
          >
            Create collection
          </button>
        </div>
      </form>
    </WikiDialog>
  );
}
