import { Input as CoreInput } from "@/shared/ui/core/input";
import type { ComponentProps } from "react";
import { cx } from "@/shared/lib/cx";

type InputProps = ComponentProps<typeof CoreInput>;

export function Input({ className, ...props }: InputProps) {
  return (
    <CoreInput
      className={cx(
        "h-10 rounded-xl border border-input bg-input px-3 text-sm font-medium text-input shadow-[inset_0_1px_0_rgba(255,255,255,0.55)] placeholder:text-input-placeholder focus-visible:border-accent-periwinkle",
        className,
      )}
      {...props}
    />
  );
}

