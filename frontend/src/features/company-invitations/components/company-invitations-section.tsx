import { useRef, useState } from "react";
import { Button } from "@/shared/ui/core/button";
import {
  CheckCircle2,
  ChevronRight,
  CircleAlert,
  Clock3,
  Mail,
} from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import {
  useListCompanyInvitationsQuery,
  useChangeCompanyInvitationMutation,
  type CompanyInvitation,
} from "../api/invitations-api";
import { RoleBadge } from "@/shared/ui/role-badge";
import { CancelInvitationDialog } from "./cancel-invitation-dialog";
import "../styles/company-invitations.css";

export function CompanyInvitationsSection({
  companyId,
}: {
  companyId: string;
}) {
  const query = useListCompanyInvitationsQuery(companyId, {
    refetchOnFocus: true,
  });
  const [change, status] = useChangeCompanyInvitationMutation();
  const [error, setError] = useState("");
  const [cancelInvitation, setCancelInvitation] =
    useState<CompanyInvitation | null>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [showAllPending, setShowAllPending] = useState(false);
  const [showAllPast, setShowAllPast] = useState(false);
  const invitations = query.currentData?.items ?? [];
  const pendingInvitations = invitations.filter(
    (invitation) => invitation.status === "pending",
  );
  const pastInvitations = invitations.filter(
    (invitation) => invitation.status !== "pending",
  );
  const visiblePendingInvitations = showAllPending
    ? pendingInvitations
    : pendingInvitations.slice(0, 5);
  const visiblePastInvitations = showAllPast
    ? pastInvitations
    : pastInvitations.slice(0, 5);

  if (
    query.currentData?.enabled &&
    invitations.length === 0 &&
    !query.isError &&
    !cancelInvitation
  ) {
    return null;
  }

  async function act(
    invitation: CompanyInvitation,
    action: "resend" | "cancel",
  ) {
    if (status.isLoading) return;
    setError("");
    try {
      await change({ companyId, invitation, action }).unwrap();
      if (action === "cancel") {
        returnFocus.current = heading.current;
        setCancelInvitation(null);
        toast(`Invitation to ${invitation.email} cancelled.`);
      } else {
        toast(`Invitation queued for ${invitation.email}.`);
      }
    } catch (cause) {
      setError(
        (cause as { message?: string }).message ??
          "Could not update invitation.",
      );
    }
  }
  return (
    <section
      className="company-invitations-section"
      aria-labelledby="company-invitations-title"
    >
      <header>
        <div className="company-invitations-heading">
          <h2 id="company-invitations-title" ref={heading} tabIndex={-1}>
            {pendingInvitations.length > 0
              ? "Pending invitations"
              : "Invitations"}
          </h2>
          {pendingInvitations.length > 0 ? (
            <span
              aria-label={`${pendingInvitations.length} pending ${pendingInvitations.length === 1 ? "invitation" : "invitations"}`}
            >
              {pendingInvitations.length}
            </span>
          ) : null}
        </div>
      </header>
      <div className="company-invitations-content">
        {query.isLoading && <p role="status">Loading invitations…</p>}
        {query.isError && (
          <p role="alert">
            Invitations could not be loaded.{" "}
            <button onClick={() => void query.refetch()}>Retry</button>
          </p>
        )}
        {query.currentData && !query.currentData.enabled && (
          <p role="status">
            Email invitations are not configured for this deployment yet.
          </p>
        )}
        {error && !cancelInvitation && <p role="alert">{error}</p>}
        {visiblePendingInvitations.length > 0 ? (
          <ul className="invitation-list">
            {visiblePendingInvitations.map((item) => (
              <InvitationRow
                key={item.id}
                invitation={item}
                emailEnabled={query.currentData?.enabled ?? false}
                disabled={status.isLoading || query.isError}
                resending={
                  status.isLoading &&
                  status.originalArgs?.action === "resend" &&
                  status.originalArgs.invitation.id === item.id
                }
                onResend={() => void act(item, "resend")}
                onCancel={(trigger) => {
                  setError("");
                  returnFocus.current = trigger;
                  setCancelInvitation(item);
                }}
              />
            ))}
          </ul>
        ) : null}
        {!showAllPending && pendingInvitations.length > 5 ? (
          <button
            className="company-invitations-more"
            type="button"
            onClick={() => setShowAllPending(true)}
          >
            View {pendingInvitations.length - 5} more pending invitations
          </button>
        ) : null}
        {pastInvitations.length > 0 ? (
          <details className="company-invitations-history">
            <summary>
              <ChevronRight aria-hidden="true" size={14} />
              <span>Past invitations ({pastInvitations.length})</span>
            </summary>
            <p>Joined, expired, and cancelled invitations.</p>
            <ul className="invitation-list invitation-list--history">
              {visiblePastInvitations.map((item) => (
                <InvitationRow
                  key={item.id}
                  invitation={item}
                  emailEnabled={query.currentData?.enabled ?? false}
                  disabled={status.isLoading || query.isError}
                  resending={
                    status.isLoading &&
                    status.originalArgs?.action === "resend" &&
                    status.originalArgs.invitation.id === item.id
                  }
                  onResend={() => void act(item, "resend")}
                  onCancel={(trigger) => {
                    setError("");
                    returnFocus.current = trigger;
                    setCancelInvitation(item);
                  }}
                />
              ))}
            </ul>
            {!showAllPast && pastInvitations.length > 5 ? (
              <button
                className="company-invitations-more"
                type="button"
                onClick={() => setShowAllPast(true)}
              >
                View all {pastInvitations.length} past invitations
              </button>
            ) : null}
          </details>
        ) : null}
      </div>
      {cancelInvitation ? (
        <CancelInvitationDialog
          invitation={cancelInvitation}
          busy={status.isLoading}
          error={error}
          onConfirm={() => void act(cancelInvitation, "cancel")}
          onClose={() => {
            setCancelInvitation(null);
            setError("");
          }}
          onRestoreFocus={() => {
            const target = returnFocus.current?.isConnected
              ? returnFocus.current
              : document.getElementById("team-invite-people");
            target?.focus();
          }}
        />
      ) : null}
    </section>
  );
}

function InvitationRow({
  invitation,
  emailEnabled,
  disabled,
  resending,
  onResend,
  onCancel,
}: {
  invitation: CompanyInvitation;
  emailEnabled: boolean;
  disabled: boolean;
  resending: boolean;
  onResend: () => void;
  onCancel: (trigger: HTMLButtonElement) => void;
}) {
  const canAct =
    invitation.status === "pending" || invitation.status === "expired";
  const legacyRole = canAct && invitation.role !== "member";
  const deliveryInProgress =
    invitation.status === "pending" &&
    (invitation.deliveryStatus === "queued" ||
      invitation.deliveryStatus === "sending");
  const retry =
    invitation.status === "pending" && invitation.deliveryStatus === "failed";
  const displayStatus =
    invitation.status === "pending"
      ? invitation.deliveryStatus
      : invitation.status;
  const StatusIcon =
    displayStatus === "failed"
      ? CircleAlert
      : displayStatus === "accepted"
        ? CheckCircle2
        : displayStatus === "sent"
          ? Mail
          : Clock3;
  return (
    <li>
      <div className="invitation-row-copy">
        <div className="invitation-row-title">
          <strong>{invitation.email}</strong>
          {invitation.role !== "member" ? (
            <RoleBadge role={invitation.role} />
          ) : null}
        </div>
        <small>
          {invitation.status === "pending" ? "Expires" : "Invited"}{" "}
          {formatInvitationDate(
            invitation.status === "pending"
              ? invitation.expiresAt
              : invitation.createdAt,
          )}
        </small>
        {legacyRole ? (
          <p>
            {invitation.status === "pending"
              ? "Cancel this invitation and invite again to grant Member access."
              : "Invite this person again to grant Member access."}
          </p>
        ) : null}
        {invitation.renewalRequestedAt ? (
          <p>A new invitation was requested.</p>
        ) : null}
      </div>
      <span className="invitation-delivery-status" data-status={displayStatus}>
        <StatusIcon size={14} aria-hidden="true" />
        {invitationStatusLabel(invitation)}
      </span>
      {canAct ? (
        <div className="invitation-row-actions">
          {!legacyRole && !deliveryInProgress ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={disabled || !emailEnabled}
              aria-label={`${retry ? "Retry" : "Resend"} invitation to ${invitation.email}`}
              onClick={onResend}
            >
              {resending
                ? "Queueing…"
                : retry
                  ? "Retry invitation"
                  : "Resend invitation"}
            </Button>
          ) : null}
          {invitation.status === "pending" ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={disabled}
              aria-label={`Cancel invitation to ${invitation.email}`}
              onClick={(event) => onCancel(event.currentTarget)}
            >
              Cancel invitation
            </Button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

function invitationStatusLabel(invitation: CompanyInvitation) {
  if (invitation.status !== "pending") {
    return {
      accepted: "Joined",
      expired: "Expired",
      revoked: "Cancelled",
    }[invitation.status];
  }
  return {
    queued: "Waiting to send",
    sending: "Sending",
    sent: "Awaiting response",
    failed: "Delivery failed",
    cancelled: "Sending cancelled",
  }[invitation.deliveryStatus];
}

function formatInvitationDate(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  }).format(new Date(value));
}
