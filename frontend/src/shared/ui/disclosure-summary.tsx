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
      className={cx(
        "workspace-disclosure-summary flex min-h-[32px] min-w-0 items-center gap-[8px] rounded-[6px] p-[6px_8px] [list-style:none] font-[500] cursor-pointer select-none [transition:background-color_120ms_ease] [&:is(:hover,:focus-visible)]:[background:var(--workspace-hover-bg,color-mix(in_srgb,currentColor_6%,transparent))]",
        className,
      )}
    >
      <span className="workspace-disclosure-summary__label inline-flex min-w-0 items-center gap-[8px] wrap-anywhere">
        {children}
      </span>
      {count != null && (
        <span className="shrink-0 text-[length:var(--workspace-text-caption,0.85em)] leading-[var(--workspace-leading-caption,1.4)] font-[400] tabular-nums text-[var(--workspace-native-text-muted,color-mix(in_srgb,currentColor_75%,transparent))]">
          {count}
        </span>
      )}
      <ChevronRight
        aria-hidden="true"
        className="workspace-disclosure-summary__chevron shrink-0 opacity-100 [transition:transform_160ms_ease,opacity_120ms_ease]"
        size={14}
      />
    </summary>
  );
}
