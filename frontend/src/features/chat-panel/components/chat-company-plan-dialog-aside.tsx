import type { AgentPlan } from "@/features/chat-panel/components/chat-company-plan-data";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Icon } from "@/shared/ui/icon";

export function AgentPlanDialogAside({ plan }: { plan: AgentPlan }) {
  return (
    <aside className="chat-company-plan-detail__aside">
      <section className="chat-company-plan-detail__context">
        <h3 className="chat-company-plan-detail__section-title">
          Agent Objective
        </h3>
        <p className="chat-company-plan-detail__objective">{plan.objective}</p>
        <div className="chat-company-plan-detail__agent">
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

      <section className="chat-company-plan-detail__context">
        <h3 className="chat-company-plan-detail__section-title">
          Responsibilities
        </h3>
        <ul className="chat-company-plan-detail__responsibilities">
          {plan.responsibilities.map((responsibility) => (
            <li key={responsibility}>
              <Icon name="checkmark" className="size-4" />
              <span>{responsibility}</span>
            </li>
          ))}
        </ul>
      </section>

      <div className="chat-company-plan-detail__notice">
        <Icon name="rocket" className="size-5" />
        <div>
          <strong>This is the first proposed plan.</strong>
          <p>You can approve, request changes, or regenerate.</p>
        </div>
      </div>
    </aside>
  );
}
