import "@/features/approvals/styles/approvals-page.css";
import { useCallback } from "react";
import { useSearchParams } from "react-router";
import { useAppDispatch } from "@/state/store";
import { RefreshCcw } from "@/shared/ui/icons";
import { boardlyApi } from "@/shared/api/boardly-api";

import { readSessionSnapshot } from "@/shared/session/session";
import { useCan } from "@/shared/permissions";
import { useListApprovalsQuery } from "@/features/approvals/api/approvals-api";
import {
  ApprovalInboxTabs,
  type ApprovalInboxTab,
} from "@/features/approvals/components/approval-inbox-tabs";
import { ApprovalsInbox } from "@/features/approvals/components/approvals-inbox";
import { useApprovalInboxCounts } from "@/features/approvals/hooks/use-approval-inbox-counts";
import { QuestionsInbox } from "@/features/approvals/routes/questions-inbox";
import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";

function ApprovalsPage() {
  const decidesApprovals = useDecidesApprovals();

  return decidesApprovals ? <ApprovalsAndQuestionsPage /> : <QuestionsPage />;
}

const ALL_APPROVAL_STATUSES = [
  "pending",
  "approved",
  "rejected",
  "cancelled",
  "expired",
] as const;

// The approvals tab is for anyone the server lets decide an Approval: a
// holder of the Company approvals.resolve, or a member who decides inside a
// Project (ADR 0017), which the decide view shows by listing any request of
// theirs, decided or not.
function useDecidesApprovals() {
  const canResolveApprovals = useCan("approvals.resolve");
  const companyId = readSessionSnapshot().company?.id ?? "";
  const decidable = useListApprovalsQuery(
    {
      companyId,
      limit: 1,
      status: [...ALL_APPROVAL_STATUSES],
      view: "decide",
    },
    { skip: !companyId || canResolveApprovals },
  );
  return canResolveApprovals || (decidable.currentData?.items.length ?? 0) > 0;
}

function ApprovalsAndQuestionsPage() {
  const dispatch = useAppDispatch();
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab: ApprovalInboxTab =
    searchParams.get("tab") === "questions" ? "questions" : "approvals";
  const setActiveTab = useCallback(
    (tab: ApprovalInboxTab) => {
      const next = new URLSearchParams(searchParams);
      next.set("tab", tab);
      next.delete(tab === "questions" ? "approvalId" : "questionId");
      setSearchParams(next);
    },
    [searchParams, setSearchParams],
  );
  const { approvalPendingCount, questionPendingCount } =
    useApprovalInboxCounts();

  return (
    <WorkspacePageFrame
      actions={
        <div className="flex flex-wrap items-center gap-3">
          <ApprovalInboxTabs
            activeTab={activeTab}
            approvalPendingCount={approvalPendingCount}
            questionPendingCount={questionPendingCount}
            onChange={setActiveTab}
          />
          <RefreshButton
            onRefresh={() =>
              dispatch(
                boardlyApi.util.invalidateTags(
                  activeTab === "approvals"
                    ? [
                        { type: "Approval", id: companyId },
                        { type: "Deliverable", id: `${companyId}:ready` },
                      ]
                    : [{ type: "Question", id: companyId }],
                ),
              )
            }
          />
        </div>
      }
      className="workspace-approvals-page"
      ariaLabel="Approvals and questions"
      contentClassName="!block"
      contentWidth="contained"
      description="Decide what agents may do, and answer what they need to keep going."
      title="Approvals and questions"
    >
      <div className="w-full">
        {activeTab === "approvals" ? (
          <>
            <ApprovalsInbox hideRefresh />
          </>
        ) : (
          <QuestionsInbox hideRefresh />
        )}
      </div>
    </WorkspacePageFrame>
  );
}

function QuestionsPage() {
  const dispatch = useAppDispatch();
  const companyId = readSessionSnapshot().company?.id ?? "";
  return (
    <WorkspacePageFrame
      actions={
        <RefreshButton
          onRefresh={() =>
            dispatch(
              boardlyApi.util.invalidateTags([
                { type: "Question", id: companyId },
              ]),
            )
          }
        />
      }
      className="workspace-approvals-page"
      ariaLabel="Questions"
      contentClassName="!block"
      contentWidth="contained"
      description="Answer what agents need to keep going."
      title="Questions"
    >
      <div className="w-full">
        <QuestionsInbox hideRefresh />
      </div>
    </WorkspacePageFrame>
  );
}

function RefreshButton({ onRefresh }: { onRefresh: () => void }) {
  return (
    <button className="dashboard-button" type="button" onClick={onRefresh}>
      <RefreshCcw aria-hidden className="size-4" />
      Refresh
    </button>
  );
}

export const Component = ApprovalsPage;
export { ApprovalsPage };
export default ApprovalsPage;
