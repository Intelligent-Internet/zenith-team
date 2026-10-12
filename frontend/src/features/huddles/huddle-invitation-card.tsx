import { CircleCheck, CircleX, Headphones } from "@/shared/ui/icons";
import "./huddles.css";
import type { HuddleInvitation } from "../discussions/api/huddle-api";
import { huddleNotificationCopy } from "@/features/notifications/lib/huddle-notification-copy";

export function HuddleInvitationCard({
  invitation,
  companyName,
  busy,
  onAccept,
  onDecline,
  onEnableAlerts,
}: {
  invitation: HuddleInvitation;
  companyName?: string;
  busy?: boolean;
  onAccept: () => void;
  onDecline: () => void;
  onEnableAlerts?: () => void;
}) {
  const copy = huddleNotificationCopy(invitation, companyName ?? "");
  return (
    <section
      className="huddle-invitation"
      role="alert"
      aria-label="Incoming Huddle invitation"
      data-invitation-id={invitation.id}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="huddle-invitation-icon">
          <Headphones size={20} />
        </span>
        <div className="min-w-0">
          <p className="text-sm font-semibold">{copy.title}</p>
          <p className="mt-1 text-xs text-muted-foreground">{copy.body}</p>
        </div>
      </div>
      <div className="huddle-invitation-actions">
        {onEnableAlerts ? (
          <button
            type="button"
            className="text-xs text-muted-foreground underline"
            onClick={onEnableAlerts}
          >
            Enable alerts
          </button>
        ) : null}
        <button
          type="button"
          className="huddle-invitation-action"
          onClick={onDecline}
          disabled={busy}
          aria-label="Decline Huddle invitation"
        >
          <CircleX size={16} /> Decline
        </button>
        <button
          type="button"
          className="huddle-invitation-action huddle-invitation-join"
          onClick={onAccept}
          disabled={busy}
          aria-label="Accept Huddle invitation"
        >
          <CircleCheck size={16} /> Join
        </button>
      </div>
    </section>
  );
}
