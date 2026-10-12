import { useId } from "react";

import type { TaskDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { EyeOff } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

import "@/features/company-plan/styles/task-assignee.css";

function outsideProjectHelp(task: Pick<TaskDto, "assignee">) {
  const name = task.assignee.name || "The assignee";
  return `${name} can't see this project. Give them access or hand the task on.`;
}

// An assignee who can no longer see the Task's Project keeps the Task until
// someone hands it over (ADR 0017). The server says so on every Task read and
// frame (`assigneeOutsideProject`); the badge shows that fact as is. It sits
// with the Task's status in a header, or as a bare eye-slash beside the
// assignee on a card or table row. It takes focus, so its help opens on
// hover, keyboard focus and tap.
export function OutsideProjectBadge({
  task,
  compact = false,
}: {
  task: Pick<TaskDto, "assignee" | "assigneeOutsideProject">;
  compact?: boolean;
}) {
  const helpId = useId();
  if (!task.assigneeOutsideProject) return null;
  const help = outsideProjectHelp(task);
  return (
    <>
      <Tooltip label={help}>
        <span
          aria-describedby={helpId}
          aria-label={compact ? "Outside project" : undefined}
          className={cx(
            "task-outside-project-badge",
            compact && "task-outside-project-badge--compact",
          )}
          role={compact ? "img" : undefined}
          tabIndex={0}
          onClick={(event) => event.stopPropagation()}
        >
          <EyeOff size={compact ? 14 : 12} aria-hidden="true" />
          {compact ? null : "Outside project"}
        </span>
      </Tooltip>
      <span className="sr-only" id={helpId}>
        {help}
      </span>
    </>
  );
}
