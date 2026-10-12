import type { ComponentPropsWithoutRef, ReactNode } from "react";

import { cx } from "@/shared/lib/cx";

import "@/features/common/styles/workspace-page-frame.css";
import "@/features/scheduled/styles/scheduled.css";

type ScheduledGlassPanelProps = ComponentPropsWithoutRef<"div"> & {
  children: ReactNode;
  contentClassName?: string;
  density?: "dense" | "regular";
  direction?: "column" | "row";
  selectionState?: "selected" | "unselected";
};

export function ScheduledGlassPanel({
  children,
  className,
  contentClassName,
  density = "regular",
  direction = "column",
  selectionState,
  ...props
}: ScheduledGlassPanelProps) {
  return (
    <div
      {...props}
      data-density={density}
      className={cx(
        "scheduled-panel relative flex rounded-lg min-w-0 overflow-hidden",
        selectionState === "selected" && "scheduled-glass-panel--selected",
        selectionState === "unselected" && "scheduled-glass-panel--unselected",
        direction === "row" ? "flex-row" : "flex-col",
        className,
        contentClassName,
      )}
    >
      {children}
    </div>
  );
}
