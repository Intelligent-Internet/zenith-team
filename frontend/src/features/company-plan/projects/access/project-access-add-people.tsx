import { useMemo, useState } from "react";

import type { GroupSummary } from "@/features/agents/api/groups-api";
import type { ActiveCompanyMember } from "@/features/agents/api/team-directory-api";
import type { GroupReviewNames } from "@/features/agents/components/group-change-review";
import { GroupDialog } from "@/features/agents/components/group-dialog";
import type {
  ProjectPermission,
  ProjectVisibility,
} from "@/shared/permissions";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import {
  Check,
  Minus,
  Pencil,
  Plus,
  Search,
  UserPlus,
  Users,
  UsersRound,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

import { TaskMemberAvatar } from "../../components/task-assignee";
import {
  PROJECT_ACCESS_LEVELS,
  levelBlockedReason,
  type ProjectAccessLevelId,
} from "./project-access-levels";
import {
  inheritedAccess,
  type AccessGiver,
  type InheritedAccess,
} from "./project-access-model";
import { ACCESS_TOOLTIP_CLASS } from "./project-access-permission-icons";
import { ProjectAccessPicker } from "./project-access-picker";
import { RoleMark } from "./project-access-row";

/** One person to add, with the permissions picked for them. */
export type NewMember = { userId: string; permissions: ProjectPermission[] };

/** One Group to add, with the permissions picked for it. */
export type NewGroup = {
  groupId: string;
  name: string;
  permissions: ProjectPermission[];
};

/** What the Add dialog needs to create and edit Groups in place. */
export type GroupTools = {
  companyId: string;
  /** Every Group of the Company, to catch a name already taken. */
  groups: readonly GroupSummary[];
  members: readonly ActiveCompanyMember[];
  names: GroupReviewNames;
};

// What a new member gets unless someone picks otherwise: they can edit and
// run tasks (owner decision 2026-10-02).
const DEFAULT_LEVEL: ProjectAccessLevelId = "work";

// Adds people and Groups to a Project the way a team invite does: tick
// them, or Select all, and each ticked entry gets the same permission
// picker as a row, set to "Edit and run" until changed, or to the lowest
// preset the viewer may give when that one is out of reach. People already
// listed and disabled accounts are not offered. Groups the Project has no
// grant for come first, with no heading (Access DESIGN 7.2); a ticked
// Group saves straight away unless its preview asks first, and then it is
// reviewed. Whoever manages Groups can make a new one here, or edit one,
// without leaving. In this dialog each picker opens beside it.
export function ProjectAccessAddPeople({
  candidates,
  disabled,
  giver,
  groupTools,
  groups,
  projectTitle,
  visibility,
  onAdd,
  onAddGroups,
}: {
  candidates: readonly ActiveCompanyMember[];
  disabled: boolean;
  /** The viewer's own standing, for the self-grant cap. */
  giver: AccessGiver;
  /** Present when the viewer may create and edit Groups. */
  groupTools: GroupTools | null;
  /** Groups the Project has no grant for yet. */
  groups: readonly GroupSummary[];
  projectTitle: string;
  visibility: ProjectVisibility;
  /** Saves the grants; resolves once every save has been tried. */
  onAdd: (members: readonly NewMember[]) => Promise<void>;
  /** Gives each Group its access; one whose change asks is reviewed. */
  onAddGroups: (groups: readonly NewGroup[]) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <button
        className="dashboard-button dashboard-button-primary project-access-add"
        disabled={disabled}
        type="button"
        onClick={() => setOpen(true)}
      >
        <UserPlus size={16} aria-hidden="true" /> Add
      </button>
      <DialogContent
        aria-describedby={undefined}
        className="project-access-dialog"
        overlayClassName="project-access-dialog__overlay"
      >
        {open ? (
          <AddPeopleForm
            candidates={candidates}
            giver={giver}
            groupTools={groupTools}
            groups={groups}
            projectTitle={projectTitle}
            visibility={visibility}
            onClose={() => setOpen(false)}
            onAdd={onAdd}
            onAddGroups={onAddGroups}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function defaultPermissions(
  visibility: ProjectVisibility,
  giver: AccessGiver,
): ProjectPermission[] {
  const level =
    PROJECT_ACCESS_LEVELS.find(
      (candidate) =>
        candidate.id === DEFAULT_LEVEL &&
        !levelBlockedReason(candidate, visibility, giver),
    ) ??
    PROJECT_ACCESS_LEVELS.find(
      (candidate) => !levelBlockedReason(candidate, visibility, giver),
    ) ??
    PROJECT_ACCESS_LEVELS[PROJECT_ACCESS_LEVELS.length - 1];
  return [...level.permissions];
}

function AddPeopleForm({
  candidates,
  giver,
  groupTools,
  groups,
  projectTitle,
  visibility,
  onClose,
  onAdd,
  onAddGroups,
}: {
  candidates: readonly ActiveCompanyMember[];
  giver: AccessGiver;
  groupTools: GroupTools | null;
  groups: readonly GroupSummary[];
  projectTitle: string;
  visibility: ProjectVisibility;
  onClose: () => void;
  onAdd: (members: readonly NewMember[]) => Promise<void>;
  onAddGroups: (groups: readonly NewGroup[]) => void;
}) {
  const [query, setQuery] = useState("");
  // Ticked people in the order they were ticked, each with their access.
  const [picked, setPicked] = useState<NewMember[]>([]);
  const [pickedGroups, setPickedGroups] = useState<NewGroup[]>([]);
  const [busy, setBusy] = useState(false);
  // The Group dialog: a new Group (null) or one to edit; undefined closed.
  const [editing, setEditing] = useState<GroupSummary | null | undefined>(
    undefined,
  );
  // On a Project open to everyone, everyone already holds the floor.
  const floor: InheritedAccess = useMemo(
    () => inheritedAccess([], () => "", visibility),
    [visibility],
  );
  const needle = query.trim().toLocaleLowerCase();
  const shown = useMemo(
    () =>
      candidates.filter((member) =>
        `${member.actor.name} ${member.email}`
          .toLocaleLowerCase()
          .includes(needle),
      ),
    [candidates, needle],
  );
  const shownGroups = useMemo(
    () =>
      groups.filter((group) =>
        `${group.name} ${group.description}`
          .toLocaleLowerCase()
          .includes(needle),
      ),
    [groups, needle],
  );
  const offersGroups = groups.length > 0 || groupTools !== null;
  const pickGroup = (group: { id: string; name: string }) =>
    setPickedGroups((current) =>
      current.some((entry) => entry.groupId === group.id)
        ? current
        : [
            ...current,
            {
              groupId: group.id,
              name: group.name,
              permissions: defaultPermissions(visibility, giver),
            },
          ],
    );
  const toggleGroup = (group: GroupSummary) =>
    pickedGroups.some((entry) => entry.groupId === group.id)
      ? setPickedGroups((current) =>
          current.filter((entry) => entry.groupId !== group.id),
        )
      : pickGroup(group);
  const setGroupPermissions = (
    groupId: string,
    permissions: ProjectPermission[],
  ) =>
    setPickedGroups((current) =>
      current.map((entry) =>
        entry.groupId === groupId ? { ...entry, permissions } : entry,
      ),
    );
  const total = picked.length + pickedGroups.length;
  const toggle = (userId: string) =>
    setPicked((current) =>
      current.some((entry) => entry.userId === userId)
        ? current.filter((entry) => entry.userId !== userId)
        : [
            ...current,
            { userId, permissions: defaultPermissions(visibility, giver) },
          ],
    );
  // Select all ticks everyone the search shows; when they are all ticked
  // it unticks them, leaving anyone ticked under another search.
  const shownIds = shown.map((member) => member.actor.id);
  const pickedShown = shownIds.filter((userId) =>
    picked.some((entry) => entry.userId === userId),
  ).length;
  const allShown = shownIds.length > 0 && pickedShown === shownIds.length;
  const toggleAll = () =>
    setPicked((current) =>
      allShown
        ? current.filter((entry) => !shownIds.includes(entry.userId))
        : [
            ...current,
            ...shownIds
              .filter(
                (userId) => !current.some((entry) => entry.userId === userId),
              )
              .map((userId) => ({
                userId,
                permissions: defaultPermissions(visibility, giver),
              })),
          ],
    );
  const setPermissions = (userId: string, permissions: ProjectPermission[]) =>
    setPicked((current) =>
      current.map((entry) =>
        entry.userId === userId ? { ...entry, permissions } : entry,
      ),
    );
  // A picker emptied (View cleared) unticks the entry.
  const changePerson = (userId: string, permissions: ProjectPermission[]) =>
    permissions.length === 0
      ? toggle(userId)
      : setPermissions(userId, permissions);
  const changeGroup = (groupId: string, permissions: ProjectPermission[]) =>
    permissions.length === 0
      ? setPickedGroups((current) =>
          current.filter((entry) => entry.groupId !== groupId),
        )
      : setGroupPermissions(groupId, permissions);

  // The Group dialog sits beside the form, not in it: a submit inside it
  // must never reach this form through React's event tree.
  return (
    <>
      <form
        className="flex min-h-0 flex-1 flex-col"
        onSubmit={(event) => {
          event.preventDefault();
          if (total === 0 || busy) return;
          setBusy(true);
          void (picked.length > 0 ? onAdd(picked) : Promise.resolve()).finally(
            () => {
              setBusy(false);
              onClose();
              if (pickedGroups.length > 0) onAddGroups(pickedGroups);
            },
          );
        }}
      >
        <DialogHeader className="project-access-dialog__header">
          <DialogTitle>{`Add to ${projectTitle}`}</DialogTitle>
        </DialogHeader>
        <div className="project-access-dialog__body">
          <label className="project-access-search">
            <Search size={14} aria-hidden="true" />
            <input
              aria-label={
                offersGroups ? "Search people or groups" : "Search people"
              }
              autoFocus
              placeholder="Search"
              type="search"
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
            />
          </label>
          <ul
            className="project-access-people"
            aria-label={offersGroups ? "Groups and people" : "People"}
          >
            {shownGroups.map((group) => {
              const entry = pickedGroups.find(
                (candidate) => candidate.groupId === group.id,
              );
              return (
                <li
                  key={group.id}
                  className="project-access-people__row"
                  data-picked={entry ? "" : undefined}
                >
                  <button
                    aria-pressed={Boolean(entry)}
                    className="project-access-people__option"
                    disabled={busy}
                    type="button"
                    onClick={() => toggleGroup(group)}
                  >
                    <span
                      className="project-access-people__check"
                      aria-hidden="true"
                    >
                      {entry ? <Check size={12} /> : null}
                    </span>
                    <span className="project-person-avatar project-access-group-mark">
                      <UsersRound size={15} aria-hidden="true" />
                    </span>
                    <span className="project-access-person__text">
                      <strong>{group.name}</strong>
                      <small className="inline-flex items-center gap-[4px] [font-variant-numeric:tabular-nums]">
                        <Users aria-hidden="true" size={12} />
                        <span aria-hidden="true">{group.memberCount}</span>
                        <span className="sr-only">
                          {group.memberCount === 1
                            ? "1 person"
                            : `${group.memberCount} people`}
                        </span>
                      </small>
                    </span>
                  </button>
                  {groupTools ? (
                    <Tooltip
                      contentClassName={ACCESS_TOOLTIP_CLASS}
                      label="Edit group"
                    >
                      <button
                        aria-label={`Edit ${group.name}`}
                        className="team-row-actions project-access-people__edit"
                        disabled={busy}
                        type="button"
                        onClick={() => setEditing(group)}
                      >
                        <Pencil aria-hidden="true" size={14} />
                      </button>
                    </Tooltip>
                  ) : null}
                  {entry ? (
                    <ProjectAccessPicker
                      disabled={busy}
                      giver={giver}
                      inherited={floor}
                      mode={{
                        kind: "draft",
                        onChange: (permissions) =>
                          changeGroup(group.id, permissions),
                      }}
                      own={entry.permissions}
                      subjectName={group.name}
                      visibility={visibility}
                    />
                  ) : null}
                </li>
              );
            })}
            {shown.length > 1 ? (
              <li className="project-access-people__row project-access-people__all">
                <button
                  aria-pressed={
                    allShown ? true : pickedShown > 0 ? "mixed" : false
                  }
                  className="project-access-people__option"
                  disabled={busy}
                  type="button"
                  onClick={toggleAll}
                >
                  <span
                    className="project-access-people__check"
                    aria-hidden="true"
                  >
                    {allShown ? (
                      <Check size={12} />
                    ) : pickedShown > 0 ? (
                      <Minus size={12} />
                    ) : null}
                  </span>
                  <strong>Select all</strong>
                </button>
              </li>
            ) : null}
            {shown.map((member) => {
              const entry = picked.find(
                (candidate) => candidate.userId === member.actor.id,
              );
              return (
                <li
                  key={member.actor.id}
                  className="project-access-people__row"
                  data-picked={entry ? "" : undefined}
                >
                  <button
                    aria-pressed={Boolean(entry)}
                    className="project-access-people__option"
                    disabled={busy}
                    type="button"
                    onClick={() => toggle(member.actor.id)}
                  >
                    <span
                      className="project-access-people__check"
                      aria-hidden="true"
                    >
                      {entry ? <Check size={12} /> : null}
                    </span>
                    <TaskMemberAvatar member={member.actor} />
                    <span className="project-access-person__text">
                      <span className="project-access-person__name">
                        <strong>{member.actor.name}</strong>
                        <RoleMark focusable={false} role={member.role} />
                      </span>
                      <small>{member.email}</small>
                    </span>
                  </button>
                  {entry ? (
                    <ProjectAccessPicker
                      disabled={busy}
                      giver={giver}
                      inherited={floor}
                      mode={{
                        kind: "draft",
                        onChange: (permissions) =>
                          changePerson(member.actor.id, permissions),
                      }}
                      own={entry.permissions}
                      subjectName={member.actor.name}
                      visibility={visibility}
                    />
                  ) : null}
                </li>
              );
            })}
            {shown.length === 0 && shownGroups.length === 0 ? (
              <li className="p-[22px_12px] text-workspace-muted text-[length:12px] text-center [list-style:none]">
                {candidates.length === 0 && groups.length === 0
                  ? "Everyone has access"
                  : "No match"}
              </li>
            ) : null}
          </ul>
        </div>
        <DialogFooter className="project-access-dialog__footer">
          {groupTools ? (
            <button
              className="dashboard-button project-access-dialog__aside"
              disabled={busy}
              type="button"
              onClick={() => setEditing(null)}
            >
              <Plus aria-hidden="true" size={15} />
              New group
            </button>
          ) : (
            <span className="project-access-dialog__match" />
          )}
          <button
            className="dashboard-button"
            disabled={busy}
            type="button"
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            className="dashboard-button dashboard-button-primary"
            disabled={busy || total === 0}
            type="submit"
          >
            {busy ? "Adding…" : "Add"}
          </button>
        </DialogFooter>
      </form>
      {groupTools && editing !== undefined ? (
        <GroupDialog
          companyId={groupTools.companyId}
          group={editing}
          groups={groupTools.groups}
          members={groupTools.members}
          names={groupTools.names}
          onClose={() => setEditing(undefined)}
          onCreated={pickGroup}
        />
      ) : null}
    </>
  );
}
