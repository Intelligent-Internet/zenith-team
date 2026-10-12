import type { ComponentPropsWithoutRef } from "react";
import { cx } from "@/shared/lib/cx";

type GlassDividerProps = ComponentPropsWithoutRef<"div"> & {
  orientation?: "horizontal" | "vertical";
};

export function GlassDivider({
  className,
  orientation = "horizontal",
  ...props
}: GlassDividerProps) {
  const isVertical = orientation === "vertical";

  return (
    <div
      aria-hidden="true"
      className={cx(
        "pointer-events-none shrink-0 opacity-30",
        isVertical
          ? "h-full w-px bg-[linear-gradient(180deg,rgba(255,255,255,0)_0%,#fff_50%,rgba(255,255,255,0)_100%)]"
          : "h-px w-full bg-[linear-gradient(90deg,rgba(255,255,255,0)_0%,#fff_50%,rgba(255,255,255,0)_100%)]",
        className,
      )}
      role="separator"
      {...props}
    />
  );
}
