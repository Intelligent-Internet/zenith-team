import type { ComponentType, SVGProps } from "react";
import { cx } from "@/shared/lib/cx";

export interface IconProps extends SVGProps<SVGSVGElement> {
  size?: number | string;
}

export type IconComponent = ComponentType<IconProps>;

export function createIcon(
  Svg: ComponentType<SVGProps<SVGSVGElement>>,
  name: string,
  source: "iconsax" | "symbol" = "iconsax",
): IconComponent {
  function IconsaxIcon({ size = 24, className, ...props }: IconProps) {
    return (
      <Svg
        width={size}
        height={size}
        aria-hidden={
          props["aria-label"] || props["aria-labelledby"] ? undefined : true
        }
        focusable="false"
        {...props}
        className={cx(
          source === "iconsax" ? "iconsax-icon" : "ui-symbol",
          className,
        )}
        data-iconsax={source === "iconsax" ? name : undefined}
        data-ui-symbol={source === "symbol" ? name : undefined}
      />
    );
  }
  IconsaxIcon.displayName = `${source === "iconsax" ? "Iconsax" : "Symbol"}(${name})`;
  return IconsaxIcon;
}
