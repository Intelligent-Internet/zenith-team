import { Link } from "react-router";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { Check, CircleAlert, LoaderCircle, X } from "@/shared/ui/icons";
import { useCompanyInitialization } from "./company-initialization-provider";
import "@/features/onboarding/styles/company-initialization-status.css";

export function CompanyInitializationStatus() {
  const initialization = useCompanyInitialization();
  if (!initialization?.showWorkspaceStatus) return null;
  const { state, ceo, failure, offline, statusText, working } = initialization;
  const completed = initialization.completed;
  const researchSaved =
    completed || state?.phase === "greeting" || state?.phase === "ready";
  const StatusIcon = completed
    ? Check
    : failure || offline || (state?.status === "awaiting_model" && !working)
      ? CircleAlert
      : LoaderCircle;
  const welcomeHref =
    completed && ceo && state?.conversationId
      ? createChatWorkspaceHref({
          kind: "direct",
          agentId: ceo.id,
          conversationId: state.conversationId,
        })
      : undefined;

  return (
    <section
      className="company-initialization-status"
      aria-label="Company preparation"
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
              : completed
                ? welcomeHref
                  ? "Open your welcome whenever you’re ready."
                  : "Your welcome is ready. Reload its details to open it."
                : state?.status === "awaiting_model" && !working
                  ? "Continue preparation when you’re ready. You can use the rest of your workspace."
                  : "You can keep working while your CEO prepares your welcome.")}
        </p>
        <ol
          className="company-initialization-status__stages"
          aria-label="Preparation stages"
        >
          <li
            data-complete={researchSaved || undefined}
            aria-current={!researchSaved ? "step" : undefined}
          >
            {researchSaved ? <Check size={12} aria-hidden="true" /> : null}{" "}
            Research
          </li>
          <li
            data-complete={completed || undefined}
            aria-current={researchSaved && !completed ? "step" : undefined}
          >
            {completed ? <Check size={12} aria-hidden="true" /> : null} Welcome
          </li>
        </ol>
      </div>
      <div className="company-initialization-status__actions">
        {initialization.canContinue ? (
          <button
            type="button"
            disabled={working}
            onClick={() => void initialization.continuePreparation()}
          >
            {working
              ? completed
                ? "Loading…"
                : "Preparing…"
              : completed
                ? "Reload welcome"
                : failure
                  ? "Try again"
                  : "Continue preparation"}
          </button>
        ) : null}
        {(failure || offline) && ceo ? (
          <Link to={`/agents/${ceo.id}?tab=settings`}>Review setup</Link>
        ) : null}
        {welcomeHref ? (
          <Link to={welcomeHref} onClick={initialization.dismiss}>
            Open welcome
          </Link>
        ) : null}
        {completed ? (
          <button
            type="button"
            className="company-initialization-status__dismiss"
            aria-label="Dismiss preparation status"
            onClick={initialization.dismiss}
          >
            <X size={16} aria-hidden="true" />
          </button>
        ) : null}
      </div>
    </section>
  );
}
