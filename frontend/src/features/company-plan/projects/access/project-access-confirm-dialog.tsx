import { useRef, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon, type IconName } from "@/shared/ui/icon";

import {
  AccessCounts,
  hasCounts,
  type AccessCountValues,
} from "./project-access-counts";

// The dialog shell every confirmation in the app is drawn with.
import "@/features/settings/styles/settings.css";
import "./project-access.css";

export type AccessConfirmation = {
  /** A short question: "Remove Mia Member from Payroll?". */
  title: string;
  /** What the change touches, as icons with counts; zeros are left out. */
  counts?: AccessCountValues;
  /** One or two words: "Remove". */
  action: string;
  /** A removal or a delete: a red alert and a red button. */
  destructive?: boolean;
  /** In place of the alert or pause icon: `archive` for Archive. */
  icon?: IconName;
  /** The button while it runs, "Saving…" unless given: "Archiving…". */
  pending?: string;
  run: () => Promise<void>;
  /** Called when they back out instead. */
  onCancel?: () => void;
};

// Asks before an access change that stops someone's work or takes their
// access away completely, or before a Project is archived: a short
// question, the counts when there are any, and the action, in a compact
// box. A removal or a delete shows a red alert; a change that only stops
// runs shows a calm pause icon; Archive shows its own icon. It
// opens focused on itself, so no button starts with a focus ring. The
// change runs only on the confirm button; Escape and Cancel leave
// everything as it was.
export function ProjectAccessConfirmDialog({
  confirmation,
  onClose,
}: {
  confirmation: AccessConfirmation | null;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const cancel = () => {
    confirmation?.onCancel?.();
    onClose();
  };
  const counts = confirmation?.counts;
  const showCounts = hasCounts(counts);
  return (
    <Dialog
      open={confirmation !== null}
      onOpenChange={(next) => {
        if (!next && !busy) cancel();
      }}
    >
      <DialogContent
        {...(showCounts ? {} : { "aria-describedby": undefined })}
        className="profile-delete-dialog project-access-confirm"
        overlayClassName="profile-delete-dialog__overlay"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          surfaceRef.current?.focus({ preventScroll: true });
        }}
      >
        {confirmation ? (
          <div
            ref={surfaceRef}
            className="profile-delete-dialog__surface"
            tabIndex={-1}
          >
            <DialogHeader className="profile-delete-dialog__header">
              <span
                className="profile-delete-dialog__icon"
                data-tone={confirmation.destructive ? "danger" : "neutral"}
              >
                <Icon
                  name={
                    confirmation.icon ??
                    (confirmation.destructive ? "alert" : "hold")
                  }
                />
              </span>
              <DialogTitle>{confirmation.title}</DialogTitle>
              {counts && showCounts ? (
                <DialogDescription asChild>
                  <div className="project-access-confirm__counts">
                    <AccessCounts counts={counts} />
                  </div>
                </DialogDescription>
              ) : null}
            </DialogHeader>
            <DialogFooter className="profile-delete-dialog__footer">
              <button
                className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
                disabled={busy}
                type="button"
                onClick={cancel}
              >
                Cancel
              </button>
              <button
                className={
                  confirmation.destructive
                    ? "profile-delete-dialog__button profile-delete-dialog__button--delete"
                    : "profile-delete-dialog__button runtime-dialog__submit"
                }
                disabled={busy}
                type="button"
                onClick={() => {
                  setBusy(true);
                  void confirmation.run().finally(() => {
                    setBusy(false);
                    onClose();
                  });
                }}
              >
                {busy
                  ? (confirmation.pending ?? "Saving…")
                  : confirmation.action}
              </button>
            </DialogFooter>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
