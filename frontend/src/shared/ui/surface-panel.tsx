import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { cx } from "@/shared/lib/cx";

type SurfacePanelElement = "article" | "aside" | "div" | "section";
type SurfacePanelVariant =
  | "detail"
  | "draft"
  | "nav"
  | "primary"
  | "soft"
  | "subtle";
type SurfacePanelPadding = "none" | "sm" | "md" | "lg";
type SurfacePanelRadius = "lg" | "xl" | "2xl";

type SurfacePanelProps = ComponentPropsWithoutRef<"div"> & {
  as?: SurfacePanelElement;
  children: ReactNode;
  padding?: SurfacePanelPadding;
  radius?: SurfacePanelRadius;
  variant?: SurfacePanelVariant;
};

const variantClassNames: Record<SurfacePanelVariant, string> = {
  detail:
    "border border-[color-mix(in_srgb,var(--brand-primary)_10%,transparent)] bg-[linear-gradient(145deg,color-mix(in_srgb,var(--surface-primary)_84%,white_16%)_0%,color-mix(in_srgb,var(--surface-tertiary)_72%,white_28%)_100%)] shadow-[0_24px_70px_rgba(15,35,63,0.1),inset_0_1px_0_rgba(255,255,255,0.82)] dark:border-white/14 dark:bg-[linear-gradient(145deg,rgba(242,238,226,0.09)_0%,rgba(154,180,224,0.08)_100%)] dark:shadow-[0_24px_80px_rgba(0,0,0,0.34),inset_0_1px_0_rgba(255,255,255,0.2)] dark:backdrop-blur-2xl",
  draft:
    "border border-[rgba(15,35,63,0.08)] bg-[#f8f4ea] shadow-[0_16px_38px_rgba(15,35,63,0.08),inset_0_1px_0_rgba(255,255,255,0.88)]",
  nav: "border border-[color-mix(in_srgb,var(--brand-primary)_10%,transparent)] bg-[color-mix(in_srgb,var(--surface-nav)_72%,transparent)] shadow-[0_18px_52px_rgba(15,35,63,0.08),inset_0_1px_0_rgba(255,255,255,0.76)] backdrop-blur-2xl dark:border-white/12 dark:bg-[rgba(242,238,226,0.045)] dark:shadow-[0_18px_58px_rgba(0,0,0,0.32),inset_0_1px_0_rgba(255,255,255,0.16)]",
  primary:
    "border border-[color-mix(in_srgb,var(--brand-primary)_10%,transparent)] bg-[color-mix(in_srgb,var(--surface-primary)_58%,transparent)] shadow-[0_12px_28px_rgba(15,35,63,0.06),inset_0_1px_0_rgba(255,255,255,0.72)] dark:border-white/10 dark:bg-[rgba(242,238,226,0.04)] dark:shadow-[0_12px_30px_rgba(0,0,0,0.24),inset_0_1px_0_rgba(255,255,255,0.14)]",
  soft: "border border-[color-mix(in_srgb,var(--brand-primary)_8%,transparent)] bg-[color-mix(in_srgb,var(--surface-primary)_48%,transparent)] dark:border-white/10 dark:bg-[rgba(242,238,226,0.035)]",
  subtle:
    "border border-[color-mix(in_srgb,var(--brand-primary)_8%,transparent)] bg-transparent dark:border-white/10",
};

const paddingClassNames: Record<SurfacePanelPadding, string> = {
  lg: "p-5",
  md: "p-4",
  none: "p-0",
  sm: "p-3",
};

const radiusClassNames: Record<SurfacePanelRadius, string> = {
  "2xl": "rounded-3xl",
  lg: "rounded-[14px]",
  xl: "rounded-[18px]",
};

export function SurfacePanel({
  as: Component = "section",
  children,
  className,
  padding = "md",
  radius = "xl",
  variant = "primary",
  ...props
}: SurfacePanelProps) {
  return (
    <Component
      className={cx(
        "min-w-0 text-primary",
        variant === "draft" && "boardly-light-surface",
        radiusClassNames[radius],
        variantClassNames[variant],
        paddingClassNames[padding],
        className,
      )}
      {...props}
    >
      {children}
    </Component>
  );
}
