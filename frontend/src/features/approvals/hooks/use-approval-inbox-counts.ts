import { useMemo } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";

export function useApprovalInboxCounts({
  agentId,
  questionConversationIds,
}: {
  agentId?: string;
  questionConversationIds?: readonly string[];
} = {}) {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const approvals = useListApprovalsQuery(
    {
      ...(agentId ? { agent_id: agentId } : {}),
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      // Only what this viewer may decide (ADR 0017): a member who decides
      // in one Project counts that Project's requests, and a reader who may
      // not decide them counts none.
      view: "decide",
    },
    { skip: !companyId },
  );
  const questions = useListQuestionsQuery(
    {
      ...(agentId ? { agent_id: agentId } : {}),
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      view: "decide",
    },
    { skip: !companyId },
  );
  const conversationIds = useMemo(
    () => (questionConversationIds ? new Set(questionConversationIds) : null),
    [questionConversationIds],
  );
  const questionPendingCount = (questions.currentData?.items ?? []).filter(
    (question) =>
      !conversationIds || conversationIds.has(question.conversationId),
  ).length;

  return {
    approvalPendingCount: approvals.currentData?.items.length ?? 0,
    questionPendingCount,
  };
}
