import { ArrowUpRight, CheckCircle2, Link2Off } from "@/shared/ui/icons";
import { Link } from "react-router";
import type { ReactNode } from "react";
import { PublicLandingBackground } from "@/features/public-site/components/public-landing-background";
import { PublicSiteFooter } from "@/features/public-site/components/public-site-footer";
import { PublicSiteHeader } from "@/features/public-site/components/public-site-header";
import { PublicPageMetadata } from "@/features/public-site/lib/public-page-metadata";
import { ContentPanel } from "@/shared/ui/content-panel";
import "@/features/public-site/styles/public-site.css";

type StripeCallbackKind = "return" | "refresh";

const callbackContent = {
  return: {
    description:
      "Stripe has closed this onboarding session. Return to Zenith Team Desktop, where your account status and any remaining requirements are checked securely.",
    desktopURL: "revenue",
    icon: <CheckCircle2 aria-hidden="true" className="size-7" />,
    note: "You can close this browser tab after Zenith Team opens.",
    title: "Return to Zenith Team",
  },
  refresh: {
    description:
      "For security, Stripe onboarding links are single-use. Return to Zenith Team Desktop and choose Continue to Stripe again to create a fresh link.",
    desktopURL: "revenue?setup=bank",
    icon: <Link2Off aria-hidden="true" className="size-7" />,
    note: "No Stripe account, identity, or bank information is included in this page URL.",
    title: "This Stripe link is no longer active",
  },
} as const satisfies Record<
  StripeCallbackKind,
  {
    description: string;
    desktopURL: string;
    icon: ReactNode;
    note: string;
    title: string;
  }
>;

export function StripeConnectReturnPage() {
  return <StripeConnectCallbackPage kind="return" />;
}

export function StripeConnectRefreshPage() {
  return <StripeConnectCallbackPage kind="refresh" />;
}

function StripeConnectCallbackPage({ kind }: { kind: StripeCallbackKind }) {
  const content = callbackContent[kind];
  const desktopURL = getDesktopURL(content.desktopURL);
  const note = desktopURL
    ? content.note
    : "Zenith Team Desktop is already running. Switch back to its open window to continue.";
  const path = `/connect/stripe/${kind}`;

  return (
    <div className="h-full min-h-0 overflow-x-hidden overflow-y-auto bg-public-site-footer text-white">
      <PublicPageMetadata
        description={content.description}
        path={path}
        robots="noindex, nofollow"
        title={`${content.title} | Zenith Team`}
      />
      <div className="relative flex min-h-[100svh] flex-col overflow-hidden bg-public-site-canvas">
        <PublicLandingBackground />
        <PublicSiteHeader />
        <main className="relative z-10 grid flex-1 place-items-center px-5 py-12 sm:px-10">
          <ContentPanel
            className="w-full max-w-[680px] overflow-hidden rounded-[32px]"
            contentClassName="flex-col items-center px-7 py-12 text-center sm:px-14 sm:py-16"
          >
            <div className="grid size-16 place-items-center rounded-[22px] border border-white/18 bg-white/10 text-[#9f9aff] shadow-[inset_0_1px_0_rgba(255,255,255,0.25)]">
              {content.icon}
            </div>
            <p className="mt-7 text-xs font-semibold uppercase tracking-[0.2em] text-white/52">
              Stripe Connect
            </p>
            <h1 className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-white sm:text-[42px] sm:leading-[1.08]">
              {content.title}
            </h1>
            <p className="mt-5 max-w-[540px] text-pretty text-base leading-7 text-white/66 sm:text-lg">
              {content.description}
            </p>
            {desktopURL ? (
              <a
                className="mt-9 inline-flex min-h-12 items-center justify-center gap-2 rounded-full bg-[#635bff] px-7 text-sm font-semibold text-white shadow-[0_16px_42px_rgba(99,91,255,0.34),inset_0_1px_0_rgba(255,255,255,0.26)] transition hover:bg-[#756eff]"
                href={desktopURL}
              >
                Open Zenith Team Desktop
                <ArrowUpRight aria-hidden="true" className="size-4" />
              </a>
            ) : (
              <div
                className="mt-9 inline-flex min-h-12 items-center justify-center rounded-full border border-white/18 bg-white/10 px-7 text-sm font-semibold text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.16)]"
                role="status"
              >
                Return to the open Zenith Team Desktop window
              </div>
            )}
            <p className="mt-5 text-sm leading-6 text-white/48">{note}</p>
            <Link
              className="mt-7 rounded-sm text-sm font-medium text-white/62 underline decoration-white/24 underline-offset-4 transition hover:text-white"
              to="/"
            >
              Go to the Zenith Team website
            </Link>
          </ContentPanel>
        </main>
      </div>
      <PublicSiteFooter />
    </div>
  );
}

function getDesktopURL(route: string) {
  if (isLoopbackHostname(window.location.hostname)) {
    return null;
  }

  const protocol =
    window.location.hostname === "dev.example.com"
      ? "boardly-dev"
      : "boardly";
  return `${protocol}://${route}`;
}

function isLoopbackHostname(hostname: string) {
  return (
    hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1"
  );
}
