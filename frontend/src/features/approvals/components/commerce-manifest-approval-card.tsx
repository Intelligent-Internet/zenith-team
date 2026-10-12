import { ArrowRight, CircleDollarSign, RefreshCcw } from "@/shared/ui/icons";
import { useEffect } from "react";
import { Link } from "react-router";

import {
  approvalStatusLabel,
  approvalStatusTone,
  formatHitlDate,
} from "@/features/approvals/lib/approval-inbox";
import { useGetCommerceManifestsInfiniteQuery } from "@/features/revenue/api/commerce-api";
import type { CommerceManifestReview } from "@/features/revenue/model/commerce";
import {
  commerceApprovalReturnContext,
  commerceManifestApprovalSubjectId,
  commerceManifestReviewHref,
  commerceManifestViewHref,
} from "@/features/revenue/model/commerce-approval-route";
import type { ApprovalDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { ContentPanel } from "@/shared/ui/content-panel";

export function CommerceManifestApprovalCard({
  approval,
  compact = false,
}: {
  approval: ApprovalDto;
  compact?: boolean;
}) {
  const manifestsQuery = useGetCommerceManifestsInfiniteQuery(
    approval.companyId,
  );
  const manifests =
    manifestsQuery.currentData?.pages.flatMap((page) => page.manifests) ?? [];
  const manifest = findCommerceManifestForApproval(approval, manifests);
  const subjectId = commerceManifestApprovalSubjectId(approval);
  useEffect(() => {
    if (
      !subjectId ||
      manifest ||
      !manifestsQuery.currentData ||
      !manifestsQuery.hasNextPage ||
      manifestsQuery.isFetchingNextPage ||
      manifestsQuery.isError
    ) {
      return;
    }
    void manifestsQuery.fetchNextPage();
  }, [
    manifest,
    manifestsQuery.currentData,
    manifestsQuery.fetchNextPage,
    manifestsQuery.hasNextPage,
    manifestsQuery.isError,
    manifestsQuery.isFetchingNextPage,
    subjectId,
  ]);
  const publishContext =
    approval.status === "pending"
      ? commerceApprovalReturnContext(approval)
      : null;

  return (
    <ContentPanel
      aria-label="Payment setup approval"
      className="isolate overflow-hidden rounded-[24px] [contain:paint]"
      contentClassName="relative flex-col overflow-hidden rounded-[24px] bg-transparent p-4 text-white sm:p-5"
      id={`approval-${approval.id}`}
      role="region"
    >
      <div className="relative z-10 flex items-start justify-between gap-4">
        <div className="flex min-w-0 items-start gap-3">
          <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-[#635bff]/18 text-[#b8b4ff]">
            <CircleDollarSign aria-hidden className="size-5" />
          </span>
          <div className="min-w-0">
            <p className="m-0 text-sm font-extrabold text-white">
              Payment setup is ready to review
            </p>
            <p className="m-0 mt-1 text-xs leading-5 text-white/52">
              Review the exact website, price, and customer access before Zenith
              Team publishes it to Stripe.
            </p>
          </div>
        </div>
        <span
          className={cx(
            "shrink-0 rounded-full border px-2.5 py-1 text-[12px] font-bold",
            approvalStatusTone(approval.status),
          )}
        >
          {approvalStatusLabel(approval.status)}
        </span>
      </div>

      <div
        className={cx(
          "relative z-10 mt-4 rounded-2xl border border-white/10 bg-black/15 p-4",
          compact && "p-3",
        )}
      >
        {manifestsQuery.isLoading ||
        (!manifestsQuery.isError &&
          !manifest &&
          (manifestsQuery.hasNextPage || manifestsQuery.isFetchingNextPage)) ? (
          <p className="m-0 text-xs font-semibold text-white/48" role="status">
            Loading the payment proposal…
          </p>
        ) : manifestsQuery.isError ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="m-0 text-xs font-semibold text-amber-100/80">
              The payment proposal could not be loaded.
            </p>
            <button
              className="inline-flex items-center gap-1.5 text-xs font-bold text-white/70 hover:text-white"
              type="button"
              onClick={() => void manifestsQuery.refetch()}
            >
              <RefreshCcw aria-hidden className="size-3.5" /> Retry
            </button>
          </div>
        ) : manifest ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="m-0 truncate text-sm font-bold text-white/82">
                {manifest.manifest.site.alias}
              </p>
              <p className="m-0 mt-1 truncate text-[11px] text-white/42">
                {manifest.manifest.site.origin} · {manifest.environment}
              </p>
            </div>
            <p className="m-0 shrink-0 text-xs font-semibold text-white/58">
              {manifest.manifest.offers.length} offer
              {manifest.manifest.offers.length === 1 ? "" : "s"}
            </p>
          </div>
        ) : (
          <p className="m-0 text-xs font-semibold leading-5 text-amber-100/80">
            This exact payment proposal is no longer available. Refresh the task
            before reviewing a different proposal.
          </p>
        )}
      </div>

      <dl className="relative z-10 mt-4 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[11px]">
        <dt className="text-white/38">Requested by</dt>
        <dd className="m-0 truncate font-semibold text-white/68">
          {approval.requestedByAgent.name}
        </dd>
        <dt className="text-white/38">Expires</dt>
        <dd className="m-0 font-semibold text-white/68">
          {formatHitlDate(approval.expiresAt)}
        </dd>
      </dl>

      {manifest ? (
        <Link
          className="relative z-10 mt-4 inline-flex h-11 w-fit items-center justify-center gap-2 rounded-xl bg-[#635bff] px-4 text-xs font-extrabold text-white transition hover:bg-[#5149e8]"
          to={
            publishContext
              ? commerceManifestReviewHref(manifest.manifestId, publishContext)
              : commerceManifestViewHref(manifest.manifestId)
          }
        >
          {publishContext
            ? "Review & publish payment setup"
            : "View payment setup"}
          <ArrowRight aria-hidden className="size-4" />
        </Link>
      ) : null}
    </ContentPanel>
  );
}

function findCommerceManifestForApproval(
  approval: ApprovalDto,
  manifests: CommerceManifestReview[],
) {
  const subjectId = commerceManifestApprovalSubjectId(approval);
  if (subjectId) {
    return (
      manifests.find((manifest) => manifest.manifestId === subjectId) ?? null
    );
  }
  return null;
}
