import { Link } from "react-router";
import { Logo } from "@/shared/ui/logo";
import { cx } from "@/shared/lib/cx";

export function PublicSiteHeader({ className }: { className?: string }) {
  return (
    <header
      aria-label="Zenith Team public navigation"
      className={cx(
        "relative z-20 flex w-full items-center justify-between px-10 py-8 max-[760px]:px-5 max-[760px]:py-5",
        className,
      )}
    >
      <Link
        aria-label="Zenith Team home"
        className="rounded-md"
        to="/"
      >
        <Logo alt="Zenith Team" imageClassName="h-6 w-auto" />
      </Link>
    </header>
  );
}
