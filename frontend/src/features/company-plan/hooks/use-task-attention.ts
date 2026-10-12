import { useCallback, useMemo } from "react";
import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";

// The Tasks with a pending question or approval this viewer may answer:
// the server's decide view, the list the Inbox counts and the Questions
// page read (ADR 0017). Only these say "your input" or "your response"; a
// reader who may not answer sees the Task's plain status. The arguments
// match useApprovalInboxCounts, so both read one cached list.
export function useTaskAttention(companyId: string) {
  const questions = useListQuestionsQuery(
    {
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      view: "decide",
    },
    { skip: !companyId },
  );
  const approvals = useListApprovalsQuery(
    {
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      view: "decide",
    },
    { skip: !companyId },
  );
  const taskIds = useMemo(
    () =>
      new Set(
        [
          ...(questions.isError ? [] : (questions.currentData?.items ?? [])),
          ...(approvals.isError ? [] : (approvals.currentData?.items ?? [])),
        ].flatMap((item) => (item.taskId ? [item.taskId] : [])),
      ),
    [
      questions.currentData,
      questions.isError,
      approvals.currentData,
      approvals.isError,
    ],
  );
  const refetchQuestions = questions.refetch;
  const refetchApprovals = approvals.refetch;
  // Settles once both lists are back; rejects when either read fails.
  const refetch = useCallback(async () => {
    if (!companyId) return;
    await Promise.all([
      refetchQuestions().unwrap(),
      refetchApprovals().unwrap(),
    ]);
  }, [companyId, refetchQuestions, refetchApprovals]);
  const retry = useCallback(() => {
    if (!companyId) return;
    void refetchQuestions();
    void refetchApprovals();
  }, [companyId, refetchQuestions, refetchApprovals]);
  return {
    taskIds,
    hasData:
      questions.currentData !== undefined &&
      approvals.currentData !== undefined,
    isLoading:
      Boolean(companyId) &&
      ((!questions.currentData && !questions.isError) ||
        (!approvals.currentData && !approvals.isError)),
    isError: questions.isError || approvals.isError,
    isFetching: questions.isFetching || approvals.isFetching,
    refetch,
    retry,
  };
}
