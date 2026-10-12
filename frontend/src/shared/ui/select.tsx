import {
  Select,
  SelectContent as CoreSelectContent,
  SelectGroup,
  SelectItem as CoreSelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger as CoreSelectTrigger,
  SelectValue,
} from "@/shared/ui/core/select";
import type { ComponentProps } from "react";
import { cx } from "@/shared/lib/cx";

type SelectContentProps = ComponentProps<typeof CoreSelectContent>;
type SelectItemProps = ComponentProps<typeof CoreSelectItem>;
type SelectTriggerProps = ComponentProps<typeof CoreSelectTrigger>;

function SelectTrigger({ className, ...props }: SelectTriggerProps) {
  return (
    <CoreSelectTrigger
      className={cx(
        "boardly-light-surface !h-9 !rounded-[var(--workspace-radius-control,8px)] !border-[var(--workspace-divider-strong,var(--input-border))] !bg-[var(--workspace-input-bg,var(--input-surface))] !px-3 !text-[var(--workspace-native-text,var(--text-primary))] !shadow-none [&_svg]:!text-[var(--workspace-native-text,var(--text-primary))]",
        className,
      )}
      {...props}
    />
  );
}

function SelectContent({ className, ...props }: SelectContentProps) {
  return (
    <CoreSelectContent
      className={cx(
        "boardly-light-overlay !rounded-[var(--workspace-radius-overlay,12px)] !border-[var(--workspace-divider,var(--border-subtle))] !bg-[var(--workspace-overlay-bg,#f8f5ec)] !text-[var(--workspace-native-text,var(--text-primary))] !shadow-[var(--workspace-overlay-shadow,0_18px_46px_rgba(15,35,63,0.18),inset_0_1px_0_rgba(255,255,255,0.78))]",
        className,
      )}
      {...props}
    />
  );
}

function SelectItem({ className, ...props }: SelectItemProps) {
  return (
    <CoreSelectItem
      className={cx(
        "rounded-[var(--workspace-radius-control,8px)] px-2.5 py-1.5 text-[13px] font-normal text-[var(--workspace-native-text,var(--text-primary))] focus:bg-[var(--workspace-hover-bg,var(--state-hover-bg))] focus:text-[var(--workspace-native-text,var(--text-primary))]",
        className,
      )}
      {...props}
    />
  );
}

export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
};
