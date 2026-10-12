import { useEffect, useMemo, useRef, useState } from "react";

import { useListAgentMailDraftGroupsQuery } from "@/features/agent-mail/api/agent-mail-api";
import { AgentMailDraftGroupCard } from "./agent-mail-draft-group-card";
import type { AgentMailDraftSelection } from "@/features/agent-mail/model/agent-mail";
import { canListAgentMailDraftGroups } from "@/features/agent-mail/model/agent-mail-query";
import { useGetMailboxStateQuery } from "@/features/mailbox/api/mailbox-api";
import type { ChatConversationDecorator } from "@/features/chat-panel/types";
import type { ChatMessage } from "@/shared/chat/types";
import { isRetryableBoardlyQueryError } from "@/shared/api/boardly-api";
import { useCompanyPermissions } from "@/shared/permissions";

export function useAgentMailConversation({
  conversationId,
  messages,
  selection,
  autoOpenInitial,
  onSelect,
}: {
  conversationId?: string;
  messages: ChatMessage[];
  selection: AgentMailDraftSelection | null;
  autoOpenInitial: boolean;
  onSelect: (selection: AgentMailDraftSelection) => void;
}) {
  const { companyId = "", can } = useCompanyPermissions();
  const canUseMailbox = can("mailbox.manage");
  const mailboxState = useGetMailboxStateQuery(companyId, {
    skip: !companyId || !canUseMailbox,
  });
  const mailboxContentAvailable = useMailQueryContentAvailable(
    mailboxState,
    companyId,
  );
  const sourceRunIds = useMemo(() => {
    const ids = new Set<string>();
    for (const message of messages) {
      if (message.author === "team" && message.runId) ids.add(message.runId);
    }
    if (selection) ids.add(selection.sourceRunId);
    return [...ids].sort();
  }, [messages, selection]);
  const enabled =
    canUseMailbox &&
    canListAgentMailDraftGroups({
      companyId,
      conversationId,
      mailboxStatus: mailboxContentAvailable
        ? mailboxState.currentData?.status
        : undefined,
      sourceRunIds,
    });
  const query = useListAgentMailDraftGroupsQuery(
    { companyId, conversationId: conversationId ?? "", sourceRunIds },
    { skip: !enabled, refetchOnMountOrArgChange: true },
  );
  const key = `${companyId}:${conversationId}`;
  const contentAvailable = useMailQueryContentAvailable(query, key);
  const groups = useMemo(
    () =>
      enabled && contentAvailable
        ? (query.currentData ?? query.data ?? [])
            .filter(
              (group) =>
                group.conversationId === conversationId && group.drafts.length,
            )
            .sort((left, right) =>
              right.createdAt.localeCompare(left.createdAt),
            )
        : [],
    [conversationId, enabled, contentAvailable, query.currentData, query.data],
  );
  const observed = useRef({ key: "", ids: new Set<string>(), latest: "" });
  useEffect(() => {
    if (!enabled || !query.currentData || query.isError) return;
    const initial = observed.current.key !== key;
    const previous = initial
      ? { ids: new Set<string>(), latest: "" }
      : observed.current;
    const newest = groups.find(
      (group) =>
        !previous.ids.has(group.id) &&
        group.createdAt >= previous.latest &&
        group.drafts.some(
          (draft) => draft.status === "draft" || draft.status === "failed",
        ),
    );
    observed.current = {
      key,
      ids: new Set([...previous.ids, ...groups.map((group) => group.id)]),
      latest:
        groups[0]?.createdAt && groups[0].createdAt > previous.latest
          ? groups[0].createdAt
          : previous.latest,
    };
    if (newest && (!initial || autoOpenInitial)) {
      onSelect({ groupId: newest.id, sourceRunId: newest.sourceRunId });
    }
  }, [
    autoOpenInitial,
    enabled,
    groups,
    key,
    onSelect,
    query.currentData,
    query.isError,
  ]);

  // Recover a creation hint missed while the Run was streaming. One refresh
  // after completion also waits for any earlier draft request to settle.
  const completedRuns = useMemo(
    () =>
      new Set(
        messages.flatMap((message) =>
          message.runId &&
          ["completed", "failed", "cancelled"].includes(message.runStatus ?? "")
            ? [message.runId]
            : [],
        ),
      ),
    [messages],
  );
  const observedCompletions = useRef({
    key: "",
    ids: new Set<string>(),
    pending: false,
  });
  useEffect(() => {
    if (!enabled) return;
    const previous = observedCompletions.current;
    if (previous.key !== key) {
      observedCompletions.current = { key, ids: completedRuns, pending: false };
      return;
    }
    previous.pending ||= [...completedRuns].some((id) => !previous.ids.has(id));
    previous.ids = completedRuns;
    if (!previous.pending || query.isUninitialized || query.isFetching) return;
    previous.pending = false;
    void query.refetch();
  }, [
    completedRuns,
    enabled,
    key,
    query.isFetching,
    query.isUninitialized,
    query.refetch,
  ]);

  const decorators = useMemo<ChatConversationDecorator[]>(() => {
    const messageByRun = new Map<string, number>();
    for (const message of messages) {
      if (message.author === "team" && message.runId)
        messageByRun.set(message.runId, message.id);
    }
    return groups.flatMap((group) => {
      const afterMessageId = messageByRun.get(group.sourceRunId);
      return afterMessageId === undefined
        ? []
        : [
            {
              afterMessageId,
              className: "w-full",
              content: (
                <AgentMailDraftGroupCard
                  group={group}
                  onReview={() =>
                    onSelect({
                      groupId: group.id,
                      sourceRunId: group.sourceRunId,
                    })
                  }
                />
              ),
              key: `agent-mail:${group.id}`,
            },
          ];
    });
  }, [groups, messages, onSelect]);

  return {
    decorators,
    canUseMailbox,
    group: groups.find((group) => group.id === selection?.groupId),
    loading:
      !groups.some((group) => group.id === selection?.groupId) &&
      (mailboxState.isLoading ||
        (enabled && (query.isLoading || query.isFetching))),
    error: query.isError || mailboxState.isError,
    refresh: () => {
      if (mailboxState.isError) void mailboxState.refetch();
      else if (!query.isUninitialized) void query.refetch();
    },
  };
}

function useMailQueryContentAvailable(
  query: { error?: unknown; isSuccess: boolean; isFetching: boolean },
  scope: string,
) {
  const [deniedScope, setDeniedScope] = useState<string | null>(null);
  const denied =
    Boolean(query.error) && !isRetryableBoardlyQueryError(query.error);

  useEffect(() => {
    if (denied) {
      setDeniedScope(scope);
    } else if (query.isSuccess && !query.isFetching) {
      setDeniedScope((current) => (current === scope ? null : current));
    }
  }, [denied, scope, query.isSuccess, query.isFetching]);

  // A later transient failure cannot restore content a denied response hid.
  return !denied && deniedScope !== scope;
}
