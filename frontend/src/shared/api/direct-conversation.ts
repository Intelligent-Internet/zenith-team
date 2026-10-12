import { requireActiveCompanyId } from "@/shared/api/active-company";
import { conversationService } from "@/shared/api/conversation.service";

export async function resolveDirectConversation(
  agentId: string,
  title?: string,
): Promise<string> {
  const companyId = requireActiveCompanyId();
  const page = await conversationService.listDirectConversations(companyId);
  const existing = page.items.find((item) => item.agent?.id === agentId);

  if (existing) {
    return existing.id;
  }

  const created = await conversationService.createDirectConversation(
    companyId,
    {
      title,
      agentId,
    },
  );
  return created.id;
}
