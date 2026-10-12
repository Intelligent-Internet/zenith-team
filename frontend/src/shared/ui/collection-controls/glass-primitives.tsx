import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";

import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";

export function GlassButton({
  children,
  className,
  icon,
  onClick,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  icon?: string;
}) {
  return (
    <button
      className={cx(
        "inline-flex h-9 min-w-0 items-center justify-center gap-2 rounded-md border border-[var(--workspace-divider-strong)] bg-[var(--workspace-input-bg)] px-3 text-sm text-[var(--workspace-native-text)] transition-colors hover:bg-[var(--workspace-hover-bg)]",
        className,
      )}
      type="button"
      onClick={onClick}
      {...props}
    >
      {icon ? <Icon name={icon} className="size-5 shrink-0" /> : null}
      {children}
    </button>
  );
}

export function GlassPanel({
  children,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement> & {
  children: ReactNode;
}) {
  return (
    <div
      className={cx(
        "collection-glass-panel rounded-xl border border-white/30 bg-white/8",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  );
}
