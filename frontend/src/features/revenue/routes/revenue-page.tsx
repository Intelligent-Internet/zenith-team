import { useGetWorkspaceCompanyQuery } from "@/shared/api/company-api";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { useLocation, useNavigate } from "react-router";

import {
  openDesktopExternal,
  shouldUseDesktopHashRouter,
} from "@/shared/desktop/desktop-environment.service";
import {
  loginWithPassword,
  readSessionSnapshot,
} from "@/shared/session/session";
import { hasPermission } from "@/shared/permissions";
import {
  clearAuthContinuation,
  writeAuthContinuation,
} from "@/features/auth/model/auth-continuation";
import { useGetApprovalQuery } from "@/features/approvals/api/approvals-api";
import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import {
  useAcceptCommerceTermsMutation,
  useApproveCommerceManifestMutation,
  useGetCommerceManifestsInfiniteQuery,
  useGetCommerceSummaryQuery,
} from "@/features/revenue/api/commerce-api";
import { useCommerceOnboardingActions } from "@/features/revenue/api/use-commerce-onboarding-actions";
import { OwnerReauthenticationDialog } from "@/features/revenue/components/owner-reauthentication-dialog";
import { SetupBankDialog } from "@/features/revenue/components/setup-bank-dialog";
import {
  commerceAccountIsReady,
  commerceAccountForEnvironment,
  commerceDashboardLinkIsValid,
  type CommerceAccountStatus,
  type CommerceAccountSummary,
  type CommerceEnvironment,
  type CommerceManifestOffer,
  type CommerceManifestReview,
} from "@/features/revenue/model/commerce";
import {
  clearPendingCommerceApproval,
  readPendingCommerceApproval,
  writePendingCommerceApproval,
} from "@/features/revenue/model/commerce-approval-continuation";
import {
  commerceApprovalConversationHref,
  commerceManifestReviewHref,
  commerceManifestViewHref,
  parseCommerceApprovalRoute,
  type CommerceApprovalReturnContext,
  type CommerceApprovalRoute,
} from "@/features/revenue/model/commerce-approval-route";
import { getApiErrorMessage } from "@/lib/api-client";
import { authService } from "@/shared/api/auth.service";
import { beginDesktopGoogleOAuthRedirect } from "@/shared/api/google-oauth";
import type { ApprovalDto } from "@/shared/api/types";

import { cx } from "@/shared/lib/cx";
import { Button } from "@/shared/ui/core/button";
import { Icon } from "@/shared/ui/icon";
import { toast } from "@/shared/ui/sonner";

const accountStatusCopy: Record<
  CommerceAccountStatus,
  { description: string; label: string; tone: string }
> = {
  action_required: {
    description: "Stripe needs more information before payments can continue.",
    label: "Action required",
    tone: "bg-[#fff4e5] text-[#935f13]",
  },
  disconnected: {
    description: "This Stripe account is no longer connected.",
    label: "Disconnected",
    tone: "bg-[#f2f3f5] text-[#69707c]",
  },
  draining: {
    description: "New payments are paused while existing obligations finish.",
    label: "Disconnecting",
    tone: "bg-[#fff4e5] text-[#935f13]",
  },
  not_started: {
    description:
      "Connect a Stripe account and bank account to receive payouts.",
    label: "Not set up",
    tone: "bg-[#f2f3f5] text-[#69707c]",
  },
  onboarding: {
    description: "Finish the remaining details in Stripe's secure form.",
    label: "Setup incomplete",
    tone: "bg-[#eef1ff] text-[#5750c7]",
  },
  pending_review: {
    description: "Stripe is reviewing the submitted account details.",
    label: "Pending review",
    tone: "bg-[#eef1ff] text-[#5750c7]",
  },
  provisioning: {
    description: "Zenith Team is creating the connected Stripe account.",
    label: "Starting setup",
    tone: "bg-[#eef1ff] text-[#5750c7]",
  },
  ready: {
    description: "Payments and payouts are enabled for this environment.",
    label: "Ready",
    tone: "bg-[#eaf7ee] text-[#287044]",
  },
  restricted: {
    description: "Payments are restricted. Review the requirements in Stripe.",
    label: "Restricted",
    tone: "bg-[#fdecec] text-[#a23b32]",
  },
};

const visibleCommerceEnvironments: readonly CommerceEnvironment[] = import.meta
  .env.PROD
  ? ["live"]
  : ["sandbox", "live"];

type CommerceApprovalGate =
  "checking" | "not_required" | "ready" | "stale" | "unavailable";

const staleCommerceApprovalMessage =
  "This approval is no longer pending or no longer matches this payment proposal. Return to the task chat for the latest request.";
const localSigningKeyCommand =
  "zen commerce dev --manifest boardly-commerce.json -- npm run dev";
const localSigningKeyRequiredMessage = `This local Site has no publishable signing key. Return to the Task chat that authored this manifest, run \`${localSigningKeyCommand}\` there, then refresh and publish again.`;

export function RevenuePage() {
  const session = readSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const location = useLocation();
  const navigate = useNavigate();
  const approvalRoute = useMemo(
    () => parseCommerceApprovalRoute(location.search),
    [location.search],
  );
  const pendingApprovalRef = useRef(readPendingCommerceApproval());
  const approvalContext =
    approvalRoute.returnContext ??
    pendingApprovalRef.current?.returnContext ??
    null;
  const [environment, setEnvironment] = useState<CommerceEnvironment>(
    import.meta.env.PROD ? "live" : "sandbox",
  );
  const [setupOpen, setSetupOpen] = useState(
    () => new URLSearchParams(location.search).get("setup") === "bank",
  );
  const companyQuery = useGetWorkspaceCompanyQuery(companyId, {
    skip: !companyId,
  });
  const company = companyQuery.currentData;
  // commerce.read opens the page; commerce.manage (owners) unlocks terms,
  // payout onboarding and the Stripe dashboard.
  const canReadCommerce = hasPermission(company, "commerce.read");
  const canManageCommerce = hasPermission(company, "commerce.manage");
  const summaryQuery = useGetCommerceSummaryQuery(companyId, {
    refetchOnFocus: true,
    refetchOnMountOrArgChange: 30,
    skip: !companyId || !canReadCommerce,
  });
  const [acceptTerms, acceptTermsState] = useAcceptCommerceTermsMutation();
  const { createDashboardLink, pendingOperation } =
    useCommerceOnboardingActions(companyId);
  const manifestsQuery = useGetCommerceManifestsInfiniteQuery(companyId, {
    refetchOnFocus: true,
    refetchOnMountOrArgChange: 30,
    skip: !companyId || !canReadCommerce,
  });
  const approvalQuery = useGetApprovalQuery(
    {
      approvalId: approvalContext?.approvalId ?? "",
      companyId,
    },
    { skip: !companyId || !approvalContext },
  );
  const refetchApproval = approvalQuery.refetch;
  const [approveManifest] = useApproveCommerceManifestMutation();
  const [publishingManifestId, setPublishingManifestId] = useState<
    string | null
  >(null);
  const [manifestActionError, setManifestActionError] = useState<string | null>(
    null,
  );
  const [reauthenticationManifest, setReauthenticationManifest] =
    useState<CommerceManifestReview | null>(null);
  const [reauthenticationError, setReauthenticationError] = useState<
    string | null
  >(null);
  const [reauthenticating, setReauthenticating] = useState(false);
  const targetManifestElementRef = useRef<HTMLElement | null>(null);
  const focusedManifestIdRef = useRef<string | null>(null);
  const summary = summaryQuery.currentData;
  const allManifests = useMemo(
    () =>
      manifestsQuery.currentData?.pages.flatMap((page) => page.manifests) ?? [],
    [manifestsQuery.currentData],
  );
  const refetchManifests = manifestsQuery.refetch;
  const selectedAccount = commerceAccountForEnvironment(summary, environment);
  const targetManifest = approvalRoute.manifestId
    ? (allManifests.find(
        (manifest) => manifest.manifestId === approvalRoute.manifestId,
      ) ?? null)
    : null;
  const manifests = allManifests.filter(
    (manifest) => manifest.environment === environment,
  );
  const pendingManifestId = pendingApprovalRef.current?.manifestId ?? null;
  const hasUnresolvedRequiredManifest = [
    approvalRoute.manifestId,
    pendingManifestId,
  ].some(
    (manifestId) =>
      manifestId !== null &&
      !allManifests.some((manifest) => manifest.manifestId === manifestId),
  );
  const isSearchingManifestHistory =
    hasUnresolvedRequiredManifest &&
    (manifestsQuery.isLoading ||
      manifestsQuery.isFetchingNextPage ||
      manifestsQuery.hasNextPage);
  const initializedEnvironmentForCompanyRef = useRef<string | null>(null);
  const targetApprovalGate = commerceApprovalGate(
    approvalRoute,
    targetManifest,
    approvalQuery.currentData,
    approvalQuery.isLoading ||
      approvalQuery.isFetching ||
      manifestsQuery.isLoading ||
      isSearchingManifestHistory ||
      manifestsQuery.isError,
    approvalQuery.isError,
  );
  const returnContextForManifest = useCallback(
    (manifest: CommerceManifestReview) =>
      approvalRoute.manifestId === manifest.manifestId &&
      approvalRoute.returnContext?.taskId === manifest.taskId
        ? approvalRoute.returnContext
        : null,
    [approvalRoute],
  );
  const verifyApprovalBeforePublish = useCallback(
    async (
      manifest: CommerceManifestReview,
      returnContext: CommerceApprovalReturnContext | null,
    ) => {
      if (!returnContext) return true;
      if (
        approvalContext?.approvalId !== returnContext.approvalId ||
        approvalRoute.manifestId !== manifest.manifestId ||
        returnContext.taskId !== manifest.taskId
      ) {
        setManifestActionError(staleCommerceApprovalMessage);
        return false;
      }
      try {
        const approval = await refetchApproval().unwrap();
        if (!commerceApprovalAllowsPublish(approval, manifest, returnContext)) {
          setManifestActionError(staleCommerceApprovalMessage);
          return false;
        }
        return true;
      } catch {
        setManifestActionError(
          "Zenith Team could not verify this payment approval. Return to the task chat and try again.",
        );
        return false;
      }
    },
    [approvalContext?.approvalId, approvalRoute.manifestId, refetchApproval],
  );

  useEffect(() => {
    if (
      !hasUnresolvedRequiredManifest ||
      !manifestsQuery.currentData ||
      !manifestsQuery.hasNextPage ||
      manifestsQuery.isFetchingNextPage ||
      manifestsQuery.isError
    ) {
      return;
    }
    void manifestsQuery.fetchNextPage();
  }, [
    hasUnresolvedRequiredManifest,
    manifestsQuery.currentData,
    manifestsQuery.fetchNextPage,
    manifestsQuery.hasNextPage,
    manifestsQuery.isError,
    manifestsQuery.isFetchingNextPage,
  ]);

  useEffect(() => {
    if (new URLSearchParams(location.search).get("setup") === "bank") {
      setSetupOpen(true);
    }
  }, [location.search]);

  useEffect(() => {
    if (targetManifest) {
      setEnvironment(targetManifest.environment);
      return;
    }
    if (
      !summary ||
      initializedEnvironmentForCompanyRef.current === summary.companyId
    ) {
      return;
    }
    initializedEnvironmentForCompanyRef.current = summary.companyId;
    if (import.meta.env.PROD) {
      setEnvironment("live");
      return;
    }
    const liveAccount = commerceAccountForEnvironment(summary, "live");
    setEnvironment(liveAccount?.configured ? "live" : "sandbox");
  }, [summary, targetManifest]);

  useEffect(() => {
    focusedManifestIdRef.current = null;
  }, [approvalRoute.manifestId]);

  useEffect(() => {
    if (
      !targetManifest ||
      targetManifest.environment !== environment ||
      !summary ||
      manifestsQuery.isLoading
    ) {
      return;
    }

    const frame = window.requestAnimationFrame(() => {
      const element = targetManifestElementRef.current;
      if (
        !element ||
        focusedManifestIdRef.current === targetManifest.manifestId
      ) {
        return;
      }
      focusedManifestIdRef.current = targetManifest.manifestId;
      element.scrollIntoView({ behavior: "smooth", block: "center" });
      element.focus({ preventScroll: true });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [environment, manifestsQuery.isLoading, summary, targetManifest]);

  const closeSetup = (open: boolean) => {
    setSetupOpen(open);
    if (!open && summary) {
      void summaryQuery.refetch();
    }
    if (!open && new URLSearchParams(location.search).has("setup")) {
      const search = new URLSearchParams(location.search);
      search.delete("setup");
      navigate(`/revenue${search.size ? `?${search.toString()}` : ""}`, {
        replace: true,
      });
    }
  };

  const openStripeDashboard = async () => {
    const isDesktop = shouldUseDesktopHashRouter();
    const pendingWindow = isDesktop
      ? null
      : window.open("about:blank", "_blank");
    if (pendingWindow) pendingWindow.opener = null;

    try {
      const link = await createDashboardLink(environment);
      if (!commerceDashboardLinkIsValid(link.url, environment)) {
        pendingWindow?.close();
        toast.error("Stripe returned an invalid account Dashboard link.");
        return;
      }
      if (pendingWindow) {
        pendingWindow.location.replace(link.url);
        return;
      }
      if (!isDesktop) {
        window.location.assign(link.url);
        return;
      }
      const result = await openDesktopExternal(link.url);
      if (!result.ok) toast.error(result.reason);
    } catch (error) {
      pendingWindow?.close();
      toast.error(
        getApiErrorMessage(error, "Stripe Dashboard could not be opened."),
      );
    }
  };

  const publishManifest = useCallback(
    async (
      manifest: CommerceManifestReview,
      allowReauthentication: boolean,
      returnContext: CommerceApprovalReturnContext | null,
    ) => {
      if (!(await verifyApprovalBeforePublish(manifest, returnContext))) {
        return false;
      }
      setPublishingManifestId(manifest.manifestId);
      setManifestActionError(null);
      try {
        await approveManifest({
          companyId,
          manifestId: manifest.manifestId,
          request: { digest: manifest.digest, schemaVersion: 1 },
        }).unwrap();
        if (returnContext) {
          navigate(commerceApprovalConversationHref(returnContext), {
            replace: true,
          });
        } else {
          await refetchManifests();
        }
        return true;
      } catch (error) {
        if (
          allowReauthentication &&
          commerceManifestErrorCode(error) === "owner_reauthentication_required"
        ) {
          setReauthenticationError(null);
          setReauthenticationManifest(manifest);
        } else {
          setManifestActionError(commerceManifestErrorMessage(error, manifest));
        }
        void refetchManifests();
        return false;
      } finally {
        setPublishingManifestId(null);
      }
    },
    [
      approveManifest,
      companyId,
      navigate,
      refetchManifests,
      verifyApprovalBeforePublish,
    ],
  );

  const approveAndPublish = useCallback(
    async (manifest: CommerceManifestReview) => {
      const returnContext = returnContextForManifest(manifest);
      if (
        approvalRoute.manifestId === manifest.manifestId &&
        (!returnContext || targetApprovalGate !== "ready")
      ) {
        setManifestActionError(staleCommerceApprovalMessage);
        return;
      }
      await publishManifest(manifest, true, returnContext);
    },
    [
      approvalRoute.manifestId,
      publishManifest,
      returnContextForManifest,
      targetApprovalGate,
    ],
  );

  const reauthenticateWithPassword = async (password: string) => {
    const manifest = reauthenticationManifest;
    if (!manifest || !session.email || !session.userId) {
      setReauthenticationError(
        "This owner session is incomplete. Sign in again and retry.",
      );
      return;
    }

    setReauthenticating(true);
    setReauthenticationError(null);
    try {
      const authenticatedSession = await loginWithPassword(
        session.email,
        password,
      );
      if (
        authenticatedSession.userId !== session.userId ||
        authenticatedSession.company?.id !== companyId
      ) {
        throw new Error(
          "The verified account does not match this company owner.",
        );
      }
      setReauthenticationManifest(null);
      await publishManifest(
        manifest,
        false,
        returnContextForManifest(manifest),
      );
    } catch (error) {
      setReauthenticationError(ownerReauthenticationErrorMessage(error));
    } finally {
      setReauthenticating(false);
    }
  };

  const reauthenticateWithGoogle = async () => {
    const manifest = reauthenticationManifest;
    if (!manifest || !session.userId) {
      setReauthenticationError(
        "This owner session is incomplete. Sign in again and retry.",
      );
      return;
    }

    const returnContext = returnContextForManifest(manifest);
    const storedApproval = writePendingCommerceApproval({
      companyId,
      digest: manifest.digest,
      manifestId: manifest.manifestId,
      returnContext,
      userId: session.userId,
    });
    const storedReturnPath = writeAuthContinuation(
      returnContext
        ? commerceManifestReviewHref(manifest.manifestId, returnContext)
        : commerceManifestViewHref(manifest.manifestId),
    );
    if (!storedApproval || !storedReturnPath) {
      clearPendingCommerceApproval();
      clearAuthContinuation();
      setReauthenticationError(
        "Zenith Team could not preserve this approval while Google opens. Try again.",
      );
      return;
    }

    setReauthenticating(true);
    setReauthenticationError(null);
    try {
      if (window.boardlyDesktop) {
        await beginDesktopGoogleOAuthRedirect();
        setReauthenticationManifest(null);
        return;
      }

      const started = await authService.continueWithGoogle();
      window.location.assign(started.authorizationUrl);
    } catch (error) {
      clearPendingCommerceApproval();
      clearAuthContinuation();
      setReauthenticationError(ownerReauthenticationErrorMessage(error));
    } finally {
      setReauthenticating(false);
    }
  };

  useEffect(() => {
    const pending = pendingApprovalRef.current;
    if (!pending || !manifestsQuery.currentData) {
      return;
    }

    if (pending.companyId !== companyId || pending.userId !== session.userId) {
      pendingApprovalRef.current = null;
      clearPendingCommerceApproval();
      setManifestActionError(
        "The verified owner no longer matches this payment approval. Review it again.",
      );
      return;
    }

    const manifest = allManifests.find(
      (candidate) => candidate.manifestId === pending.manifestId,
    );
    if (
      !manifest &&
      (manifestsQuery.hasNextPage ||
        manifestsQuery.isFetchingNextPage ||
        manifestsQuery.isError)
    ) {
      return;
    }

    pendingApprovalRef.current = null;
    clearPendingCommerceApproval();
    if (!manifest || manifest.digest !== pending.digest) {
      setManifestActionError(
        "This payment proposal changed while you verified your identity. Review the latest version before publishing.",
      );
      void refetchManifests();
      return;
    }

    setEnvironment(manifest.environment);
    void publishManifest(manifest, false, pending.returnContext);
  }, [
    allManifests,
    companyId,
    manifestsQuery.currentData,
    manifestsQuery.hasNextPage,
    manifestsQuery.isError,
    manifestsQuery.isFetchingNextPage,
    publishManifest,
    refetchManifests,
    session.userId,
  ]);

  if (!companyId || companyQuery.isLoading) {
    return <RevenuePageState state="loading" />;
  }
  if (companyQuery.isError) {
    return (
      <RevenuePageState
        onRetry={() => void companyQuery.refetch()}
        state="error"
      />
    );
  }
  if (!canReadCommerce) {
    return <RevenuePageState state="forbidden" />;
  }

  return (
    <WorkspacePageFrame
      actions={
        <EnvironmentSelector
          environment={environment}
          onEnvironmentChange={setEnvironment}
        />
      }
      ariaLabel="Revenue"
      contentClassName="!block"
      contentWidth="contained"
      description="Connect Stripe and receive website payments directly."
      title="Revenue"
    >
      <div className="w-full">
        {summaryQuery.isLoading ? (
          <RevenueSummaryState state="loading" />
        ) : commerceManifestErrorCode(summaryQuery.error) ===
          "commerce_checkout_disabled" ? (
          <RevenueSummaryState state="disabled" />
        ) : summaryQuery.isError || !summary ? (
          <RevenueSummaryState
            onRetry={() => void summaryQuery.refetch()}
            state="error"
          />
        ) : (
          <>
            <section className="grid gap-4 lg:grid-cols-[minmax(0,1.55fr)_minmax(280px,0.85fr)]">
              <AccountOverviewCard
                account={selectedAccount}
                canManage={canManageCommerce}
                environment={environment}
                isOpeningDashboard={pendingOperation === "dashboard-link"}
                onOpenDashboard={() => void openStripeDashboard()}
                onSetup={() => setSetupOpen(true)}
              />
              <TermsCard
                accepted={summary.terms.accepted}
                acceptedAt={summary.terms.acceptedAt}
                canManage={canManageCommerce}
                onReview={() => setSetupOpen(true)}
                version={summary.terms.currentVersion}
              />
            </section>

            <section className="mt-4 grid gap-4 sm:grid-cols-3">
              <RevenueMetricCard
                description="Appears after Stripe confirms successful charges."
                label="Payments"
                value="—"
              />
              <RevenueMetricCard
                description="Active recurring plans will be tracked here."
                label="Subscriptions"
                value="—"
              />
              <RevenueMetricCard
                description="Payout timing and bank details remain managed by Stripe."
                label="Payouts"
                value="—"
              />
            </section>

            <CommerceIntegrationsSection
              actionError={
                manifestActionError ??
                commerceApprovalGateMessage(targetApprovalGate) ??
                (approvalRoute.manifestId &&
                manifestsQuery.currentData &&
                !targetManifest &&
                !manifestsQuery.hasNextPage &&
                !manifestsQuery.isFetchingNextPage &&
                !manifestsQuery.isError
                  ? "This exact payment proposal is no longer available. Return to the task chat for the latest request."
                  : null)
              }
              environment={environment}
              hasMoreManifests={manifestsQuery.hasNextPage}
              isLoading={manifestsQuery.isLoading}
              isLoadingMore={manifestsQuery.isFetchingNextPage}
              manifests={manifests}
              onApprove={approveAndPublish}
              onLoadMore={() => void manifestsQuery.fetchNextPage()}
              onRetry={() => void refetchManifests()}
              publishingManifestId={publishingManifestId}
              queryFailed={
                manifestsQuery.isError && !manifestsQuery.currentData
              }
              targetApprovalGate={targetApprovalGate}
              targetManifestId={targetManifest?.manifestId ?? null}
              targetManifestRef={targetManifestElementRef}
              targetReadOnly={approvalRoute.mode === "view"}
            />

            <SetupBankDialog
              account={selectedAccount}
              companyId={companyId}
              environment={environment}
              onAcceptTerms={() =>
                acceptTerms({
                  companyId,
                  request: {
                    accepted: true,
                    schemaVersion: 1,
                    termsVersion: summary.terms.currentVersion,
                  },
                }).unwrap()
              }
              onOpenChange={closeSetup}
              onRefreshSummary={() => summaryQuery.refetch()}
              open={setupOpen}
              terms={summary.terms}
              termsAreSaving={acceptTermsState.isLoading}
            />

            <OwnerReauthenticationDialog
              email={session.email ?? ""}
              errorMessage={reauthenticationError}
              isSubmitting={reauthenticating}
              onGoogleSubmit={() => void reauthenticateWithGoogle()}
              onOpenChange={(open) => {
                if (!open) {
                  setReauthenticationError(null);
                  setReauthenticationManifest(null);
                }
              }}
              onPasswordSubmit={(password) =>
                void reauthenticateWithPassword(password)
              }
              open={reauthenticationManifest !== null}
              provider={session.provider ?? "password"}
            />
          </>
        )}
      </div>
    </WorkspacePageFrame>
  );
}

function commerceApprovalAllowsPublish(
  approval: ApprovalDto,
  manifest: CommerceManifestReview,
  returnContext: CommerceApprovalReturnContext,
) {
  return (
    approval.id === returnContext.approvalId &&
    approval.kind === "commerce_manifest_publish" &&
    approval.status === "pending" &&
    approval.subjectId === manifest.manifestId &&
    approval.taskId === manifest.taskId &&
    approval.originRunId === manifest.runId &&
    approval.conversationId === returnContext.conversationId &&
    approval.requestedByAgent.id === returnContext.agentId &&
    returnContext.taskId === manifest.taskId
  );
}

function commerceApprovalGate(
  route: CommerceApprovalRoute,
  manifest: CommerceManifestReview | null,
  approval: ApprovalDto | undefined,
  isLoading: boolean,
  hasError: boolean,
): CommerceApprovalGate {
  if (!route.manifestId) return "not_required";
  if (route.mode === "view") return "not_required";
  if (route.mode !== "publish" || !route.returnContext) return "stale";
  if (isLoading) return "checking";
  if (hasError) return "unavailable";
  if (!manifest || !approval) return "stale";
  return commerceApprovalAllowsPublish(approval, manifest, route.returnContext)
    ? "ready"
    : "stale";
}

function commerceApprovalGateMessage(gate: CommerceApprovalGate) {
  if (gate === "stale") return staleCommerceApprovalMessage;
  if (gate === "unavailable") {
    return "Zenith Team could not verify this payment approval. Return to the task chat and try again.";
  }
  return null;
}

const manifestStateCopy: Record<
  CommerceManifestReview["state"],
  { label: string; tone: string }
> = {
  active: { label: "Active", tone: "bg-[#eaf7ee] text-[#287044]" },
  apply_failed: {
    label: "Publish failed",
    tone: "bg-[#fdecec] text-[#a23b32]",
  },
  draft: {
    label: "Needs approval",
    tone: "bg-[#fff4e5] text-[#935f13]",
  },
  publishing: {
    label: "Publishing",
    tone: "bg-[#eef1ff] text-[#5750c7]",
  },
  rejected: {
    label: "Rejected",
    tone: "bg-[#f2f3f5] text-[#69707c]",
  },
  superseded: {
    label: "Superseded",
    tone: "bg-[#f2f3f5] text-[#69707c]",
  },
};

const signingKeyStateTone: Record<
  CommerceManifestReview["signingKeys"][number]["state"],
  string
> = {
  active: "border-[#80d49b]/25 bg-[#2b6940]/24 text-[#a9e7bb]",
  pending: "border-[#e7bd72]/25 bg-[#75561f]/24 text-[#f4cf8c]",
  retiring: "border-[#aaa6ff]/25 bg-[#5149a0]/24 text-[#c8c5ff]",
  revoked: "border-[#ff9caa]/25 bg-[#6b2430]/24 text-[#ffc4cd]",
};

function CommerceIntegrationsSection({
  actionError,
  environment,
  hasMoreManifests,
  isLoading,
  isLoadingMore,
  manifests,
  onApprove,
  onLoadMore,
  onRetry,
  publishingManifestId,
  queryFailed,
  targetApprovalGate,
  targetManifestId,
  targetManifestRef,
  targetReadOnly,
}: {
  actionError: string | null;
  environment: CommerceEnvironment;
  hasMoreManifests: boolean;
  isLoading: boolean;
  isLoadingMore: boolean;
  manifests: CommerceManifestReview[];
  onApprove: (manifest: CommerceManifestReview) => Promise<void>;
  onLoadMore: () => void;
  onRetry: () => void;
  publishingManifestId: string | null;
  queryFailed: boolean;
  targetApprovalGate: CommerceApprovalGate;
  targetManifestId: string | null;
  targetManifestRef: RefObject<HTMLElement | null>;
  targetReadOnly: boolean;
}) {
  return (
    <section className="workspace-matte-panel mt-4 rounded-2xl p-6 sm:p-7">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-start gap-4">
          <span className="grid size-11 shrink-0 place-items-center rounded-2xl bg-white/8 text-white/72">
            <Icon aria-hidden="true" className="size-5" name="lock" />
          </span>
          <div>
            <h2 className="m-0 text-base font-bold">Website integrations</h2>
            <p className="m-0 mt-1 max-w-3xl text-sm leading-6 text-white/55">
              Review the exact Site origin, environment, signing keys, prices,
              and access each Task proposed. Private signing material, Stripe
              credentials, and provider IDs never enter the Task or this review.
            </p>
          </div>
        </div>
        <span className="rounded-full border border-white/10 bg-white/[0.055] px-3 py-1.5 text-[11px] font-bold uppercase tracking-[0.12em] text-white/45">
          {environment}
        </span>
      </div>

      {actionError ? (
        <p
          className="m-0 mt-5 rounded-2xl border border-[#ff9caa]/32 bg-[#6b2430]/28 px-4 py-3 text-sm text-[#ffd2d8]"
          role="alert"
        >
          {actionError}
        </p>
      ) : null}

      {isLoading ? (
        <p className="m-0 mt-6 text-sm text-white/48" role="status">
          Loading payment proposals…
        </p>
      ) : queryFailed ? (
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <p className="m-0 text-sm text-white/55">
            Payment proposals could not be loaded.
          </p>
          <button
            className="text-sm font-bold text-[#aaa6ff] hover:text-[#c6c3ff]"
            onClick={onRetry}
            type="button"
          >
            Try again
          </button>
        </div>
      ) : manifests.length === 0 ? (
        <div className="mt-6 rounded-2xl border border-dashed border-white/12 bg-white/[0.035] px-5 py-6">
          <p className="m-0 text-sm font-semibold text-white/72">
            No {environment} payment proposal yet
          </p>
          <p className="m-0 mt-1 text-xs leading-5 text-white/42">
            A website Task appears here after it submits boardly-commerce.json.
            Submission never creates Stripe resources.
          </p>
          {hasMoreManifests ? (
            <CommerceManifestHistoryButton
              isLoading={isLoadingMore}
              onLoad={onLoadMore}
            />
          ) : null}
        </div>
      ) : (
        <div className="mt-6 grid gap-4">
          {manifests.map((manifest) => (
            <CommerceManifestCard
              approvalGate={
                targetManifestId === manifest.manifestId
                  ? targetApprovalGate
                  : "not_required"
              }
              elementRef={targetManifestRef}
              key={manifest.manifestId}
              manifest={manifest}
              onApprove={onApprove}
              publishing={publishingManifestId === manifest.manifestId}
              readOnly={
                targetReadOnly && targetManifestId === manifest.manifestId
              }
              targeted={targetManifestId === manifest.manifestId}
            />
          ))}
          {hasMoreManifests ? (
            <CommerceManifestHistoryButton
              isLoading={isLoadingMore}
              onLoad={onLoadMore}
            />
          ) : null}
        </div>
      )}
    </section>
  );
}

function CommerceManifestHistoryButton({
  isLoading,
  onLoad,
}: {
  isLoading: boolean;
  onLoad: () => void;
}) {
  return (
    <button
      className="mx-auto mt-4 inline-flex h-10 items-center justify-center rounded-xl border border-white/12 bg-white/[0.045] px-4 text-xs font-bold text-white/68 transition hover:bg-white/[0.075] hover:text-white disabled:cursor-wait disabled:opacity-60"
      disabled={isLoading}
      onClick={onLoad}
      type="button"
    >
      {isLoading ? "Loading older proposals…" : "Load older proposals"}
    </button>
  );
}

function CommerceManifestCard({
  approvalGate,
  elementRef,
  manifest,
  onApprove,
  publishing,
  readOnly,
  targeted,
}: {
  approvalGate: CommerceApprovalGate;
  elementRef: RefObject<HTMLElement | null>;
  manifest: CommerceManifestReview;
  onApprove: (manifest: CommerceManifestReview) => Promise<void>;
  publishing: boolean;
  readOnly: boolean;
  targeted: boolean;
}) {
  const state = manifestStateCopy[manifest.state];
  const needsLocalSigningKey =
    isLocalCommerceOrigin(manifest.manifest.site.origin) &&
    !hasPublishableSigningKey(manifest);
  const canApprove =
    (manifest.state === "draft" ||
      manifest.state === "apply_failed" ||
      manifest.state === "publishing") &&
    (approvalGate === "not_required" || approvalGate === "ready") &&
    !needsLocalSigningKey;
  const manifestCanBePublished =
    manifest.state === "draft" ||
    manifest.state === "apply_failed" ||
    manifest.state === "publishing";
  const isResume = manifest.state === "publishing";

  return (
    <article
      aria-current={targeted ? "true" : undefined}
      className={cx(
        "rounded-3xl border bg-white/[0.045] p-5 outline-none transition",
        targeted
          ? "border-[#8f89ff]/70 shadow-[0_0_0_3px_rgba(99,91,255,0.18),0_22px_60px_rgba(0,0,0,0.24)]"
          : "border-white/10",
      )}
      id={`commerce-manifest-${manifest.manifestId}`}
      ref={targeted ? elementRef : undefined}
      tabIndex={targeted ? -1 : undefined}
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="m-0 text-base font-bold">
              {manifest.manifest.site.alias}
            </h3>
            <span
              className={cx(
                "rounded-full px-2.5 py-1 text-[11px] font-bold",
                state.tone,
              )}
            >
              {state.label}
            </span>
            {readOnly ? (
              <span className="rounded-full border border-white/10 bg-white/[0.055] px-2.5 py-1 text-[11px] font-bold text-white/48">
                Read-only
              </span>
            ) : null}
          </div>
        </div>
        <div className="text-right text-[11px] text-white/38">
          <p className="m-0">Digest</p>
          <code className="mt-1 block" title={manifest.digest}>
            {manifest.digest.slice(0, 12)}…{manifest.digest.slice(-8)}
          </code>
        </div>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        <div className="rounded-2xl border border-white/8 bg-black/10 px-4 py-3">
          <p className="m-0 text-[11px] font-bold uppercase tracking-[0.1em] text-white/36">
            Site origin
          </p>
          <p className="m-0 mt-1 break-all text-xs text-white/62">
            {manifest.manifest.site.origin}
          </p>
        </div>
        <div className="rounded-2xl border border-white/8 bg-black/10 px-4 py-3">
          <p className="m-0 text-[11px] font-bold uppercase tracking-[0.1em] text-white/36">
            Environment
          </p>
          <p className="m-0 mt-1 text-xs capitalize text-white/62">
            {manifest.environment}
          </p>
          <code className="mt-1 block break-all text-[11px] text-white/38">
            Site {manifest.siteId}
          </code>
        </div>
        <div className="rounded-2xl border border-white/8 bg-black/10 px-4 py-3">
          <p className="m-0 text-[11px] font-bold uppercase tracking-[0.1em] text-white/36">
            Customer Portal
          </p>
          <p className="m-0 mt-1 break-all text-xs text-white/62">
            {manifest.manifest.site.portalReturnPath}
          </p>
        </div>
        <ManifestSigningKeys
          manifest={manifest}
          needsLocalSigningKey={needsLocalSigningKey}
        />
      </div>

      <div className="mt-4 grid gap-2">
        {manifest.manifest.offers.map((offer) => (
          <ManifestOfferRow key={offer.alias} offer={offer} />
        ))}
      </div>

      <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-white/8 pt-4">
        <p className="m-0 text-xs text-white/40">
          {manifest.manifest.offers.length} fixed-price offer
          {manifest.manifest.offers.length === 1 ? "" : "s"} · quantity 1
        </p>
        {manifestCanBePublished && !readOnly ? (
          <Button
            className="h-9 rounded-md !bg-[var(--workspace-accent-fill)] px-3 !text-[var(--workspace-accent-foreground)] hover:!bg-[var(--workspace-accent-hover)]"
            disabled={!canApprove || publishing}
            onClick={() => void onApprove(manifest)}
            type="button"
            variant="default"
          >
            {approvalGate === "checking"
              ? "Checking approval…"
              : approvalGate === "stale" || approvalGate === "unavailable"
                ? "Approval unavailable"
                : needsLocalSigningKey
                  ? "Signing key required"
                  : publishing
                    ? "Publishing…"
                    : isResume
                      ? "Resume publishing"
                      : "Approve & Publish"}
            <Icon aria-hidden="true" className="size-4" name="arrow-right-2" />
          </Button>
        ) : null}
      </div>
    </article>
  );
}

function ManifestSigningKeys({
  manifest,
  needsLocalSigningKey,
}: {
  manifest: CommerceManifestReview;
  needsLocalSigningKey: boolean;
}) {
  const signingKeys = manifest.signingKeys;

  return (
    <div className="rounded-2xl border border-white/8 bg-black/10 px-4 py-3 sm:col-span-2">
      <p className="m-0 text-[11px] font-bold uppercase tracking-[0.1em] text-white/36">
        Signing keys
      </p>
      {signingKeys.length === 0 ? (
        <p className="m-0 mt-2 text-xs text-white/48">
          No signing key synchronized yet
        </p>
      ) : (
        <div className="mt-2 grid gap-2">
          {signingKeys.map((signingKey) => (
            <div
              className="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-white/8 bg-white/[0.025] px-3 py-2.5"
              key={signingKey.keyId}
            >
              <div className="min-w-0 flex-1">
                <p className="m-0 text-[11px] text-white/38">Fingerprint</p>
                <code
                  className="mt-1 block break-all text-xs text-white/62"
                  title={signingKey.keyId}
                >
                  {signingKey.fingerprint}
                </code>
                {signingKey.activatedAt ? (
                  <p className="m-0 mt-1 text-[11px] text-white/38">
                    Activated{" "}
                    {formatSigningKeyTimestamp(signingKey.activatedAt)}
                  </p>
                ) : null}
                {signingKey.retireAt ? (
                  <p className="m-0 mt-1 text-[11px] text-white/38">
                    Retires {formatSigningKeyTimestamp(signingKey.retireAt)}
                  </p>
                ) : null}
              </div>
              <span
                className={cx(
                  "rounded-full border px-2.5 py-1 text-[11px] font-semibold capitalize",
                  signingKeyStateTone[signingKey.state],
                )}
              >
                {signingKey.state}
              </span>
            </div>
          ))}
        </div>
      )}
      {needsLocalSigningKey ? (
        <div className="mt-2 text-xs leading-5 text-[#f4cf8c]">
          <p className="m-0">No publishable local signing key registered.</p>
          <code className="mt-1 block break-all text-[11px] text-white/62">
            {localSigningKeyCommand}
          </code>
        </div>
      ) : null}
    </div>
  );
}

function formatSigningKeyTimestamp(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function isLocalCommerceOrigin(origin: string) {
  try {
    const parsed = new URL(origin);
    if (parsed.protocol !== "http:") {
      return false;
    }
    const hostname = parsed.hostname.toLowerCase();
    if (
      hostname === "localhost" ||
      hostname.endsWith(".localhost") ||
      hostname === "[::1]"
    ) {
      return true;
    }
    const octets = hostname.split(".");
    return (
      octets.length === 4 &&
      octets[0] === "127" &&
      octets.every(
        (octet) => /^(0|[1-9]\d{0,2})$/.test(octet) && Number(octet) <= 255,
      )
    );
  } catch {
    return false;
  }
}

function hasPublishableSigningKey(manifest: CommerceManifestReview) {
  return manifest.signingKeys.some(
    (key) => key.state === "pending" || key.state === "active",
  );
}

function ManifestOfferRow({ offer }: { offer: CommerceManifestOffer }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-white/8 bg-black/10 px-4 py-3">
      <div>
        <p className="m-0 text-sm font-semibold text-white/78">
          {offer.displayName}
        </p>
        <p className="m-0 mt-1 text-[11px] text-white/40">
          {offer.alias} · unlocks {offer.entitlement}
          {offer.planGroup ? ` · group ${offer.planGroup}` : ""}
        </p>
        <p className="m-0 mt-2 break-all text-[11px] text-white/36">
          Success: {offer.successPath}
        </p>
        <p className="m-0 mt-1 break-all text-[11px] text-white/36">
          Cancel: {offer.cancelPath}
        </p>
      </div>
      <div className="text-right">
        <p className="m-0 text-sm font-bold text-white/82">
          {formatManifestAmount(offer)}
          {offer.interval ? ` / ${offer.interval}` : ""}
        </p>
        <p className="m-0 mt-1 text-[11px] text-white/40">
          {offer.type === "subscription" ? "Subscription" : "One-time"}
          {offer.automaticTax ? " · automatic tax" : ""}
          {` · ${offer.amount} ${offer.currency.toUpperCase()} minor units`}
        </p>
      </div>
    </div>
  );
}

function formatManifestAmount(offer: CommerceManifestOffer) {
  try {
    const formatter = new Intl.NumberFormat(undefined, {
      currency: offer.currency.toUpperCase(),
      style: "currency",
    });
    const digits = formatter.resolvedOptions().maximumFractionDigits ?? 2;
    return formatter.format(offer.amount / 10 ** digits);
  } catch {
    return `${offer.amount} ${offer.currency.toUpperCase()} minor units`;
  }
}

function commerceManifestErrorMessage(
  error: unknown,
  manifest: CommerceManifestReview,
) {
  if (
    error &&
    typeof error === "object" &&
    "code" in error &&
    (error.code === "commerce_site_signing_key_required" ||
      (error.code === "commerce_site_verification_failed" &&
        isLocalCommerceOrigin(manifest.manifest.site.origin) &&
        !hasPublishableSigningKey(manifest)))
  ) {
    return localSigningKeyRequiredMessage;
  }
  if (
    error &&
    typeof error === "object" &&
    "code" in error &&
    error.code === "commerce_site_verification_failed"
  ) {
    return "Zenith Team could not verify the Site origin proof or public signing key. Check the Site verification document and try again.";
  }
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string" &&
    error.message.trim()
  ) {
    return error.message;
  }
  return "The payment integration could not be published. Refresh and try again.";
}

function commerceManifestErrorCode(error: unknown) {
  if (
    error &&
    typeof error === "object" &&
    "code" in error &&
    typeof error.code === "string"
  ) {
    return error.code;
  }
  return null;
}

function ownerReauthenticationErrorMessage(error: unknown) {
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string" &&
    error.message.trim()
  ) {
    return error.message;
  }
  return "Zenith Team could not verify the company owner. Try again.";
}

function EnvironmentSelector({
  environment,
  onEnvironmentChange,
}: {
  environment: CommerceEnvironment;
  onEnvironmentChange: (environment: CommerceEnvironment) => void;
}) {
  return (
    <div
      aria-label="Stripe environment"
      className="inline-flex w-fit rounded-full border border-white/12 bg-black/16 p-1 backdrop-blur-xl"
      role="group"
    >
      {visibleCommerceEnvironments.map((option) => (
        <button
          aria-pressed={environment === option}
          className={cx(
            "rounded-full px-4 py-2 text-xs font-bold capitalize transition",
            environment === option
              ? "bg-white text-[#252a33] shadow-lg"
              : "text-white/55 hover:text-white",
          )}
          key={option}
          onClick={() => onEnvironmentChange(option)}
          type="button"
        >
          {option}
        </button>
      ))}
    </div>
  );
}

function AccountOverviewCard({
  account,
  canManage,
  environment,
  isOpeningDashboard,
  onOpenDashboard,
  onSetup,
}: {
  account: CommerceAccountSummary | undefined;
  canManage: boolean;
  environment: CommerceEnvironment;
  isOpeningDashboard: boolean;
  onOpenDashboard: () => void;
  onSetup: () => void;
}) {
  const accountIsReady = commerceAccountIsReady(account);
  const effectiveStatus =
    account?.status === "ready" && !accountIsReady
      ? "pending_review"
      : (account?.status ?? "not_started");
  const status =
    account?.syncStatus === "stale"
      ? {
          description:
            "Zenith Team could not verify the latest Stripe state. Refresh before relying on payment or payout availability.",
          label: "Status unavailable",
          tone: "bg-[#fff4e5] text-[#935f13]",
        }
      : accountStatusCopy[effectiveStatus];
  const requirementsDue = account
    ? account.requirements.currentlyDue + account.requirements.pastDue
    : 0;

  return (
    <section className="workspace-matte-panel rounded-2xl p-6 sm:p-7">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="m-0 text-xl font-bold">Stripe account</h2>
            <span
              className={cx(
                "rounded-full px-2.5 py-1 text-[11px] font-bold",
                status.tone,
              )}
            >
              {status.label}
            </span>
          </div>
          <p className="m-0 mt-2 max-w-xl text-sm leading-6 text-white/58">
            {account?.configured
              ? status.description
              : `Zenith Team has not configured the ${environment} Stripe environment yet.`}
          </p>
        </div>
        <Button
          className="h-9 rounded-md !bg-[var(--workspace-accent-fill)] px-3 !text-[var(--workspace-accent-foreground)] hover:!bg-[var(--workspace-accent-hover)]"
          disabled={!canManage || !account?.configured || isOpeningDashboard}
          onClick={accountIsReady ? onOpenDashboard : onSetup}
          title={
            canManage ? undefined : "Only a Company owner can manage payouts."
          }
          type="button"
          variant="default"
        >
          {accountIsReady
            ? isOpeningDashboard
              ? "Opening Stripe Dashboard…"
              : "Open Stripe Dashboard"
            : "Set up bank"}
          <Icon
            aria-hidden="true"
            className="size-4"
            name={accountIsReady ? "open-external" : "arrow-right-2"}
          />
        </Button>
      </div>

      <div className="mt-6 grid gap-3 sm:grid-cols-3">
        <Capability
          enabled={accountIsReady && (account?.chargesEnabled ?? false)}
          label="Payments"
        />
        <Capability
          enabled={accountIsReady && (account?.payoutsEnabled ?? false)}
          label="Payouts"
        />
        <Capability
          enabled={Boolean(accountIsReady && requirementsDue === 0)}
          label={
            requirementsDue > 0
              ? `${requirementsDue} details due`
              : "No details due"
          }
        />
      </div>

      {account?.country || account?.defaultCurrency ? (
        <p className="m-0 mt-5 text-xs font-medium uppercase tracking-[0.12em] text-white/38">
          {[account.country, account.defaultCurrency?.toUpperCase()]
            .filter(Boolean)
            .join(" · ")}
        </p>
      ) : null}
    </section>
  );
}

function Capability({ enabled, label }: { enabled: boolean; label: string }) {
  return (
    <div className="flex items-center gap-2 rounded-2xl border border-white/8 bg-white/[0.045] px-4 py-3 text-xs font-semibold text-white/66">
      <span
        aria-hidden="true"
        className={cx(
          "size-2 rounded-full",
          enabled ? "bg-[#73dd96]" : "bg-white/24",
        )}
      />
      {label}
      <span className="sr-only">: {enabled ? "Enabled" : "Unavailable"}</span>
    </div>
  );
}

function TermsCard({
  accepted,
  acceptedAt,
  canManage,
  onReview,
  version,
}: {
  accepted: boolean;
  acceptedAt: string | null;
  canManage: boolean;
  onReview: () => void;
  version: string;
}) {
  const acceptedDate = useMemo(
    () =>
      acceptedAt
        ? new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(
            new Date(acceptedAt),
          )
        : null,
    [acceptedAt],
  );

  return (
    <section className="workspace-matte-panel-raised flex flex-col rounded-2xl p-6 sm:p-7">
      <span className="grid size-10 place-items-center rounded-2xl bg-white/8 text-white/70">
        <Icon aria-hidden="true" className="size-5" name="check-circle" />
      </span>
      <h2 className="m-0 mt-4 text-base font-bold">Commerce terms</h2>
      <p className="m-0 mt-1 text-sm leading-6 text-white/55">
        {accepted
          ? `Version ${version} accepted${acceptedDate ? ` on ${acceptedDate}` : ""}.`
          : `Version ${version} must be accepted by a Company owner.`}
      </p>
      <button
        className="mt-auto pt-5 text-left text-sm font-bold text-[#aaa6ff] transition hover:text-[#c6c3ff] disabled:cursor-not-allowed disabled:opacity-60"
        disabled={!canManage && !accepted}
        onClick={onReview}
        title={
          canManage || accepted
            ? undefined
            : "Only a Company owner can accept commerce terms."
        }
        type="button"
      >
        {accepted || !canManage ? "Review terms" : "Read and accept"}{" "}
        <Icon name="arrow-right" className="inline-block size-4 align-middle" />
      </button>
    </section>
  );
}

function RevenueMetricCard({
  description,
  label,
  value,
}: {
  description: string;
  label: string;
  value: string;
}) {
  return (
    <article className="workspace-matte-panel rounded-2xl p-5">
      <p className="m-0 text-xs font-bold uppercase tracking-[0.12em] text-white/40">
        {label}
      </p>
      <strong className="mt-3 block text-2xl text-white/82">{value}</strong>
      <p className="m-0 mt-2 text-xs leading-5 text-white/45">{description}</p>
    </article>
  );
}

// "disabled" is this deployment's answer (503 commerce_checkout_disabled):
// Commerce is not configured here, which is a fact to state, not an outage
// to retry (docs/api/errors.md: do not retry, hide Commerce actions).
function RevenueSummaryState({
  onRetry,
  state,
}: {
  onRetry?: () => void;
  state: "disabled" | "error" | "loading";
}) {
  return (
    <section
      className="workspace-matte-panel mt-7 grid min-h-[360px] place-content-center justify-items-center rounded-2xl px-6 text-center"
      role={state === "error" ? "alert" : "status"}
    >
      <Icon
        aria-hidden="true"
        className={cx(
          "size-10 text-white/45",
          state === "loading" && "animate-spin",
        )}
        name={state === "loading" ? "refresh" : "info"}
      />
      <h2 className="m-0 mt-4 text-lg font-bold">
        {state === "loading"
          ? "Loading Revenue…"
          : state === "disabled"
            ? "Revenue is not enabled here"
            : "Revenue is unavailable"}
      </h2>
      <p className="m-0 mt-2 max-w-md text-sm leading-6 text-white/52">
        {state === "loading"
          ? "Zenith Team is checking the latest Stripe account state."
          : state === "disabled"
            ? "This Zenith Team deployment runs without Stripe Commerce, so there is nothing to connect or collect. Nothing is wrong with your account."
            : "Zenith Team could not load the latest Stripe state. Try again without starting a new onboarding flow."}
      </p>
      {state === "error" ? (
        <Button
          className="mt-5 border-white/28 text-white"
          onClick={onRetry}
          type="button"
          variant="outline"
        >
          Try again
        </Button>
      ) : null}
    </section>
  );
}

function RevenuePageState({
  onRetry,
  state,
}: {
  onRetry?: () => void;
  state: "error" | "forbidden" | "loading";
}) {
  return (
    <WorkspacePageFrame
      ariaLabel="Revenue"
      contentClassName="!grid place-items-center"
      contentWidth="contained"
      description="Connect Stripe and receive website payments directly."
      title="Revenue"
    >
      <section className="grid justify-items-center text-center">
        <Icon
          aria-hidden="true"
          className={cx(
            "size-12 text-white/42",
            state === "loading" && "animate-spin",
          )}
          name={
            state === "loading"
              ? "refresh"
              : state === "forbidden"
                ? "lock"
                : "info"
          }
        />
        <h2 className="m-0 mt-4 text-xl font-bold">
          {state === "loading"
            ? "Loading Revenue…"
            : state === "forbidden"
              ? "Permission required"
              : "Company could not be loaded"}
        </h2>
        <p className="m-0 mt-2 max-w-md text-sm leading-6 text-white/55">
          {state === "forbidden"
            ? "Only Company owners and admins can view Revenue; only an owner can accept commerce terms or connect a payout account."
            : "Zenith Team needs the authoritative Company membership before opening Revenue."}
        </p>
        {state === "error" ? (
          <Button
            className="mt-5 border-white/28 text-white"
            onClick={onRetry}
            type="button"
            variant="outline"
          >
            Try again
          </Button>
        ) : null}
      </section>
    </WorkspacePageFrame>
  );
}

export const Component = RevenuePage;

export default RevenuePage;
