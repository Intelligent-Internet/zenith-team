import { CompanyPlanApprovalRequestCard } from "@/features/chat-panel/components/company-plan-approval-request-card";
import { CommerceManifestApprovalCard } from "@/features/approvals/components/commerce-manifest-approval-card";
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
  if (approval.kind === "commerce_manifest_publish") {
    return (
      <CommerceManifestApprovalCard approval={approval} compact={compact} />
    );
  }

  return (
    <CompanyPlanApprovalRequestCard
      approval={approval}
      compact={compact}
      onResolved={onResolved}
      onSubmittingChange={onSubmittingChange}
    />
  );
}
