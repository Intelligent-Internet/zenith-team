import { accessErrorCopy } from "@/lib/api-error-copy";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import { useProjectAccess } from "@/shared/permissions";
import { Building2 } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";

import {
  useSetProjectVisibilityMutation,
  type Project,
} from "../../api/project-api";
import type { AccessConfirmation } from "./project-access-confirm-dialog";

// A Project shows only the people added to it; opening one to the whole
// Company is not offered (owner decision 2026-10-02). A Project that is
// already open to everyone says so in one line above the list ("Open to
// everyone"), and whoever may change it can limit it to the people added
// ("Limit access", after a short confirm). That is an access change, so it
// names the access revision it was decided on; a stale one reloads.
export function ProjectEveryoneNotice({
  accessRevision,
  companyId,
  project,
  onConfirm,
  onConflict,
}: {
  /** The newest access revision the panel knows, saves included. */
  accessRevision: number;
  companyId: string;
  project: Project;
  onConfirm: (confirmation: AccessConfirmation) => void;
  onConflict: () => void;
}) {
  const access = useProjectAccess(project);
  const [setVisibility] = useSetProjectVisibilityMutation();
  if (project.visibility !== "company") return null;
  const canLimit =
    access.can("project.visibility.update") && !project.archivedAt;

  const limit = async () => {
    try {
      await setVisibility({
        companyId,
        id: project.id,
        visibility: "restricted",
        expectedRevision: project.revision,
        expectedAccessRevision: accessRevision,
      }).unwrap();
    } catch (reason) {
      const failure = reason as Partial<BoardlyApiError>;
      if (
        failure.code === "project_grant_revision_conflict" ||
        failure.code === "project_conflict"
      ) {
        onConflict();
      }
      toast.error(accessErrorCopy(failure.code) ?? "Couldn't change access");
    }
  };
  return (
    <p className="project-access-everyone flex flex-wrap items-center gap-[8px] m-0 p-[8px_12px] [border:1px_solid_var(--workspace-divider)] rounded-[10px] text-workspace-muted text-[length:12px] leading-[1.4]">
      <Building2 aria-hidden="true" size={14} />
      <span>Open to everyone</span>
      {canLimit ? (
        <button
          type="button"
          onClick={() =>
            onConfirm({
              title: "Limit access?",
              action: "Limit",
              destructive: true,
              run: limit,
            })
          }
        >
          Limit access
        </button>
      ) : null}
    </p>
  );
}
