import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cx } from "@/shared/lib/cx";
import "@/shared/styles/immersive-flow.css";
import "./gradient-cta-button.css";
import { Icon } from "./icon";

type GradientCtaButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  children: ReactNode;
  icon?: ReactNode;
  size?: "auto" | "default";
  hideIcon?: boolean;
};

export function GradientCtaButton({
  children,
  className,
  disabled,
  icon,
  size,
  hideIcon,
  ...props
}: GradientCtaButtonProps) {
  return (
    <button
      {...props}
      disabled={disabled}
      className={cx(
        "workspace-cta-button group inline-flex h-12 max-w-full items-center justify-between gap-3 rounded-[109px] text-base font-semibold transition min-[520px]:w-[391px]",
        disabled
          ? "cursor-not-allowed bg-white/10 text-white/40"
          : "gradient-cta-button text-white hover:brightness-110",
        size === "auto"
          ? "!w-auto rounded-xl text-sm px-6"
          : "w-full px-5 min-[520px]:text-lg min-[520px]:px-8",
        className,
      )}
    >
      <span className="inline-flex min-w-0 items-center gap-3">{children}</span>
      {!hideIcon &&
        (icon ?? (
          <Icon name="arrow-right-2" className="block size-6 shrink-0" />
        ))}
    </button>
  );
}
