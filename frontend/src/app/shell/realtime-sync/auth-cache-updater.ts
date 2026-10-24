import { clearInvalidClientSessionState } from "@/app/session/client-session-state";
import { getCurrentSessionId } from "@/lib/auth-token";
import {
  readSessionSnapshot,
  refreshSessionSnapshot,
  updateSessionCompanyAccess,
  updateSessionUserSummary,
} from "@/shared/session/session";
import type { components as ApiComponents } from "@/shared/api/generated/api.generated";
import type { ZenithTeamRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import {
  replaceVersioned,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";
import { accountApi } from "@/shared/api/account-api";
import { zenithTeamApi } from "@/shared/api/zenith-team-api";
import {
  removeMember,
  teamDirectoryApi,
  upsertMember,
} from "@/features/agents/api/team-directory-api";
import { invitationsApi } from "@/features/company-invitations/api/invitations-api";
import { roleLabel } from "@/shared/permissions";
import { toast } from "@/shared/ui/sonner";

type User = ApiComponents["schemas"]["User"];
type ActiveMember = ApiComponents["schemas"]["ActiveMember"];
type MemberRemoved = { revision: number; userId: string };
type CompanyAccessChanged =
  ApiComponents["schemas"]["CompanyAccessChangedPayload"];
type CompanyInvitation = ApiComponents["schemas"]["CompanyInvitation"];

let sessionReconciliation: Promise<void> | undefined;

export function applyAuthCacheEvent(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  switch (event.type) {
    case "user.updated":
      updateCurrentUser(context, event.payload as User);
      return true;
    case "auth.session_revoked":
      if (isOwnSession(event.payload.sessionId)) reconcileSession();
      return true;
    case "auth.company_access_changed":
      applyCompanyAccessChange(context, event);
      return true;
    case "auth.company_access_revoked":
      if (event.payload.companyId === context.companyId) {
        reconcileSession();
      }
      return true;
    case "member.added":
    case "member.updated":
      updateMembership(
        context,
        event.payload as ActiveMember,
        event.type === "member.added",
      );
      return true;
    case "member.removed":
      removeMembership(context, event.payload as MemberRemoved);
      return true;
    case "invitation.changed":
      updateInvitation(context, event);
      return true;
    case "invitation.share_link_changed":
      invalidateShareLink(context, event);
      return true;
    default:
      return false;
  }
}

/**
 * The member's own access moved: role and permissions land in the session
 * from the frame itself, so every permission gate re-renders before the
 * Company refetch confirms it, and a role change is announced once.
 */
function applyCompanyAccessChange(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  const payload = event.payload as Partial<CompanyAccessChanged>;
  if (payload.companyId !== context.companyId) return;
  const current = readSessionSnapshot();
  // A change this person made themselves (transferring ownership) already
  // has its own confirmation; announce only what someone else decided. The
  // server emits role_changed only for a real change, and the session may
  // already hold the new role when the company-wide member.updated frame
  // that precedes this one triggered a refetch, so the frame is the fact.
  const decidedByOther = event.actorId !== current.userId;
  if (
    typeof payload.role === "string" &&
    Array.isArray(payload.permissions) &&
    current.company?.id === payload.companyId
  ) {
    updateSessionCompanyAccess(payload.companyId, {
      role: payload.role,
      permissions: payload.permissions,
    });
    if (!payload.permissions.includes("mailbox.manage")) {
      // Discard cached email bodies, signed attachment links and draft content
      // when Mailbox access is lost. The visible queries reload with new rights.
      context.dispatch(zenithTeamApi.util.resetApiState());
    }
    if (decidedByOther && payload.reason === "role_changed") {
      const companyName = current.company?.workspaceName ?? "this company";
      toast(`Your role in ${companyName} is now ${roleLabel(payload.role)}.`);
    }
  }
  // A role that gains or loses projects.manage changes the management
  // permissions on every Project and the Managed projects view (ADR 0017),
  // so the Project reads refetch with it.
  context.dispatch(
    zenithTeamApi.util.invalidateTags([
      { id: context.companyId, type: "Member" },
      { id: context.companyId, type: "Invitation" },
      { id: context.companyId, type: "Project" },
      { id: context.companyId, type: "Activity" },
    ]),
  );
  context.requestRefresh("shell");
  reconcileSession();
}

function updateMembership(
  context: RealtimeSyncContext,
  member: ActiveMember,
  joined: boolean,
) {
  if (member.companyId !== context.companyId) return;
  context.dispatch(
    teamDirectoryApi.util.updateQueryData(
      "listActiveCompanyMembers",
      context.companyId,
      (page) => {
        upsertMember(page.items, member);
      },
    ),
  );
  // A member who rejoins has their Tasks back, and those Tasks stop reading
  // assignee_inactive. Their Agent setups went when they left, so no Task
  // frame says so.
  if (joined) context.requestRefresh("runtimes", "tasks");
  else context.requestRefresh("runtimes");
  if (readSessionSnapshot().userId === member.actor.id) {
    reconcileSession();
  }
}

function removeMembership(context: RealtimeSyncContext, member: MemberRemoved) {
  context.dispatch(
    teamDirectoryApi.util.updateQueryData(
      "listActiveCompanyMembers",
      context.companyId,
      (page) => {
        removeMember(page.items, member.userId, member.revision);
      },
    ),
  );
  // The Tasks they were assigned stay theirs until someone hands them over,
  // and now read as not ready (assignee_inactive). No Task frame says so.
  context.requestRefresh("runtimes", "tasks");
  if (readSessionSnapshot().userId === member.userId) {
    reconcileSession();
  }
}

function updateInvitation(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  if (event.companyId !== context.companyId) return;
  const invitation = event.payload as CompanyInvitation;
  if (typeof invitation.id !== "string") return;
  context.dispatch(
    invitationsApi.util.updateQueryData(
      "listCompanyInvitations",
      context.companyId,
      (collection) => {
        const index = collection.items.findIndex(
          (item) => item.id === invitation.id,
        );
        if (index === -1) {
          collection.items.unshift(invitation);
          return;
        }
        replaceVersioned(collection.items, invitation);
      },
    ),
  );
}

function invalidateShareLink(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  if (event.companyId !== context.companyId) return;
  // The frame never carries the URL, so a panel showing the link refetches
  // it through the authorized route.
  context.dispatch(
    zenithTeamApi.util.invalidateTags([
      { id: `link:${context.companyId}`, type: "Invitation" },
    ]),
  );
}

function updateCurrentUser(context: RealtimeSyncContext, user: User) {
  const current = readSessionSnapshot();
  if (current.userId && current.userId !== user.id) {
    reconcileSession();
    return;
  }
  const cached = accountApi.endpoints.getCurrentUser.select()(
    context.getState(),
  ).data;
  if (cached && cached.revision > user.revision) return;
  updateSessionUserSummary(user);
  context.dispatch(
    accountApi.util.upsertQueryData("getCurrentUser", undefined, user),
  );
}

// Every device sees every revocation, and most are another device's routine
// rotation. A client that never learned its own session id still reconciles
// on each one.
function isOwnSession(sessionId: unknown) {
  const currentSessionId = getCurrentSessionId();
  return currentSessionId === null || sessionId === currentSessionId;
}

function reconcileSession() {
  if (sessionReconciliation) return;
  sessionReconciliation = reconcileSessionOnce().finally(() => {
    sessionReconciliation = undefined;
  });
}

async function reconcileSessionOnce() {
  try {
    await refreshSessionSnapshot();
  } catch {
    await clearInvalidClientSessionState().catch(() => undefined);
  }
}
