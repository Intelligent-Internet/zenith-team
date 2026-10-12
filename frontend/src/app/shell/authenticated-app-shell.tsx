import { useCallback, useState, useSyncExternalStore } from "react";
import { Outlet, useLocation } from "react-router";

import { detectDesktopPlatform } from "@/app/desktop/desktop-download.service";
import { shouldUseDesktopHashRouter } from "@/app/desktop/desktop-environment.service";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { DiscussionProvider } from "@/features/discussions/components/discussion-provider";
import { CompanyInitializationStatus } from "@/features/onboarding/company/components/company-initialization-status";
import { AgentAvatarProvider } from "@/features/agent-avatar/agent-avatar-provider";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { RuntimeModelPreloader } from "@/features/agent-setup/components/runtime-model-preloader";
import {
  WorkspaceBottomNavigation,
  WorkspaceRail,
} from "@/app/shell/workspace-navigation";
import { WorkspaceUtilityHeader } from "@/app/shell/workspace-utility-header";
import { WorkspacePersonalMenu } from "@/app/shell/workspace-personal-menu";
import {
  WorkspaceAppearanceProvider,
  useWorkspaceAppearance,
} from "@/app/appearance/workspace-appearance-provider";
import { MailboxRealtimeController } from "@/features/mailbox/components/mailbox-realtime-controller";
import { PluginInstallFlow } from "@/features/settings/components/plugin-install-flow";
import { WorkCompletionController } from "@/features/company-plan/components/work-completion-controller";
import { TutorialTourProvider } from "@/features/tutorial-tour/tutorial-tour-context";
import { FirstRunTour } from "@/features/tutorial-tour/components/first-run-tour";
import { ChatSessionMemoryProvider } from "@/shared/chat/chat-session-memory";
import { cx } from "@/shared/lib/cx";
import {
  WorkspaceLayoutProvider,
  type WorkspaceLayoutOptions,
} from "@/app/shell/workspace-layout-context";
import "@/features/chat-panel/styles/chat-surfaces.css";
import "@/app/shell/workspace-theme.css";
import canvasStyles from "./workspace-canvas.module.css";
import { WorkspaceSidebarProvider } from "./workspace-page-sidebar";

const mobileNavigationQuery = "(max-width: 899px)";

function subscribeToNavigationLayout(onChange: () => void) {
  const query = window.matchMedia(mobileNavigationQuery);
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

function isMobileNavigation() {
  return window.matchMedia(mobileNavigationQuery).matches;
}

export function AuthenticatedAppShell() {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const agents = agentsQuery.currentData?.items;
  const ceoAgent =
    agents?.find((agent) => !agent.archivedAt && agent.reportsTo === null) ??
    agents?.find((agent) => agent.reportsTo === null);

  return (
    <AgentAvatarProvider agents={agents} ceoAgentId={ceoAgent?.id}>
      {companyId ? <RuntimeModelPreloader companyId={companyId} /> : null}
      <WorkspaceAppearanceProvider
        userId={session.userId ?? undefined}
        companyId={session.company?.id}
      >
        <TutorialTourProvider
          key={`${session.userId ?? "anonymous"}:${session.company?.id ?? "no-company"}`}
          userId={session.userId}
          companyId={session.company?.id}
        >
          <WorkspaceShellContent />
        </TutorialTourProvider>
      </WorkspaceAppearanceProvider>
    </AgentAvatarProvider>
  );
}

function WorkspaceShellContent() {
  const mobileNavigation = useSyncExternalStore(
    subscribeToNavigationLayout,
    isMobileNavigation,
  );
  const { preferences } = useWorkspaceAppearance();
  const [, setWorkspaceLayoutOptions] = useState<WorkspaceLayoutOptions>({});
  const [sidebarViewport, setSidebarViewport] = useState<HTMLElement | null>(
    null,
  );
  const location = useLocation();
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const chatDraftStorageScope =
    session.userId && companyId
      ? JSON.stringify([session.userId, companyId])
      : undefined;
  const showTeam = Boolean(session.company?.id);
  const isWhiteboardRoute = /^\/whiteboards\/[^/]+/.test(location.pathname);
  const isProjectCanvasRoute =
    /^\/projects\/[^/]+\/artifacts\/?$/.test(location.pathname) &&
    new URLSearchParams(location.search).get("view") !== "gallery";
  const [fullscreenCanvasPath, setFullscreenCanvasPath] = useState<
    string | null
  >(null);
  const canvasFullscreen =
    isProjectCanvasRoute && fullscreenCanvasPath === location.pathname;
  const showWorkspaceChrome = !canvasFullscreen;
  const toggleCanvasFullscreen = useCallback(() => {
    setFullscreenCanvasPath((current) =>
      current === location.pathname ? null : location.pathname,
    );
  }, [location.pathname]);
  const isDesktopRuntime = shouldUseDesktopHashRouter();
  const desktopMac = isDesktopRuntime && detectDesktopPlatform() === "macos";

  return (
    <div
      className="boardly-workspace-theme"
      data-boardly-theme={preferences.themeId}
      data-desktop-mac={desktopMac || undefined}
    >
      <ChatSessionMemoryProvider
        key={chatDraftStorageScope ?? companyId}
        storageScope={chatDraftStorageScope}
      >
        <DiscussionProvider>
          <WorkspaceSidebarProvider
            key={companyId}
            companyId={companyId}
            desktop={!mobileNavigation}
            viewport={sidebarViewport}
          >
            <div
              className={cx(
                "workspace-shell",
                canvasFullscreen && canvasStyles.fullscreen,
              )}
            >
              <MailboxRealtimeController companyId={companyId} />
              {companyId ? <PluginInstallFlow companyId={companyId} /> : null}
              <FirstRunTour enabled={Boolean(companyId)} />
              {showWorkspaceChrome ? (
                <WorkspaceUtilityHeader
                  companyId={companyId}
                  desktopMac={desktopMac}
                  accountMenu={
                    mobileNavigation ? (
                      <WorkspacePersonalMenu placement="header" />
                    ) : null
                  }
                />
              ) : null}
              <CompanyInitializationStatus />
              <div
                className={cx(
                  "workspace-shell-body",
                  canvasFullscreen && canvasStyles.body,
                )}
              >
                {showWorkspaceChrome ? (
                  <WorkspaceRail
                    pathname={location.pathname}
                    showTeam={showTeam}
                    accountMenu={
                      mobileNavigation ? null : (
                        <WorkspacePersonalMenu placement="sidebar" />
                      )
                    }
                  />
                ) : null}
                <section
                  ref={setSidebarViewport}
                  className={cx(
                    "workspace-shell-content",
                    canvasFullscreen && "workspace-shell-content--canvas",
                    canvasFullscreen && canvasStyles.content,
                  )}
                  data-project-canvas={isProjectCanvasRoute || undefined}
                  aria-label={
                    isProjectCanvasRoute
                      ? "Zenith Team project canvas workspace"
                      : isWhiteboardRoute
                        ? "Zenith Team whiteboard workspace"
                        : "Zenith Team company workspace"
                  }
                >
                  {showTeam && companyId ? (
                    <WorkCompletionController
                      key={chatDraftStorageScope ?? companyId}
                      companyId={companyId}
                    />
                  ) : null}
                  <WorkspaceLayoutProvider
                    setWorkspaceLayoutOptions={setWorkspaceLayoutOptions}
                    canvasFullscreen={canvasFullscreen}
                    toggleCanvasFullscreen={toggleCanvasFullscreen}
                  >
                    <Outlet />
                  </WorkspaceLayoutProvider>
                </section>
              </div>
              {showWorkspaceChrome ? (
                <WorkspaceBottomNavigation
                  pathname={location.pathname}
                  showTeam={showTeam}
                />
              ) : null}
            </div>
          </WorkspaceSidebarProvider>
        </DiscussionProvider>
      </ChatSessionMemoryProvider>
    </div>
  );
}
