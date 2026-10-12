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
        className="chat-company-plan-detail__summary-icon"
        aria-hidden="true"
      >
        {icon}
      </span>
      <div className="min-w-0">
        <span className="chat-company-plan-detail__summary-label">{label}</span>
        <strong className="chat-company-plan-detail__summary-value">
          {value}
        </strong>
        <span className="chat-company-plan-detail__summary-caption">
          {caption}
        </span>
      </div>
    </div>
  );
}
