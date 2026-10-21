import { useEffect } from "react";

import { agentMailInvalidationTags } from "@/features/agent-mail/api/agent-mail-api";
import { subscribeToAgentMailChanges } from "@/features/agent-mail/model/agent-mail-realtime";
import { mailboxInvalidationTags } from "@/features/mailbox/api/mailbox-api";
import { subscribeToMailboxChanges } from "@/features/mailbox/model/mailbox-realtime";
import { zenithTeamApi } from "@/shared/api/zenith-team-api";
import { useCan } from "@/shared/permissions";
import { useAppDispatch } from "@/state";

const mailboxRefreshDebounceMs = 100;

export function MailboxRealtimeController({
  companyId,
}: {
  companyId: string;
}) {
  const dispatch = useAppDispatch();
  const canUseMailbox = useCan("mailbox.manage");

  useEffect(() => {
    if (!companyId || !canUseMailbox) {
      return undefined;
    }

    const pendingMessageIds = new Set<string>();
    const pendingDraftConversationIds = new Set<string>();
    let mailboxChanged = false;
    let refreshTimer: ReturnType<typeof setTimeout> | undefined;
    const scheduleRefresh = () => {
      if (refreshTimer) {
        return;
      }
      refreshTimer = setTimeout(() => {
        refreshTimer = undefined;
        const tags = [
          ...(mailboxChanged
            ? mailboxInvalidationTags(companyId, [...pendingMessageIds])
            : []),
          ...[...pendingDraftConversationIds].flatMap((conversationId) =>
            agentMailInvalidationTags(companyId, conversationId),
          ),
        ];
        mailboxChanged = false;
        pendingMessageIds.clear();
        pendingDraftConversationIds.clear();
        dispatch(zenithTeamApi.util.invalidateTags(tags));
      }, mailboxRefreshDebounceMs);
    };

    const removeMailboxListener = subscribeToMailboxChanges(
      companyId,
      (change) => {
        mailboxChanged = true;
        if (change.messageId) pendingMessageIds.add(change.messageId);
        scheduleRefresh();
      },
    );
    // Keep inactive conversations fresh too: RTK removes their cached drafts
    // without fetching until the user opens that conversation again.
    const removeDraftListener = subscribeToAgentMailChanges(
      companyId,
      (change) => {
        pendingDraftConversationIds.add(change.conversationId);
        scheduleRefresh();
      },
    );
    // The shell owns socket readiness and reconnect convergence.

    return () => {
      if (refreshTimer) {
        clearTimeout(refreshTimer);
      }
      removeMailboxListener();
      removeDraftListener();
    };
  }, [companyId, canUseMailbox, dispatch]);

  return null;
}
