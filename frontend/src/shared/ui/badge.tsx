import { Badge as CoreBadge } from "@/shared/ui/core/badge";
import type { ComponentProps } from "react";
import { cx } from "@/shared/lib/cx";

type BadgeProps = ComponentProps<typeof CoreBadge>;

export function Badge({ className, ...props }: BadgeProps) {
  return (
    <CoreBadge
      className={cx(
        "rounded-full border border-brand-primary/10 bg-accent-sky/14 px-2.5 py-1 text-xs font-semibold leading-none text-brand-primary shadow-[inset_0_1px_0_rgba(255,255,255,0.48)]",
        className,
      )}
      {...props}
    />
  );
}
