import { useMemo, useRef } from "react";
import {
  Dialog,
  DialogContent as CoreDialogContent,
  DialogTitle,
} from "@/shared/ui/core/dialog";

import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import {
  agentPlanTasks,
  agentPlansFromCompanyPlan,
  type AgentPlan,
} from "@/features/chat-panel/components/chat-company-plan-data";
import { AgentPlanDialogAside } from "@/features/chat-panel/components/chat-company-plan-dialog-aside";
import { PlanSummaryCard } from "@/features/chat-panel/components/chat-company-plan-dialog-summary-card";
import { AgentPlanIcon } from "@/features/chat-panel/components/chat-company-plan-icons";
import { usePlanDialogFocus } from "@/features/chat-panel/hooks/use-plan-dialog-focus";
import type { CeoCompanyPlanMessagePayload } from "@/shared/chat/types";
import "@/features/chat-panel/styles/chat-company-plan-overview.css";
import { Icon } from "@/shared/ui/icon";
import "@/features/chat-panel/styles/chat-company-plan-detail.css";

export type ChatCompanyPlanOverviewProps = {
  /** Replaces the "Here's your plan" header, e.g. for a transaction. */
  heading?: { title: string; summary: string };
  onModifyPlan: () => void;
  onReviewAgentPlan: (plan: AgentPlan) => void;
  plan: CeoCompanyPlanMessagePayload;
  showActions?: boolean;
};

/** The Company's Agents, so plan slices show who carries them. */
function useCompanyPlanAgents() {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const query = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  return query.currentData?.items ?? emptyAgents;
}

const emptyAgents: never[] = [];

export function ChatCompanyPlanOverview({
  heading,
  onModifyPlan,
  onReviewAgentPlan,
  plan,
  showActions = true,
}: ChatCompanyPlanOverviewProps) {
  const agents = useCompanyPlanAgents();
  const agentPlans = useMemo(
    () => agentPlansFromCompanyPlan(plan, agents),
    [agents, plan],
  );
  const agentPlansById = new Map(
    agentPlans.map((agentPlan) => [agentPlan.id, agentPlan] as const),
  );
  const taskRows = plan.tasks.flatMap((task) => {
    const agentPlan = agentPlansById.get(task.agentId || "unassigned");
    return agentPlan ? [{ agentPlan, id: task.id, title: task.title }] : [];
  });

  return (
    <section className="company-plan-overview" aria-label="Company plan">
      <header className="company-plan-overview__header">
        <h2>{heading?.title ?? "Here's your plan"}</h2>
        <p>
          {heading?.summary ??
            `${taskRows.length} focused ${taskRows.length === 1 ? "task" : "tasks"}, ready for your review.`}
        </p>
      </header>
      <div className="company-plan-overview__tasks">
        {taskRows.map((task, index) => (
          <button
            aria-label={`Review ${task.title}`}
            className="company-plan-overview__task"
            key={task.id}
            type="button"
            onClick={() => onReviewAgentPlan(task.agentPlan)}
          >
            <span className="company-plan-overview__task-number">
              {index + 1}
            </span>
            <span className="company-plan-overview__task-copy">
              <strong>{task.title}</strong>
              <span>{task.agentPlan.title}</span>
            </span>
            <span
              className="company-plan-overview__task-open"
              aria-hidden="true"
            >
              <Icon name="arrow-right" className="size-4" />
            </span>
          </button>
        ))}
      </div>
      {showActions ? (
        <button
          className="company-plan-overview__review"
          onClick={onModifyPlan}
          type="button"
        >
          Request plan changes
        </button>
      ) : null}
    </section>
  );
}

export function ChatAgentPlanDialog({
  agentPlan,
  onClose,
}: {
  agentPlan: AgentPlan | null;
  onClose: () => void;
}) {
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const tasks = agentPlan ? agentPlanTasks(agentPlan) : [];
  usePlanDialogFocus({ closeButtonRef, open: agentPlan !== null });

  return (
    <Dialog
      open={agentPlan !== null}
      onOpenChange={(open) => {
        if (!open) {
          onClose();
        }
      }}
    >
      {agentPlan ? (
        <CoreDialogContent
          className="chat-company-plan-detail"
          overlayClassName="chat-company-plan-detail__overlay"
          aria-describedby={undefined}
          showCloseButton={false}
        >
          <div className="chat-company-plan-detail__header">
            <div className="chat-company-plan-detail__identity">
              <AgentPlanIcon
                accentColor={agentPlan.accentColor}
                agentId={agentPlan.agentId}
                avatarSrc={agentPlan.avatarSrc}
              />
              <div className="min-w-0">
                <DialogTitle className="chat-company-plan-detail__title">
                  {agentPlan.title}
                </DialogTitle>
                <p className="chat-company-plan-detail__subtitle">
                  Initial Plan version 1.0
                </p>
              </div>
            </div>
            <button
              ref={closeButtonRef}
              type="button"
              className="chat-company-plan-detail__close"
              onClick={onClose}
            >
              Close
            </button>
          </div>
          <div className="chat-company-plan-detail__body">
            <div className="chat-company-plan-detail__columns">
              <div className="min-w-0">
                <h3 className="chat-company-plan-detail__section-title">
                  Plan Summary
                </h3>
                <div className="chat-company-plan-detail__summary">
                  <PlanSummaryCard
                    icon={<Icon name="task-square" className="size-5" />}
                    label="Tasks"
                    value={String(tasks.length)}
                    caption="Tasks to execute"
                  />
                </div>

                <h3 className="chat-company-plan-detail__section-title">
                  Task Preview
                </h3>
                <ol className="m-0 grid list-none gap-3 p-0">
                  {tasks.map((task, index) => (
                    <li key={task.id} className="rounded-xl bg-white/8 p-4">
                      <h4 className="m-0 text-sm font-semibold">
                        {index + 1}. {task.title}
                      </h4>
                      <p className="m-0 mt-2 text-sm text-white/60">
                        {task.outcome}
                      </p>
                    </li>
                  ))}
                </ol>
              </div>

              <AgentPlanDialogAside plan={agentPlan} />
            </div>
          </div>
          <p className="chat-company-plan-detail__footer">
            Plans are AI-generated and editable. Please review carefully before
            approval.
          </p>
        </CoreDialogContent>
      ) : null}
    </Dialog>
  );
}
