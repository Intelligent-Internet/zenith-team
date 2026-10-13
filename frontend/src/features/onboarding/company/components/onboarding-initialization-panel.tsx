import { useEffect, useRef } from "react";
import { useCompanyInitialization } from "./company-initialization-provider";
import { OnboardingActions } from "./onboarding-actions";
import { OnboardingResearchSources } from "./onboarding-research-sources";
import { CircleAlert } from "@/shared/ui/icons";
import "@/features/onboarding/styles/onboarding-initialization.css";

export function OnboardingInitializationPanel({
  companyId,
  openingWorkspace,
  workspaceEntryError,
  onOpenWorkspace,
  onReviewSetup,
}: {
  companyId: string;
  openingWorkspace: boolean;
  workspaceEntryError: string;
  onOpenWorkspace: () => void | Promise<void>;
  onReviewSetup: () => void;
}) {
  const initialization = useCompanyInitialization();
  const entered = useRef(false);
  const resumed = useRef(false);
  const started = initialization?.started ?? false;
  const ready = Boolean(
    initialization?.state &&
    !initialization.working &&
    !initialization.failure &&
    initialization.canContinue,
  );
  const continuePreparation = initialization?.continuePreparation;

  // A reload on this screen resumes the start once, as the first visit does.
  useEffect(() => {
    if (!ready || resumed.current || !continuePreparation) return;
    resumed.current = true;
    void continuePreparation();
  }, [continuePreparation, ready]);

  // A research Run that exists works on the server without this screen, so
  // the founder goes straight to the app. A failed attempt to enter leaves the
  // button below to try again.
  useEffect(() => {
    if (!started || entered.current) return;
    entered.current = true;
    void onOpenWorkspace();
  }, [onOpenWorkspace, started]);

  if (!initialization) return null;
  const { ceo, ceoName, failure, offline, statusText } = initialization;

  return (
    <div className="onboarding-initialization">
      <div className="onboarding-initialization__heading text-center">
        <h1>Starting your company research</h1>
        <p
          className="m-[12px_0_0] text-[length:15px] leading-[1.5] text-[var(--workspace-text-muted)]"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {offline
            ? "Waiting for your computer to reconnect"
            : failure
              ? "Your progress is saved"
              : initialization.working || started
                ? `${ceoName} is getting set up on your computer.`
                : statusText}
        </p>
      </div>
      <ul className="grid gap-[8px] max-w-[440px] m-0 p-0 [list-style:none] text-[var(--workspace-text-muted)] text-[length:14px] leading-[1.5] text-center">
        <li>
          Zenith is researching your company in the background. You can keep
          working and we’ll notify you when it’s done.
        </li>
        <li>The results will appear in the Wiki.</li>
      </ul>
      {ceo?.binding ? (
        <OnboardingResearchSources
          companyId={companyId}
          runtimeId={ceo.binding.runtime.id}
        />
      ) : null}
      {failure ? (
        <div
          className="onboarding-initialization__error flex items-start gap-[10px] max-w-[480px] text-[var(--workspace-text)]"
          role="alert"
        >
          <CircleAlert size={18} />
          <p>{failure}</p>
        </div>
      ) : null}
      {offline ? (
        <p className="m-0 text-[var(--workspace-text-muted)] text-[length:12px] leading-[1.6] text-center">
          {initialization.waitingForStart
            ? "Open Codex on your connected computer, then choose Start research."
            : "Open Codex and keep Zenith Team running on your connected computer. We’ll continue automatically."}
        </p>
      ) : null}
      <OnboardingActions align="center">
        {failure || offline ? (
          <button
            type="button"
            className="onboarding-button onboarding-button--secondary"
            onClick={onReviewSetup}
          >
            Review Codex setup
          </button>
        ) : null}
        {initialization.canContinue ? (
          <button
            type="button"
            className="onboarding-button onboarding-button--secondary"
            disabled={initialization.working}
            onClick={() => void initialization.continuePreparation()}
          >
            {initialization.working
              ? "Starting…"
              : failure
                ? "Try again"
                : "Start research"}
          </button>
        ) : null}
        <button
          type="button"
          className="onboarding-button workspace-onboarding__submit"
          disabled={!initialization.hasReceipt || openingWorkspace}
          onClick={() => void onOpenWorkspace()}
        >
          {openingWorkspace ? "Opening workspace…" : "Open workspace"}
        </button>
      </OnboardingActions>
      {!initialization.hasReceipt ? (
        <p className="m-0 text-[var(--workspace-text-muted)] text-[length:12px] leading-[1.6] text-center">
          Your research must be saved before you enter the workspace.
        </p>
      ) : null}
      {workspaceEntryError ? (
        <p
          className="onboarding-initialization__error flex items-start gap-[10px] max-w-[480px] text-[var(--workspace-text)]"
          role="alert"
        >
          {workspaceEntryError}
        </p>
      ) : null}
    </div>
  );
}
