import { useCallback } from "react";

import { createDiscoveryPlanApprovalMessage } from "@/features/chat-panel/model/discovery-plan-approval";
import {
  createCeoConversationUnavailableMessage,
  hasCeoConversationUnavailableMessage,
} from "@/features/chat-panel/model/ceo-chat-status";
import { formatChatTimestamp } from "@/features/chat-panel/utils";
import type {
  ChatSendMessageOptions,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import type { ChatMessage } from "@/shared/chat/types";
import { appendChatMessage, useAppDispatch } from "@/state";

const CEO_TEAM_ID = "ceo";

type SendChatMessage = (
  message: ChatMessage,
  options?: ChatSendMessageOptions,
) => Promise<void> | void;

type UseChatPanelFlowActionsOptions = {
  messages: ChatMessage[];
  sendAgentMessage?: SendChatMessage;
};

export function useChatPanelFlowActions({
  messages,
  sendAgentMessage,
}: UseChatPanelFlowActionsOptions) {
  const dispatch = useAppDispatch();

  const appendUnavailableMessage = useCallback(() => {
    if (hasCeoConversationUnavailableMessage(messages)) {
      return;
    }

    dispatch(appendChatMessage(createCeoConversationUnavailableMessage()));
  }, [dispatch, messages]);

  const approveDiscoveryPlan = useCallback(
    (handoff: DiscoveryPlanHandoff) => {
      if (!sendAgentMessage) {
        appendUnavailableMessage();
        return;
      }

      const sentAt = Date.now();

      void sendAgentMessage(
        {
          author: "user",
          id: sentAt,
          team: CEO_TEAM_ID,
          text: createDiscoveryPlanApprovalMessage(handoff.selectedMode),
          timestamp: formatChatTimestamp(sentAt),
        },
        {
          displayUserMessage: false,
          hiddenTranscript: true,
        },
      );
    },
    [appendUnavailableMessage, sendAgentMessage],
  );

  return {
    approveDiscoveryPlan,
  };
}
