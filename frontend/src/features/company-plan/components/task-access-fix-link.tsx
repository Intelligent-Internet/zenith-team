import { Link } from "react-router";
import { teamProjectAccessPath } from "@/features/company-plan/projects/access/team-access-path";
import { taskOffersAccessFix } from "@/features/company-plan/model/task-execution";
import type { TaskDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { Key } from "@/shared/ui/icons";

// The fix for a Task whose assignee may not run tasks in its Project, offered
// to someone who manages that Project's access (ADR 0017): one link to Team ›
// Project access, on that Project, beside the short reason. Nobody else gets
// it, and nobody is told to ask someone.
export function TaskAccessFixLink({
  className,
  task,
}: {
  className?: string;
  task: Pick<TaskDto, "access" | "execution" | "projectId">;
}) {
  if (!taskOffersAccessFix(task)) return null;
  return <ManageAccessLink className={className} projectId={task.projectId} />;
}

// `Key` "Manage access": Team › Project access, on one Project when named.
// The caller shows it only to someone who manages that access.
export function ManageAccessLink({
  className,
  onNavigate,
  projectId,
}: {
  className?: string;
  /** Runs before the move, e.g. to close the dialog it sits in. */
  onNavigate?: () => void;
  projectId?: string;
}) {
  return (
    <Link
      className={cx(
        "inline-flex items-center gap-1 font-medium text-[var(--workspace-native-text)] underline-offset-2 hover:underline",
        className,
      )}
      to={teamProjectAccessPath(projectId)}
      onClick={onNavigate}
    >
      <Key aria-hidden="true" size={13} />
      Manage access
    </Link>
  );
}
