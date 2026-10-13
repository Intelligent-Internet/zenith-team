import { useRef } from "react";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import type { CompanyInvitation } from "../api/invitations-api";

export function CancelInvitationDialog({
  invitation,
  busy,
  error,
  onConfirm,
  onClose,
  onRestoreFocus,
}: {
  invitation: CompanyInvitation;
  busy: boolean;
  error: string;
  onConfirm: () => void;
  onClose: () => void;
  onRestoreFocus: () => void;
}) {
  const keepButton = useRef<HTMLButtonElement>(null);

  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="company-invite-dialog company-invitation-cancel-dialog"
        overlayClassName="company-invite-overlay"
        showCloseButton={!busy}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          keepButton.current?.focus();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          onRestoreFocus();
        }}
      >
        <header className="company-invite-header">
          <DialogTitle className="company-invite-title">
            Cancel this invitation?
          </DialogTitle>
          <DialogDescription className="company-invite-description">
            <strong>{invitation.email}</strong> will no longer be able to join
            with this invitation. You can invite them again later.
          </DialogDescription>
        </header>
        <div className="company-invite-body">
          {error ? (
            <p
              className="m-[8px_0_0] text-workspace-danger text-[length:12px] leading-[1.5] wrap-anywhere"
              role="alert"
            >
              {error}
            </p>
          ) : null}
          <div className="company-invite-actions">
            <Button
              ref={keepButton}
              className="rounded-[9px] text-workspace-muted text-[length:12px]"
              disabled={busy}
              onClick={onClose}
            >
              Keep invitation
            </Button>
            <Button
              variant="destructive"
              className="company-invite-link-destructive"
              disabled={busy}
              onClick={onConfirm}
            >
              {busy ? "Cancelling…" : "Cancel invitation"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
