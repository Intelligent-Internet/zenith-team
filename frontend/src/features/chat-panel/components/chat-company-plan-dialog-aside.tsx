import type { AgentPlan } from "@/features/chat-panel/components/chat-company-plan-data";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Icon } from "@/shared/ui/icon";

export function AgentPlanDialogAside({ plan }: { plan: AgentPlan }) {
  return (
    <aside className="grid min-w-0 gap-[16px]">
      <section className="min-w-0 p-[18px] [border:1px_solid_var(--plan-detail-border)] rounded-[12px] [background:var(--workspace-panel-raised-bg,_#1d2a36)]">
        <h3 className="m-0 text-[var(--plan-detail-text)] text-workspace-label font-[700] leading-workspace-label">
          Agent Objective
        </h3>
        <p className="m-[10px_0_0] text-[var(--plan-detail-text)] text-workspace-body leading-workspace-body whitespace-pre-wrap">
          {plan.objective}
        </p>
        <div className="chat-company-plan-detail__agent flex items-center gap-[10px] mt-[16px] text-[var(--plan-detail-muted)] text-workspace-body leading-workspace-body">
          {plan.avatarSrc ? (
            <img src={plan.avatarSrc} alt="" />
          ) : plan.agentId ? (
            <FoldAvatar agentId={plan.agentId} paused size={40} />
          ) : null}
          <span>
            <strong>{plan.agentName}</strong>
            {plan.agentId ? ` is your ${plan.agentRole} to help you` : null}
          </span>
        </div>
      </section>

      <section className="min-w-0 p-[18px] [border:1px_solid_var(--plan-detail-border)] rounded-[12px] [background:var(--workspace-panel-raised-bg,_#1d2a36)]">
        <h3 className="m-0 text-[var(--plan-detail-text)] text-workspace-label font-[700] leading-workspace-label">
          Responsibilities
        </h3>
        <ul className="chat-company-plan-detail__responsibilities grid gap-[12px] m-[12px_0_0] p-0 [list-style:none]">
          {plan.responsibilities.map((responsibility) => (
            <li key={responsibility}>
              <Icon name="checkmark" className="size-4" />
              <span>{responsibility}</span>
            </li>
          ))}
        </ul>
      </section>

      <div className="chat-company-plan-detail__notice grid grid-cols-[20px_minmax(0,_1fr)] [align-items:start] gap-[10px] p-[16px] rounded-[12px] [background:var(--workspace-info-soft,_#18343e)] text-[var(--workspace-native-info,_#67c7db)] text-workspace-label leading-workspace-label">
        <Icon name="rocket" className="size-5" />
        <div>
          <strong>This is the first proposed plan.</strong>
          <p>You can approve, request changes, or regenerate.</p>
        </div>
      </div>
    </aside>
  );
}
