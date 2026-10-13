import { ChevronRight, Mail } from "@/shared/ui/icons";
import type { AgentMailDraftGroup } from "@/features/agent-mail/model/agent-mail";
import "@/features/chat-panel/styles/turn-artifact-card.css";

export function AgentMailDraftGroupCard({
  group,
  onReview,
}: {
  group: AgentMailDraftGroup;
  onReview: () => void;
}) {
  const status = group.drafts.some((draft) => draft.status === "failed")
    ? "Needs review"
    : group.drafts.some((draft) => draft.status === "draft")
      ? "Ready to send"
      : group.drafts.some((draft) => draft.status === "sending")
        ? "Sending"
        : "Sent";
  return (
    <button
      className="turn-artifact-card"
      type="button"
      onClick={onReview}
      aria-label={`Review email: ${group.drafts[0]?.subject ?? "Agent Mail"}`}
    >
      <span
        className="grid [flex:0_0_32px] h-[34px] place-items-center [border:1px_solid_color-mix(in_srgb,_var(--artifact-card-tint)_14%,_transparent)] rounded-[7px] [background:color-mix(in_srgb,_var(--artifact-card-tint)_8%,_transparent)] text-[var(--artifact-card-tint)]"
        aria-hidden
      >
        <Mail size={18} />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-[2px]">
        <span className="[display:-webkit-box] overflow-hidden [-webkit-box-orient:vertical] [-webkit-line-clamp:2] wrap-anywhere text-[length:13px] font-[600] leading-[1.45]">
          {group.drafts[0]?.subject ?? "Agent Mail"}
        </span>
        <span className="overflow-hidden text-[var(--workspace-native-text-muted,_#697168)] text-[length:11px] leading-[1.45] text-ellipsis whitespace-nowrap">
          {status} · {group.recipientCount}{" "}
          {group.recipientCount === 1 ? "recipient" : "recipients"}
        </span>
      </span>
      <ChevronRight
        className="turn-artifact-card__arrow"
        aria-hidden
        size={16}
      />
    </button>
  );
}
