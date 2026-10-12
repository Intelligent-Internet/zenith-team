export type ChatPanelSurface = "conversation" | "history";

type ChatPanelDataSources = {
  agentMailConversationId: string | undefined;
  approvalsConversationId: string | undefined;
  questionsConversationId: string | undefined;
  runQueueConversationId: string | undefined;
  structuredActionsEnabled: boolean;
};

// A history surface renders an immutable Conversation window. Its private
// infrastructure identity must not enable ordinary Conversation projections
// or controls; those belong only to a direct or Task Conversation panel.
export function chatPanelDataSources(
  surface: ChatPanelSurface,
  conversationId?: string | null,
): ChatPanelDataSources {
  const interactiveConversationId =
    surface === "conversation" ? conversationId || undefined : undefined;
  return {
    agentMailConversationId: interactiveConversationId,
    approvalsConversationId: interactiveConversationId,
    questionsConversationId: interactiveConversationId,
    runQueueConversationId: interactiveConversationId,
    structuredActionsEnabled: surface === "conversation",
  };
}
