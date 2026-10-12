import { cx } from "@/shared/lib/cx";
import type { StatusPillTone } from "@/shared/ui/status-pill";

const toneClassNames: Record<StatusPillTone, string> = {
  active:
    "border-[color-mix(in_srgb,var(--workspace-native-info)_25%,transparent)] bg-[var(--workspace-info-soft)] text-[var(--workspace-native-info)]",
  danger:
    "border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] text-[var(--workspace-native-danger)]",
  draft:
    "border-[color-mix(in_srgb,var(--workspace-native-accent)_25%,transparent)] bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]",
  muted:
    "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]",
  paused:
    "border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]",
  success:
    "border-[color-mix(in_srgb,var(--workspace-native-success)_25%,transparent)] bg-[var(--workspace-success-soft)] text-[var(--workspace-native-success)]",
  warning:
    "border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]",
};

export function ScheduledStatusPill({
  children,
  className,
  tone = "muted",
}: {
  children: string;
  className?: string;
  tone?: StatusPillTone;
}) {
  return (
    <span
      className={cx(
        "scheduled-status-pill inline-flex min-h-6 shrink-0 items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold capitalize",
        toneClassNames[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}
