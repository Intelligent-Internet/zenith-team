import { LockKeyhole } from "@/shared/ui/icons";

/**
 * A file on a message that this viewer may not open: a Project file of a
 * Project they do not read (ADR 0017). Every message read says so with
 * `attachmentsWithheld`; the chip says that something was there and nothing
 * about it (no name, type or size). Channels, group DMs and Task chats all
 * show the same chip.
 */
export function WithheldAttachmentChip() {
  return (
    <span className="inline-flex items-center gap-[6px] p-[6px_10px] [border:1px_dashed_var(--workspace-divider-strong,_var(--workspace-divider))] rounded-[8px] [background:var(--workspace-control-bg)] text-workspace-muted text-[length:var(--workspace-text-caption,_12px)] leading-[var(--workspace-leading-caption,_1.4)]">
      <LockKeyhole size={14} aria-hidden="true" />
      Attachment not available to you
    </span>
  );
}
