import { useRef, useState, type FormEvent } from "react";
import { MoreHorizontal, Pencil, Trash2 } from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { toast } from "@/shared/ui/sonner";
import {
  useDeleteWikiCollectionMutation,
  useRenameWikiCollectionMutation,
  type WikiCollection,
} from "./wiki-api";
import { WikiDialog } from "./wiki-dialogs";

export function WikiCollectionMenu({
  companyId,
  collection,
  disabled,
  focusAfterDelete,
  onDeleted,
}: {
  companyId: string;
  collection: WikiCollection;
  disabled: boolean;
  /** A control that outlives this menu; focus lands there after a delete. */
  focusAfterDelete: { current: HTMLElement | null };
  onDeleted: (id: string) => void;
}) {
  // Freeze the reviewed revision; a realtime rename must not silently change it.
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [review, setReview] = useState<{
    action: "rename" | "delete";
    collection: WikiCollection;
  } | null>(null);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            ref={triggerRef}
            className="zen-wiki__collection-menu"
            disabled={disabled}
            aria-label={`Actions for ${collection.name}`}
            title="Collection actions"
          >
            <MoreHorizontal size={16} aria-hidden="true" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onSelect={() => setReview({ action: "rename", collection })}
          >
            <Pencil size={16} /> Rename
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setReview({ action: "delete", collection })}
          >
            <Trash2 size={16} /> Delete collection
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {review && (
        <CollectionDialog
          companyId={companyId}
          collection={review.collection}
          action={review.action}
          returnFocus={triggerRef}
          focusAfterDelete={focusAfterDelete}
          onClose={() => setReview(null)}
          onDeleted={onDeleted}
        />
      )}
    </>
  );
}

function CollectionDialog({
  companyId,
  collection,
  action,
  onClose,
  onDeleted,
  returnFocus,
  focusAfterDelete,
}: {
  companyId: string;
  collection: WikiCollection;
  action: "rename" | "delete";
  onClose: () => void;
  onDeleted: (id: string) => void;
  returnFocus: { current: HTMLElement | null };
  focusAfterDelete: { current: HTMLElement | null };
}) {
  const [name, setName] = useState(collection.name);
  const [error, setError] = useState<string | null>(null);
  const [renameCollection, renameState] = useRenameWikiCollectionMutation();
  const [deleteCollection, deleteState] = useDeleteWikiCollectionMutation();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  // The dialog restores focus after it unmounts. A successful delete removes
  // the menu trigger, so it retargets focus to a control that survives.
  const closeFocus = useRef<HTMLElement | null>(returnFocus.current);
  const busy = renameState.isLoading || deleteState.isLoading;
  const deleting = action === "delete";

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy || (!deleting && !name.trim())) return;
    setError(null);
    const input = {
      companyId,
      collectionId: collection.id,
      request: { expectedRevision: collection.revision },
    };
    const result = deleting
      ? await deleteCollection(input)
      : await renameCollection({
          ...input,
          request: { ...input.request, name: name.trim() },
        });
    if ("error" in result) {
      const conflict =
        result.error && "status" in result.error && result.error.status === 409;
      if (conflict) {
        setError(
          deleting
            ? "This collection changed since you opened this dialog. Close it and review the collection before deleting."
            : "The collection changed or that name is already in use. Close this dialog and try again with the latest collection.",
        );
      } else {
        setError(
          result.error?.message ??
            (deleting
              ? "Collection could not be deleted."
              : "Collection could not be updated."),
        );
      }
      return;
    }
    if (deleting) closeFocus.current = focusAfterDelete.current;
    onClose();
    if (deleting) onDeleted(collection.id);
    toast.success(
      deleting
        ? "Collection deleted. Pages moved to Unfiled."
        : "Collection renamed.",
    );
  }

  return (
    <WikiDialog
      title={deleting ? "Delete collection?" : "Rename collection"}
      description={
        deleting
          ? `Delete “${collection.name}”? All its pages, including archived pages, will move to Unfiled. Their content, history and links will be kept. This collection cannot be restored.`
          : undefined
      }
      alert={deleting}
      busy={busy}
      initialFocus={deleting ? cancelRef : nameRef}
      returnFocus={closeFocus}
      onClose={onClose}
    >
      <form
        className="zen-wiki-dialog__form"
        onSubmit={(event) => void submit(event)}
      >
        {!deleting && (
          <label>
            Name
            <input
              ref={nameRef}
              value={name}
              maxLength={120}
              disabled={busy}
              onChange={(event) => setName(event.target.value)}
            />
          </label>
        )}
        {error && (
          <p className="text-workspace-danger p-[0_10px]" role="alert">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-[8px]">
          <button
            ref={cancelRef}
            type="button"
            className="zen-wiki__button"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            className={`zen-wiki__button ${deleting ? "zen-wiki__delete-collection" : "zen-wiki__primary"}`}
            disabled={
              busy ||
              (!deleting && (!name.trim() || name.trim() === collection.name))
            }
          >
            {busy
              ? deleting
                ? "Deleting…"
                : "Saving…"
              : deleting
                ? "Delete collection"
                : "Save name"}
          </button>
        </div>
      </form>
    </WikiDialog>
  );
}
