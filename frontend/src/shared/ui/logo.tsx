import type { ReactNode, SVGProps } from "react";
import { Icon } from "./icon";

type LogoProps = {
  className?: string;
  imageClassName?: string;
  label?: ReactNode;
  labelClassName?: string;
  labelWrapperClassName?: string;
  showBeta?: boolean;
  betaLabel?: ReactNode;
  betaClassName?: string;
  alt?: string;
  showIconWhenCollapsed?: boolean;
} & Omit<SVGProps<SVGSVGElement>, "className" | "children">;

function cn(...classes: Array<string | false | null | undefined>) {
  return classes.filter(Boolean).join(" ");
}

export function Logo({
  className,
  imageClassName,
  label,
  labelClassName,
  labelWrapperClassName,
  showBeta = false,
  betaLabel = "BETA",
  betaClassName,
  alt = "Zenith Team",
  showIconWhenCollapsed: _showIconWhenCollapsed,
}: LogoProps) {
  return (
    <div
      aria-label={alt}
      className={cn("relative flex items-center gap-x-2", className)}
      role="img"
    >
      <Icon name="logo" className={`w-auto h-6 ${imageClassName}`} />

      {showBeta || labelWrapperClassName ? (
        <div className={labelWrapperClassName}>
          {label ? <span className={cn(labelClassName)}>{label}</span> : null}
          {showBeta ? (
            <span
              className={cn(
                "absolute -right-8 -top-1 text-[10px]",
                betaClassName,
              )}
            >
              {betaLabel}
            </span>
          ) : null}
        </div>
      ) : label ? (
        <span className={cn(labelClassName)}>{label}</span>
      ) : null}
    </div>
  );
}
