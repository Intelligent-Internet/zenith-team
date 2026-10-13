import { useEffect, useRef, useState } from "react";
import { detectDesktopPlatform } from "@/app/desktop/desktop-download.service";
import { shouldUseDesktopHashRouter } from "@/app/desktop/desktop-environment.service";
import type { SessionSnapshot } from "@/app/session/session";
import {
  WorkspaceAppearanceProvider,
  useWorkspaceAppearance,
} from "@/app/appearance/workspace-appearance-provider";
import { logoutBoardlySession } from "@/features/auth/model/logout";
import { OnboardingCompanyForm } from "@/features/onboarding/company/components/onboarding-company-form";
import { OnboardingConnectionsPanel } from "@/features/onboarding/company/components/onboarding-connections-panel";
import { OnboardingInitializationPanel } from "@/features/onboarding/company/components/onboarding-initialization-panel";
import { OnboardingTeamPanel } from "@/features/onboarding/company/components/onboarding-team-panel";
import { OnboardingIntroduction } from "@/features/onboarding/company/components/onboarding-introduction";
import { OnboardingProgress } from "@/features/onboarding/company/components/onboarding-progress";
import { CeoAgentSetupPanel } from "@/features/agent-setup/components/ceo-agent-setup-panel";
import { useOnboardingController } from "@/features/onboarding/hooks/use-onboarding-controller";
import {
  onboardingScreens,
  type OnboardingScreen,
} from "@/features/onboarding/model/onboarding-flow";
import { FoldAvatarMotionContext } from "@/shared/ui/agent-avatar/fold-avatar";
import { AppTitlebar } from "@/shared/ui/app-titlebar";
import "@/app/shell/workspace-theme.css";
import "@/features/onboarding/styles/onboarding.css";
import "@/features/onboarding/styles/onboarding-motion.css";

type OnboardingPageProps = {
  session: SessionSnapshot;
  creatingCompany?: boolean;
};

// One entry per screen, so the copy is read rather than assembled out of
// nested ternaries at the point of rendering.
const screenCopy: Record<
  Exclude<OnboardingScreen, "intro">,
  { title: string; intro: string }
> = {
  company: {
    title: "Let’s bring your idea to life.",
    intro:
      "Tell us about your company. Andrew researches your site and anything you attach in the background, then writes it up in your Company Wiki.",
  },
  agent: {
    title: "Your idea is saved. Let’s connect Andrew.",
    intro: "Set up Codex so Andrew can work with your company context.",
  },
  connections: {
    title: "Choose your plugin connections",
    intro:
      "Choose Gmail, Drive, or GitHub for Andrew. You can skip this step and connect plugins later.",
  },
  initializing: {
    title: "Getting your team ready.",
    intro: "Research runs in the background while you get started.",
  },
  team: {
    title: "A little team. A big beginning.",
    intro: "Choose your teammates, then make them your own.",
  },
};

export function OnboardingPage({
  session,
  creatingCompany = false,
}: OnboardingPageProps) {
  return (
    <WorkspaceAppearanceProvider
      userId={session.userId ?? undefined}
      companyId={session.company?.id}
    >
      <FoldAvatarMotionContext.Provider value={true}>
        <OnboardingContent
          session={session}
          creatingCompany={creatingCompany}
        />
      </FoldAvatarMotionContext.Provider>
    </WorkspaceAppearanceProvider>
  );
}

function OnboardingContent({
  session,
  creatingCompany = false,
}: OnboardingPageProps) {
  const { preferences } = useWorkspaceAppearance();
  const onboarding = useOnboardingController(session, { creatingCompany });
  const { screen } = onboarding;
  const companyId = session.company?.id;
  const firstName = session.displayName?.trim().split(/\s+/)[0];
  const desktopMac =
    shouldUseDesktopHashRouter() && detectDesktopPlatform() === "macos";
  const direction = useStepDirection(screen);
  const scrollRef = useRef<HTMLElement>(null);

  // A new step starts at its own top, not wherever the last one was scrolled.
  useEffect(() => {
    scrollRef.current?.scrollTo({ top: 0 });
  }, [screen]);

  return (
    <main
      ref={scrollRef}
      className="boardly-workspace-theme workspace-onboarding"
      data-boardly-theme={preferences.themeId}
      data-desktop-mac={desktopMac || undefined}
      aria-label="Zenith Team onboarding"
    >
      <AppTitlebar
        className="workspace-onboarding__titlebar"
        onLogout={logoutBoardlySession}
        profileContentClassName="!h-[156px] !w-[min(280px,calc(100vw-24px))]"
        profileSurfaceClassName="workspace-profile-menu"
        showProfileFacts={false}
        showSettingsItems={false}
      />
      {screen === "intro" ? (
        <div
          key={screen}
          className="onboarding-stage"
          data-direction={direction}
        >
          <OnboardingIntroduction
            firstName={firstName}
            onCreateCompany={onboarding.continueFromIntroduction}
          />
        </div>
      ) : (
        <div className="workspace-onboarding__frame">
          <OnboardingProgress current={screen} />
          <section
            key={screen}
            className="onboarding-stage onboarding-step"
            data-direction={direction}
            data-width={screen === "team" ? "wide" : "narrow"}
          >
            {screen !== "initializing" ? (
              <header className="onboarding-step__header">
                <h1>
                  {creatingCompany
                    ? "Create a new company"
                    : screenCopy[screen].title}
                </h1>
                <p>{screenCopy[screen].intro}</p>
              </header>
            ) : null}
            <div className="onboarding-step__body">
              {screen === "company" ? (
                <OnboardingCompanyForm
                  draft={onboarding.draft}
                  disabled={onboarding.isSubmitting}
                  errorMessage={onboarding.errorMessage}
                  noticeMessage={onboarding.noticeMessage}
                  referenceFiles={onboarding.referenceFiles}
                  onAddFiles={onboarding.addReferenceFiles}
                  onBack={
                    creatingCompany
                      ? onboarding.cancelCompanyCreation
                      : onboarding.returnToIntroduction
                  }
                  onChange={onboarding.updateDraft}
                  onRemoveFile={onboarding.removeReferenceFile}
                  onSubmit={onboarding.submitCompany}
                />
              ) : companyId ? (
                screen === "initializing" ? (
                  <OnboardingInitializationPanel
                    companyId={companyId}
                    openingWorkspace={onboarding.isSubmitting}
                    workspaceEntryError={onboarding.errorMessage}
                    onOpenWorkspace={onboarding.openWorkspace}
                    onReviewSetup={onboarding.reviewConnection}
                  />
                ) : screen === "team" ? (
                  <OnboardingTeamPanel
                    key={companyId}
                    companyId={companyId}
                    completionError={onboarding.errorMessage}
                    onComplete={onboarding.startInitializing}
                    onBack={onboarding.goToConnections}
                  />
                ) : screen === "connections" ? (
                  <OnboardingConnectionsPanel
                    companyId={companyId}
                    onComplete={onboarding.goToTeam}
                    onSkipCompletion={onboarding.goToTeam}
                  />
                ) : (
                  <CeoAgentSetupPanel
                    companyId={companyId}
                    onComplete={onboarding.goToConnections}
                  />
                )
              ) : (
                <p className="workspace-onboarding__error" role="alert">
                  Company context is unavailable. Sign in again and retry setup.
                </p>
              )}
            </div>
          </section>
        </div>
      )}
    </main>
  );
}

// Steps slide in from the side they come from: forward from the right, back
// from the left. Derived during render so the first frame already knows.
function useStepDirection(screen: OnboardingScreen) {
  const [transition, setTransition] = useState<{
    screen: OnboardingScreen;
    direction: "forward" | "backward";
  }>({ screen, direction: "forward" });
  if (transition.screen !== screen) {
    const direction =
      onboardingScreens.indexOf(screen) <
      onboardingScreens.indexOf(transition.screen)
        ? "backward"
        : "forward";
    setTransition({ screen, direction });
    return direction;
  }
  return transition.direction;
}

export const Component = OnboardingPage;
export default OnboardingPage;
