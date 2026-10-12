import { cx } from "@/shared/lib/cx";

export type StatusPillTone =
  | "active"
  | "danger"
  | "draft"
  | "muted"
  | "paused"
  | "success"
  | "warning";

type StatusPillProps = {
  children: string;
  className?: string;
  inverse?: boolean;
  tone?: StatusPillTone;
};

const toneClassNames: Record<StatusPillTone, { base: string; inverse: string }> = {
  active: {
    base: "border-[color-mix(in_srgb,var(--accent-sky)_78%,transparent)] bg-[color-mix(in_srgb,var(--accent-sky)_56%,white_44%)] text-[#0f4c5c] dark:border-[rgba(190,230,237,0.7)] dark:bg-[rgba(190,230,237,0.22)] dark:text-[#d8f8fb]",
    inverse:
      "border-[rgba(190,230,237,0.62)] bg-[rgba(190,230,237,0.24)] text-[#d8f8fb]",
  },
  danger: {
    base: "border-[rgba(205,92,92,0.26)] bg-[rgba(205,92,92,0.12)] text-[#8b2f2f] dark:border-[rgba(255,176,176,0.58)] dark:bg-[rgba(205,92,92,0.24)] dark:text-[#ffd8d8]",
    inverse:
      "border-[rgba(255,176,176,0.52)] bg-[rgba(205,92,92,0.22)] text-[#ffd8d8]",
  },
  draft: {
    base: "border-[rgba(131,161,204,0.38)] bg-[color-mix(in_srgb,var(--accent-periwinkle)_26%,white_74%)] text-[#40547a] dark:border-[rgba(154,180,224,0.62)] dark:bg-[rgba(154,180,224,0.22)] dark:text-[#e7efff]",
    inverse:
      "border-[rgba(154,180,224,0.58)] bg-[rgba(154,180,224,0.24)] text-[#e7efff]",
  },
  muted: {
    base: "border-[color-mix(in_srgb,var(--brand-primary)_18%,transparent)] bg-[color-mix(in_srgb,var(--brand-primary)_8%,white_92%)] text-secondary dark:border-[rgba(242,238,226,0.34)] dark:bg-[rgba(242,238,226,0.12)] dark:text-[rgba(242,238,226,0.82)]",
    inverse:
      "border-[rgba(242,238,226,0.28)] bg-[rgba(242,238,226,0.14)] text-[rgba(242,238,226,0.78)]",
  },
  paused: {
    base: "border-[rgba(209,154,55,0.3)] bg-[rgba(245,190,85,0.18)] text-[#7a4f11] dark:border-[rgba(255,213,128,0.6)] dark:bg-[rgba(245,190,85,0.24)] dark:text-[#ffe6ad]",
    inverse:
      "border-[rgba(255,213,128,0.56)] bg-[rgba(245,190,85,0.22)] text-[#ffe6ad]",
  },
  success: {
    base: "border-[rgba(82,168,111,0.28)] bg-[rgba(82,168,111,0.15)] text-[#23633d] dark:border-[rgba(142,230,171,0.58)] dark:bg-[rgba(82,168,111,0.24)] dark:text-[#d9f8e2]",
    inverse:
      "border-[rgba(142,230,171,0.56)] bg-[rgba(82,168,111,0.24)] text-[#d9f8e2]",
  },
  warning: {
    base: "border-[rgba(209,154,55,0.3)] bg-[rgba(245,190,85,0.18)] text-[#7a4f11] dark:border-[rgba(255,213,128,0.6)] dark:bg-[rgba(245,190,85,0.24)] dark:text-[#ffe6ad]",
    inverse:
      "border-[rgba(255,213,128,0.56)] bg-[rgba(245,190,85,0.22)] text-[#ffe6ad]",
  },
};

export function StatusPill({
  children,
  className,
  inverse = false,
  tone = "muted",
}: StatusPillProps) {
  const toneClassName = toneClassNames[tone];

  return (
    <span
      className={cx(
        "inline-flex h-6 shrink-0 items-center rounded-full border px-2.5 text-[10.5px] font-semibold uppercase tracking-[0.08em] shadow-[inset_0_1px_0_rgba(255,255,255,0.54)] dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.2)]",
        inverse ? toneClassName.inverse : toneClassName.base,
        className,
      )}
    >
      {children}
    </span>
  );
}

export function HealthPill({ children, className }: { children: string; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex h-6 items-center rounded-full bg-[color-mix(in_srgb,var(--accent-sky)_38%,transparent)] px-2.5 text-[10.5px] font-semibold uppercase tracking-[0.08em] text-primary dark:bg-[rgba(190,230,237,0.22)] dark:text-[#f2eee2]",
        className,
      )}
    >
      {children}
    </span>
  );
}
