import { useRef, useState } from "react";
import { Link, useLocation } from "react-router";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { findCeoAgentInPage } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  useCompanyResearch,
  useCompanyResearchRetry,
} from "@/features/onboarding/hooks/use-company-research";
import {
  readStorageItem,
  writeStorageItem,
} from "@/shared/lib/browser-storage";
import {
  Check,
  CircleAlert,
  LoaderCircle,
  Spinner,
  X,
} from "@/shared/ui/icons";
import { useCompanyInitialization } from "./company-initialization-provider";
import "@/features/onboarding/styles/company-initialization-status.css";

/**
 * The founder's company research across the whole workspace, from the
 * receipt to the result. Until the research starts the onboarding controller
 * drives it, so a founder who left onboarding early can start it here. Once
 * the research Run exists the server owns it, and the strip follows that Run:
 * who is working, how many Wiki pages it wrote, and the way to them, to a
 * retry, or to the CEO's setup.
 */
export function CompanyInitializationStatus() {
  const initialization = useCompanyInitialization();
  return initialization?.showWorkspaceStatus ? (
    <ResearchNotStartedStatus />
  ) : (
    <ResearchRunStatus />
  );
}

function ResearchNotStartedStatus() {
  const initialization = useCompanyInitialization()!;
  const { state, ceo, failure, offline, statusText, working } = initialization;
  const StatusIcon =
    failure || offline || (state?.status === "awaiting_model" && !working)
      ? CircleAlert
      : LoaderCircle;

  return (
    <section
      className="company-initialization-status"
      aria-label="Company research"
    >
      <StatusIcon
        size={18}
        aria-hidden="true"
        className={
          StatusIcon === LoaderCircle
            ? "company-initialization-status__spinner"
            : undefined
        }
      />
      <div className="company-initialization-status__body">
        <p
          className="company-initialization-status__title"
          role="status"
          aria-live="polite"
        >
          {statusText}
        </p>
        <p className="company-initialization-status__description">
          {failure ||
            (offline
              ? "Reconnect your computer to continue. Your workspace is available."
              : state?.status === "awaiting_model" && !working
                ? "Start the research when you’re ready. The results will appear in the Wiki."
                : "You can keep working. The results will appear in the Wiki.")}
        </p>
      </div>
      <div className="company-initialization-status__actions">
        {initialization.canContinue ? (
          <button
            type="button"
            disabled={working}
            onClick={() => void initialization.continuePreparation()}
          >
            {working ? "Starting…" : failure ? "Try again" : "Start research"}
          </button>
        ) : null}
        {(failure || offline) && ceo ? (
          <Link to={`/agents/${ceo.id}?tab=settings`}>Review setup</Link>
        ) : null}
      </div>
    </section>
  );
}

/**
 * It stays out of the Wiki, which shows its own banner, and says "ready" only
 * in a tab that saw the research run, so an old company never announces it.
 * Dismissing it hides that state of that Run for the browser session.
 */
function ResearchRunStatus() {
  const session = useSessionSnapshot();
  const location = useLocation();
  const companyId = session.company?.id ?? "";
  const research = useCompanyResearch(companyId);
  const roster = useListCompanyAgentsQuery(companyId, {
    skip: !research.state?.runId,
  });
  const ceo = findCeoAgentInPage(roster.currentData);
  const ceoName = ceo?.name || "Your CEO";
  const sawActive = useRef(false);
  if (research.active) sawActive.current = true;
  const stage = research.active
    ? "active"
    : research.failed
      ? "failed"
      : research.completed && sawActive.current
        ? "ready"
        : null;
  const dismissKey = `zenith:research-status-dismissed:${research.state?.runId}:${stage}`;
  // Read during render, so a strip dismissed before a reload never paints.
  const [dismissedKey, setDismissedKey] = useState("");
  const dismissed =
    dismissedKey === dismissKey ||
    Boolean(readStorageItem("session", dismissKey));
  const dismiss = () => {
    writeStorageItem("session", dismissKey, "1");
    setDismissedKey(dismissKey);
  };

  if (
    !stage ||
    dismissed ||
    location.pathname === "/onboarding" ||
    location.pathname === "/wiki" ||
    location.pathname.startsWith("/wiki/")
  )
    return null;

  const { pages, overviewPageId } = research;
  const written = `${pages} ${pages === 1 ? "page" : "pages"}`;
  const overviewHref = overviewPageId
    ? `/wiki/pages/${overviewPageId}`
    : "/wiki";
  const needsSetup = stage === "failed" || research.waiting;

  return (
    <section
      className="company-initialization-status"
      aria-label="Company research"
    >
      {stage === "active" ? (
        <Spinner
          size={18}
          aria-hidden="true"
          className="company-initialization-status__spinner"
        />
      ) : stage === "failed" ? (
        <CircleAlert size={18} aria-hidden="true" />
      ) : (
        <Check size={18} aria-hidden="true" />
      )}
      <div className="company-initialization-status__body">
        <p
          className="company-initialization-status__title"
          role="status"
          aria-live="polite"
        >
          {stage === "active"
            ? research.waiting
              ? `${ceoName} is waiting for your computer to reconnect`
              : `${ceoName} is researching your company`
            : stage === "failed"
              ? "Company research didn’t finish"
              : "Your company wiki is ready"}
        </p>
        <p className="company-initialization-status__description">
          {stage === "active"
            ? pages > 0
              ? `${written} written to your Wiki so far. You can keep working.`
              : "You can keep working. Pages will appear in your Wiki as they are written."
            : stage === "failed"
              ? research.state?.message ||
                "Something went wrong while researching."
              : pages > 0
                ? `${ceoName} wrote ${written} about your company.`
                : `${ceoName} finished researching your company.`}
        </p>
      </div>
      <div className="company-initialization-status__actions">
        {stage === "failed" ? (
          <ResearchRetryButton companyId={companyId} />
        ) : null}
        {needsSetup && ceo ? (
          <Link to={`/agents/${ceo.id}?tab=settings`}>Review setup</Link>
        ) : null}
        <Link
          to={stage === "failed" ? "/wiki" : overviewHref}
          onClick={stage === "ready" ? dismiss : undefined}
        >
          {overviewPageId && stage !== "failed" ? "Open overview" : "Open Wiki"}
        </Link>
        <button
          type="button"
          className="company-initialization-status__dismiss"
          aria-label="Dismiss"
          onClick={dismiss}
        >
          <X size={16} aria-hidden="true" />
        </button>
      </div>
    </section>
  );
}

function ResearchRetryButton({ companyId }: { companyId: string }) {
  const retry = useCompanyResearchRetry(companyId);
  return (
    <button
      type="button"
      disabled={!retry.ready || retry.busy}
      title={retry.error || retry.hint || undefined}
      onClick={() => void retry.retry()}
    >
      {retry.busy ? "Retrying…" : "Retry"}
    </button>
  );
}
