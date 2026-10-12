import { useCallback, useState } from "react";

import type { ConversationArtifactsContext } from "@/features/chat-panel/api/conversation-artifacts-api";
import type { ConversationArtifactReference } from "@/features/chat-panel/model/conversation-artifact";
import type { AgentMailDraftSelection } from "@/features/agent-mail/model/agent-mail";
import {
  clearConversationArtifactFeedback,
  selectConversationArtifactsPanel,
  updateConversationArtifactsPanel,
  useAppDispatch,
  useAppSelector,
  type ConversationArtifactsPanelState,
} from "@/state";

export function useConversationArtifactsPanel(
  context: ConversationArtifactsContext | undefined,
) {
  const dispatch = useAppDispatch();
  const companyId = context?.companyId ?? "";
  const conversationId = context?.conversationId ?? "";
  const key = context ? `${context.companyId}:${context.conversationId}` : "";
  const savedState = useAppSelector((state) =>
    selectConversationArtifactsPanel(state, companyId, conversationId),
  );
  const defaultOpen = false;
  const [focusRequestKey, setFocusRequestKey] = useState(0);
  const state = savedState ?? {
    open: defaultOpen,
    selection: null,
    mailSelection: null,
    feedback: null,
    feedbackRevision: 0,
  };
  const update = useCallback(
    (
      changes: Partial<
        Pick<
          ConversationArtifactsPanelState,
          "open" | "selection" | "mailSelection" | "feedback"
        >
      >,
    ) => {
      if (!companyId || !conversationId) return;
      dispatch(
        updateConversationArtifactsPanel({
          companyId,
          conversationId,
          defaultOpen,
          changes,
        }),
      );
    },
    [companyId, conversationId, defaultOpen, dispatch],
  );
  const clearFeedback = useCallback(
    (expectedRevision?: number) => {
      if (!companyId || !conversationId) return;
      dispatch(
        clearConversationArtifactFeedback({
          companyId,
          conversationId,
          expectedRevision,
        }),
      );
    },
    [companyId, conversationId, dispatch],
  );
  const selectMail = useCallback(
    (mailSelection: AgentMailDraftSelection) =>
      update({ open: true, mailSelection }),
    [update],
  );
  const giveFeedback = useCallback(
    (selection: ConversationArtifactReference) => {
      update({
        feedback: selection,
        ...(!window.matchMedia("(min-width: 1440px)").matches
          ? { open: false }
          : {}),
      });
      setFocusRequestKey((current) => current + 1);
    },
    [update],
  );

  return {
    key,
    isOpen: Boolean(context && state.open),
    selection: context ? state.selection : null,
    mailSelection: context ? (state.mailSelection ?? null) : null,
    hasSavedState: Boolean(savedState),
    selectMail,
    feedback: context ? state.feedback : null,
    feedbackRevision: state.feedbackRevision,
    focusRequestKey,
    open: () => update({ open: true, mailSelection: null }),
    close: () => update({ open: false }),
    select: (selection: ConversationArtifactReference) =>
      update({ open: true, selection }),
    back: () => update({ selection: null }),
    clearFeedback,
    giveFeedback,
  };
}
