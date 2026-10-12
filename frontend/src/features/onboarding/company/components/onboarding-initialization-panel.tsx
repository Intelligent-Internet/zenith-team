import { useEffect, useState } from "react";
import { skipToken } from "@reduxjs/toolkit/query";
import { useGetInitializationActivityQuery } from "@/features/onboarding/api/initialization-activity-api";
import { useGetRunDetailsQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { toolActivities } from "@/features/onboarding/model/initialization-activity";
import { useCompanyInitialization } from "./company-initialization-provider";
import { OnboardingResearchCharacter } from "./onboarding-research-character";
import { OnboardingInitializationActivity } from "./onboarding-initialization-activity";
import { OnboardingActions } from "./onboarding-actions";
import { OnboardingResearchSources } from "./onboarding-research-sources";
import { Check, CircleAlert } from "@/shared/ui/icons";
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
  const state = initialization?.state;
  const runDetails = useGetRunDetailsQuery(
    state?.runId ? { companyId, runId: state.runId } : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const activityQuery = useGetInitializationActivityQuery(
    state?.runId && state.conversationId && state.phase !== "ready"
      ? { companyId, conversationId: state.conversationId, runId: state.runId }
      : skipToken,
  );
  const activities = toolActivities(activityQuery.currentData?.entries ?? []);
  if (!initialization) return null;
  const { ceo, ceoName, needsModel, failure, offline, statusText } =
    initialization;
  const saved = state?.phase === "greeting" || state?.phase === "ready";
  const paused = Boolean(failure || offline);
  const motion = initialization.completed
    ? "ready"
    : paused ||
        needsModel ||
        (state?.status !== "running" && state?.status !== "completed")
      ? "idle"
      : saved || state?.status === "completed"
        ? "building"
        : activityQuery.currentData?.unavailable
          ? "idle"
          : activities.some((item) => item.status === "in_progress")
            ? "searching"
            : activities.length
              ? "understanding"
              : "searching";
  const activeRun =
    state?.runId === runDetails.currentData?.id &&
    !needsModel &&
    state?.status !== "completed" &&
    state?.status !== "failed"
      ? runDetails.currentData
      : undefined;

  return (
    <div className="onboarding-initialization">
      <OnboardingResearchCharacter motion={motion} paused={paused} />
      <div className="onboarding-initialization__heading">
        <h1>
          {initialization.completed
            ? `${ceoName} is ready`
            : "Getting to know your business"}
        </h1>
        <p
          className="onboarding-initialization__status"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {statusText}
        </p>
        {activeRun ? (
          <InitializationElapsedTime
            key={activeRun.id}
            createdAt={activeRun.createdAt}
            attempt={activeRun.attempt}
            label={saved ? "Welcome" : "Research"}
          />
        ) : null}
        <p className="onboarding-initialization__purpose">
          Your CEO is preparing your business context and welcome. You can open
          your workspace while this continues.
        </p>
      </div>
      <ol
        className="onboarding-initialization__phases"
        aria-label="Preparation stages"
      >
        {["Research", "Welcome"].map((label, index) => {
          const complete = index === 0 ? saved : initialization.completed;
          const current = !complete && index === (saved ? 1 : 0);
          return (
            <li
              key={label}
              aria-current={current ? "step" : undefined}
              data-complete={complete || undefined}
            >
              {complete ? <Check size={15} aria-hidden="true" /> : null}
              {label}
              {complete ? <span className="sr-only"> complete</span> : null}
            </li>
          );
        })}
      </ol>
      {ceo?.binding ? (
        <OnboardingResearchSources
          companyId={companyId}
          runtimeId={ceo.binding.runtime.id}
        />
      ) : null}
      <OnboardingInitializationActivity
        activities={activities}
        running={!paused && state?.status === "running"}
        unavailable={activityQuery.currentData?.unavailable ?? false}
        waitingText={
          saved
            ? "Your business context is saved. Your welcome will be available when it is ready."
            : "Source checks appear here as research progresses."
        }
      />
      {failure ? (
        <div className="onboarding-initialization__error" role="alert">
          <CircleAlert size={18} />
          <p>{failure}</p>
        </div>
      ) : null}
      {offline ? (
        <p className="onboarding-initialization__note">
          {initialization.waitingForStart
            ? "Open Codex on your connected computer, then choose Continue preparation."
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
              ? "Preparing…"
              : failure
                ? "Try again"
                : state?.phase === "not_started"
                  ? "Start preparation"
                  : "Continue preparation"}
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
        <p className="onboarding-initialization__note">
          Your preparation must be saved before you enter the workspace.
        </p>
      ) : null}
      {workspaceEntryError ? (
        <p className="onboarding-initialization__error" role="alert">
          {workspaceEntryError}
        </p>
      ) : null}
    </div>
  );
}

function InitializationElapsedTime({
  createdAt,
  attempt,
  label,
}: {
  createdAt: string;
  attempt: number;
  label: string;
}) {
  const [now, setNow] = useState(Date.now);
  const createdAtMs = Date.parse(createdAt);

  useEffect(() => {
    if (!Number.isFinite(createdAtMs)) return;
    let interval: number | undefined;
    const sync = () => {
      window.clearInterval(interval);
      if (document.hidden) return;
      setNow(Date.now());
      interval = window.setInterval(() => setNow(Date.now()), 1000);
    };
    sync();
    document.addEventListener("visibilitychange", sync);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", sync);
    };
  }, [createdAtMs]);

  if (!Number.isFinite(createdAtMs)) return null;
  const elapsedSeconds = Math.max(0, Math.floor((now - createdAtMs) / 1000));
  const seconds = elapsedSeconds % 60;
  const minutes = Math.floor(elapsedSeconds / 60) % 60;
  const hours = Math.floor(elapsedSeconds / 3600);
  const duration = hours
    ? `${hours}h ${minutes}m ${String(seconds).padStart(2, "0")}s`
    : elapsedSeconds >= 60
      ? `${minutes}m ${String(seconds).padStart(2, "0")}s`
      : `${seconds}s`;

  return (
    <p
      className="onboarding-initialization__elapsed"
      role="timer"
      aria-live="off"
      title="Time in this attempt, including waiting."
    >
      {label}
      {attempt > 1 ? ` attempt ${attempt}` : ""} ·{" "}
      <time dateTime={`PT${elapsedSeconds}S`}>{duration} elapsed</time>
    </p>
  );
}
