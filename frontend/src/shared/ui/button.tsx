import { Button as CoreButton } from "@/shared/ui/core/button";
import type { ComponentProps } from "react";
import { cx } from "@/shared/lib/cx";

type ButtonProps = ComponentProps<typeof CoreButton>;

export function Button({ className, ...props }: ButtonProps) {
  const variant = props.variant ?? "ghost";

  return (
    <CoreButton
      className={cx(
        "rounded-full font-semibold shadow-none",
        variant === "default" &&
          "!bg-brand-primary !text-inverse hover:!bg-brand-hover active:!bg-brand-active",
        variant === "outline" &&
          "!border-brand-primary/30 !text-primary hover:!bg-state-hover-bg",
        variant === "ghost" &&
          "!text-secondary hover:!bg-state-hover-bg hover:!text-primary",
        className,
      )}
      {...props}
    />
  );
}
