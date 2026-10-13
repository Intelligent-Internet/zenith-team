import { useCreateWorkspaceChatSessionMutation } from "@/shared/api/conversation-api";
import { useEffect, useState } from "react";
import { ChatModelSettings } from "@/features/chat-panel/components/chat-model-settings";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";

import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { ChevronDown } from "@/shared/ui/icons";

export function NewChatModelSettings({
  companyId,
  agentId,
  requestKey,
  disabled,
  onSavingChange,
}: {
  companyId: string;
  agentId: string;
  requestKey: string;
  disabled: boolean;
  onSavingChange: (saving: boolean) => void;
}) {
  const [conversationId, setConversationId] = useState<string>();
  const agents = useListCompanyAgentsQuery(companyId);
  const agent = agents.currentData?.items.find((item) => item.id === agentId);
  const [create, mutation] = useCreateWorkspaceChatSessionMutation();
  useEffect(() => {
    onSavingChange(mutation.isLoading);
    return () => onSavingChange(false);
  }, [mutation.isLoading, onSavingChange]);

  async function open() {
    if (mutation.isLoading) return;
    onSavingChange(true);
    try {
      // Reuse the new-chat request key: sending the first message resolves
      // this same Conversation and preserves the choices made here.
      const conversation = await create({
        companyId,
        agentId,
        requestKey,
      }).unwrap();
      setConversationId(conversation.id);
    } catch {
      // The mutation state keeps the error visible and allows a retry.
    } finally {
      onSavingChange(false);
    }
  }

  if (conversationId) {
    return (
      <ChatModelSettings
        companyId={companyId}
        conversationId={conversationId}
        initiallyOpen
        onSavingChange={onSavingChange}
      />
    );
  }
  return (
    <div>
      <button
        type="button"
        className="chat-model-trigger"
        disabled={disabled || mutation.isLoading || !agent}
        onClick={() => void open()}
        aria-label="Choose model and thinking for this new chat"
        title="Starts with this Agent’s defaults"
      >
        <span>
          {mutation.isLoading
            ? "Loading…"
            : `${agent?.model ?? "Provider default"} · ${agent?.thinkingLevel ?? "Default thinking"}`}
        </span>
        <ChevronDown size={13} aria-hidden />
      </button>
      {mutation.isError && (
        <p className="chat-model-error" role="alert">
          {getBoardlyQueryErrorMessage(
            mutation.error,
            "Could not load model settings. Try again.",
          )}
        </p>
      )}
    </div>
  );
}
