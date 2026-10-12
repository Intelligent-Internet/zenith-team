import type { ReactNode } from "react";

import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";

export function AssetsWorkspaceShell({
  children,
  breadcrumb,
  title = "Assets",
  description = "Files from your Company workspace.",
}: {
  children: ReactNode;
  breadcrumb?: ReactNode;
  title?: string;
  description?: string;
}) {
  return (
    <WorkspacePageFrame
      ariaLabel="Workspace Assets"
      className="assets-workspace-frame collection-page"
      breadcrumb={breadcrumb}
      title={title}
      description={description}
      contentClassName="!overflow-hidden"
      contentWidth="contained"
    >
      {children}
    </WorkspacePageFrame>
  );
}
