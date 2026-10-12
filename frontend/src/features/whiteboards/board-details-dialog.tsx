import { useId, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { LayoutDashboard } from "@/shared/ui/icons";
import type { Board } from "./board-api";

type Props = {
  board?: Board;
  pending: boolean;
  onClose: () => void;
  onSave: (details: Pick<Board, "title" | "purpose">) => Promise<void>;
};

export function WhiteboardDetailsDialog({
  board,
  pending,
  onClose,
  onSave,
}: Props) {
  const fieldId = useId();
  const [title, setTitle] = useState(board?.title ?? "");
  const [purpose, setPurpose] = useState(board?.purpose ?? "");
  const [returnFocusTarget] = useState(() =>
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null,
  );

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <DialogContent
        className="wb-dialog wb-details-dialog"
        overlayClassName="wb-details-overlay"
        showCloseButton={!pending}
        onInteractOutside={(event) => event.preventDefault()}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          if (returnFocusTarget?.isConnected) returnFocusTarget.focus();
        }}
      >
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!pending && title.trim())
              void onSave({ title: title.trim(), purpose });
          }}
        >
          <header className="wb-details-header">
            <span className="wb-details-symbol" aria-hidden="true">
              <LayoutDashboard size={24} />
            </span>
            <DialogTitle>
              {board ? "Edit whiteboard" : "New whiteboard"}
            </DialogTitle>
            <DialogDescription>
              {board
                ? "Give your team a little context for this canvas."
                : "A little room for your team's next big idea."}
            </DialogDescription>
          </header>
          <div className="wb-details-fields">
            <label htmlFor={`${fieldId}-name`}>
              Name
              <input
                id={`${fieldId}-name`}
                autoFocus
                autoComplete="off"
                required
                maxLength={200}
                placeholder="e.g. Product brainstorm"
                value={title}
                disabled={pending}
                onChange={(event) => setTitle(event.target.value)}
              />
            </label>
            <label htmlFor={`${fieldId}-purpose`}>
              <span className="wb-details-label">
                Purpose <span>Optional</span>
              </span>
              <textarea
                id={`${fieldId}-purpose`}
                maxLength={2000}
                rows={3}
                value={purpose}
                disabled={pending}
                onChange={(event) => setPurpose(event.target.value)}
                placeholder="What are we exploring together?"
              />
            </label>
          </div>
          <footer className="wb-details-footer">
            <button type="button" disabled={pending} onClick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              className="wb-primary"
              disabled={pending || !title.trim()}
            >
              {pending
                ? "Saving…"
                : board
                  ? "Save changes"
                  : "Create whiteboard"}
            </button>
          </footer>
        </form>
      </DialogContent>
    </Dialog>
  );
}
