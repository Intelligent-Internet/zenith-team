import { Link } from "react-router";
import { Logo } from "@/shared/ui/logo";
import { ProfileMenu } from "@/shared/ui/profile-menu";

type BrandHeaderProps = {
  onLogout?: () => Promise<void> | void;
  showProfileMenu?: boolean;
};

export function BrandHeader({
  onLogout,
  showProfileMenu = true,
}: BrandHeaderProps) {
  return (
    <header
      className="relative z-10 flex w-full items-center justify-between gap-6"
      aria-label="Zenith Team navigation"
    >
      <Link
        to="/"
        className="inline-flex items-center gap-5 rounded-xl"
        aria-label="Zenith Team home"
      >
        <Logo alt="Zenith Team" />
      </Link>
      {showProfileMenu ? (
        <ProfileMenu onLogout={onLogout} />
      ) : (
        <span aria-hidden="true" />
      )}
    </header>
  );
}
