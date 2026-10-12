import type { MailboxStatus } from "@/features/mailbox/model/mailbox";

export function canListAgentMailDraftGroups({
  companyId,
  conversationId,
  mailboxStatus,
  sourceRunIds,
}: {
  companyId: string;
  conversationId?: string | null;
  mailboxStatus?: MailboxStatus;
  sourceRunIds: readonly string[];
}): boolean {
  const agentMailAvailable =
    mailboxStatus === "unconfigured" || mailboxStatus === "active";
  return Boolean(
    agentMailAvailable && companyId && conversationId && sourceRunIds.length,
  );
}
