import { CompanyPlanApprovalRequestCard } from "@/features/chat-panel/components/company-plan-approval-request-card";
import type { ApprovalDto } from "@/shared/api/types";

export function ConversationApprovalCard({
  approval,
  compact = true,
  onResolved,
  onSubmittingChange,
}: {
  approval: ApprovalDto;
  compact?: boolean;
  onResolved?: (approval: ApprovalDto) => void;
  onSubmittingChange?: (approvalId: string, pending: boolean) => void;
}) {
  return (
    <CompanyPlanApprovalRequestCard
      approval={approval}
      compact={compact}
      onResolved={onResolved}
      onSubmittingChange={onSubmittingChange}
    />
  );
}
