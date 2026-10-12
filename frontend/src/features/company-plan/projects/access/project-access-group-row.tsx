import { useState } from "react";

import type { GroupChangePreview } from "@/features/agents/api/groups-api";
import type { GroupReview } from "@/features/agents/components/group-change-review";
import { changeAsksFirst } from "@/features/agents/model/group-change";
import {
  PROJECT_VISIBILITY_FLOOR_COPY,
  accessErrorCopy,
} from "@/lib/api-error-copy";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type {
  ProjectPermission,
  ProjectVisibility,
} from "@/shared/permissions";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  MoreHorizontal,
  PencilLine,
  Trash2,
  Users,
  UsersRound,
} from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";

import {
  usePreviewProjectGroupGrantMutation,
  useRemoveProjectGroupGrantMutation,
  useSetProjectGroupGrantMutation,
  type ProjectGroupGrant,
} from "../../api/project-api";
import type { AccessConfirmation } from "./project-access-confirm-dialog";
import {
  inheritedAccess,
  orderedPermissions,
  withinGiver,
  type AccessGiver,
} from "./project-access-model";
import { StaticPermissions } from "./project-access-permission-icons";
import { ProjectAccessPicker } from "./project-access-picker";
import { CapLock } from "./project-access-row";

// The answers that mean the Project's access moved since the preview.
const STALE_CODES = ["project_grant_revision_conflict"] as const;

/** One change to one Group's grant on a Project. */
export type GroupGrantChange = {
  companyId: string;
  groupId: string;
  groupName: string;
  permissions: ProjectPermission[];
  preview: (input: {
    companyId: string;
    id: string;
    groupId: string;
    permissions: ProjectPermission[];
  }) => { unwrap: () => Promise<GroupChangePreview> };
  projectId: string;
  save: (input: {
    companyId: string;
    id: string;
    groupId: string;
    permissions: ProjectPermission[];
    expectedRevision: number;
  }) => { unwrap: () => Promise<{ revision: number }> };
  onSaved: (revision: number) => void;
};

/** Builds the review of a change to one Group's grant on a Project. */
function groupGrantReview({
  companyId,
  groupId,
  groupName,
  permissions,
  preview,
  projectId,
  save,
  onSaved,
}: GroupGrantChange): GroupReview {
  return {
    title: `Change ${groupName}?`,
    scope: "project",
    load: () =>
      preview({ companyId, id: projectId, groupId, permissions }).unwrap(),
    save: async (revision) => {
      const result = await save({
        companyId,
        id: projectId,
        groupId,
        permissions,
        expectedRevision: revision,
      }).unwrap();
      onSaved(result.revision);
    },
    staleCodes: STALE_CODES,
    staleCopy: "Access changed",
  };
}

/**
 * Changes one Group's grant on a Project (Access DESIGN 7.0): reads the
 * server's preview and saves straight away, unless the change asks first
 * (`changeAsksFirst`). Then it saves nothing and returns the review to
 * show, with the preview it read. Throws what the preview or save threw.
 */
export async function changeGroupGrant(
  change: GroupGrantChange,
): Promise<{ review: GroupReview; preview: GroupChangePreview } | null> {
  const review = groupGrantReview(change);
  const preview = await review.load();
  if (changeAsksFirst(preview)) return { review, preview };
  await review.save(preview.revision);
  return null;
}

// One Group with access to the Project (Access DESIGN 7.0): its mark, name
// and size, the same access label and picker as a person, and a menu to
// edit or delete it for whoever manages Groups. A change of its access
// reads the server's preview first and saves straight away unless someone
// loses the Project, a run stops or Projects the viewer can't see move;
// only then it asks "Change {Group}?" with the preview's rows. Remove
// access lives in the picker and asks on the same terms. Under the
// self-grant cap a Group that holds more than the viewer shows only its
// label, with a lock where the menu sits.
export function ProjectAccessGroupRow({
  canManageGroups,
  companyId,
  deleting,
  editable,
  giver,
  grant,
  projectId,
  projectTitle,
  visibility,
  onConfirm,
  onDelete,
  onEdit,
  onReview,
  onSaved,
}: {
  /** The viewer may create, edit and delete Groups (members.manage). */
  canManageGroups: boolean;
  companyId: string;
  /** Its delete is being checked. */
  deleting: boolean;
  /** The viewer holds project.grants.manage here. */
  editable: boolean;
  giver: AccessGiver;
  grant: ProjectGroupGrant;
  projectId: string;
  projectTitle: string;
  visibility: ProjectVisibility;
  onConfirm: (confirmation: AccessConfirmation) => void;
  onDelete: () => void;
  /** Opens the Group dialog. */
  onEdit: () => void;
  onReview: (review: GroupReview) => void;
  onSaved: (revision: number) => void;
}) {
  const [checking, setChecking] = useState(false);
  const [previewGrant] = usePreviewProjectGroupGrantMutation();
  const [setGrant] = useSetProjectGroupGrantMutation();
  const [removeGrant] = useRemoveProjectGroupGrantMutation();
  const granted = orderedPermissions(grant.granted);
  const inherited = inheritedAccess([], () => "", visibility);
  const outranks = editable && !withinGiver(granted, giver);
  const changeable = editable && !outranks;
  const companyVisible = visibility === "company";
  const busy = checking || deleting;
  const people =
    grant.memberCount === 1 ? "1 person" : `${grant.memberCount} people`;

  const change = async (permissions: ProjectPermission[]) => {
    try {
      const asks = await changeGroupGrant({
        companyId,
        groupId: grant.groupId,
        groupName: grant.name,
        permissions,
        preview: previewGrant,
        projectId,
        save: setGrant,
        onSaved,
      });
      if (asks) onReview({ ...asks.review, preview: asks.preview });
      return true;
    } catch (reason) {
      toast.error(
        accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
          "Couldn't change access",
      );
      return false;
    }
  };
  const remove = async () => {
    setChecking(true);
    try {
      const preview = await previewGrant({
        companyId,
        id: projectId,
        groupId: grant.groupId,
        permissions: [],
      }).unwrap();
      const run = async () => {
        try {
          const result = await removeGrant({
            companyId,
            id: projectId,
            groupId: grant.groupId,
            expectedRevision: preview.revision,
          }).unwrap();
          onSaved(result.revision);
        } catch (reason) {
          toast.error(
            accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
              "Couldn't remove group",
          );
        }
      };
      if (!changeAsksFirst(preview)) {
        await run();
        return;
      }
      onConfirm({
        title: `Remove ${grant.name} from ${projectTitle}?`,
        counts: {
          people: preview.peopleAffected,
          runs: preview.runsStopping,
        },
        action: "Remove",
        destructive: true,
        run,
      });
    } catch (reason) {
      toast.error(
        accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
          "Couldn't check change",
      );
    } finally {
      setChecking(false);
    }
  };

  return (
    <li className="project-access-row" data-kind="group">
      <div className="project-access-row__who">
        <span
          aria-hidden="true"
          className="team-person-avatar project-access-row__avatar project-access-group-mark"
        >
          <UsersRound size={17} />
        </span>
        <div className="project-access-row__copy">
          <span className="project-access-row__name">
            <strong title={grant.name}>{grant.name}</strong>
          </span>
          <span
            className="project-access-row__sub project-access-row__count"
            title={people}
          >
            <Users aria-hidden="true" size={12} />
            <span aria-hidden="true">{grant.memberCount}</span>
            <span className="sr-only">{people}</span>
          </span>
        </div>
      </div>
      <div className="project-access-row__perms">
        {changeable ? (
          <ProjectAccessPicker
            disabled={busy}
            giver={giver}
            inherited={inherited}
            mode={{
              kind: "save",
              onSave: change,
              onRemove: () => void remove(),
              removeBlocked: companyVisible
                ? PROJECT_VISIBILITY_FLOOR_COPY
                : null,
            }}
            own={granted}
            subjectName={grant.name}
            visibility={visibility}
          />
        ) : (
          <StaticPermissions
            inherited={inherited}
            own={granted}
            subjectName={grant.name}
          />
        )}
      </div>
      <div className="project-access-row__menu">
        {outranks ? <CapLock /> : null}
        {canManageGroups ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                aria-label={`More options for ${grant.name}`}
                className="team-row-actions"
                disabled={busy}
                title="More options"
                type="button"
              >
                <MoreHorizontal aria-hidden="true" size={18} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={onEdit}>
                <PencilLine size={16} />
                Edit group
              </DropdownMenuItem>
              <DropdownMenuItem
                className="project-access-menu__danger"
                onSelect={onDelete}
              >
                <Trash2 size={16} />
                Delete group
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
      </div>
    </li>
  );
}
