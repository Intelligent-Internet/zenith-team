import { useState } from "react";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import {
  ROLE_DESCRIPTIONS,
  roleLabel,
  type CompanyRole,
} from "@/shared/permissions";
import {
  useChangeCompanyMemberRoleMutation,
  useLeaveCompanyMutation,
  useRemoveCompanyMemberMutation,
  type ActiveCompanyMember,
} from "../api/team-directory-api";
import "@/features/company-invitations/styles/company-invitations.css";

export type MembershipAction =
  | { kind: "remove"; member: ActiveCompanyMember }
  | { kind: "leave"; member: ActiveCompanyMember }
  | { kind: "change-role"; member: ActiveCompanyMember; nextRole: CompanyRole };

/**
 * The one confirmation for every membership change. Each variant names the
 * exact person, the resulting role, and what happens to their machines and
 * work, and a double submission cannot repeat the change: the mutation
 * carries the row's revision.
 */
export function MembershipActionDialog({
  action,
  companyId,
  companyName,
  onClose,
  onLeft,
  onRestoreFocus,
}: {
  action: MembershipAction;
  companyId: string;
  companyName: string;
  onClose: () => void;
  onLeft: () => Promise<void>;
  onRestoreFocus?: () => void;
}) {
  const [removeMember] = useRemoveCompanyMemberMutation();
  const [leaveCompany] = useLeaveCompanyMutation();
  const [changeRole] = useChangeCompanyMemberRoleMutation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [left, setLeft] = useState(false);
  const { member } = action;
  const name = member.actor.name;

  async function confirm() {
    setBusy(true);
    setError("");
    try {
      switch (action.kind) {
        case "leave":
          if (!left) {
            await leaveCompany({
              companyId,
              body: { expectedRevision: member.revision },
            }).unwrap();
            setLeft(true);
          }
          await onLeft();
          break;
        case "remove":
          await removeMember({
            companyId,
            userId: member.actor.id,
            body: { expectedRevision: member.revision },
          }).unwrap();
          toast(`${name} was removed from ${companyName}.`);
          break;
        case "change-role":
          await changeRole({
            companyId,
            userId: member.actor.id,
            body: { expectedRevision: member.revision, role: action.nextRole },
          }).unwrap();
          toast(`${name} is now ${roleLabel(action.nextRole)}.`);
          break;
      }
      onClose();
    } catch (cause) {
      const failure = cause as { code?: string; message?: string };
      setError(
        failure.code === "team_member_revision_conflict"
          ? "This member changed just now. Refresh and try again."
          : (failure.message ?? "Something went wrong. Please try again."),
      );
    } finally {
      setBusy(false);
    }
  }

  const copy = dialogCopy(action, companyName);

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy && !left) onClose();
      }}
    >
      <DialogContent
        className="company-invite-dialog"
        overlayClassName="company-invite-overlay"
        onCloseAutoFocus={(event) => {
          if (onRestoreFocus) {
            event.preventDefault();
            onRestoreFocus();
          }
        }}
      >
        <header className="company-invite-header">
          <DialogTitle className="company-invite-title">
            {copy.title}
          </DialogTitle>
          <DialogDescription className="company-invite-description">
            {copy.description}
          </DialogDescription>
        </header>
        <div className="company-invite-body">
          {copy.hint ? (
            <p className="company-invite-hint">{copy.hint}</p>
          ) : null}
          {error && (
            <p className="company-invite-errors" role="alert">
              {error}
            </p>
          )}
          <div className="company-invite-actions">
            <Button disabled={busy || left} onClick={onClose}>
              Cancel
            </Button>
            <Button
              variant={copy.destructive ? "destructive" : "default"}
              disabled={busy}
              onClick={() => void confirm()}
            >
              {busy ? "Please wait…" : left ? "Continue" : copy.confirm}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function dialogCopy(action: MembershipAction, companyName: string) {
  const name = action.member.actor.name;
  switch (action.kind) {
    case "leave":
      return {
        title: `Leave ${companyName}?`,
        description:
          "You will lose access to this team's work. Your account and the team's work history will be kept.",
        hint: "Agents connected to your machines will become unavailable, and their queued or running work will stop. An owner or admin can invite you back later.",
        confirm: "Leave team",
        destructive: true,
      };
    case "remove":
      return {
        title: `Remove ${name} from ${companyName}?`,
        description: `${name} will lose access to ${companyName}. Their contributions and work history will be kept.`,
        hint: "Agents connected to this person's machines will become unavailable, and their queued or running work will stop. An owner or admin can invite them back later.",
        confirm: "Remove member",
        destructive: true,
      };
    case "change-role":
      return {
        title: `Change ${name}'s role to ${roleLabel(action.nextRole)}?`,
        description: `${name} is currently ${roleLabel(action.member.role)}. As ${roleLabel(action.nextRole)}: ${ROLE_DESCRIPTIONS[action.nextRole]}`,
        hint:
          action.nextRole === "owner"
            ? `${name} will be able to delete ${companyName}, manage owners and manage revenue. You keep your own role.`
            : undefined,
        confirm: `Change to ${roleLabel(action.nextRole)}`,
        destructive: false,
      };
  }
}
