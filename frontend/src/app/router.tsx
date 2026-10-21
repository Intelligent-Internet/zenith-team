import { getNextRoute } from "@/app/session/session-routing";
import { InvitationDesktopBridge } from "@/features/company-invitations/routes/invitation-desktop-bridge";
import { hasPendingCompanyOnboarding } from "@/features/onboarding/model/onboarding-flow";
import { CompanyInitializationProvider } from "@/features/onboarding/company/components/company-initialization-provider";
import { AppErrorPage } from "@/app/routes/app-error";
import { InvitationPage } from "@/features/company-invitations/routes/invitation-page";
import { ArtifactDesktopBridge } from "@/features/run-deliverables/routes/artifact-desktop-bridge";
import {
  clearAuthContinuation,
  getAuthReturnRoute,
  readAuthContinuationReturnPath,
  writeAuthContinuation,
} from "@/features/auth/model/auth-continuation";
import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import {
  createBrowserRouter,
  createHashRouter,
  createRoutesFromElements,
  Navigate,
  Outlet,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
} from "react-router";
import { RouterProvider } from "react-router/dom";
import { SkillFileDraftGuard } from "@/state/skill-file-draft-guard";
import { WorkspaceNavigationGuard } from "@/app/shell/workspace-navigation-guard";
import {
  reportDesktopDiagnostic,
  shouldUseDesktopHashRouter,
  subscribeToDesktopGoogleOAuthCallback,
} from "@/shared/desktop/desktop-environment.service";
import { DesktopMenuCommandRouter } from "@/app/desktop/desktop-menu-command-router";
import { DesktopNativeThemeAttributes } from "@/app/desktop/desktop-native-theme";
import { DesktopNativeContextMenuController } from "@/app/desktop/desktop-native-context-menu";
import { DesktopUpdateDialog } from "@/app/desktop/desktop-update-dialog";
import {
  refreshSessionSnapshot,
  unauthenticatedSession,
  type SessionSnapshot,
} from "@/shared/session/session";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { RequirePermission } from "@/shared/permissions";
import { clearInvalidClientSessionState } from "@/app/session/client-session-state";
import { WorkspaceAccessState } from "@/features/common/components/workspace-page-frame";
import { getStoredAccessToken } from "@/lib/auth-token";
import { isAuthenticationFailure } from "@/lib/api-client";
import {
  readStorageItem,
  writeStorageItem,
} from "@/shared/lib/browser-storage";
import { deployment } from "@/shared/lib/deployment";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

const AuthenticatedAppShell = lazy(() =>
  import("@/app/shell/authenticated-app-shell").then((module) => ({
    default: module.AuthenticatedAppShell,
  })),
);
const PublicArtifactShell = lazy(() =>
  import("@/app/shell/public-artifact-shell").then((module) => ({
    default: module.PublicArtifactShell,
  })),
);
const NotificationRealtimeController = lazy(() =>
  import("@/features/notifications/components/notification-realtime-controller").then(
    (module) => ({
      default: module.NotificationRealtimeController,
    }),
  ),
);
const RealtimeSyncController = lazy(() =>
  import("@/app/shell/realtime-sync-controller").then((module) => ({
    default: module.RealtimeSyncController,
  })),
);
const HuddleAlertController = lazy(() =>
  import("@/features/huddles/huddle-alert-controller").then((module) => ({
    default: module.HuddleAlertController,
  })),
);
const HuddleWindowPage = lazy(() =>
  import("@/features/huddles/routes/huddle-window-page").then((module) => ({
    default: module.HuddleWindowPage,
  })),
);
const HomePage = lazy(() =>
  import("@/app/routes/home").then((module) => ({
    default: module.HomePage,
  })),
);
const CalendarPage = lazy(() =>
  import("@/features/calendar/calendar-page").then((module) => ({
    default: module.CalendarPage,
  })),
);
const AnalyticsPage = lazy(() =>
  import("@/features/analytics/routes/analytics-page").then((module) => ({
    default: module.AnalyticsPage,
  })),
);
const LegacyUsageRedirect = lazy(() =>
  import("@/features/analytics/routes/legacy-usage-redirect").then(
    (module) => ({
      default: module.LegacyUsageRedirect,
    }),
  ),
);
const MailboxPage = lazy(() =>
  import("@/features/mailbox/routes/mailbox-page").then((module) => ({
    default: module.MailboxPage,
  })),
);
const PublicLandingPage = lazy(() =>
  import("@/features/public-site/routes/public-landing-page").then(
    (module) => ({
      default: module.PublicLandingPage,
    }),
  ),
);
const PrivacyPolicyPage = lazy(() =>
  import("@/features/public-site/routes/privacy-policy-page").then(
    (module) => ({
      default: module.PrivacyPolicyPage,
    }),
  ),
);
const TermsOfServicePage = lazy(() =>
  import("@/features/public-site/routes/terms-of-service-page").then(
    (module) => ({
      default: module.TermsOfServicePage,
    }),
  ),
);
const NotFoundPage = lazy(() =>
  import("@/app/routes/not-found").then((module) => ({
    default: module.NotFoundPage,
  })),
);
const ProjectsPage = lazy(() =>
  import("@/features/company-plan/routes/projects-page").then((module) => ({
    default: module.ProjectsPage,
  })),
);
const WikiPage = lazy(() =>
  import("@/features/company-wiki/wiki-page").then((module) => ({
    default: module.WikiPage,
  })),
);
const AgentsPage = lazy(() =>
  import("@/features/agents/routes/agents-page").then((module) => ({
    default: module.AgentsPage,
  })),
);
const TeamPage = lazy(() =>
  import("@/features/agents/routes/team-page").then((module) => ({
    default: module.TeamPage,
  })),
);
const AssetsPage = lazy(() =>
  import("@/app/routes/assets").then((module) => ({
    default: module.AssetsPage,
  })),
);
const AssetGroupDetailPage = lazy(() =>
  import("@/app/routes/assets").then((module) => ({
    default: module.AssetGroupDetailPage,
  })),
);
const CompanyPlanPage = lazy(() =>
  import("@/app/routes/company-plan").then((module) => ({
    default: module.CompanyPlanPage,
  })),
);
const ApprovalsPage = lazy(() =>
  import("@/app/routes/approvals").then((module) => ({
    default: module.ApprovalsPage,
  })),
);
const ScheduledTasksPage = lazy(() =>
  import("@/features/scheduled/routes/scheduled-tasks-page").then((module) => ({
    default: module.ScheduledTasksPage,
  })),
);
const ScheduledTaskEditorPage = lazy(() =>
  import("@/features/scheduled/routes/scheduled-task-editor-page").then(
    (module) => ({ default: module.ScheduledTaskEditorPage }),
  ),
);
const ScheduledTaskDetailPage = lazy(() =>
  import("@/features/scheduled/routes/scheduled-task-detail-page").then(
    (module) => ({ default: module.ScheduledTaskDetailPage }),
  ),
);
const RunResultPage = lazy(() =>
  import("@/app/routes/run-result").then((module) => ({
    default: module.RunResultPage,
  })),
);
const PublicArtifactPage = lazy(() =>
  import("@/features/run-deliverables/routes/public-artifact-page").then(
    (module) => ({ default: module.PublicArtifactPage }),
  ),
);
const SharedArtifactPage = lazy(() =>
  import("@/features/run-deliverables/routes/shared-artifact-page").then(
    (module) => ({
      default: module.SharedArtifactPage,
    }),
  ),
);
const WorkflowsPage = lazy(() =>
  import("@/features/workflows/routes/workflows-page").then((module) => ({
    default: module.WorkflowsPage,
  })),
);
const SettingsHubPage = lazy(() =>
  import("@/app/routes/settings").then((module) => ({
    default: module.SettingsHubPage,
  })),
);
const LoginPage = lazy(() =>
  import("@/features/auth/routes/login-page").then((module) => ({
    default: module.LoginPage,
  })),
);
const InvitationPreviewPage = lazy(() =>
  import("@/features/company-invitations/routes/invitation-preview-page").then(
    (module) => ({ default: module.InvitationPreviewPage }),
  ),
);
const GoogleOAuthCallbackPage = lazy(() =>
  import("@/features/auth/routes/google-oauth-callback-page").then(
    (module) => ({
      default: module.GoogleOAuthCallbackPage,
    }),
  ),
);
const OnboardingPage = lazy(() =>
  import("@/features/onboarding/routes/onboarding-page").then((module) => ({
    default: module.OnboardingPage,
  })),
);
const MemberAgentSetupPage = lazy(() =>
  import("@/features/company-invitations/routes/member-agent-setup-page").then(
    (module) => ({
      default: module.MemberAgentSetupPage,
    }),
  ),
);
function RouteFallback() {
  return (
    <div className="workspace-loading-screen">
      <div className="flex flex-1 flex-col items-center justify-center p-[64px_24px_48px] text-center">
        <div className="workspace-loading-screen__character" aria-hidden="true">
          <FoldAvatar agentId="workspace-loading" paletteIndex={1} size={144} />
        </div>
        <h1 className="m-0 text-[length:20px] font-[600] leading-[28px] tracking-[-0.02em]">
          Zenith Team
        </h1>
        <p
          className="m-[12px_0_0] text-[var(--loading-muted)] text-[length:13px] leading-[1.5] tracking-[-0.12px]"
          role="status"
          aria-live="polite"
        >
          Preparing your workspace
        </p>
        <div
          className="workspace-loading-screen__progress"
          aria-hidden="true"
        />
      </div>
      <p className="m-0 p-[0_24px_max(26px,_env(safe-area-inset-bottom))] text-[var(--loading-muted)] text-[length:11px] leading-[1.5] tracking-[0.1px] text-center">
        A little space for your next big idea.
      </p>
    </div>
  );
}

function hasStoredAccessToken() {
  return getStoredAccessToken() !== null;
}

export function shouldValidateStoredSession(
  session: SessionSnapshot,
  hasAccessToken = hasStoredAccessToken(),
) {
  return session.status === "authenticated" || hasAccessToken;
}

export function shouldStartWithSessionValidation({
  hasDevSessionOverride,
  hasAccessToken = hasStoredAccessToken(),
  storedSession,
}: {
  hasDevSessionOverride: boolean;
  hasAccessToken?: boolean;
  storedSession: SessionSnapshot;
}) {
  return (
    !hasDevSessionOverride &&
    shouldValidateStoredSession(storedSession, hasAccessToken)
  );
}

function getDevSessionOverride(search: string): SessionSnapshot | null {
  if (!import.meta.env.DEV) {
    return null;
  }

  const params = new URLSearchParams(search);
  const sessionOverride = params.get("zenith_team_qa_session");

  if (sessionOverride === "guest") {
    return unauthenticatedSession;
  }

  if (sessionOverride === "onboarding") {
    return {
      status: "authenticated",
      provider: "password",
      email: "qa-founder@zenith-team.local",
      displayName: "QA Founder",
      user: {
        displayName: "QA Founder",
        role: "Founder",
        timezone: "Asia/Ho_Chi_Minh",
      },
      company: {
        workspaceName: "Zenith Team QA Workspace",
      },
      nextStep: "company",
    };
  }

  if (sessionOverride === "app") {
    return {
      status: "authenticated",
      provider: "password",
      email: "qa-founder@zenith-team.local",
      displayName: "QA Founder",
      user: {
        displayName: "QA Founder",
        role: "Founder",
        timezone: "Asia/Ho_Chi_Minh",
      },
      company: {
        workspaceName: "Zenith Team QA Workspace",
      },
      nextStep: "complete",
    };
  }

  return null;
}

function RedirectToNextRoute({ session }: { session: SessionSnapshot }) {
  return <Navigate to={getAuthReturnRoute(session)} replace />;
}

function AuthenticationRedirect({
  to,
  returnPath,
}: {
  to: string;
  returnPath?: string;
}) {
  const navigate = useNavigate();
  useEffect(() => {
    if (returnPath) writeAuthContinuation(returnPath);
    void navigate(to, { replace: true });
  }, [navigate, returnPath, to]);
  return null;
}

function GuestRoute({
  children,
  session,
}: {
  children: ReactNode;
  session: SessionSnapshot;
}) {
  if (session.status === "authenticated") {
    return <RedirectToNextRoute session={session} />;
  }

  return children;
}

function OnboardingRoute({
  session,
  creatingCompany = false,
}: {
  session: SessionSnapshot;
  creatingCompany?: boolean;
}) {
  if (session.status !== "authenticated") {
    return <Navigate to="/login" replace />;
  }

  if (
    !creatingCompany &&
    session.nextStep === "complete" &&
    !hasPendingCompanyOnboarding(session)
  ) {
    return <RedirectToNextRoute session={session} />;
  }

  return (
    <OnboardingPage
      key={creatingCompany ? "new-company" : "onboarding"}
      session={session}
      creatingCompany={creatingCompany}
    />
  );
}

function MemberAgentSetupRoute({ session }: { session: SessionSnapshot }) {
  const { companyId } = useParams();
  const location = useLocation();
  useEffect(() => {
    if (
      session.status === "authenticated" &&
      readAuthContinuationReturnPath() === location.pathname
    ) {
      clearAuthContinuation();
    }
  }, [location.pathname, session.status]);
  if (session.status !== "authenticated") {
    return (
      <AuthenticationRedirect to="/login" returnPath={location.pathname} />
    );
  }
  if (!companyId || companyId !== session.company?.id || !session.userId) {
    return <Navigate to={getNextRoute(session)} replace />;
  }
  return (
    <MemberAgentSetupPage
      key={`${session.userId}:${companyId}`}
      session={session}
      companyId={companyId}
      userId={session.userId}
    />
  );
}

function AppShellRoute({ session }: { session: SessionSnapshot }) {
  const nextRoute = getNextRoute(session);
  const location = useLocation();
  const [browserArtifactPath, setBrowserArtifactPath] = useState<string | null>(
    null,
  );
  const artifactPath =
    /^\/companies\/[0-9a-f-]{36}\/artifacts\/[0-9a-f-]{36}$/i.test(
      location.pathname,
    );
  const requestedPath = location.pathname + location.search + location.hash;
  const pendingReturnPath = readAuthContinuationReturnPath();
  // Earlier versions stored the item query too; consent belongs to the artifact.
  const storedBrowserArtifactPath = readStorageItem(
    "session",
    "zenithArtifactBrowserPath",
  )?.split(/[?#]/, 1)[0];

  useEffect(() => {
    if (artifactPath && location.state?.artifactInBrowser === true) {
      writeStorageItem(
        "session",
        "zenithArtifactBrowserPath",
        location.pathname,
      );
      setBrowserArtifactPath(location.pathname);
    }
  }, [artifactPath, location.pathname, location.state]);

  useEffect(() => {
    if (nextRoute === "/app" && pendingReturnPath === requestedPath) {
      clearAuthContinuation();
    }
  }, [nextRoute, pendingReturnPath, requestedPath]);

  if (
    !window.zenithTeamDesktop &&
    artifactPath &&
    browserArtifactPath !== location.pathname &&
    location.state?.artifactInBrowser !== true &&
    storedBrowserArtifactPath !== location.pathname
  ) {
    return (
      <ArtifactDesktopBridge
        path={location.pathname + location.search}
        onContinueInBrowser={() => {
          writeStorageItem(
            "session",
            "zenithArtifactBrowserPath",
            location.pathname,
          );
          setBrowserArtifactPath(location.pathname);
        }}
      />
    );
  }

  if (nextRoute !== "/app") {
    const isArtifactRoute =
      /^\/companies\/[0-9a-f-]{36}\/artifacts\/(?:[0-9a-f-]{36}|unowned)$/i.test(
        location.pathname,
      );
    return (
      <AuthenticationRedirect
        to={nextRoute}
        returnPath={isArtifactRoute ? requestedPath : undefined}
      />
    );
  }

  if (location.pathname === "/app" && pendingReturnPath) {
    return <Navigate to={pendingReturnPath} replace />;
  }

  return <AuthenticatedAppShell />;
}

function ScheduleRoute({ children }: { children: ReactNode }) {
  return (
    <RequirePermission
      permission="schedules.manage"
      fallback={
        <WorkspaceAccessState
          description="Scheduled work is managed by Company owners and admins. You can continue with permitted Tasks and conversations from Work and Chat."
          title="Permission required"
        />
      }
    >
      {children}
    </RequirePermission>
  );
}

function LegacyResourcesRouteRedirect() {
  const { hash, search } = useLocation();

  return <Navigate to={`/assets${search}${hash}`} replace />;
}

export function getDesktopRootRouteTarget(
  session: SessionSnapshot,
  isDesktopRuntime: boolean,
) {
  return isDesktopRuntime ? getNextRoute(session) : null;
}

function RootRoute({
  isDesktopRuntime,
  session,
}: {
  isDesktopRuntime: boolean;
  session: SessionSnapshot;
}) {
  const desktopTarget = getDesktopRootRouteTarget(session, isDesktopRuntime);

  if (desktopTarget) {
    return <Navigate to={desktopTarget} replace />;
  }

  // Without a public site the web root is the app itself.
  if (!deployment.publicSiteOrigin) {
    return <Navigate to={getNextRoute(session)} replace />;
  }

  return <PublicLandingPage />;
}

function AppRoutes() {
  const location = useLocation();
  const isPublicArtifactRoute = /^\/share\/artifacts\/[^/]+\/?$/.test(
    location.pathname,
  );
  const isDesktopRuntime = shouldUseDesktopHashRouter();
  const storedSession = useSessionSnapshot();
  const devSessionOverride = getDevSessionOverride(location.search);
  const hasDevSessionOverride = devSessionOverride !== null;
  const session = devSessionOverride ?? storedSession;
  const [sessionRefreshFailed, setSessionRefreshFailed] = useState(false);
  const [sessionRetry, setSessionRetry] = useState(0);
  const [isValidatingSession, setIsValidatingSession] = useState(() =>
    shouldStartWithSessionValidation({
      hasDevSessionOverride,
      storedSession,
    }),
  );
  const publicArtifactWorkspaceAvailable =
    isDesktopRuntime &&
    !isValidatingSession &&
    !sessionRefreshFailed &&
    getNextRoute(session) === "/app";

  useEffect(() => {
    let cancelled = false;

    if (hasDevSessionOverride || !shouldValidateStoredSession(storedSession)) {
      setIsValidatingSession(false);
      setSessionRefreshFailed(false);
      return () => {
        cancelled = true;
      };
    }

    setIsValidatingSession(true);
    setSessionRefreshFailed(false);
    void refreshSessionSnapshot()
      .catch((error: unknown) => {
        if (cancelled) return;
        if (isAuthenticationFailure(error)) {
          return clearInvalidClientSessionState();
        }
        setSessionRefreshFailed(true);
      })
      .finally(() => {
        if (!cancelled) {
          setIsValidatingSession(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [
    hasDevSessionOverride,
    sessionRetry,
    storedSession.email,
    storedSession.nextStep,
    storedSession.status,
  ]);

  useEffect(() => {
    if (!sessionRefreshFailed) return;

    const retryOnConnection = () => setSessionRetry((value) => value + 1);
    window.addEventListener("online", retryOnConnection);
    return () => window.removeEventListener("online", retryOnConnection);
  }, [sessionRefreshFailed]);

  if (isValidatingSession && !isPublicArtifactRoute) {
    return <RouteFallback />;
  }

  if (sessionRefreshFailed && !isPublicArtifactRoute) {
    return (
      <main className="grid min-h-dvh place-items-center bg-background px-6 text-foreground">
        <section
          className="max-w-md rounded-xl border border-border bg-card p-6"
          aria-labelledby="session-reconnect-title"
        >
          <h1 id="session-reconnect-title" className="text-xl font-semibold">
            Reconnect to Zenith Team
          </h1>
          <p
            className="mt-3 text-sm leading-6 text-muted-foreground"
            role="alert"
          >
            We could not check your session. Your saved sign-in and preferences
            are still on this device. Check your connection and try again.
          </p>
          <div className="mt-5 flex flex-wrap gap-3">
            <button
              type="button"
              className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-primary-foreground"
              onClick={() => setSessionRetry((value) => value + 1)}
            >
              Try again
            </button>
            <button
              type="button"
              className="rounded-lg border border-border px-4 py-2 text-sm"
              onClick={() => {
                setSessionRefreshFailed(false);
                void clearInvalidClientSessionState();
              }}
            >
              Sign in again
            </button>
          </div>
        </section>
      </main>
    );
  }

  return (
    <Suspense fallback={<RouteFallback />}>
      {(!isPublicArtifactRoute || publicArtifactWorkspaceAvailable) &&
      session.status === "authenticated" &&
      session.company?.id ? (
        <>
          <RealtimeSyncController companyId={session.company.id} />
          <HuddleAlertController
            key={`${session.userId}:${session.company.id}`}
            companyId={session.company.id}
            companyName={session.company.workspaceName}
          />
          <NotificationRealtimeController key={session.userId} />
        </>
      ) : null}
      <Routes>
        <Route path="/huddles/:huddleId" element={<HuddleWindowPage />} />
        <Route
          path="/huddles/start/:conversationId"
          element={<HuddleWindowPage />}
        />
        <Route
          path="/join/:token"
          element={<InvitationPage session={session} />}
        />
        <Route
          path="/invite/:token"
          element={<InvitationPage session={session} />}
        />
        <Route
          path="/auth/google/callback"
          element={<GoogleOAuthCallbackPage />}
        />
        <Route
          path="/login"
          element={
            <GuestRoute session={session}>
              <LoginPage />
            </GuestRoute>
          }
        />
        <Route
          path="/login/reset"
          element={
            <GuestRoute session={session}>
              <LoginPage />
            </GuestRoute>
          }
        />
        <Route
          path="/onboarding"
          element={<OnboardingRoute session={session} />}
        />
        <Route
          path="/companies/new"
          element={<OnboardingRoute session={session} creatingCompany />}
        />
        <Route
          path="/companies/:companyId/setup-agents"
          element={<MemberAgentSetupRoute session={session} />}
        />
        {/* Onboarding is one route: which screen shows follows the session and
            the founder's progress, never the URL. The old per-step paths are
            redirected so a bookmark or a stale link still lands correctly. */}
        <Route
          path="/onboarding/*"
          element={<Navigate to="/onboarding" replace />}
        />
        {deployment.publicSiteOrigin ? (
          <>
            <Route path="/privacy" element={<PrivacyPolicyPage />} />
            <Route path="/terms" element={<TermsOfServicePage />} />
          </>
        ) : null}
        <Route
          path="/"
          element={
            <RootRoute isDesktopRuntime={isDesktopRuntime} session={session} />
          }
        />
        <Route
          element={
            !isDesktopRuntime ? (
              <Outlet />
            ) : publicArtifactWorkspaceAvailable ? (
              <AuthenticatedAppShell />
            ) : (
              <PublicArtifactShell />
            )
          }
        >
          <Route
            path="/share/artifacts/:token"
            element={<PublicArtifactPage />}
          />
        </Route>
        <Route path="/app" element={<AppShellRoute session={session} />}>
          <Route index element={<HomePage />} />
        </Route>
        <Route element={<AppShellRoute session={session} />}>
          <Route path="more" element={<Navigate to="/app" replace />} />
          <Route path="calendar" element={<CalendarPage />} />
          <Route path="social" element={<SocialSettingsRedirect />} />
          <Route path="analytics" element={<AnalyticsPage />} />
          <Route path="settings/usage" element={<LegacyUsageRedirect />} />
          <Route
            path="activity"
            element={<SettingsTabRedirect tab="activity" />}
          />
          <Route
            path="mailbox"
            element={
              <RequirePermission
                permission="mailbox.manage"
                fallback={
                  <WorkspaceAccessState
                    title="Mailbox access required"
                    description="You don't have permission to use Mailbox and Agent Mail in this company."
                  />
                }
              >
                <MailboxPage />
              </RequirePermission>
            }
          />
          <Route path="settings/*" element={<SettingsHubPage />} />
          <Route path="approvals" element={<ApprovalsPage />} />
          <Route
            path="notifications"
            element={<SettingsTabRedirect tab="notifications" />}
          />
          <Route
            path="scheduled"
            element={
              <ScheduleRoute>
                <ScheduledTasksPage />
              </ScheduleRoute>
            }
          />
          <Route
            path="scheduled/new"
            element={
              <ScheduleRoute>
                <ScheduledTaskEditorPage />
              </ScheduleRoute>
            }
          />
          <Route
            path="scheduled/:scheduleId/edit"
            element={
              <ScheduleRoute>
                <ScheduledTaskEditorPage />
              </ScheduleRoute>
            }
          />
          <Route
            path="scheduled/:scheduleId"
            element={
              <ScheduleRoute>
                <ScheduledTaskDetailPage />
              </ScheduleRoute>
            }
          />
          <Route path="runs/:runId" element={<RunResultPage />} />
          <Route
            path="companies/:companyId/artifacts/:artifactId"
            element={<SharedArtifactPage />}
          />
          <Route
            path="inbox"
            element={<SettingsTabRedirect tab="notifications" />}
          />
          <Route path="agents" element={<AgentsPage />} />
          <Route path="agents/:agentId" element={<AgentsPage />} />
          <Route
            path="agents/:agentId/skills/:skillId"
            element={<AgentsPage />}
          />
          <Route path="team" element={<TeamPage />} />
          <Route path="tasks" element={<CompanyPlanPage />} />
          <Route path="projects" element={<ProjectsPage />} />
          <Route path="wiki" element={<WikiPage />} />
          <Route path="wiki/pages/:pageId" element={<WikiPage />} />
          <Route
            path="projects/:projectId/:section?"
            element={<ProjectsPage />}
          />
          <Route path="assets" element={<AssetsPage />} />
          <Route path="assets/:groupId" element={<AssetGroupDetailPage />} />
          <Route path="resources" element={<LegacyResourcesRouteRedirect />} />
          <Route
            path="resources/*"
            element={<LegacyResourcesRouteRedirect />}
          />
          <Route path="workflows" element={<WorkflowsPage />} />
          <Route path="workflows/:workflowKey" element={<WorkflowsPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    </Suspense>
  );
}

function DesktopGoogleOAuthCallbackRouter() {
  const navigate = useNavigate();

  useEffect(() => {
    return subscribeToDesktopGoogleOAuthCallback((route) => {
      reportDesktopDiagnostic({
        kind: "google-oauth-callback-navigate",
        message: "Routing Google OAuth callback in renderer.",
        source: "router",
      });
      navigate(route, { replace: true });
    });
  }, [navigate]);

  return null;
}

function AppRouterLayout() {
  return (
    <WorkspaceNavigationGuard>
      <SkillFileDraftGuard />
      <DesktopNativeThemeAttributes />
      <DesktopMenuCommandRouter />
      <DesktopNativeContextMenuController />
      <DesktopGoogleOAuthCallbackRouter />
      <DesktopUpdateDialog />
      <CompanyInitializationProvider>
        <Outlet />
      </CompanyInitializationProvider>
    </WorkspaceNavigationGuard>
  );
}

// Data routers can block links and Back/Forward before the editor is left.
const createAppRouter = shouldUseDesktopHashRouter()
  ? createHashRouter
  : createBrowserRouter;
const appRouter = createAppRouter(
  createRoutesFromElements(
    <Route element={<AppRouterLayout />} errorElement={<AppErrorPage />}>
      <Route
        path="/invite/preview"
        element={
          <Suspense fallback={<RouteFallback />}>
            <InvitationPreviewPage />
          </Suspense>
        }
      />
      {!window.zenithTeamDesktop ? (
        <Route path="/invite/:token" element={<InvitationDesktopBridge />} />
      ) : null}
      <Route path="*" element={<AppRoutes />} />
    </Route>,
  ),
);

if (import.meta.hot) {
  import.meta.hot.dispose(() => appRouter.dispose());
}

export default function AppRouter() {
  return <RouterProvider router={appRouter} />;
}

function SettingsTabRedirect({ tab }: { tab: "activity" | "notifications" }) {
  const location = useLocation();
  return (
    <Navigate
      replace
      to={`/settings/${tab}${location.search}${location.hash}`}
    />
  );
}

// Keep existing OAuth callbacks and saved links working after moving into Settings.
function SocialSettingsRedirect() {
  const { search } = useLocation();
  return <Navigate replace to={`/settings/plugins${search}`} />;
}
