import {
  boardlyRealtimeClient,
  realtimeEventConversationId,
  type BoardlyRealtimeEvent,
} from "@/shared/realtime/realtime-client.service";

export type AgentMailChange = {
  conversationId: string;
  draftId: string;
  groupId: string;
  reason: "created" | "updated" | "deleted" | "queued" | "sent" | "failed";
  recipientCount: number;
  status: "draft" | "sending" | "sent" | "failed" | "deleted";
};

export function subscribeToAgentMailChanges(
  companyId: string,
  handleChange: (change: AgentMailChange) => void,
): () => void {
  return boardlyRealtimeClient.onEvent((event) => {
    const change = parseAgentMailChange(event, companyId);
    if (change) handleChange(change);
  });
}

function parseAgentMailChange(
  event: BoardlyRealtimeEvent,
  companyId: string,
): AgentMailChange | null {
  if (event.type !== "mailbox.draft_changed" || event.companyId !== companyId) {
    return null;
  }
  const conversationId = realtimeEventConversationId(event);
  const groupId = event.payload?.groupId;
  const draftId = event.payload?.draftId;
  const recipientCount = event.payload?.recipientCount;
  const reason = event.payload?.reason;
  const status = event.payload?.status;
  if (
    !conversationId ||
    typeof groupId !== "string" ||
    typeof draftId !== "string" ||
    typeof recipientCount !== "number" ||
    !Number.isInteger(recipientCount) ||
    recipientCount < 1 ||
    recipientCount > 100 ||
    (reason !== "created" &&
      reason !== "updated" &&
      reason !== "deleted" &&
      reason !== "queued" &&
      reason !== "sent" &&
      reason !== "failed") ||
    (status !== "draft" &&
      status !== "sending" &&
      status !== "sent" &&
      status !== "failed" &&
      status !== "deleted")
  ) {
    return null;
  }
  return { conversationId, draftId, groupId, reason, recipientCount, status };
}
