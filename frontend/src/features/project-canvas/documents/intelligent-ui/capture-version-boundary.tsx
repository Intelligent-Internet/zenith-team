import type { ReactNode } from "react";
import { supportsCaptureVersions } from "./versions";

export function CaptureVersionBoundary({
  evaluations,
  children,
}: {
  evaluations: unknown;
  children: ReactNode;
}) {
  if (!supportsCaptureVersions(evaluations))
    return (
      <p
        role="status"
        className="rounded-workspace-control border border-workspace-divider p-4 text-sm text-workspace-muted"
      >
        This capture uses a format this version of Zenith Team cannot display.
        Update Zenith Team to review it.
      </p>
    );
  return children;
}
