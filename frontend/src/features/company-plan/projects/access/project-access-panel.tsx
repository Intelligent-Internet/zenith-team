import { useMemo, useState, type ReactNode } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  useListGroupsQuery,
  type GroupSummary,
} from "@/features/agents/api/groups-api";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import {
  GroupChangeReviewDialog,
  type GroupReview,
  type GroupReviewNames,
} from "@/features/agents/components/group-change-review";
import { GroupDialog } from "@/features/agents/components/group-dialog";
import {
  useDeleteGroup,
  useGroupReviewNames,
} from "@/features/agents/hooks/use-group-actions";
import { accessErrorCopy } from "@/lib/api-error-copy";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import { useCan, useProjectAccess } from "@/shared/permissions";
import { Eye, LogIn } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";
import { useAppDispatch } from "@/state";

import {
  projectApi,
  useJoinProjectMutation,
  useListProjectGrantsQuery,
  usePreviewProjectGroupGrantMutation,
  useSetProjectGrantMutation,
  useSetProjectGroupGrantMutation,
  type Project,
} from "../../api/project-api";
import {
  ProjectAccessAddPeople,
  type NewGroup,
  type NewMember,
} from "./project-access-add-people";
import {
  ProjectAccessConfirmDialog,
  type AccessConfirmation,
} from "./project-access-confirm-dialog";
import {
  ProjectAccessGroupRow,
  changeGroupGrant,
} from "./project-access-group-row";
import { ACCESS_TOOLTIP_CLASS } from "./project-access-permission-icons";
import {
  ProjectAccessRow,
  type ProjectAccessPerson,
} from "./project-access-row";
import { AccessLoading, LoadFailed } from "./project-access-status";
import { ProjectEveryoneNotice } from "./project-visibility-control";
import "@/features/agents/styles/agents.css";
import "@/features/agents/styles/groups.css";
import "./project-access.css";

// One Project's access on the Team page (ADR 0017): one list, the Groups
// first and then everyone added, each with one label of what they can do.
// Holders of "Manage access" change it through that label's picker;
// everyone else who can see the Project reads it, marked "View only".
// Presets are a shortcut of the picker only; what is stored is each
// person's, or Group's, permissions. Someone who manages access only
// through their own grant gives only what they hold (the self-grant cap):
// what is above it is greyed, and people or Groups who hold more are
// read-only. A Group change reads the server's preview first and is
// reviewed only when someone loses the Project, a run stops or Projects
// the viewer can't see move (Access DESIGN 7.0). A manager who cannot read
// the Project gets Join beside the Project picker.
export function ProjectAccessPanel({
  companyId,
  project,
  toolbarStart,
}: {
  companyId: string;
  project: Project;
  /** The left of the toolbar: the Project picker. */
  toolbarStart?: ReactNode;
}) {
  const dispatch = useAppDispatch();
  const viewerId = useSessionSnapshot().userId;
  const access = useProjectAccess(project);
  const canManageGroups = useCan("members.manage");
  // Access stays manageable on an archive (ADR 0017): only the visibility
  // notice freezes there.
  const editable = access.can("project.grants.manage");
  const grants = useListProjectGrantsQuery({ companyId, id: project.id });
  const members = useListActiveCompanyMembersQuery(companyId);
  const companyGroups = useListGroupsQuery(companyId, {
    skip: !editable && !canManageGroups,
  });
  const allGroupNames = useGroupReviewNames(companyId);
  const [setGrant] = useSetProjectGrantMutation();
  const [previewGroupGrant] = usePreviewProjectGroupGrantMutation();
  const [setGroupGrant] = useSetProjectGroupGrantMutation();
  // Group changes waiting to be reviewed, the first one on screen.
  const [reviews, setReviews] = useState<GroupReview[]>([]);
  const [savedRevision, setSavedRevision] = useState(0);
  const [confirmation, setConfirmation] = useState<AccessConfirmation | null>(
    null,
  );
  // The Group dialog for a row's Edit group.
  const [editingGroup, setEditingGroup] = useState<GroupSummary | null>(null);
  const { askDelete, checking } = useDeleteGroup(companyId, setConfirmation);
  const revision = Math.max(project.access.revision, savedRevision);

  const reload = () => {
    dispatch(
      projectApi.util.invalidateTags([
        { type: "Project", id: companyId },
        { type: "Project", id: `${project.id}:grants` },
      ]),
    );
  };
  const memberById = useMemo(
    () =>
      new Map(
        (members.currentData?.items ?? []).map((member) => [
          member.actor.id,
          member,
        ]),
      ),
    [members.currentData],
  );
  const person = (userId: string): ProjectAccessPerson => {
    const member = memberById.get(userId);
    return {
      userId,
      name: member?.actor.name ?? "Former member",
      email: member?.email ?? null,
      avatarUrl: member?.actor.avatarUrl ?? null,
      role: member?.role,
      isSelf: userId === viewerId,
    };
  };
  const stored = [...(grants.currentData?.items ?? [])].sort((left, right) =>
    person(left.userId).name.localeCompare(person(right.userId).name),
  );
  const groupGrants = [...(grants.currentData?.groups ?? [])].sort(
    (left, right) => left.name.localeCompare(right.name),
  );
  const groupNameById = new Map(
    groupGrants.map((grant) => [grant.groupId, grant.name]),
  );
  const groupName = (groupId: string) =>
    groupNameById.get(groupId) ?? "a group";
  const allGroups = companyGroups.currentData?.items ?? [];
  const groupById = new Map(allGroups.map((group) => [group.id, group]));
  const groupCandidates = allGroups.filter(
    (group) => !groupNameById.has(group.id),
  );
  const reviewNames: GroupReviewNames = {
    project: (projectId) =>
      projectId === project.id ? { title: project.title } : null,
    person: (userId) => ({
      name: person(userId).name,
      avatarUrl: person(userId).avatarUrl,
      isSelf: userId === viewerId,
      role: person(userId).role,
    }),
  };
  const storedIds = new Set(stored.map((grant) => grant.userId));
  // A manager who has not joined gets in through Join, its one home, so
  // Add does not offer them themself.
  const candidates = (members.currentData?.items ?? []).filter(
    (member) =>
      !member.accountDisabled &&
      !storedIds.has(member.actor.id) &&
      !(member.actor.id === viewerId && !access.reads),
  );
  const saved = (next: number) =>
    setSavedRevision((current) => Math.max(current, next));
  const review = (next: GroupReview) =>
    setReviews((current) => [...current, next]);
  // Gives each Group its access one after another: each saves straight
  // away unless its preview asks first. Those that ask are reviewed after
  // the rest have saved, each reading its preview again when it shows, so
  // none is reviewed against a revision a later save moved.
  const addGroups = async (entries: readonly NewGroup[]) => {
    const asking: GroupReview[] = [];
    for (const entry of entries) {
      try {
        const asks = await changeGroupGrant({
          companyId,
          groupId: entry.groupId,
          groupName: entry.name,
          permissions: entry.permissions,
          preview: previewGroupGrant,
          projectId: project.id,
          save: setGroupGrant,
          onSaved: saved,
        });
        if (asks) asking.push(asks.review);
      } catch (reason) {
        toast.error(
          accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
            `Couldn't add ${entry.name}`,
        );
        break;
      }
    }
    if (asking.length > 0) setReviews((current) => [...current, ...asking]);
  };

  // Adds people one after another, each with the access picked for them
  // and each save naming the revision the one before it returned, so
  // nobody's change is lost to a conflict.
  const addPeople = async (entries: readonly NewMember[]) => {
    let current = revision;
    for (const { userId, permissions } of entries) {
      try {
        const result = await setGrant({
          companyId,
          id: project.id,
          userId,
          permissions,
          expectedRevision: current,
        }).unwrap();
        current = result.revision;
        saved(current);
      } catch (reason) {
        const failure = reason as Partial<BoardlyApiError>;
        if (failure.code === "project_grant_revision_conflict") reload();
        toast.error(
          accessErrorCopy(failure.code) ??
            `Couldn't add ${person(userId).name}`,
        );
        break;
      }
    }
  };
  const loaded = Boolean(grants.currentData);
  const empty = loaded && stored.length === 0 && groupGrants.length === 0;

  return (
    <section
      aria-label={`Access to ${project.title}`}
      className="project-access"
    >
      <div className="project-access-toolbar">
        <div className="project-access-toolbar__start">
          {toolbarStart}
          {!access.reads && access.management ? (
            <JoinButton companyId={companyId} project={project} />
          ) : null}
        </div>
        {editable ? (
          <ProjectAccessAddPeople
            candidates={candidates}
            disabled={!members.currentData || !loaded}
            giver={access}
            groupTools={
              canManageGroups
                ? {
                    companyId,
                    groups: allGroups,
                    members: members.currentData?.items ?? [],
                    names: allGroupNames,
                  }
                : null
            }
            groups={groupCandidates}
            projectTitle={project.title}
            visibility={project.visibility}
            onAdd={addPeople}
            onAddGroups={(entries) => void addGroups(entries)}
          />
        ) : loaded ? (
          <Tooltip contentClassName={ACCESS_TOOLTIP_CLASS} label="View only">
            <span
              aria-label="View only"
              className="project-access-readonly"
              role="img"
              tabIndex={0}
            >
              <Eye size={15} aria-hidden="true" />
            </span>
          </Tooltip>
        ) : null}
      </div>
      <ProjectEveryoneNotice
        accessRevision={revision}
        companyId={companyId}
        project={project}
        onConfirm={setConfirmation}
        onConflict={reload}
      />
      {grants.isError ? (
        <LoadFailed
          message="Couldn't load access"
          onRetry={() => void grants.refetch()}
        />
      ) : null}
      {!loaded && grants.isFetching ? <AccessLoading /> : null}
      {loaded ? (
        <ul aria-label="Who has access" className="project-access-list">
          {groupGrants.map((grant) => {
            const group = groupById.get(grant.groupId);
            return (
              <ProjectAccessGroupRow
                key={grant.groupId}
                canManageGroups={canManageGroups && Boolean(group)}
                companyId={companyId}
                deleting={checking === grant.groupId}
                editable={editable}
                giver={access}
                grant={grant}
                projectId={project.id}
                projectTitle={project.title}
                visibility={project.visibility}
                onConfirm={setConfirmation}
                onDelete={() =>
                  void askDelete({ id: grant.groupId, name: grant.name })
                }
                onEdit={() => {
                  if (group) setEditingGroup(group);
                }}
                onReview={review}
                onSaved={saved}
              />
            );
          })}
          {stored.map((grant) => (
            <ProjectAccessRow
              key={grant.userId}
              companyId={companyId}
              editable={editable}
              giver={access}
              grant={grant}
              groupName={groupName}
              person={person(grant.userId)}
              projectId={project.id}
              projectTitle={project.title}
              revision={revision}
              visibility={project.visibility}
              onConfirm={setConfirmation}
              onConflict={reload}
              onSaved={saved}
            />
          ))}
          {empty ? <li className="project-access-empty">Nobody yet</li> : null}
        </ul>
      ) : null}
      <ProjectAccessConfirmDialog
        confirmation={confirmation}
        onClose={() => setConfirmation(null)}
      />
      <GroupChangeReviewDialog
        names={reviewNames}
        review={reviews[0] ?? null}
        onClose={() => setReviews((current) => current.slice(1))}
      />
      {editingGroup ? (
        <GroupDialog
          key={editingGroup.id}
          companyId={companyId}
          group={editingGroup}
          groups={allGroups}
          members={members.currentData?.items ?? []}
          names={allGroupNames}
          onClose={() => setEditingGroup(null)}
        />
      ) : null}
    </section>
  );
}

// A manager who cannot read the Project sees its access list and may join
// it, which grants them View and is recorded like any other change. The
// button sits beside the Project picker with an arrow going in; a lock on
// this page only ever means "Beyond your access".
function JoinButton({
  companyId,
  project,
}: {
  companyId: string;
  project: Project;
}) {
  const [join, state] = useJoinProjectMutation();
  return (
    <button
      aria-label={`Join ${project.title}`}
      className="dashboard-button project-access-join"
      disabled={state.isLoading}
      type="button"
      onClick={async () => {
        try {
          await join({
            companyId,
            id: project.id,
            expectedRevision: project.access.revision,
          }).unwrap();
        } catch (reason) {
          toast.error(
            accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
              "Couldn't join",
          );
        }
      }}
    >
      <LogIn size={15} aria-hidden="true" />
      {state.isLoading ? "Joining…" : "Join"}
    </button>
  );
}
