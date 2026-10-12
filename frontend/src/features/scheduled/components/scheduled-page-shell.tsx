import type { ReactNode } from "react";

import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import "@/features/scheduled/styles/scheduled.css";

export function ScheduledPageShell({
  actions,
  children,
  description,
  title = "Schedules",
}: {
  actions?: ReactNode;
  children: ReactNode;
  description?: ReactNode;
  title?: ReactNode;
}) {
  return (
    <WorkspacePageFrame
      actions={actions}
      ariaLabel="Schedules"
      className="scheduled-page"
      contentClassName="scheduled-page-content"
      contentWidth="contained"
      description={description}
      title={title}
    >
      {children}
    </WorkspacePageFrame>
  );
}
