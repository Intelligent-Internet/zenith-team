import type { ReactNode } from "react";

import { cx } from "@/shared/lib/cx";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Icon } from "@/shared/ui/icon";

type PlanIconTone =
  | "design"
  | "engineering"
  | "finance"
  | "marketing"
  | "organization"
  | "operation"
  | "support";

/** The Agent that carries a slice of the plan, in its own colour. */
export function AgentPlanIcon({
  accentColor,
  agentId,
  avatarSrc,
}: {
  accentColor: string;
  agentId: string;
  avatarSrc?: string;
}) {
  if (!agentId) {
    return (
      <span
        className="chat-company-plan-icon grid size-10 shrink-0 place-items-center rounded-[10px] text-white"
        style={{ background: accentColor }}
        aria-hidden="true"
      >
        <Icon name="user-circle" className="size-6" />
      </span>
    );
  }
  return (
    <span
      className="chat-company-plan-icon grid size-10 shrink-0 place-items-center overflow-hidden rounded-[10px] bg-white"
      style={{ boxShadow: `inset 0 0 0 2px ${accentColor}` }}
      aria-hidden="true"
    >
      {avatarSrc ? (
        <img
          alt=""
          className="size-full max-w-none object-cover object-[50%_12%]"
          draggable={false}
          src={avatarSrc}
        />
      ) : (
        <FoldAvatar agentId={agentId} paused size={40} />
      )}
    </span>
  );
}

export function PlanIcon({
  children,
  size = "small",
  tone,
}: {
  children: ReactNode;
  size?: "small" | "medium" | "large";
  tone: PlanIconTone;
}) {
  return (
    <span
      className={cx(
        "chat-company-plan-icon grid shrink-0 place-items-center text-white",
        size === "large" ? "size-[56px] rounded-[11px]" : null,
        size === "medium" ? "size-10 rounded-[10px]" : null,
        size === "small" ? "size-10 rounded-[10px]" : null,
        `chat-company-plan-icon--${tone}`,
      )}
      aria-hidden="true"
    >
      {children}
    </span>
  );
}
