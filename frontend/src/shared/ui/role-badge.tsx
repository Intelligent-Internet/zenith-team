import { Crown, ShieldCheck, UserRound } from "@/shared/ui/icons";
import { roleLabel, type CompanyRole } from "@/shared/permissions";
import "./role-badge.css";

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
      className={["team-role-badge", className].filter(Boolean).join(" ")}
      data-role={resolved}
      title={title}
    >
      <Icon aria-hidden="true" size={13} />
      {roleLabel(resolved)}
    </span>
  );
}
