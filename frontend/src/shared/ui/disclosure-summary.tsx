import type { ComponentProps } from "react";

import { cx } from "@/shared/lib/cx";
import { ChevronRight } from "@/shared/ui/icons";
import "./disclosure-summary.css";

type DisclosureSummaryProps = ComponentProps<"summary"> & {
  count?: number | string;
};

export function DisclosureSummary({
  children,
  className,
  count,
  ...props
}: DisclosureSummaryProps) {
  return (
    <summary
      {...props}
      className={cx("workspace-disclosure-summary", className)}
    >
      <span className="workspace-disclosure-summary__label">{children}</span>
      {count != null && (
        <span className="workspace-disclosure-summary__count">{count}</span>
      )}
      <ChevronRight
        aria-hidden="true"
        className="workspace-disclosure-summary__chevron"
        size={14}
      />
    </summary>
  );
}
