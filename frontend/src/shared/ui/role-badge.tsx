import { Crown, ShieldCheck, UserRound } from "@/shared/ui/icons";
import { roleLabel, type CompanyRole } from "@/shared/permissions";

const ROLE_ICONS = {
  owner: Crown,
  admin: ShieldCheck,
  member: UserRound,
} as const;

/**
 * A member's role as a compact labelled indicator: icon plus the role's
 * name, tinted by role. One component so the roster, invitations and the
 * invitation landing page never disagree about what a role looks like.
 */
export function RoleBadge({
  role,
  className,
  title,
}: {
  role: CompanyRole | string | null | undefined;
  className?: string;
  title?: string;
}) {
  const resolved: CompanyRole =
    role === "owner" || role === "admin" ? role : "member";
  const Icon = ROLE_ICONS[resolved];
  return (
    <span
      className={[
        "inline-flex items-center gap-[5px] rounded-[4px] p-[3px_7px] [background:var(--workspace-control-bg)] text-workspace-muted text-[length:var(--workspace-text-caption,12px)] leading-[var(--workspace-leading-caption,1.4)] whitespace-nowrap data-[role=owner]:[background:var(--workspace-accent-soft)] data-[role=owner]:text-workspace-accent data-[role=admin]:[background:color-mix(in_srgb,var(--workspace-native-success)_14%,transparent)] data-[role=admin]:text-workspace-success",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
      data-role={resolved}
      title={title}
    >
      <Icon aria-hidden="true" size={13} />
      {roleLabel(resolved)}
    </span>
  );
}
