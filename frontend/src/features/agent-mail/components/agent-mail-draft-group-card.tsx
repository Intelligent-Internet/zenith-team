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
      <span className="turn-artifact-card__icon" aria-hidden>
        <Mail size={18} />
      </span>
      <span className="turn-artifact-card__body">
        <span className="turn-artifact-card__title">
          {group.drafts[0]?.subject ?? "Agent Mail"}
        </span>
        <span className="turn-artifact-card__filename">
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
