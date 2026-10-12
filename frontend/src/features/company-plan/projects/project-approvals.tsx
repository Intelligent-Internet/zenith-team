import { useState } from "react";
import { useListApprovalsQuery } from "@/features/approvals/api/approvals-api";
import { ConversationApprovalCard } from "@/features/approvals/components/conversation-approval-card";
import type { ApprovalStatusDto, TaskDto } from "@/shared/api/types";

export function ProjectApprovals({
  companyId,
  tasks,
}: {
  companyId: string;
  tasks: TaskDto[];
}) {
  const [status, setStatus] = useState<ApprovalStatusDto>("pending");
  const query = useListApprovalsQuery({
    companyId,
    status: [status],
    allPages: true,
  });
  const conversations = new Set(tasks.map((task) => task.conversationId));
  const approvals = (query.currentData?.items ?? []).filter((approval) =>
    conversations.has(approval.conversationId),
  );
  return (
    <section className="project-collection-section">
      <div className="project-section-heading">
        <div>
          <h3>Approvals</h3>
          <p>Requests from this project's task conversations</p>
        </div>
        <select
          aria-label="Approval status"
          value={status}
          onChange={(event) =>
            setStatus(event.target.value as ApprovalStatusDto)
          }
        >
          <option value="pending">Pending</option>
          <option value="approved">Approved</option>
          <option value="rejected">Rejected</option>
          <option value="cancelled">Cancelled</option>
        </select>
      </div>
      {query.isFetching && !query.currentData ? (
        <p role="status">Loading approvals…</p>
      ) : null}
      {query.isError ? (
        <p role="alert">
          Approvals could not be loaded.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : null}
      {query.currentData && approvals.length === 0 ? (
        <div className="project-workspace-empty">
          No {status} approvals for this project.
        </div>
      ) : null}
      <div className="project-approval-list">
        {approvals.map((approval) => (
          <ConversationApprovalCard key={approval.id} approval={approval} />
        ))}
      </div>
    </section>
  );
}
