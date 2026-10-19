import { Link } from "react-router";

const publicFooterLinkClassName =
  "rounded-sm text-sm font-medium text-white/68 transition hover:text-white";

export function PublicSiteFooter() {
  return (
    <footer
      aria-label="Zenith Team public footer"
      className="relative z-10 border-t border-white/10 bg-public-site-footer text-white"
    >
      <div className="mx-auto flex w-full max-w-[1220px] flex-col gap-8 px-10 py-10 max-[760px]:px-5">
        <div className="flex items-start justify-between gap-8 max-[680px]:flex-col">
          <div className="max-w-[430px]">
            <p className="text-base font-semibold tracking-[-0.01em] text-white">
              Zenith Team, a product of ii.inc.
            </p>
            <p className="mt-2 text-sm leading-6 text-white/54">
              Build and direct your AI company from one focused desktop
              workspace.
            </p>
          </div>
          <nav
            aria-label="Legal and support"
            className="flex flex-wrap items-center gap-x-6 gap-y-3"
          >
            <Link className={publicFooterLinkClassName} to="/privacy">
              Privacy Policy
            </Link>
            <Link className={publicFooterLinkClassName} to="/terms">
              Terms of Service
            </Link>
            <a
              className={publicFooterLinkClassName}
              href="mailto:support@ii.inc"
            >
              Support
            </a>
          </nav>
        </div>
        <div className="border-t border-white/8 pt-5 text-xs leading-5 text-white/42">
          © 2026 Intelligent Internet. All rights reserved.
        </div>
      </div>
    </footer>
  );
}
