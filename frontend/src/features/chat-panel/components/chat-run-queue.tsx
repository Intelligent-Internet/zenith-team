import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  FileText,
  GripVertical,
  Image as ImageIcon,
  ListEnd,
  LoaderCircle,
  Trash2,
} from "@/shared/ui/icons";
import { useEffect, useState } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  conversationRunQueueMutationErrorMessage,
  type ConversationRunQueueAvailability,
} from "@/features/chat-panel/hooks/use-conversation-run-queue";
import { attachmentService } from "@/shared/api/attachment.service";
import type {
  AttachmentDto,
  ConversationRunQueueItem,
} from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { toast } from "@/shared/ui/sonner";
import "../styles/chat-run-queue.css";

export function ChatRunQueue({
  deletingItemIds,
  error,
  availability,
  isExpanded,
  isReordering,
  items,
  onRefresh,
  onRemove,
  onReorder,
}: {
  availability: ConversationRunQueueAvailability;
  deletingItemIds: ReadonlySet<string>;
  error: string | null;
  isExpanded: boolean;
  isReordering: boolean;
  items: ConversationRunQueueItem[];
  onRefresh: () => Promise<unknown>;
  onRemove: (item: ConversationRunQueueItem) => Promise<void>;
  onReorder: (items: ConversationRunQueueItem[]) => Promise<void>;
}) {
  const [announcement, setAnnouncement] = useState("");
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  if (!items.length && availability !== "unavailable") return null;

  const handleDragEnd = (event: DragEndEvent) => {
    if (!event.over || event.active.id === event.over.id) return;
    const from = items.findIndex((item) => item.id === event.active.id);
    const to = items.findIndex((item) => item.id === event.over?.id);
    if (from < 0 || to < 0) return;
    const reordered = arrayMove(items, from, to);
    setAnnouncement(
      `Moved queued prompt to position ${to + 1} of ${items.length}.`,
    );
    void onReorder(reordered).catch((cause) => {
      toast.error(conversationRunQueueMutationErrorMessage(cause, "reorder"));
    });
  };

  return (
    <section
      aria-label="Queued prompts"
      className={cx(
        "workspace-chat-run-queue",
        isExpanded && "workspace-chat-run-queue--expanded",
      )}
    >
      {availability === "unavailable" ? (
        <div
          role="status"
          className="chat-run-queue-status"
          data-state={availability}
        >
          <p>
            {error ??
              "Queue unavailable. Sending is paused until it reconnects."}
          </p>
          <button
            className="chat-run-queue-retry"
            type="button"
            onClick={() => void onRefresh().catch(() => undefined)}
          >
            Retry
          </button>
        </div>
      ) : null}
      <DndContext
        collisionDetection={closestCenter}
        sensors={sensors}
        onDragEnd={handleDragEnd}
      >
        <SortableContext
          items={items.map((item) => item.id)}
          strategy={verticalListSortingStrategy}
        >
          <ol className="chat-run-queue-list">
            {items.map((item, index) => (
              <SortableRunQueueItem
                key={item.id}
                deleting={deletingItemIds.has(item.id)}
                disabled={
                  isReordering ||
                  deletingItemIds.size > 0 ||
                  availability !== "ready"
                }
                index={index}
                item={item}
                total={items.length}
                onRemove={onRemove}
              />
            ))}
          </ol>
        </SortableContext>
      </DndContext>
      <span className="sr-only" aria-live="polite">
        {announcement}
      </span>
    </section>
  );
}

function SortableRunQueueItem({
  deleting,
  disabled,
  index,
  item,
  onRemove,
  total,
}: {
  deleting: boolean;
  disabled: boolean;
  index: number;
  item: ConversationRunQueueItem;
  onRemove: (item: ConversationRunQueueItem) => Promise<void>;
  total: number;
}) {
  const {
    attributes,
    isDragging,
    listeners,
    setNodeRef,
    transform,
    transition,
  } = useSortable({ disabled: disabled || total < 2, id: item.id });
  const firstAttachment = item.attachments[0];
  const prompt = item.content || firstAttachment?.filename;
  // In a task chat a colleague's message waits here for the assignee's next
  // run (ADR 0014), so it says whose words those are; your own need no name.
  const viewerUserId = useSessionSnapshot().userId;
  const author =
    item.initiatedBy.id === viewerUserId ? null : item.initiatedBy.name;

  return (
    <li
      ref={setNodeRef}
      className={cx(
        "relative flex min-w-0 min-h-[36px] items-center gap-[8px] rounded-[8px] py-[4px]",
        isDragging && "z-[1] [background:var(--queue-hover)]",
      )}
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
      }}
    >
      {total > 1 ? (
        <button
          {...attributes}
          {...listeners}
          aria-label={`Reorder queued prompt ${index + 1} of ${total}`}
          className="chat-run-queue-action chat-run-queue-drag"
          disabled={disabled}
          title="Drag to reorder"
          type="button"
        >
          <GripVertical size={16} aria-hidden />
        </button>
      ) : (
        <span
          className="chat-run-queue-icon"
          title="Sends after the current response"
        >
          <ListEnd size={16} aria-hidden />
        </span>
      )}
      {firstAttachment ? (
        <RunQueueAttachment
          key={firstAttachment.id}
          attachment={firstAttachment}
        />
      ) : null}
      {item.attachments.length > 1 ? (
        <span
          className="shrink-0 text-[var(--queue-muted)] text-[length:11px]"
          aria-label={
            item.attachments.length === 2
              ? "1 more attachment"
              : `${item.attachments.length - 1} more attachments`
          }
        >
          +{item.attachments.length - 1}
        </span>
      ) : null}
      <p
        className="min-w-0 flex-1 m-0 overflow-hidden text-ellipsis whitespace-nowrap"
        title={prompt}
      >
        {author ? <strong>{author}: </strong> : null}
        {prompt}
      </p>
      <button
        aria-label={`Delete queued prompt ${index + 1}`}
        className="chat-run-queue-action chat-run-queue-remove"
        disabled={deleting || disabled}
        title="Remove from queue"
        type="button"
        onClick={() => {
          void onRemove(item).catch((cause) => {
            toast.error(
              conversationRunQueueMutationErrorMessage(cause, "remove"),
            );
          });
        }}
      >
        {deleting ? (
          <LoaderCircle className="animate-spin" size={15} aria-hidden />
        ) : (
          <Trash2 size={15} aria-hidden />
        )}
      </button>
    </li>
  );
}

function RunQueueAttachment({ attachment }: { attachment: AttachmentDto }) {
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const isImage = attachment.contentType.startsWith("image/");

  useEffect(() => {
    if (!isImage) return;

    const controller = new AbortController();
    let objectUrl: string | undefined;
    void attachmentService
      .download(attachment.companyId, attachment.id, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return;
        objectUrl = URL.createObjectURL(blob);
        setPreviewUrl(objectUrl);
      })
      .catch(() => {
        // Keep the file icon when a thumbnail cannot be loaded.
      });

    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [attachment.companyId, attachment.id, isImage]);

  return (
    <span
      className="chat-run-queue-attachment flex shrink-0 items-center justify-center w-[24px] h-[24px] overflow-hidden [border:1px_solid_var(--queue-border)] rounded-[5px] [background:var(--workspace-chat-bg,_#191919)] text-[var(--queue-muted)]"
      title={attachment.filename}
    >
      {previewUrl ? (
        <img
          alt={attachment.filename}
          src={previewUrl}
          onError={() => setPreviewUrl(null)}
        />
      ) : (
        <span role="img" aria-label={attachment.filename}>
          {isImage ? (
            <ImageIcon size={16} aria-hidden />
          ) : (
            <FileText size={16} aria-hidden />
          )}
        </span>
      )}
    </span>
  );
}
