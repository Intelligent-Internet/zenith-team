import { useEffect, useRef, useState } from "react";

import { displayItemsToChatMessages } from "@/features/chat-panel/hooks/use-agent-realtime-chat";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";
import { conversationService } from "@/shared/api";

import type { ChatFocusedMessageTarget } from "@/features/chat-panel/types";

type FocusedMessageHistory = {
  failed: boolean;
  messages: ChatMessage[];
  targetSeq: number | null;
};

const idleHistory: FocusedMessageHistory = {
  failed: false,
  messages: [],
  targetSeq: null,
};

export function useFocusedMessageHistory({
  focusedMessage,
  onAvailable,
  onUnavailable,
  requestKey,
  team,
}: {
  focusedMessage?: ChatFocusedMessageTarget | null;
  onAvailable?: () => void;
  onUnavailable?: () => void;
  requestKey?: number | string;
  team: ChatTeamId;
}) {
  const [history, setHistory] = useState<FocusedMessageHistory>(idleHistory);
  const conversationId = focusedMessage?.conversationId;
  const messageId = focusedMessage?.messageId;
  const messageSeq = focusedMessage?.messageSeq;
  const callbacksRef = useRef({ onAvailable, onUnavailable });
  callbacksRef.current = { onAvailable, onUnavailable };

  useEffect(() => {
    if (!conversationId || !messageId || !messageSeq) {
      setHistory((current) =>
        current === idleHistory ||
        (!current.failed &&
          current.messages.length === 0 &&
          current.targetSeq === null)
          ? current
          : idleHistory,
      );
      return;
    }

    const controller = new AbortController();
    setHistory({
      failed: false,
      messages: [],
      targetSeq: messageSeq,
    });

    void conversationService
      .loadMessageContext(conversationId, messageId, 50, controller.signal)
      .then((page) => {
        if (controller.signal.aborted) return;

        const displayItems = conversationService.displayItemsFromMessageViews(
          page.items,
        );
        setHistory({
          failed: false,
          messages: displayItemsToChatMessages(
            displayItems,
            team,
            conversationId,
          ),
          targetSeq: page.targetSeq,
        });
        callbacksRef.current.onAvailable?.();
      })
      .catch(() => {
        if (controller.signal.aborted) return;
        setHistory({
          failed: true,
          messages: [],
          targetSeq: messageSeq,
        });
        callbacksRef.current.onUnavailable?.();
      });

    return () => controller.abort();
  }, [conversationId, messageId, messageSeq, requestKey, team]);

  return history;
}
