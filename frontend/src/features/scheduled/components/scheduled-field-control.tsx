import type { ReactNode } from "react";

import { cx } from "@/shared/lib/cx";

import "@/features/scheduled/styles/scheduled.css";

type ScheduledFieldControlProps = {
  children: ReactNode;
  className?: string;
  compact?: boolean;
  contentClassName?: string;
  icon?: ReactNode;
  multiline?: boolean;
  trailing?: ReactNode;
};

export function ScheduledFieldControl({
  children,
  className,
  compact = false,
  contentClassName,
  icon,
  multiline = false,
  trailing,
}: ScheduledFieldControlProps) {
  return (
    <div
      className={cx(
        "scheduled-control w-full overflow-hidden rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-input-bg)] text-[var(--workspace-native-text)] transition-colors focus-within:border-[var(--workspace-native-accent)]",
        multiline ? "min-h-36" : "h-9",
        className,
      )}
    >
      <div
        className={cx(
          "scheduled-control-content flex h-full w-full",
          multiline
            ? "items-start gap-3 px-3 py-3"
            : compact
              ? "items-center gap-2 px-3"
              : "items-center gap-2 px-3",
          contentClassName,
        )}
      >
        {icon ? (
          <span
            className={cx(
              "grid shrink-0 place-items-center text-[var(--workspace-native-text-muted)]",
              compact ? "size-4" : "size-5",
            )}
            aria-hidden="true"
          >
            {icon}
          </span>
        ) : null}
        {children}
        {trailing ? (
          <span
            className="grid size-4 shrink-0 place-items-center text-[var(--workspace-native-text-muted)]"
            aria-hidden="true"
          >
            {trailing}
          </span>
        ) : null}
      </div>
    </div>
  );
}

export const scheduledNativeControlClassName =
  "min-w-0 flex-1 border-0 bg-transparent p-0 text-sm leading-5 text-[var(--workspace-native-text)] caret-[var(--workspace-native-accent)] outline-none ring-0 [color-scheme:inherit] placeholder:text-[var(--workspace-native-text-faint)] disabled:cursor-not-allowed disabled:text-[var(--workspace-native-text-faint)]";
