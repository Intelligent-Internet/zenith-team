import { getNextRoute } from "@/app/session/session-routing";
import { useEffect, useRef } from "react";
import { useNavigate } from "react-router";
import { detectDesktopPlatform } from "@/shared/desktop/desktop-download.service";
import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { type SessionSnapshot } from "@/shared/session/session";
import {
  WorkspaceAppearanceProvider,
  useWorkspaceAppearance,
} from "@/app/appearance/workspace-appearance-provider";
import { getCeoAgentApiErrorMessage } from "@/shared/api/agent-runtime-api";
import {
  clearAuthContinuation,
  readAuthContinuationReturnPath,
} from "@/features/auth/model/auth-continuation";
import { logoutBoardlySession } from "@/features/auth/model/logout";
import { OnboardingActions } from "@/features/onboarding/company/components/onboarding-actions";
import { AppTitlebar } from "@/app/shell/app-titlebar";
import { ArrowLeft, ArrowRight, RefreshCwIcon } from "@/shared/ui/icons";
import {
  MemberSetupAgents,
  MemberSetupComputers,
} from "../components/member-agent-setup-choices";
import { useMemberAgentSetup } from "../hooks/use-member-agent-setup";
import {
  finishMemberAgentSetup,
  memberAgentSetupPath,
} from "../model/member-agent-setup";
import "@/app/shell/workspace-theme.css";
import "@/features/onboarding/styles/onboarding.css";
import "@/features/settings/styles/settings.css";
import "../styles/member-agent-setup.css";

type MemberAgentSetupPageProps = {
  session: SessionSnapshot;
  companyId: string;
  userId: string;
};

export function MemberAgentSetupPage(props: MemberAgentSetupPageProps) {
  return (
    <WorkspaceAppearanceProvider
      userId={props.userId}
      companyId={props.companyId}
    >
      <MemberAgentSetupContent {...props} />
    </WorkspaceAppearanceProvider>
  );
}

function MemberAgentSetupContent({
  session,
  companyId,
  userId,
}: MemberAgentSetupPageProps) {
  const navigate = useNavigate();
  const { preferences } = useWorkspaceAppearance();
  const setup = useMemberAgentSetup(companyId);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    heading.current?.focus();
  }, [setup.step]);
  const companyName = session.company?.workspaceName ?? "your company";
  const computerStep = setup.step === "computer";
  const canFinish =
    !setup.loading &&
    !setup.loadError &&
    (!setup.agents.length || setup.allSetUp);

  function finish() {
    finishMemberAgentSetup(userId, companyId);
    if (readAuthContinuationReturnPath() === memberAgentSetupPath(companyId))
      clearAuthContinuation();
    navigate(getNextRoute(session), { replace: true });
  }

  async function submit() {
    if (canFinish) return finish();
    if (computerStep) return setup.reviewAgents();
    if (await setup.save()) finish();
  }

  return (
    <main
      className="boardly-workspace-theme workspace-onboarding member-agent-setup"
      data-boardly-theme={preferences.themeId}
      data-desktop-mac={
        (shouldUseDesktopHashRouter() && detectDesktopPlatform() === "macos") ||
        undefined
      }
      aria-label="Set up your company agents"
    >
      <AppTitlebar
        className="workspace-onboarding__titlebar"
        onLogout={logoutBoardlySession}
        showProfileFacts={false}
        showSettingsItems={false}
      />
      <div className="workspace-onboarding__frame">
        <nav
          className="onboarding-progress grid [justify-items:center] gap-[10px]"
          aria-label="Agent setup progress"
        >
          <p
            className="onboarding-progress__label flex items-baseline gap-[8px] m-0 text-workspace-muted text-[length:12px] font-[500]"
            aria-live="polite"
          >
            <span>Step {computerStep ? 1 : 2} of 2</span>
            {computerStep ? "Your computer" : "Your agents"}
          </p>
          <ol className="flex gap-[6px] m-0 p-0 [list-style:none]">
            <li
              className="onboarding-progress__step"
              data-state={computerStep ? "current" : "complete"}
              aria-current={computerStep ? "step" : undefined}
            >
              <span className="sr-only">Choose a computer</span>
            </li>
            <li
              className="onboarding-progress__step"
              data-state={computerStep ? "upcoming" : "current"}
              aria-current={!computerStep ? "step" : undefined}
            >
              <span className="sr-only">Set up agents</span>
            </li>
          </ol>
        </nav>
        <section className="onboarding-step">
          <header className="onboarding-step__header">
            <p className="member-agent-setup__company">
              You’ve joined {companyName}
            </p>
            <h1 ref={heading} tabIndex={-1}>
              {computerStep
                ? "Where will your agents work?"
                : "Bring your team to your computer."}
            </h1>
            <p>
              {computerStep
                ? "Choose one of your computers for the company’s agents. You can set this up later and explore the company first."
                : `Choose the agents you want to run on ${setup.computer?.name ?? "your computer"}. Their names and roles are shared with everyone in ${companyName}.`}
            </p>
          </header>
          <div className="grid gap-[16px]" aria-busy={setup.saving}>
            <div className="member-agent-setup__toolbar">
              <h2>{computerStep ? "Your computers" : "Company agents"}</h2>
              <button
                type="button"
                className="onboarding-button onboarding-button--ghost"
                disabled={setup.refreshing || setup.saving}
                onClick={() => void setup.refresh()}
              >
                <RefreshCwIcon size={16} aria-hidden="true" />
                {setup.refreshing ? "Refreshing…" : "Refresh"}
              </button>
            </div>
            {setup.loading ? (
              <p role="status">Loading your agents and computers…</p>
            ) : setup.loadError ? (
              <p className="member-agent-setup__error" role="alert">
                {getCeoAgentApiErrorMessage(
                  setup.loadError,
                  "Your setup could not be loaded. Refresh to try again.",
                )}
              </p>
            ) : !setup.agents.length ? (
              <MemberSetupAgents setup={setup} />
            ) : computerStep ? (
              <MemberSetupComputers companyId={companyId} setup={setup} />
            ) : (
              <MemberSetupAgents setup={setup} />
            )}
            {setup.error ? (
              <p className="member-agent-setup__error" role="alert">
                {setup.error}
              </p>
            ) : null}
            {setup.saving ? (
              <p role="status">Setting up your selected agents…</p>
            ) : null}
            <OnboardingActions
              start={
                !computerStep ? (
                  <button
                    className="onboarding-button onboarding-button--ghost"
                    type="button"
                    disabled={setup.saving}
                    onClick={() => setup.setStep("computer")}
                  >
                    <ArrowLeft size={16} aria-hidden="true" />
                    Back
                  </button>
                ) : undefined
              }
            >
              <button
                className="onboarding-button onboarding-button--ghost"
                type="button"
                disabled={setup.saving}
                onClick={finish}
              >
                Set up later
              </button>
              <button
                className="onboarding-button workspace-onboarding__submit"
                type="button"
                disabled={
                  setup.loading ||
                  !!setup.loadError ||
                  setup.saving ||
                  (!canFinish &&
                    (computerStep ? !setup.computer : !setup.selectedCount))
                }
                onClick={() => void submit()}
              >
                {setup.saving
                  ? "Setting up…"
                  : canFinish
                    ? "Open company"
                    : computerStep
                      ? "Continue"
                      : `Set up ${setup.selectedCount} ${setup.selectedCount === 1 ? "agent" : "agents"}`}
                <ArrowRight size={17} aria-hidden="true" />
              </button>
            </OnboardingActions>
          </div>
        </section>
      </div>
    </main>
  );
}
