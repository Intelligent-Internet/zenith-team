import * as Avatar from "@radix-ui/react-avatar";
import {
  CheckCircle2,
  ChevronDown,
  CircleAlert,
  MoreHorizontal,
} from "@/shared/ui/icons";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/shared/ui/core/dropdown-menu";
import {
  assignableRoles,
  canChangeMemberRole,
  canRemoveMember,
  decisionTitle,
  roleLabel,
  type CompanyRole,
  type MembershipContext,
} from "@/shared/permissions";
import { personInitials } from "@/shared/ui/person-initials";
import { RoleBadge } from "@/shared/ui/role-badge";

import type { ActiveCompanyMember } from "../api/team-directory-api";

/** A person as every Team table shows them: avatar, name and email. */
export function TeamPersonIdentity({
  avatarUrl,
  email,
  isSelf,
  name,
}: {
  avatarUrl: string | null | undefined;
  email: string | null | undefined;
  isSelf: boolean;
  name: string;
}) {
  return (
    <div className="team-person-identity collection-list__identity">
      <Avatar.Root className="team-person-avatar">
        {avatarUrl ? (
          <Avatar.Image src={avatarUrl} alt="" draggable={false} />
        ) : null}
        <Avatar.Fallback>{personInitials(name)}</Avatar.Fallback>
      </Avatar.Root>
      <div className="min-w-0 wrap-anywhere">
        <strong title={name}>
          {name}
          {isSelf ? <small> (you)</small> : null}
        </strong>
        {email ? <p title={email}>{email}</p> : null}
      </div>
    </div>
  );
}

export type MemberRowAction =
  | { kind: "change-role"; member: ActiveCompanyMember; nextRole: CompanyRole }
  | { kind: "remove"; member: ActiveCompanyMember };

/**
 * One roster row. The role cell is a compact picker for callers who may
 * change this person's role and a read-only badge for everyone else; every
 * disabled option carries the rule that disabled it as its title.
 */
export function TeamMemberRow({
  member,
  context,
  ownerCount,
  showActions,
  onAction,
}: {
  member: ActiveCompanyMember;
  context: MembershipContext;
  ownerCount: number;
  showActions: boolean;
  onAction: (action: MemberRowAction) => void;
}) {
  const isSelf = member.actor.id === context.userId;
  const roles = assignableRoles(context);
  const changeDecision = canChangeMemberRole(member, context, ownerCount);
  const removeDecision = canRemoveMember(member, context, ownerCount);
  const canPickRole = roles.length > 0 && changeDecision.allowed;

  return (
    <tr className="collection-list__row" role="row">
      <td role="cell">
        <TeamPersonIdentity
          avatarUrl={member.actor.avatarUrl}
          email={member.email}
          isSelf={isSelf}
          name={member.actor.name}
        />
      </td>
      <td role="cell">
        {canPickRole ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                aria-label={`Change ${member.actor.name}'s role, currently ${roleLabel(member.role)}`}
                className="team-role-picker"
                type="button"
              >
                <RoleBadge role={member.role} />
                <ChevronDown aria-hidden="true" size={12} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" sideOffset={6}>
              <DropdownMenuLabel>Change role</DropdownMenuLabel>
              <DropdownMenuRadioGroup
                value={member.role}
                onValueChange={(value) =>
                  onAction({
                    kind: "change-role",
                    member,
                    nextRole: value as CompanyRole,
                  })
                }
              >
                {roles.map((role) => {
                  const decision = canChangeMemberRole(
                    member,
                    context,
                    ownerCount,
                    role,
                  );
                  return (
                    <DropdownMenuRadioItem
                      key={role}
                      disabled={!decision.allowed}
                      title={decisionTitle(decision)}
                      value={role}
                    >
                      {roleLabel(role)}
                    </DropdownMenuRadioItem>
                  );
                })}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          <RoleBadge
            role={member.role}
            title={
              showActions && !isSelf ? decisionTitle(changeDecision) : undefined
            }
          />
        )}
      </td>
      <td role="cell">
        <span className="team-membership" data-active={!member.accountDisabled}>
          {member.accountDisabled ? (
            <CircleAlert size={14} aria-hidden="true" />
          ) : (
            <CheckCircle2 size={14} aria-hidden="true" />
          )}
          {member.accountDisabled ? "Disabled" : "Active"}
        </span>
      </td>
      {showActions ? (
        <td className="team-member-actions-cell" role="cell">
          {isSelf || !removeDecision.allowed ? null : (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  aria-label={`Actions for ${member.actor.name}`}
                  className="team-row-actions"
                  type="button"
                >
                  <MoreHorizontal aria-hidden="true" size={18} />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" sideOffset={6}>
                <DropdownMenuItem
                  disabled={!removeDecision.allowed}
                  title={decisionTitle(removeDecision)}
                  variant="destructive"
                  onSelect={() => onAction({ kind: "remove", member })}
                >
                  Remove from company
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </td>
      ) : null}
    </tr>
  );
}
