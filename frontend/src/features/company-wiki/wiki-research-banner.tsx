import {
  useCompanyResearch,
  useCompanyResearchRetry,
} from "@/features/onboarding/hooks/use-company-research";
import { CircleAlert, Spinner } from "@/shared/ui/icons";

/**
 * Progress of the onboarding company research, shown above the Wiki while it
 * runs, and its Retry when it failed. Only the founder (agents.manage) may
 * read the initialization, so any other viewer, or an error, renders nothing.
 */
export function WikiResearchBanner({
  companyId,
  currentPageId,
  onOpenPage,
}: {
  companyId: string;
  currentPageId?: string | null;
  onOpenPage: (pageId: string) => void;
}) {
  const research = useCompanyResearch(companyId);
  if (research.failed) {
    return (
      <WikiResearchFailure
        companyId={companyId}
        message={research.state?.message}
      />
    );
  }
  if (!research.active) return null;
  const { pages, overviewPageId } = research;
  return (
    <div className="zen-wiki__research" role="status" aria-live="polite">
      <Spinner size={18} className="motion-safe:animate-spin" aria-hidden />
      <div>
        <strong>
          {research.waiting
            ? "Waiting for your computer to reconnect"
            : "Researching your company…"}
        </strong>
        <span>
          {pages > 0
            ? `${pages} ${pages === 1 ? "page" : "pages"} written so far.`
            : "Pages will appear here as they are written."}{" "}
          {overviewPageId && overviewPageId !== currentPageId ? (
            <button
              type="button"
              className="zen-wiki__retry"
              onClick={() => onOpenPage(overviewPageId)}
            >
              Open the overview
            </button>
          ) : null}
        </span>
      </div>
    </div>
  );
}

function WikiResearchFailure({
  companyId,
  message,
}: {
  companyId: string;
  message?: string;
}) {
  const retry = useCompanyResearchRetry(companyId);
  return (
    <div className="zen-wiki__research" data-failed="true" role="alert">
      <CircleAlert size={18} />
      <div>
        <strong>Company research didn’t finish</strong>
        <span>
          {retry.error || message || "Something went wrong while researching."}{" "}
          {retry.hint}{" "}
          <button
            type="button"
            className="zen-wiki__retry"
            disabled={!retry.ready || retry.busy}
            onClick={() => void retry.retry()}
          >
            Retry
          </button>
        </span>
      </div>
    </div>
  );
}
