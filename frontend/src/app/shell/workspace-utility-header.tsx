import { Tooltip } from "@/shared/ui/tooltip";
import { WorkspaceHistoryControls } from "./workspace-history-controls";
import { openDesktopCommandPalette } from "@/app/desktop/desktop-command-palette";
import { Calendar, Search } from "@/shared/ui/icons";
import type { ReactNode } from "react";
import { Link, NavLink } from "react-router";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { TopNotificationMenu } from "@/features/notifications/components/recent-notifications-menu";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

type WorkspaceUtilityHeaderProps = {
  companyId: string;
  accountMenu: ReactNode;
  desktopMac: boolean;
};

export function WorkspaceUtilityHeader({
  companyId,
  accountMenu,
  desktopMac,
}: WorkspaceUtilityHeaderProps) {
  const agents = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const ceo = agents.currentData?.items.find(
    (agent) => !agent.archivedAt && agent.reportsTo === null,
  );
  const ceoConversationHref = ceo
    ? createChatWorkspaceHref({ kind: "direct", agentId: ceo.id })
    : undefined;
  const ceoHref = ceoConversationHref
    ? `${ceoConversationHref}&newSession=1`
    : undefined;
  return (
    <header
      className="workspace-utility-header"
      data-desktop-mac={desktopMac || undefined}
    >
      <WorkspaceHistoryControls key={companyId} />

      <div
        className="workspace-utility-actions"
        data-tour-id="workspace-utility-actions"
      >
        <Tooltip label="Search (⌘K / Ctrl+K)">
          <button
            aria-keyshortcuts="Meta+K Control+K"
            aria-label="Open command palette"
            className="workspace-command-button workspace-icon-button"
            type="button"
            onClick={openDesktopCommandPalette}
          >
            <Search aria-hidden="true" size={18} />
          </button>
        </Tooltip>
        <Tooltip label="Calendar">
          <NavLink
            aria-label="Open calendar"
            className="workspace-icon-button"
            to="/calendar"
          >
            <Calendar aria-hidden="true" size={18} />
          </NavLink>
        </Tooltip>
        <TopNotificationMenu className="workspace-header-action" />
        {ceo && ceoHref ? (
          <Link
            className="workspace-ceo-shortcut"
            to={ceoHref}
            aria-label={`Ask CEO: start a new chat with ${ceo.name}`}
            title={`Start a new chat with ${ceo.name}, your CEO`}
          >
            <FoldAvatar agentId={ceo.id} size={20} paused />
            <span className="workspace-ceo-shortcut-label">Ask CEO</span>
          </Link>
        ) : null}
        {accountMenu}
      </div>
    </header>
  );
}
