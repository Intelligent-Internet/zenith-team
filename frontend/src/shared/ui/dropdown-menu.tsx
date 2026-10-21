import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent as CoreDropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem as CoreDropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuPortal,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator as CoreDropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent as CoreDropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/shared/ui/core/dropdown-menu";
import type { ComponentProps } from "react";
import { cx } from "@/shared/lib/cx";

type DropdownMenuContentProps = ComponentProps<typeof CoreDropdownMenuContent>;
type DropdownMenuItemProps = ComponentProps<typeof CoreDropdownMenuItem>;
type DropdownMenuSeparatorProps = ComponentProps<
  typeof CoreDropdownMenuSeparator
>;
type DropdownMenuSubContentProps = ComponentProps<
  typeof CoreDropdownMenuSubContent
>;

const menuContentClassName =
  "zenith-team-light-overlay !rounded-[var(--workspace-radius-overlay,12px)] !border !border-[var(--workspace-divider,var(--border-subtle))] !bg-[var(--workspace-overlay-bg,#f8f5ec)] !p-1 !text-[var(--workspace-native-text,var(--text-primary))] !shadow-[var(--workspace-overlay-shadow,0_18px_46px_rgba(15,35,63,0.18),inset_0_1px_0_rgba(255,255,255,0.78))]";

function DropdownMenuContent({
  className,
  ...props
}: DropdownMenuContentProps) {
  return (
    <CoreDropdownMenuContent
      className={cx(menuContentClassName, className)}
      {...props}
    />
  );
}

function DropdownMenuItem({ className, ...props }: DropdownMenuItemProps) {
  return (
    <CoreDropdownMenuItem
      className={cx(
        "rounded-md px-2.5 py-1.5 text-[13px] font-medium text-[var(--workspace-native-text,var(--text-primary))] focus:bg-[var(--workspace-hover-bg,var(--state-hover-bg))] focus:text-[var(--workspace-native-text,var(--text-primary))]",
        className,
      )}
      {...props}
    />
  );
}

function DropdownMenuSeparator({
  className,
  ...props
}: DropdownMenuSeparatorProps) {
  return (
    <CoreDropdownMenuSeparator
      className={cx(
        "my-1 bg-[var(--workspace-divider,color-mix(in_srgb,var(--brand-primary)_10%,transparent))]",
        className,
      )}
      {...props}
    />
  );
}

function DropdownMenuSubContent({
  className,
  ...props
}: DropdownMenuSubContentProps) {
  return (
    <CoreDropdownMenuSubContent
      className={cx(menuContentClassName, className)}
      {...props}
    />
  );
}

export {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuPortal,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
};
