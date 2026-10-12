import { useMemo } from "react";

import { readSessionSnapshot } from "@/app/session/session";
import { useListQuestionsQuery } from "@/features/approvals/api/approvals-api";

export function useConversationQuestions(conversationId?: string | null) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const query = useListQuestionsQuery(
    { companyId, limit: 100, allPages: true },
    { skip: !companyId || !conversationId },
  );
  const questions = useMemo(
    () =>
      (query.currentData?.items ?? []).filter(
        (question) => question.conversationId === conversationId,
      ),
    [conversationId, query.currentData?.items],
  );

  return { isError: query.isError, isLoading: query.isLoading, questions };
}
