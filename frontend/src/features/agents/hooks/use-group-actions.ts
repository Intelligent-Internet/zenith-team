import { useMemo, useState } from "react";

import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import {
  useListManagedProjectsQuery,
  useListProjectsQuery,
} from "@/features/company-plan/api/project-api";
import type { AccessConfirmation } from "@/features/company-plan/projects/access/project-access-confirm-dialog";
import { accessErrorCopy } from "@/lib/api-error-copy";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import { useCan } from "@/shared/permissions";
import { toast } from "@/shared/ui/sonner";

import {
  useDeleteGroupMutation,
  usePreviewGroupChangeMutation,
  type GroupSummary,
} from "../api/groups-api";
import { useListActiveCompanyMembersQuery } from "../api/team-directory-api";
import type { GroupReviewNames } from "../components/group-change-review";

/**
 * The Projects and people a Group screen can name: the Projects the viewer
 * reads, and those they manage without reading; the Team's members.
 */
export function useGroupReviewNames(companyId: string): GroupReviewNames {
  const viewerId = useSessionSnapshot().userId;
  const managesProjects = useCan("projects.manage");
  const readable = useListProjectsQuery(companyId);
  const managed = useListManagedProjectsQuery(companyId, {
    skip: !managesProjects,
  });
  const members = useListActiveCompanyMembersQuery(companyId);
  return useMemo(() => {
    const projects = new Map(
      [
        ...(managed.currentData?.items ?? []),
        ...(readable.currentData?.items ?? []),
      ].map((project) => [project.id, project]),
    );
    const people = new Map(
      (members.currentData?.items ?? []).map((member) => [
        member.actor.id,
        member,
      ]),
    );
    return {
      project: (projectId) => {
        const project = projects.get(projectId);
        return project ? { title: project.title } : null;
      },
      person: (userId) => ({
        name: people.get(userId)?.actor.name ?? "Former member",
        avatarUrl: people.get(userId)?.actor.avatarUrl ?? null,
        isSelf: userId === viewerId,
        role: people.get(userId)?.role,
      }),
    };
  }, [
    managed.currentData,
    readable.currentData,
    members.currentData,
    viewerId,
  ]);
}

/**
 * Deleting a Group (Access DESIGN 6.4): a delete can't be undone, so it
 * always asks "Delete {Group}?", with what the server says it would do as
 * counts. It deletes exactly the Group that was previewed (its revision).
 * `checking` is the Group being previewed or deleted, so its menu can wait.
 */
export function useDeleteGroup(
  companyId: string,
  onConfirm: (confirmation: AccessConfirmation) => void,
) {
  const [previewChange] = usePreviewGroupChangeMutation();
  const [deleteGroup] = useDeleteGroupMutation();
  const [checking, setChecking] = useState<string | null>(null);
  const deleteNow = async (groupId: string, revision: number) => {
    try {
      await deleteGroup({
        companyId,
        id: groupId,
        expectedRevision: revision,
      }).unwrap();
    } catch (reason) {
      toast.error(
        accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
          "Couldn't delete group",
      );
    }
  };
  const askDelete = async (group: Pick<GroupSummary, "id" | "name">) => {
    setChecking(group.id);
    try {
      const preview = await previewChange({
        companyId,
        id: group.id,
        addUserIds: [],
        removeUserIds: [],
        delete: true,
      }).unwrap();
      onConfirm({
        title: `Delete ${group.name}?`,
        counts: {
          people: preview.peopleAffected,
          projects: preview.projects.length,
          runs: preview.runsStopping,
          hidden: preview.projectsHidden,
        },
        action: "Delete",
        destructive: true,
        run: () => deleteNow(group.id, preview.revision),
      });
    } catch (reason) {
      toast.error(
        accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
          "Couldn't check change",
      );
    } finally {
      setChecking(null);
    }
  };
  return { askDelete, checking };
}
