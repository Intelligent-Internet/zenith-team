import { ShieldCheck, UserPlus, Users } from "@/shared/ui/icons";
import { useRef, useState, type KeyboardEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { TeamProjectAccess } from "@/features/company-plan/projects/access/team-project-access";
import {
  TEAM_PROJECT_ACCESS_VIEW,
  opensProjectAccess,
} from "@/features/company-plan/projects/access/team-access-path";
import { refreshSessionSnapshot, getNextRoute } from "@/app/session/session";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import {
  WorkspaceAccessState,
  WorkspacePageFrame,
} from "@/features/common/components/workspace-page-frame";
import {
  useListActiveCompanyMembersQuery,
  type ActiveCompanyMember,
} from "@/features/agents/api/team-directory-api";
import { InvitePeopleDialog } from "@/features/company-invitations/components/invite-people-dialog";
import "@/features/agents/styles/agents.css";
import { CompanyInvitationsSection } from "@/features/company-invitations/components/company-invitations-section";
import { useListCompanyInvitationsQuery } from "@/features/company-invitations/api/invitations-api";
import {
  canLeave,
  decisionTitle,
  useCompanyPermissions,
  type MembershipContext,
} from "@/shared/permissions";

import {
  MembershipActionDialog,
  type MembershipAction,
} from "../components/membership-action-dialog";
import { TeamMemberRow } from "../components/team-member-row";
import { Button } from "@/shared/ui/core/button";

export function TeamPage() {
  const session = useSessionSnapshot();
  if (!session.company?.id)
    return (
      <WorkspaceAccessState
        title="Choose a company"
        description="Company owners and admins manage membership and invitations."
      />
    );
  return (
    <CompanyTeam key={session.company.id} companyId={session.company.id} />
  );
}

function CompanyTeam({ companyId }: { companyId: string }) {
  const session = useSessionSnapshot();
  const permissions = useCompanyPermissions();
  const query = useListActiveCompanyMembersQuery(companyId, {
    refetchOnFocus: true,
  });
  const canInvite = permissions.can("members.invite");
  const showActions = permissions.can("members.manage");
  const invitations = useListCompanyInvitationsQuery(companyId, {
    skip: !canInvite,
  });
  const navigate = useNavigate();
  const [membershipAction, setMembershipAction] =
    useState<MembershipAction | null>(null);
  const [inviteOpen, setInviteOpen] = useState(false);
  const membershipReturnFocus = useRef<HTMLElement | null>(null);
  const members = query.currentData?.items ?? [];
  const self = members.find(
    (member: ActiveCompanyMember) => member.actor.id === session.userId,
  );
  const ownerCount = members.filter((member) => member.role === "owner").length;
  const context: MembershipContext = {
    userId: session.userId,
    role: permissions.role,
    permissions: permissions.permissions,
  };
  const leaveDecision = canLeave(context, ownerCount);
  const isLastOwner =
    Boolean(self) &&
    !query.isError &&
    !leaveDecision.allowed &&
    leaveDecision.reason === "last_owner";
  const companyName = session.company?.workspaceName ?? "your company";
  // Members, or one Project's access (owner decision 2026-10-02: access is
  // managed here). Groups live in Project access, listed with the people
  // (2026-10-07), so an old Groups link opens it, and so does the tab's
  // own name. The view and Project live in the URL, so links can open a
  // Project's access directly.
  const [searchParams, setSearchParams] = useSearchParams();
  const tabsRef = useRef<HTMLDivElement>(null);
  const view = opensProjectAccess(searchParams.get("view"))
    ? "projects"
    : "members";
  const showView = (next: "members" | "projects", projectId?: string) => {
    const params = new URLSearchParams(searchParams);
    if (next === "projects") params.set("view", TEAM_PROJECT_ACCESS_VIEW);
    else params.delete("view");
    if (projectId) params.set("project", projectId);
    setSearchParams(params, { replace: true });
  };
  const onViewKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const next =
      event.key === "Home"
        ? "members"
        : event.key === "End"
          ? "projects"
          : view === "members"
            ? "projects"
            : "members";
    showView(next);
    tabsRef.current
      ?.querySelector<HTMLButtonElement>(`#team-${next}-tab`)
      ?.focus();
  };

  return (
    <WorkspacePageFrame
      ariaLabel="Company team"
      className="team-page collection-page"
      contentWidth="contained"
      tourId="team-overview"
      title="Team"
      toolbar={
        <div className="team-roster-heading">
          <div
            ref={tabsRef}
            className="team-view-tabs"
            role="tablist"
            aria-label="Team view"
          >
            <button
              id="team-members-tab"
              aria-controls="team-view-panel"
              aria-selected={view === "members"}
              className="team-view-tab"
              role="tab"
              tabIndex={view === "members" ? 0 : -1}
              type="button"
              onClick={() => showView("members")}
              onKeyDown={onViewKeyDown}
            >
              <Users size={15} />
              Members
            </button>
            <button
              id="team-projects-tab"
              aria-controls="team-view-panel"
              aria-selected={view === "projects"}
              className="team-view-tab"
              role="tab"
              tabIndex={view === "projects" ? 0 : -1}
              type="button"
              onClick={() => showView("projects")}
              onKeyDown={onViewKeyDown}
            >
              <ShieldCheck size={15} />
              Project access
            </button>
          </div>
        </div>
      }
      actions={
        canInvite ? (
          <button
            id="team-invite-people"
            className="dashboard-button dashboard-button-primary"
            type="button"
            onClick={() => setInviteOpen(true)}
          >
            <UserPlus size={16} />
            Invite people
          </button>
        ) : null
      }
    >
      <div
        id="team-view-panel"
        role="tabpanel"
        aria-labelledby={`team-${view}-tab`}
      >
        {view === "projects" ? (
          <TeamProjectAccess
            companyId={companyId}
            projectId={searchParams.get("project")}
            onSelectProject={(projectId) => showView("projects", projectId)}
          />
        ) : (
          <>
            <div className="team-roster collection-list">
              {query.isLoading ? (
                <p role="status">Loading people…</p>
              ) : query.isError ? (
                <p role="alert">
                  People could not be loaded.{" "}
                  <button onClick={() => void query.refetch()}>Retry</button>
                </p>
              ) : (
                <table
                  className="team-member-table"
                  role="table"
                  aria-label="Team members"
                >
                  <colgroup>
                    <col />
                    <col className="team-member-table__role" />
                    <col className="team-member-table__status" />
                    {showActions && (
                      <col className="team-member-table__actions" />
                    )}
                  </colgroup>
                  <thead role="rowgroup">
                    <tr className="collection-list__header" role="row">
                      <th scope="col">Member</th>
                      <th scope="col">Company role</th>
                      <th scope="col">Status</th>
                      {showActions && (
                        <th scope="col">
                          <span className="sr-only">Actions</span>
                        </th>
                      )}
                    </tr>
                  </thead>
                  <tbody role="rowgroup">
                    {members.map((member) => (
                      <TeamMemberRow
                        key={member.actor.id}
                        context={context}
                        member={member}
                        onAction={(action) => {
                          membershipReturnFocus.current = null;
                          setMembershipAction(action);
                        }}
                        ownerCount={ownerCount}
                        showActions={showActions}
                      />
                    ))}
                  </tbody>
                </table>
              )}
            </div>
            {canInvite && <CompanyInvitationsSection companyId={companyId} />}
            <div className="team-leave-section">
              <div>
                <strong>Your membership</strong>
                <p id="team-leave-description">
                  {isLastOwner
                    ? "You’re the only Owner. Assign another Owner before leaving."
                    : `You are ${roleLabelSentence(permissions.role)} of ${companyName}. Leaving keeps your account and the team’s work history.`}
                </p>
              </div>
              <div className="team-leave-section__control">
                <Button
                  variant="ghost"
                  size="sm"
                  className="team-leave-button"
                  disabled={!self || query.isError || !leaveDecision.allowed}
                  aria-describedby="team-leave-description"
                  title={decisionTitle(leaveDecision)}
                  onClick={(event) => {
                    membershipReturnFocus.current = event.currentTarget;
                    if (self)
                      setMembershipAction({ kind: "leave", member: self });
                  }}
                >
                  Leave team
                </Button>
              </div>
            </div>
          </>
        )}
      </div>
      {membershipAction && (
        <MembershipActionDialog
          action={membershipAction}
          companyId={companyId}
          companyName={companyName}
          onClose={() => setMembershipAction(null)}
          onRestoreFocus={
            membershipReturnFocus.current
              ? () => {
                  const target = membershipReturnFocus.current?.isConnected
                    ? membershipReturnFocus.current
                    : document.getElementById("team-invite-people");
                  target?.focus();
                }
              : undefined
          }
          onLeft={async () => {
            const updated = await refreshSessionSnapshot();
            navigate(getNextRoute(updated), { replace: true });
          }}
        />
      )}
      {inviteOpen ? (
        <InvitePeopleDialog
          companyName={companyName}
          emailEnabled={invitations.data?.enabled ?? false}
          companyId={companyId}
          onClose={() => setInviteOpen(false)}
        />
      ) : null}
    </WorkspacePageFrame>
  );
}

function roleLabelSentence(role: string | undefined) {
  switch (role) {
    case "owner":
      return "an Owner";
    case "admin":
      return "an Admin";
    default:
      return "a Member";
  }
}
