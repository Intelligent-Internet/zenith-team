import * as Avatar from "@radix-ui/react-avatar";
import { useState } from "react";

import {
  PROJECT_GRANT_EXCEEDS_OWN_COPY,
  PROJECT_VISIBILITY_FLOOR_COPY,
  accessErrorCopy,
} from "@/lib/api-error-copy";
import type { ZenithTeamApiError } from "@/shared/api/zenith-team-api";
import type {
  CompanyRole,
  ProjectPermission,
  ProjectVisibility,
} from "@/shared/permissions";
import { Crown, LockKeyhole, ShieldCheck } from "@/shared/ui/icons";
import { personInitials } from "@/shared/ui/person-initials";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";

import {
  usePreviewProjectGrantMutation,
  useRemoveProjectGrantMutation,
  useSetProjectGrantMutation,
  type ProjectGrant,
  type ProjectGrantPreview,
} from "../../api/project-api";
import type { AccessConfirmation } from "./project-access-confirm-dialog";
import {
  grantAsks,
  groupPermissions,
  inheritedAccess,
  orderedPermissions,
  stopsRuns,
  withinGiver,
  type AccessGiver,
} from "./project-access-model";
import {
  ACCESS_TOOLTIP_CLASS,
  StaticPermissions,
} from "./project-access-permission-icons";
import { ProjectAccessPicker } from "./project-access-picker";

export type ProjectAccessPerson = {
  userId: string;
  name: string;
  email: string | null;
  avatarUrl: string | null;
  role?: CompanyRole;
  isSelf: boolean;
};

/** Under a row whose change the server refused under the self-grant cap. */
const NOT_SAVED_COPY = "Not saved: beyond your access";

// One person with access: who they are, a role mark for Company owners
// and admins (who manage every Project through their role), and one label
// of what they can do here. The label opens the picker, the one home of
// every change and of Remove access ("Leave" on your own row); a change
// saves with the access revision it was read at, and a stale revision
// reloads the list instead of overwriting someone else. A change or a
// removal asks first only when the server's preview says a run of theirs
// stops or they lose the Project, as a Group change does; anything else
// saves straight away. What their Groups give shows in the
// picker locked, with the Group's name. Under the self-grant cap a person
// whose own grant holds more than the viewer shows only their label, with
// a lock in the last column, so every label keeps one right edge.
export function ProjectAccessRow({
  companyId,
  editable,
  giver,
  grant,
  groupName,
  person,
  projectId,
  projectTitle,
  revision,
  visibility,
  onConfirm,
  onConflict,
  onSaved,
}: {
  companyId: string;
  /** The viewer holds project.grants.manage here. */
  editable: boolean;
  /** The viewer's own standing, for the self-grant cap. */
  giver: AccessGiver;
  grant: ProjectGrant;
  /** The name of a Group the Project lists. */
  groupName: (groupId: string) => string;
  person: ProjectAccessPerson;
  projectId: string;
  projectTitle: string;
  revision: number;
  visibility: ProjectVisibility;
  onConfirm: (confirmation: AccessConfirmation) => void;
  onConflict: () => void;
  /** A change landed; `revision` is the access revision after it. */
  onSaved: (revision: number) => void;
}) {
  // The server refused the last change under the self-grant cap.
  const [refused, setRefused] = useState(false);
  const [setGrant, setState] = useSetProjectGrantMutation();
  const [removeGrant, removeState] = useRemoveProjectGrantMutation();
  const [previewGrant, previewState] = usePreviewProjectGrantMutation();
  const busy =
    setState.isLoading || removeState.isLoading || previewState.isLoading;
  const granted = orderedPermissions(grant.granted);
  const fromGroups = groupPermissions(grant.sources);
  const inherited = inheritedAccess(grant.sources, groupName, visibility);
  // Only through Groups: nothing of their own to remove here.
  const groupOnly = granted.length === 0;
  const companyVisible = visibility === "company";
  // Only their own grant counts, never what their Company role adds.
  const outranks = editable && !withinGiver(granted, giver);
  const changeable = editable && !outranks;

  const fail = (reason: unknown, fallback: string) => {
    const failure = reason as Partial<ZenithTeamApiError>;
    if (failure.code === "project_grant_exceeds_own") {
      // The viewer's own access likely changed: reload so the greying
      // matches the server, and say why under the row.
      setRefused(true);
      onConflict();
      return;
    }
    toast.error(accessErrorCopy(failure.code) ?? fallback);
    if (failure.code === "project_grant_revision_conflict") onConflict();
  };
  const store = async (permissions: ProjectPermission[]) => {
    setRefused(false);
    try {
      const result = await setGrant({
        companyId,
        id: projectId,
        userId: person.userId,
        permissions,
        expectedRevision: revision,
      }).unwrap();
      onSaved(result.revision);
      return true;
    } catch (reason) {
      fail(reason, "Couldn't save access");
      return false;
    }
  };
  // What saving `permissions` would do, as the server's preview counts it
  // (`[]` is a removal). A change that keeps their View and Run tasks with
  // agents, beyond what their Groups give, can neither stop a run nor take
  // the Project, so it is not checked: null. Undefined when the check
  // failed, which is reported like a failed save.
  const check = async (
    permissions: ProjectPermission[],
  ): Promise<ProjectGrantPreview | null | undefined> => {
    if (
      permissions.length > 0 &&
      !stopsRuns(
        orderedPermissions([...granted, ...fromGroups]),
        orderedPermissions([...permissions, ...fromGroups]),
        visibility,
      )
    ) {
      return null;
    }
    setRefused(false);
    try {
      return await previewGrant({
        companyId,
        id: projectId,
        userId: person.userId,
        permissions,
      }).unwrap();
    } catch (reason) {
      fail(reason, "Couldn't check change");
      return undefined;
    }
  };
  // The question a previewed save asks first, or null to save at once
  // (`grantAsks`): losing the Project asks "Remove {Name} from
  // {Project}?" ("Leave {Project}?" for yourself), a stopping run asks
  // "Change access?", each with the runs that stop.
  const confirmation = (
    preview: ProjectGrantPreview | null,
    run: () => Promise<void>,
  ): AccessConfirmation | null => {
    const asks = preview ? grantAsks(preview) : null;
    if (!preview || !asks) return null;
    const counts = { runs: preview.runsStopping };
    if (asks === "loss") {
      return person.isSelf
        ? {
            title: `Leave ${projectTitle}?`,
            counts,
            action: "Leave",
            destructive: true,
            run,
          }
        : {
            title: `Remove ${person.name} from ${projectTitle}?`,
            counts,
            action: "Remove",
            destructive: true,
            run,
          };
    }
    return { title: "Change access?", counts, action: "Change", run };
  };
  const change = async (permissions: ProjectPermission[]) => {
    const preview = await check(permissions);
    if (preview === undefined) return false;
    const asking = confirmation(preview, async () => {
      await store(permissions);
    });
    if (asking) {
      onConfirm(asking);
      return true;
    }
    return store(permissions);
  };
  const removeNow = async () => {
    setRefused(false);
    try {
      const result = await removeGrant({
        companyId,
        id: projectId,
        userId: person.userId,
        expectedRevision: revision,
      }).unwrap();
      onSaved(result.revision);
    } catch (reason) {
      fail(reason, "Couldn't remove access");
    }
  };
  // A removal asks on the same terms as a change: only when
  // they lose the Project (no Group still gives it) or a run of theirs
  // stops; otherwise it happens straight away and their row stays.
  const remove = async () => {
    const preview = await check([]);
    if (preview === undefined) return;
    const asking = confirmation(preview, removeNow);
    if (asking) {
      onConfirm(asking);
      return;
    }
    await removeNow();
  };

  return (
    <li className="project-access-row">
      <div className="flex min-w-0 items-center gap-[12px] [grid-area:who]">
        <Avatar.Root className="team-person-avatar project-access-row__avatar">
          {person.avatarUrl ? (
            <Avatar.Image alt="" draggable={false} src={person.avatarUrl} />
          ) : null}
          <Avatar.Fallback>{personInitials(person.name)}</Avatar.Fallback>
        </Avatar.Root>
        <div className="flex min-w-0 flex-col gap-[2px]">
          <span className="project-access-row__name flex min-w-0 items-center gap-[6px]">
            <strong title={person.name}>{person.name}</strong>
            {person.isSelf ? <small>you</small> : null}
            <RoleMark role={person.role} />
          </span>
          {person.email ? (
            <span
              className="overflow-hidden text-workspace-muted text-[length:var(--workspace-text-caption,_12px)] leading-[var(--workspace-leading-caption,_1.4)] text-ellipsis whitespace-nowrap"
              title={person.email}
            >
              {person.email}
            </span>
          ) : null}
        </div>
      </div>
      <div className="flex min-w-0 justify-end [grid-area:perms]">
        {changeable ? (
          <ProjectAccessPicker
            disabled={busy}
            giver={giver}
            inherited={inherited}
            mode={{
              kind: "save",
              onSave: change,
              onRemove: groupOnly ? undefined : () => void remove(),
              removeBlocked: companyVisible
                ? PROJECT_VISIBILITY_FLOOR_COPY
                : null,
              self: person.isSelf,
            }}
            own={granted}
            subjectName={person.name}
            visibility={visibility}
          />
        ) : (
          <StaticPermissions
            inherited={inherited}
            own={granted}
            subjectName={person.name}
          />
        )}
      </div>
      <div className="flex items-center justify-center [grid-area:menu]">
        {outranks ? <CapLock /> : null}
      </div>
      {refused ? (
        <small className="project-access-row__refused" role="alert">
          {NOT_SAVED_COPY}
        </small>
      ) : null}
    </li>
  );
}

/**
 * The lock after a label the viewer may not change, in the row's last
 * column: the person or Group holds more here than the viewer (the
 * self-grant cap).
 */
export function CapLock() {
  return (
    <Tooltip
      contentClassName={ACCESS_TOOLTIP_CLASS}
      label={PROJECT_GRANT_EXCEEDS_OWN_COPY}
    >
      <span
        aria-label={PROJECT_GRANT_EXCEEDS_OWN_COPY}
        className="project-access-perms__lock"
        role="img"
        tabIndex={0}
      >
        <LockKeyhole aria-hidden="true" size={13} />
      </span>
    </Tooltip>
  );
}

const ROLE_MARKS = {
  owner: { Icon: Crown, label: "Owner" },
  admin: { Icon: ShieldCheck, label: "Admin" },
} as const;

// A crown for a Company owner, a shield for an admin, nothing for a
// member: their role lets them manage every Project, without its content.
// Inside a button (Add's rows) it takes no focus of its own: the button's
// name already includes it.
export function RoleMark({
  focusable = true,
  role,
}: {
  focusable?: boolean;
  role?: CompanyRole;
}) {
  if (role !== "owner" && role !== "admin") return null;
  const { Icon, label } = ROLE_MARKS[role];
  return (
    <Tooltip contentClassName={ACCESS_TOOLTIP_CLASS} label={label}>
      <span
        aria-label={label}
        className="project-access-role"
        data-role={role}
        role="img"
        tabIndex={focusable ? 0 : undefined}
      >
        <Icon aria-hidden="true" size={13} />
      </span>
    </Tooltip>
  );
}
