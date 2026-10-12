import { useMemo } from "react";

import { readSessionSnapshot } from "@/app/session/session";
import { useListApprovalsQuery } from "@/features/approvals/api/approvals-api";

export function useConversationApprovals(conversationId?: string | null) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const query = useListApprovalsQuery(
    { companyId, limit: 100, allPages: true },
    { skip: !companyId || !conversationId },
  );
  const approvals = useMemo(
    () =>
      (query.currentData?.items ?? []).filter(
        (approval) => approval.conversationId === conversationId,
      ),
    [conversationId, query.currentData?.items],
  );

  return { approvals, isError: query.isError, isLoading: query.isLoading };
}
