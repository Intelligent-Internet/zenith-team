import {
  boardlyRealtimeClient,
  type BoardlyRealtimeEvent,
} from "@/shared/realtime/realtime-client.service";

export type MailboxChangeReason =
  "configured" | "message_received" | "message_read" | "message_removed";

export type MailboxChange = {
  messageId?: string;
  reason: MailboxChangeReason;
};

const messageReasons = new Set<MailboxChangeReason>([
  "message_received",
  "message_read",
  "message_removed",
]);

export function subscribeToMailboxChanges(
  companyId: string,
  handleChange: (change: MailboxChange) => void,
) {
  return boardlyRealtimeClient.onEvent((event) => {
    const change = parseMailboxChange(event, companyId);
    if (change) {
      handleChange(change);
    }
  });
}

function parseMailboxChange(
  event: BoardlyRealtimeEvent,
  companyId: string,
): MailboxChange | null {
  if (event.type !== "mailbox.changed" || event.companyId !== companyId) {
    return null;
  }

  const reason = event.payload?.reason;
  if (
    reason !== "configured" &&
    reason !== "message_received" &&
    reason !== "message_read" &&
    reason !== "message_removed"
  ) {
    return null;
  }

  if (reason === "configured") {
    return { reason };
  }

  const messageId = event.payload?.messageId;
  return messageReasons.has(reason) && typeof messageId === "string"
    ? { messageId, reason }
    : null;
}
