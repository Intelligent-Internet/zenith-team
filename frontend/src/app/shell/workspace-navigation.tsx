import {
  isProjectFilesPath,
  WorkspaceExploreMenu,
} from "./workspace-explore-menu";
import type { ReactNode } from "react";
import { DesktopUpdateButton } from "@/app/desktop/desktop-update-button";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  Bot,
  FolderOpen,
  MessagesSquare,
  BookOpen,
  FileText,
  Users,
  Chart2,
  type IconComponent,
} from "@/shared/ui/icons";
// Link, not NavLink: the rail decides what is current itself, and NavLink
// would drop aria-current on pages it cannot match, such as /tasks.
import { Link } from "react-router";

import { cx } from "@/shared/lib/cx";
import { useCompanyResearch } from "@/features/onboarding/hooks/use-company-research";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { selectCompanyConversationWorkspace, useAppSelector } from "@/state";
import { WorkspaceSidebarLink } from "./workspace-sidebar";
import { workspaceSidebarPages } from "./workspace-sidebar-pages";

type PrimaryDestination = {
  icon: IconComponent;
  id: "chat" | "projects" | "agents" | "wiki" | "team" | "analytics" | "pages";
  label: string;
  to: string;
};

const primaryDestinations: PrimaryDestination[] = [
  { icon: MessagesSquare, id: "chat", label: "Chat", to: "/app" },
  { icon: FolderOpen, id: "projects", label: "Projects", to: "/projects" },
  { icon: Bot, id: "agents", label: "Agents", to: "/agents" },
  { icon: FileText, id: "pages", label: "Pages", to: "/pages" },
  { icon: BookOpen, id: "wiki", label: "Wiki", to: "/wiki" },
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

  if (destination.id === "pages")
    return (
      pathname === "/pages" || /^\/companies\/[^/]+\/pages\//.test(pathname)
    );
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
  // The onboarding research writes the Wiki in the background: its rail item
  // says so on every page.
  const researching = useCompanyResearch(session.company?.id ?? "").active;

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
            title:
              destination.id === "wiki" && researching
                ? "Your company research is writing the Wiki"
                : undefined,
            children: (
              <>
                <span className="workspace-primary-nav-icon" aria-hidden="true">
                  <destination.icon size={20} />
                  {destination.id === "wiki" && researching ? (
                    <span className="workspace-primary-nav-activity" />
                  ) : null}
                </span>
                <span>{destination.label}</span>
                {destination.id === "wiki" && researching ? (
                  <span className="sr-only">, research in progress</span>
                ) : null}
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
      <div className="mt-auto pt-[16px] pb-[8px] flex shrink-0 flex-col items-center gap-[16px]">
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
