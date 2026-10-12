import { LockKeyhole } from "@/shared/ui/icons";

import "@/shared/chat/withheld-attachment-chip.css";

/**
 * A file on a message that this viewer may not open: a Project file of a
 * Project they do not read (ADR 0017). Every message read says so with
 * `attachmentsWithheld`; the chip says that something was there and nothing
 * about it (no name, type or size). Channels, group DMs and Task chats all
 * show the same chip.
 */
export function WithheldAttachmentChip() {
  return (
    <span className="chat-withheld-attachment">
      <LockKeyhole size={14} aria-hidden="true" />
      Attachment not available to you
    </span>
  );
}
