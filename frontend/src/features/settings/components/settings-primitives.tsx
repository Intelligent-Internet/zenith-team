import type {
  ButtonHTMLAttributes,
  HTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
} from "react";

import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";
import { Switch, type SwitchProps } from "@/shared/ui/switch";

export function SettingsCard({
  children,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cx(
        "settings-card rounded-[8px] border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)]",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  );
}

export function SettingsSection({
  action,
  children,
  className,
  description,
  eyebrow,
  title,
}: {
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  description?: string;
  eyebrow?: string;
  title: string;
}) {
  return (
    <section className={cx("grid gap-4", className)}>
      <header className="flex min-w-0 flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          {eyebrow ? (
            <p className="mb-2 text-[12px] leading-[18px] font-bold uppercase tracking-[0.18em] text-[var(--workspace-native-accent)]">
              {eyebrow}
            </p>
          ) : null}
          <h2 className="break-words text-[18px] font-semibold leading-[26px] text-[var(--workspace-native-text)]">
            {title}
          </h2>
          {description ? (
            <p className="mt-1.5 max-w-[640px] break-words text-[12px] leading-[18px] text-[var(--workspace-native-text-muted)]">
              {description}
            </p>
          ) : null}
        </div>
        {action ? <div className="shrink-0">{action}</div> : null}
      </header>
      {children}
    </section>
  );
}

export function SettingsRow({
  action,
  children,
  className,
  description,
  disabled = false,
  icon,
  title,
}: {
  action?: ReactNode;
  children?: ReactNode;
  className?: string;
  description?: string;
  disabled?: boolean;
  icon?: string;
  title: string;
}) {
  return (
    <div
      className={cx(
        "flex min-h-[66px] flex-wrap items-center justify-between gap-4 border-b border-[var(--workspace-divider)] px-5 py-3.5 last:border-b-0",
        disabled && "opacity-55",
        className,
      )}
      data-disabled={disabled || undefined}
    >
      <div className="flex min-w-0 items-center gap-3.5">
        {icon ? (
          <span className="grid size-8 shrink-0 place-items-center rounded-[6px] border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] [--fill-0:currentColor] [--stroke-0:currentColor]">
            <Icon className="size-[15px]" name={icon} />
          </span>
        ) : null}
        <div className="min-w-0">
          <p className="break-words text-sm font-semibold leading-5 text-[var(--workspace-native-text)]">
            {title}
          </p>
          {description ? (
            <p className="mt-0.5 max-w-[510px] break-words text-[12px] leading-[18px] text-[var(--workspace-native-text-muted)]">
              {description}
            </p>
          ) : null}
          {children}
        </div>
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  );
}

export function SettingsSelect({
  className,
  children,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="relative inline-flex min-w-[160px] max-w-full items-center">
      <select
        className={cx(
          "h-8 w-full appearance-none rounded-[6px] border border-[var(--workspace-divider-strong)] bg-[var(--workspace-input-bg)] px-3 pr-8 text-sm font-medium text-[var(--workspace-native-text)] outline-none transition enabled:hover:bg-[var(--workspace-hover-bg)] disabled:cursor-not-allowed disabled:opacity-55",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <Icon
        aria-hidden="true"
        className="pointer-events-none absolute right-2.5 size-3 text-[var(--workspace-native-text-muted)]"
        name="arrow-down"
      />
    </span>
  );
}

export function SettingsSwitch({
  className,
  thumbClassName,
  ...props
}: SwitchProps) {
  return (
    <Switch
      className={cx("settings-switch", className)}
      thumbClassName={cx("settings-switch__thumb", thumbClassName)}
      {...props}
    />
  );
}

export function SettingsBadge({
  children,
  className,
  tone = "neutral",
}: {
  children: ReactNode;
  className?: string;
  tone?: "danger" | "info" | "neutral" | "success" | "warning";
}) {
  return (
    <span
      className={cx(
        "inline-flex h-6 items-center rounded-[4px] border px-2 text-[12px] leading-[18px] font-medium",
        tone === "success" &&
          "border-[color-mix(in_srgb,var(--workspace-native-success)_25%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-success)_10%,transparent)] text-[var(--workspace-native-success)]",
        tone === "info" &&
          "border-[color-mix(in_srgb,var(--workspace-native-info)_25%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-info)_10%,transparent)] text-[var(--workspace-native-info)]",
        tone === "warning" &&
          "border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-approval)_10%,transparent)] text-[var(--workspace-native-approval)]",
        tone === "danger" &&
          "border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-danger)_10%,transparent)] text-[var(--workspace-native-danger)]",
        tone === "neutral" &&
          "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function SettingsButton({
  children,
  className,
  variant = "secondary",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "danger" | "primary" | "secondary" | "text";
}) {
  return (
    <button
      className={cx(
        "inline-flex min-h-8 items-center justify-center gap-2 rounded-[6px] px-3 py-1.5 text-sm font-medium transition disabled:cursor-not-allowed disabled:opacity-45 [--fill-0:currentColor] [--stroke-0:currentColor]",
        variant === "secondary" &&
          "border border-[var(--workspace-divider-strong)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text)] enabled:hover:bg-[var(--workspace-hover-bg)]",
        variant === "primary" &&
          "border border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-fill)] text-[var(--workspace-accent-foreground)] enabled:hover:brightness-95",
        variant === "danger" &&
          "border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-danger)_10%,transparent)] text-[var(--workspace-native-danger)] enabled:hover:bg-[color-mix(in_srgb,var(--workspace-native-danger)_16%,transparent)]",
        variant === "text" &&
          "min-h-0 bg-transparent px-1 text-[var(--workspace-native-accent)] enabled:hover:underline",
        className,
      )}
      type="button"
      {...props}
    >
      {children}
    </button>
  );
}

export function SettingsMetricCard({
  detail,
  icon,
  label,
  value,
}: {
  detail?: string;
  icon: string;
  label: string;
  value: string;
}) {
  return (
    <SettingsCard className="min-h-[112px] p-4">
      <div className="flex items-start justify-between gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-[6px] border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-accent)] [--fill-0:currentColor] [--stroke-0:currentColor]">
          <Icon className="size-4" name={icon} />
        </span>
        {detail ? (
          <span className="text-[12px] leading-[18px] font-semibold text-[var(--workspace-native-success)]">
            {detail}
          </span>
        ) : null}
      </div>
      <p className="mt-4 break-words text-[21px] font-semibold tracking-[-0.04em] text-[var(--workspace-native-text)]">
        {value}
      </p>
      <p className="mt-1 text-[12px] leading-[18px] text-[var(--workspace-native-text-muted)]">
        {label}
      </p>
    </SettingsCard>
  );
}

export function SettingsEmptyState({
  description,
  icon = "box-tick",
  title,
}: {
  description: string;
  icon?: string;
  title: string;
}) {
  return (
    <div className="grid min-h-[154px] place-items-center px-6 py-8 text-center">
      <div>
        <span className="mx-auto grid size-9 place-items-center rounded-[12px] border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)] [--fill-0:currentColor] [--stroke-0:currentColor]">
          <Icon className="size-4" name={icon} />
        </span>
        <p className="mt-3 text-[14px] leading-[20px] font-semibold text-[var(--workspace-native-text)]">
          {title}
        </p>
        <p className="mx-auto mt-1 max-w-sm text-[12px] leading-[18px] text-[var(--workspace-native-text-muted)]">
          {description}
        </p>
      </div>
    </div>
  );
}
