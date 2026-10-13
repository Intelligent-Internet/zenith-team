import type { ReactNode } from "react";

export function PlanSummaryCard({
  caption,
  icon,
  label,
  value,
}: {
  caption: string;
  icon: ReactNode;
  label: string;
  value: string;
}) {
  return (
    <div className="chat-company-plan-detail__summary-card">
      <span
        className="grid shrink-0 w-[32px] h-[32px] place-items-center rounded-[8px] [background:var(--workspace-accent-soft,_#35404a)] text-[var(--plan-detail-accent)]"
        aria-hidden="true"
      >
        {icon}
      </span>
      <div className="min-w-0">
        <span className="chat-company-plan-detail__summary-label">{label}</span>
        <strong className="block m-[3px_0] text-[var(--plan-detail-text)] text-[length:26px] font-[750] leading-[1.2]">
          {value}
        </strong>
        <span className="chat-company-plan-detail__summary-caption">
          {caption}
        </span>
      </div>
    </div>
  );
}
