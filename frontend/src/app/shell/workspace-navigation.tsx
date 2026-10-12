import {
  isProjectFilesPath,
  WorkspaceExploreMenu,
} from "./workspace-explore-menu";
import type { ReactNode } from "react";
import { DesktopUpdateButton } from "@/app/desktop/desktop-update-button";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import {
  Bot,
  FolderOpen,
  MessagesSquare,
  BookOpen,
  Users,
  Chart2,
  LayoutDashboard,
  type IconComponent,
} from "@/shared/ui/icons";
// Link, not NavLink: the rail decides what is current itself, and NavLink
// would drop aria-current on pages it cannot match, such as /tasks.
import { Link } from "react-router";

import { cx } from "@/shared/lib/cx";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { selectCompanyConversationWorkspace, useAppSelector } from "@/state";
import { WorkspaceSidebarLink } from "./workspace-page-sidebar";
import { workspaceSidebarPages } from "./workspace-sidebar-pages";

type PrimaryDestination = {
  icon: IconComponent;
  id:
    | "chat"
    | "projects"
    | "agents"
    | "wiki"
    | "team"
    | "analytics"
    | "whiteboards";
  label: string;
  to: string;
};

const primaryDestinations: PrimaryDestination[] = [
  { icon: MessagesSquare, id: "chat", label: "Chat", to: "/app" },
  { icon: FolderOpen, id: "projects", label: "Projects", to: "/projects" },
  { icon: Bot, id: "agents", label: "Agents", to: "/agents" },
  { icon: BookOpen, id: "wiki", label: "Wiki", to: "/wiki" },
  {
    icon: LayoutDashboard,
    id: "whiteboards",
    label: "Whiteboards",
    to: "/whiteboards",
  },
  { icon: Users, id: "team", label: "Team", to: "/team" },
  { icon: Chart2, id: "analytics", label: "Analytics", to: "/analytics" },
];

function isPrimaryDestinationActive(
  destination: PrimaryDestination,
  pathname: string,
) {
  if (destination.id === "chat") {
    return pathname === "/app";
  }

  // Every Task lives in a Project, so the Task list and a Project's Files are
  // Project pages too.
  if (destination.id === "projects")
    return (
      pathname === "/projects" ||
      pathname.startsWith("/projects/") ||
      pathname === "/tasks" ||
      isProjectFilesPath(pathname)
    );

  if (destination.id === "whiteboards")
    return pathname === "/whiteboards" || pathname.startsWith("/whiteboards/");
  if (destination.id === "analytics") return pathname === "/analytics";
  if (destination.id === "wiki")
    return pathname === "/wiki" || pathname.startsWith("/wiki/");
  if (destination.id === "team")
    return pathname === "/team" || pathname.startsWith("/team/");
  return pathname === "/agents" || pathname.startsWith("/agents/");
}

type WorkspaceNavigationProps = {
  pathname: string;
  showTeam: boolean;
  mobile?: boolean;
};

function PrimaryNavigationItems({
  pathname,
  showTeam,
  mobile = false,
}: WorkspaceNavigationProps) {
  const session = useSessionSnapshot();
  const workspace = useAppSelector((state) =>
    selectCompanyConversationWorkspace(state, session.company?.id ?? ""),
  );
  const lastChatTarget = workspace.activeDiscussion ?? workspace.activeTarget;
  const chatHref = lastChatTarget
    ? createChatWorkspaceHref(lastChatTarget)
    : "/app";

  return (
    <>
      {primaryDestinations
        .filter(
          (destination) =>
            !["wiki", "agents", "team"].includes(destination.id) || showTeam,
        )
        .map((destination) => {
          const active = isPrimaryDestinationActive(destination, pathname);
          const sidebar =
            !mobile &&
            workspaceSidebarPages.find((page) => page.id === destination.id);
          const linkProps = {
            "aria-current": active ? ("page" as const) : undefined,
            className: cx("workspace-primary-nav-item", active && "is-active"),
            "data-tour-id": `workspace-navigation-${destination.id}`,
            to: destination.id === "chat" ? chatHref : destination.to,
            children: (
              <>
                <span className="workspace-primary-nav-icon" aria-hidden="true">
                  <destination.icon size={20} />
                </span>
                <span
                  className={cx(
                    destination.id === "whiteboards" &&
                      "workspace-primary-nav-label--compact",
                  )}
                >
                  {destination.label}
                </span>
              </>
            ),
          };

          return sidebar ? (
            <WorkspaceSidebarLink
              key={destination.id}
              sidebarId={sidebar.id}
              {...linkProps}
            />
          ) : (
            <Link key={destination.id} {...linkProps} />
          );
        })}
      <WorkspaceExploreMenu
        key={pathname}
        pathname={pathname}
        mobile={mobile}
      />
    </>
  );
}

export function WorkspaceRail({
  accountMenu,
  ...props
}: WorkspaceNavigationProps & { accountMenu: ReactNode }) {
  return (
    <nav
      className="workspace-rail"
      aria-label="Workspace"
      data-tour-id="workspace-navigation-desktop"
    >
      <PrimaryNavigationItems {...props} />
      <div className="workspace-rail-footer">
        <DesktopUpdateButton />
        {accountMenu}
      </div>
    </nav>
  );
}

export function WorkspaceBottomNavigation(props: WorkspaceNavigationProps) {
  return (
    <nav
      className="workspace-bottom-nav"
      aria-label="Workspace"
      data-tour-id="workspace-navigation-mobile"
      data-team-visible={props.showTeam || undefined}
    >
      <PrimaryNavigationItems {...props} mobile />
    </nav>
  );
}
