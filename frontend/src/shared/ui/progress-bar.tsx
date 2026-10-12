import { cx } from "@/shared/lib/cx";

type ProgressBarProps = {
  className?: string;
  indicatorClassName?: string;
  max?: number;
  value: number;
};

export function ProgressBar({
  className,
  indicatorClassName,
  max = 100,
  value,
}: ProgressBarProps) {
  const normalizedValue =
    max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0;

  return (
    <div
      className={cx(
        "h-2 overflow-hidden rounded-full bg-[color-mix(in_srgb,var(--brand-primary)_10%,transparent)] shadow-[inset_0_1px_0_rgba(255,255,255,0.48)]",
        className,
      )}
      aria-valuemax={max}
      aria-valuemin={0}
      aria-valuenow={value}
      role="progressbar"
    >
      <span
        className={cx(
          "block h-full rounded-full bg-brand-primary transition-[width] duration-700 ease-out",
          indicatorClassName,
        )}
        style={{ width: `${normalizedValue}%` }}
      />
    </div>
  );
}
