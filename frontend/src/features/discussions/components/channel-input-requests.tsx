import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";
import { QuestionCard } from "@/features/approvals/components/question-card";
import { CompanyPlanApprovalRequestCard } from "@/features/chat-panel/components/company-plan-approval-request-card";
import { useDiscussionUpdates } from "./discussion-provider";

export function ChannelInputRequests({
  ids,
  onResolved,
}: {
  ids: string[];
  onResolved: () => void;
}) {
  const runtime = useDiscussionUpdates();
  const args = {
    companyId: runtime.session.companyId,
    limit: 100,
    allPages: true,
  };
  const options = {
    skip: ids.length === 0,
  };
  const questions = useListQuestionsQuery(args, options);
  const approvals = useListApprovalsQuery(args, options);
  if (!ids.length) return null;
  return (
    <div className="channel-input-requests max-w-[640px] mt-[12px] text-[length:13px] text-[var(--discussion-text)]">
      {questions.isError || approvals.isError ? (
        <p role="alert">Could not load the request. Open Inbox to respond.</p>
      ) : null}
      {questions.currentData?.items
        .filter((item) => ids.includes(item.id))
        .map((question) => (
          <QuestionCard
            key={question.id}
            question={question}
            variant="conversation"
            onResolved={onResolved}
          />
        ))}
      {approvals.currentData?.items
        .filter((item) => ids.includes(item.id))
        .map((approval) => (
          <CompanyPlanApprovalRequestCard
            key={approval.id}
            approval={approval}
            onResolved={onResolved}
          />
        ))}
    </div>
  );
}
