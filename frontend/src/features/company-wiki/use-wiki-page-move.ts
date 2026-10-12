import { useRef, useState, type DragEvent } from "react";
import { toast } from "@/shared/ui/sonner";
import {
  useLazyGetWikiDocumentQuery,
  useUpdateWikiDraftMetadataMutation,
  type WikiPageSummary,
} from "./wiki-api";

const dragType = "application/x-zenith-wiki-page";

/**
 * Drag a sidebar page onto a collection (or Unfiled, `null`) to move it.
 * The move updates only draft metadata. Publication updates the visible collection.
 */
export function useWikiPageMove({
  companyId,
  enabled,
  pages,
  collectionName,
  onMoved,
}: {
  companyId: string;
  /** Publishers can move pages while no draft is open. */
  enabled: boolean;
  pages: WikiPageSummary[];
  collectionName: (collectionId: string | null) => string;
  onMoved: (collectionId: string | null) => void;
}) {
  const [draggedPageId, setDraggedPageId] = useState<string | null>(null);
  const [dropCollectionId, setDropCollectionId] = useState<
    string | null | undefined
  >();
  const draggedPageIdRef = useRef<string | null>(null);
  const movingRef = useRef(false);
  const [loadPage] = useLazyGetWikiDocumentQuery();
  const [updatePage] = useUpdateWikiDraftMetadataMutation();

  function endDrag() {
    draggedPageIdRef.current = null;
    setDraggedPageId(null);
    setDropCollectionId(undefined);
  }

  async function move(sourceId: string, targetId: string | null) {
    const summary = pages.find((item) => item.id === sourceId);
    if (!summary || summary.collectionId === targetId) return;
    movingRef.current = true;
    try {
      const source = await loadPage({ companyId, pageId: sourceId }).unwrap();
      if (!source.canEdit || source.collectionId === targetId) return;
      const result = await updatePage({
        companyId,
        pageId: sourceId,
        request: {
          expectedSequence: source.sequence,
          title: source.title,
          summary: source.summary,
          collectionId: targetId,
        },
      });
      if ("error" in result) {
        const conflict =
          result.error &&
          "status" in result.error &&
          result.error.status === 409;
        toast.error(
          conflict
            ? "This page changed while you moved it. Try again after reloading."
            : (result.error?.message ?? "Page could not be moved."),
          conflict ? { duration: Infinity } : undefined,
        );
        return;
      }
      onMoved(targetId);
      toast.success(
        `Saved ${collectionName(targetId)} in the draft. Publish the page to update its Wiki collection.`,
      );
    } catch {
      toast.error("Page could not be moved. Try again.");
    } finally {
      movingRef.current = false;
    }
  }

  return {
    draggedPageId,
    dropCollectionId,
    /** Props for a draggable page link. */
    pageDragProps: (item: WikiPageSummary) => ({
      draggable: enabled,
      onDragStart: (event: DragEvent<HTMLElement>) => {
        if (!enabled) {
          event.preventDefault();
          return;
        }
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData(dragType, item.id);
        draggedPageIdRef.current = item.id;
        setDraggedPageId(item.id);
      },
      onDragEnd: endDrag,
    }),
    /** Props for a collection (or Unfiled when `null`) drop target. */
    dropTargetProps: (targetId: string | null) => ({
      onDragOver: (event: DragEvent<HTMLElement>) => {
        const source = pages.find(
          (item) => item.id === draggedPageIdRef.current,
        );
        if (!enabled || !source || source.collectionId === targetId) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        setDropCollectionId(targetId);
      },
      onDragLeave: (event: DragEvent<HTMLElement>) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          setDropCollectionId(undefined);
        }
      },
      onDrop: (event: DragEvent<HTMLElement>) => {
        const sourceId =
          event.dataTransfer.getData(dragType) || draggedPageIdRef.current;
        if (!sourceId) return;
        event.preventDefault();
        endDrag();
        if (!enabled || movingRef.current) return;
        void move(sourceId, targetId);
      },
    }),
  };
}
