import {
  CheckCircle2,
  ExternalLink,
  Info,
  LoaderCircle,
  ShieldCheck,
  X,
} from "@/shared/ui/icons";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type RefObject,
} from "react";

import {
  openDesktopExternal,
  shouldUseDesktopHashRouter,
} from "@/shared/desktop/desktop-environment.service";
import { useCommerceOnboardingActions } from "@/features/revenue/api/use-commerce-onboarding-actions";
import { StripeAccountOnboardingBoundary } from "@/features/revenue/components/stripe-account-onboarding-boundary";
import {
  commerceAccountIsReady,
  commerceDashboardLinkIsValid,
  type CommerceAccountSummary,
  type CommerceEnvironment,
  type CommerceTermsState,
} from "@/features/revenue/model/commerce";
import { getApiErrorMessage } from "@/lib/api-client";
import { cx } from "@/shared/lib/cx";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import "@/features/revenue/styles/setup-bank-dialog.css";

const MAX_DESKTOP_POLLING_MS = 10 * 60 * 1_000;

type SetupBankDialogProps = {
  account: CommerceAccountSummary | undefined;
  companyId: string;
  environment: CommerceEnvironment;
  onAcceptTerms: () => Promise<unknown>;
  onOpenChange: (open: boolean) => void;
  onRefreshSummary: () => Promise<unknown>;
  open: boolean;
  terms: CommerceTermsState;
  termsAreSaving: boolean;
};

export function SetupBankDialog({
  account,
  companyId,
  environment,
  onAcceptTerms,
  onOpenChange,
  onRefreshSummary,
  open,
  terms,
  termsAreSaving,
}: SetupBankDialogProps) {
  const [step, setStep] = useState<1 | 2>(1);
  const [accepted, setAccepted] = useState(false);
  const [accountAwaitingSetupCompletion, setAccountAwaitingSetupCompletion] =
    useState<string | null>(null);
  const isAccountReady = commerceAccountIsReady(account);
  const accountKey = `${companyId}:${environment}`;

  useEffect(() => {
    if (
      !open ||
      step !== 2 ||
      accountAwaitingSetupCompletion !== accountKey ||
      !isAccountReady
    ) {
      return;
    }

    toast.success("Stripe account setup is complete.");
    onOpenChange(false);
  }, [
    accountAwaitingSetupCompletion,
    accountKey,
    isAccountReady,
    onOpenChange,
    open,
    step,
  ]);

  useEffect(() => {
    if (!open) {
      setStep(1);
      setAccepted(false);
      setAccountAwaitingSetupCompletion(null);
    }
  }, [open]);

  const continueFromTerms = async () => {
    if (!terms.accepted) {
      if (!accepted) return;
      try {
        await onAcceptTerms();
      } catch (error) {
        toast.error(
          getApiErrorMessage(
            error,
            "The Commerce Addendum could not be accepted.",
          ),
        );
        return;
      }
    }
    setAccountAwaitingSetupCompletion(isAccountReady ? null : accountKey);
    setStep(2);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-describedby="setup-bank-description"
        className="setup-bank-dialog"
        overlayClassName="setup-bank-dialog__overlay"
        showCloseButton={false}
      >
        <header className="setup-bank-dialog__header">
          <div className="flex items-start justify-between gap-4">
            <DialogHeader className="min-w-0 flex-1 text-left">
              <DialogTitle className="setup-bank-dialog__title">
                {step === 1
                  ? "Payment terms"
                  : isAccountReady
                    ? "Manage Stripe account"
                    : "Set up bank details"}
              </DialogTitle>
              <DialogDescription
                className="setup-bank-dialog__description"
                id="setup-bank-description"
              >
                {step === 1
                  ? "Review how payments work before continuing to Stripe."
                  : isAccountReady
                    ? "Manage business and payout details on Stripe's secure surface."
                    : "Stripe securely collects the details needed to receive payouts."}
              </DialogDescription>
            </DialogHeader>
            <button
              aria-label="Close bank setup"
              className="setup-bank-dialog__close"
              onClick={() => onOpenChange(false)}
              type="button"
            >
              <X aria-hidden="true" className="size-4" />
            </button>
          </div>
          <div className="setup-bank-dialog__steps">
            <span className="shrink-0 text-xs font-semibold">
              Step {step} of 2
            </span>
            <div aria-hidden="true" className="setup-bank-dialog__progress">
              <div style={{ width: step === 1 ? "50%" : "100%" }} />
            </div>
          </div>
        </header>

        <div className="setup-bank-dialog__body">
          {step === 1 ? (
            <TermsStep
              accepted={accepted}
              onAcceptedChange={setAccepted}
              terms={terms}
            />
          ) : !account?.configured ? (
            <OnboardingMessage
              description={`The ${environment} Stripe environment has not been configured by a Zenith Team operator.`}
              title="Stripe is not available yet"
              tone="warning"
            />
          ) : shouldUseDesktopHashRouter() ? (
            <DesktopStripeOnboarding
              accountIsReady={isAccountReady}
              companyId={companyId}
              environment={environment}
              onRefreshSummary={onRefreshSummary}
            />
          ) : (
            <EmbeddedStripeOnboarding
              account={account}
              companyId={companyId}
              environment={environment}
              onExit={onRefreshSummary}
              privacyPolicyUrl={terms.privacyUrl}
            />
          )}
        </div>

        <footer className="setup-bank-dialog__footer">
          <Button
            className="setup-bank-dialog__button setup-bank-dialog__button--secondary"
            onClick={() => (step === 1 ? onOpenChange(false) : setStep(1))}
            type="button"
            variant="ghost"
          >
            {step === 1 ? "Cancel" : "Back"}
          </Button>
          {step === 1 ? (
            <Button
              className="setup-bank-dialog__button setup-bank-dialog__button--primary"
              disabled={termsAreSaving || (!terms.accepted && !accepted)}
              onClick={() => void continueFromTerms()}
              type="button"
              variant="default"
            >
              {termsAreSaving ? "Saving…" : "Continue"}
            </Button>
          ) : (
            <Button
              className="setup-bank-dialog__button setup-bank-dialog__button--secondary"
              onClick={() => void onRefreshSummary().catch(() => undefined)}
              type="button"
              variant="outline"
            >
              Refresh status
            </Button>
          )}
        </footer>
      </DialogContent>
    </Dialog>
  );
}

function TermsStep({
  accepted,
  onAcceptedChange,
  terms,
}: {
  accepted: boolean;
  onAcceptedChange: (accepted: boolean) => void;
  terms: CommerceTermsState;
}) {
  const openDocument = async (url: string) => {
    const result = await openDesktopExternal(url);
    if (!result.ok) toast.error(result.reason);
  };

  return (
    <section className="setup-bank-dialog__terms">
      <span className="setup-bank-dialog__icon grid size-11 place-items-center rounded-xl">
        <ShieldCheck aria-hidden="true" className="size-6" />
      </span>
      <h2 className="setup-bank-dialog__section-title mb-0 mt-4 text-lg font-bold">
        Zenith Team Commerce Addendum
      </h2>
      <p className="setup-bank-dialog__muted mb-0 mt-2 text-sm leading-6">
        Your Company is the merchant for payments collected through its
        websites. Stripe processes payments and sends payouts to the bank
        account you provide directly to Stripe.
      </p>

      <div className="mt-6 grid gap-3 sm:grid-cols-2">
        <TermsDocumentButton
          label="Read Commerce Addendum"
          onClick={() => void openDocument(terms.documentUrl)}
        />
        <TermsDocumentButton
          label="Read Privacy Policy"
          onClick={() => void openDocument(terms.privacyUrl)}
        />
      </div>

      {terms.accepted ? (
        <div className="setup-bank-dialog__accepted mt-6 flex items-start gap-3 rounded-xl border p-4 text-sm">
          <CheckCircle2 aria-hidden="true" className="mt-0.5 size-5 shrink-0" />
          <div>
            <strong>Current terms accepted</strong>
            <p className="mb-0 mt-1 text-xs leading-5">
              Version {terms.currentVersion}
              {terms.acceptedAt
                ? ` · ${new Intl.DateTimeFormat(undefined, {
                    dateStyle: "medium",
                  }).format(new Date(terms.acceptedAt))}`
                : ""}
            </p>
          </div>
        </div>
      ) : (
        <label className="setup-bank-dialog__consent mt-6 flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-sm leading-6 transition-colors">
          <input
            checked={accepted}
            className="mt-1 size-4 shrink-0 cursor-pointer"
            onChange={(event) => onAcceptedChange(event.target.checked)}
            type="checkbox"
          />
          <span>
            I have read and accept version {terms.currentVersion} of the Zenith
            Team Commerce Addendum on behalf of this Company.
          </span>
        </label>
      )}
    </section>
  );
}

function TermsDocumentButton({
  label,
  onClick,
}: {
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      className="setup-bank-dialog__document flex items-center justify-between gap-3 rounded-xl border px-4 py-3 text-left text-sm font-semibold transition-colors"
      onClick={onClick}
      type="button"
    >
      {label}
      <ExternalLink
        aria-hidden="true"
        className="setup-bank-dialog__muted size-4 shrink-0"
      />
    </button>
  );
}

function EmbeddedStripeOnboarding({
  account,
  companyId,
  environment,
  onExit,
  privacyPolicyUrl,
}: {
  account: CommerceAccountSummary | undefined;
  companyId: string;
  environment: CommerceEnvironment;
  onExit: () => Promise<unknown>;
  privacyPolicyUrl: string;
}) {
  const { createAccountSession } = useCommerceOnboardingActions(companyId);
  const [bootstrap, setBootstrap] = useState<{
    clientSecret: string;
    environment: CommerceEnvironment;
    publishableKey: string;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const sessionRequestRef = useRef<{
    environment: CommerceEnvironment;
    request: ReturnType<typeof createAccountSession>;
  } | null>(null);

  useEffect(() => {
    if (!account?.configured) return;
    let active = true;
    setError(null);
    const request =
      sessionRequestRef.current?.environment === environment
        ? sessionRequestRef.current.request
        : createAccountSession(environment);
    sessionRequestRef.current = { environment, request };
    void request
      .then((session) => {
        if (active) {
          setBootstrap({
            clientSecret: session.clientSecret,
            environment,
            publishableKey: session.publishableKey,
          });
        }
      })
      .catch((requestError) => {
        if (active) {
          setError(
            getApiErrorMessage(
              requestError,
              "Stripe onboarding could not be started.",
            ),
          );
        }
      });
    return () => {
      active = false;
    };
  }, [account?.configured, createAccountSession, environment]);

  const requestAccountSession = useCallback(
    () => createAccountSession(environment),
    [createAccountSession, environment],
  );

  if (!account?.configured) {
    return (
      <OnboardingMessage
        description="This Stripe environment has not been configured by a Zenith Team operator."
        title="Stripe is not available yet"
        tone="warning"
      />
    );
  }
  if (error) {
    return (
      <OnboardingMessage
        description={error}
        title="Stripe onboarding could not be loaded"
        tone="error"
      />
    );
  }
  if (!bootstrap || bootstrap.environment !== environment) {
    return (
      <OnboardingMessage
        description="Preparing Stripe's secure form…"
        title="Connecting securely"
        tone="neutral"
      />
    );
  }

  return (
    <div className="setup-bank-dialog__stripe-form overflow-hidden rounded-xl border bg-white p-1">
      <StripeAccountOnboardingBoundary
        initialClientSecret={bootstrap.clientSecret}
        key={`${environment}:${bootstrap.publishableKey}`}
        mode={commerceAccountIsReady(account) ? "management" : "onboarding"}
        onExit={() => void onExit().catch(() => undefined)}
        onLoadError={() =>
          setError("Stripe's secure form could not be loaded. Try again later.")
        }
        privacyPolicyUrl={privacyPolicyUrl}
        publishableKey={bootstrap.publishableKey}
        requestAccountSession={requestAccountSession}
      />
    </div>
  );
}

function DesktopStripeOnboarding({
  accountIsReady,
  companyId,
  environment,
  onRefreshSummary,
}: {
  accountIsReady: boolean;
  companyId: string;
  environment: CommerceEnvironment;
  onRefreshSummary: () => Promise<unknown>;
}) {
  const { createDashboardLink, createOnboardingLink, pendingOperation } =
    useCommerceOnboardingActions(companyId);
  const [launched, setLaunched] = useState(false);
  const [pollUntil, setPollUntil] = useState<number | null>(null);
  const refreshSummaryRef = useRef(onRefreshSummary);
  refreshSummaryRef.current = onRefreshSummary;

  useBoundedDesktopPolling(
    launched && !accountIsReady,
    pollUntil,
    refreshSummaryRef,
  );

  const openStripe = async () => {
    try {
      const link = await createOnboardingLink(environment);
      if (!validHostedStripeLink(link.url, link.expiresAt)) {
        toast.error("Stripe returned an invalid or expired onboarding link.");
        return;
      }
      const result = await openDesktopExternal(link.url);
      if (!result.ok) {
        toast.error(result.reason);
        return;
      }
      const expiresAt = Date.parse(link.expiresAt);
      setLaunched(true);
      setPollUntil(
        Math.min(
          Date.now() + MAX_DESKTOP_POLLING_MS,
          Number.isFinite(expiresAt)
            ? expiresAt
            : Date.now() + MAX_DESKTOP_POLLING_MS,
        ),
      );
    } catch (error) {
      toast.error(
        getApiErrorMessage(error, "Stripe onboarding could not be opened."),
      );
    }
  };

  const openStripeDashboard = async () => {
    try {
      const link = await createDashboardLink(environment);
      if (!commerceDashboardLinkIsValid(link.url, environment)) {
        toast.error("Stripe returned an invalid account Dashboard link.");
        return;
      }
      const result = await openDesktopExternal(link.url);
      if (!result.ok) toast.error(result.reason);
    } catch (error) {
      toast.error(
        getApiErrorMessage(error, "Stripe Dashboard could not be opened."),
      );
    }
  };

  return (
    <section className="setup-bank-dialog__onboarding mx-auto grid w-full place-items-center py-8 text-center">
      <span className="setup-bank-dialog__icon grid size-14 place-items-center rounded-2xl">
        <ExternalLink aria-hidden="true" className="size-6" />
      </span>
      <h2 className="setup-bank-dialog__section-title mb-0 mt-4 text-lg font-bold">
        {accountIsReady
          ? "Manage your Stripe account in a browser"
          : "Continue securely in your browser"}
      </h2>
      <p className="setup-bank-dialog__muted mb-0 mt-2 max-w-lg text-sm leading-6">
        {accountIsReady
          ? "Bank details for a ready account are managed in the full Stripe Dashboard. Zenith Team never receives your bank credentials."
          : "Stripe onboarding cannot run inside a desktop webview. Zenith Team will open a one-time Stripe link in your default browser and keep this window updated."}
      </p>
      <button
        className="setup-bank-dialog__button setup-bank-dialog__button--primary mt-6 inline-flex max-w-full items-center justify-center gap-3 px-5 py-3 text-sm font-semibold"
        disabled={
          pendingOperation === "onboarding-link" ||
          pendingOperation === "dashboard-link"
        }
        onClick={() =>
          void (accountIsReady ? openStripeDashboard() : openStripe())
        }
        type="button"
      >
        <StripeWordmark className="h-[18px] w-[43px] shrink-0" />
        <span
          aria-hidden="true"
          className="h-5 w-px shrink-0 bg-current opacity-25"
        />
        <span>
          {accountIsReady
            ? pendingOperation === "dashboard-link"
              ? "Opening Stripe Dashboard…"
              : "Open Stripe Dashboard"
            : pendingOperation === "onboarding-link"
              ? "Creating secure link…"
              : launched
                ? "Open a new Stripe link"
                : "Continue to Stripe"}
        </span>
      </button>
      {!accountIsReady && launched ? (
        <p
          className="setup-bank-dialog__muted mb-0 mt-4 text-xs leading-5"
          role="status"
        >
          Waiting for Stripe. Automatic checks stop when the one-time link
          expires or after ten minutes. You can refresh manually at any time.
        </p>
      ) : null}
    </section>
  );
}

function validHostedStripeLink(url: string, expiresAt: string) {
  if (url.length === 0 || url.length > 4096) return false;
  const expiry = Date.parse(expiresAt);
  if (
    !Number.isFinite(expiry) ||
    expiry <= Date.now() ||
    expiry > Date.now() + 24 * 60 * 60 * 1_000
  ) {
    return false;
  }
  try {
    const parsed = new URL(url);
    return (
      parsed.protocol === "https:" &&
      parsed.hostname === "connect.stripe.com" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

function StripeWordmark({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="none"
      focusable="false"
      viewBox="0 0 360 151"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        clipRule="evenodd"
        d="M360 78.0002C360 52.4002 347.6 32.2002 323.9 32.2002C300.1 32.2002 285.7 52.4002 285.7 77.8002C285.7 107.9 302.7 123.1 327.1 123.1C339 123.1 348 120.4 354.8 116.6V96.6002C348 100 340.2 102.1 330.3 102.1C320.6 102.1 312 98.7002 310.9 86.9002H359.8C359.8 85.6002 360 80.4002 360 78.0002ZM310.6 68.5002C310.6 57.2002 317.5 52.5002 323.8 52.5002C329.9 52.5002 336.4 57.2002 336.4 68.5002H310.6Z"
        fill="currentColor"
        fillRule="evenodd"
      />
      <path
        clipRule="evenodd"
        d="M247.1 32.2002C237.3 32.2002 231 36.8002 227.5 40.0002L226.2 33.8002H204.2V150.4L229.2 145.1L229.3 116.8C232.9 119.4 238.2 123.1 247 123.1C264.9 123.1 281.2 108.7 281.2 77.0002C281.1 48.0002 264.6 32.2002 247.1 32.2002ZM241.1 101.1C235.2 101.1 231.7 99.0002 229.3 96.4002L229.2 59.3002C231.8 56.4002 235.4 54.4002 241.1 54.4002C250.2 54.4002 256.5 64.6002 256.5 77.7002C256.5 91.1002 250.3 101.1 241.1 101.1Z"
        fill="currentColor"
        fillRule="evenodd"
      />
      <path
        clipRule="evenodd"
        d="M169.8 26.3001L194.9 20.9001V0.600098L169.8 5.9001V26.3001Z"
        fill="currentColor"
        fillRule="evenodd"
      />
      <path d="M194.9 33.9001H169.8V121.4H194.9V33.9001Z" fill="currentColor" />
      <path
        clipRule="evenodd"
        d="M142.9 41.3001L141.3 33.9001H119.7V121.4H144.7V62.1001C150.6 54.4001 160.6 55.8001 163.7 56.9001V33.9001C160.5 32.7001 148.8 30.5001 142.9 41.3001Z"
        fill="currentColor"
        fillRule="evenodd"
      />
      <path
        clipRule="evenodd"
        d="M92.8999 12.2002L68.4999 17.4002L68.3999 97.5002C68.3999 112.3 79.4999 123.2 94.2999 123.2C102.5 123.2 108.5 121.7 111.8 119.9V99.6002C108.6 100.9 92.7999 105.5 92.7999 90.7002V55.2002H111.8V33.9002H92.7999L92.8999 12.2002Z"
        fill="currentColor"
        fillRule="evenodd"
      />
      <path
        clipRule="evenodd"
        d="M25.3 59.3002C25.3 55.4002 28.5 53.9002 33.8 53.9002C41.4 53.9002 51 56.2002 58.6 60.3002V36.8002C50.3 33.5002 42.1 32.2002 33.8 32.2002C13.5 32.2002 0 42.8002 0 60.5002C0 88.1002 38 83.7002 38 95.6002C38 100.2 34 101.7 28.4 101.7C20.1 101.7 9.5 98.3002 1.1 93.7002V117.5C10.4 121.5 19.8 123.2 28.4 123.2C49.2 123.2 63.5 112.9 63.5 95.0002C63.4 65.2002 25.3 70.5002 25.3 59.3002Z"
        fill="currentColor"
        fillRule="evenodd"
      />
    </svg>
  );
}

function useBoundedDesktopPolling(
  enabled: boolean,
  pollUntil: number | null,
  refreshSummaryRef: RefObject<() => Promise<unknown>>,
) {
  useEffect(() => {
    if (!enabled || !pollUntil || Date.now() >= pollUntil) return;
    let cancelled = false;
    let timer: number | undefined;
    let attempt = 0;

    const schedule = () => {
      if (cancelled || Date.now() >= pollUntil) return;
      const delay = Math.min(30_000, 10_000 + attempt * 5_000);
      timer = window.setTimeout(
        () => {
          if (cancelled || Date.now() >= pollUntil) return;
          attempt += 1;
          void refreshSummaryRef
            .current?.()
            .catch(() => undefined)
            .finally(schedule);
        },
        Math.min(delay, Math.max(0, pollUntil - Date.now())),
      );
    };

    schedule();
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [enabled, pollUntil, refreshSummaryRef]);
}

function OnboardingMessage({
  description,
  title,
  tone,
}: {
  description: string;
  title: string;
  tone: "error" | "neutral" | "warning";
}) {
  const StatusIcon = tone === "neutral" ? LoaderCircle : Info;
  return (
    <div
      className="setup-bank-dialog__onboarding mx-auto grid min-h-[280px] w-full place-content-center justify-items-center text-center"
      data-tone={tone}
      role={tone === "error" ? "alert" : "status"}
    >
      <StatusIcon
        aria-hidden="true"
        className={cx(
          "setup-bank-dialog__status-icon size-10",
          tone === "neutral" && "animate-spin",
        )}
      />
      <h2 className="setup-bank-dialog__section-title mb-0 mt-4 text-lg font-bold">
        {title}
      </h2>
      <p className="setup-bank-dialog__muted mb-0 mt-2 max-w-md text-sm leading-6">
        {description}
      </p>
    </div>
  );
}
