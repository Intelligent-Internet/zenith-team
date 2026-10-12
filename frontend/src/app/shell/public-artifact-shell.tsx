import { Link, Outlet } from "react-router";
import { WorkspaceAppearanceProvider } from "@/app/appearance/workspace-appearance-provider";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { AppTitlebar } from "@/shared/ui/app-titlebar";
import { ArrowLeft } from "@/shared/ui/icons";
import "./workspace-theme.css";
import "./public-artifact-shell.css";

// Public links remain readable before a workspace session is available.
export function PublicArtifactShell() {
  const session = useSessionSnapshot();
  return (
    <WorkspaceAppearanceProvider
      userId={session.userId}
      companyId={session.company?.id}
      restoreLastAppearance
    >
      <div className="boardly-workspace-theme public-artifact-shell">
        <AppTitlebar
          className="public-artifact-shell__titlebar"
          showProfileMenu={false}
          trailing={
            <Link className="artifact-action-button" to="/">
              <ArrowLeft size={16} aria-hidden /> Back to Zenith Team
            </Link>
          }
        />
        <Outlet />
      </div>
    </WorkspaceAppearanceProvider>
  );
}
