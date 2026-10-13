import type { ReactNode } from "react";
import {
  Building2,
  Bell,
  NotebookText,
  CircleHelp,
  CreditCard,
  Chart2,
  Monitor,
  Palette,
  Puzzle,
  UserRound,
} from "@/shared/ui/icons";
import { NavLink } from "react-router";
import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import {
  getVisibleSettingsTabs,
  type SettingsTabId,
} from "@/features/settings/model/settings-navigation";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useCompanyPermissions } from "@/shared/permissions";
import "@/features/settings/styles/settings.css";
import "@/features/settings/styles/settings-workspace.css";
import "@/features/settings/styles/settings-detail.css";
const icons = {
  profile: UserRound,
  appearance: Palette,
  notifications: Bell,
  activity: NotebookText,
  agents: Monitor,
  plugins: Puzzle,
  general: Building2,
  subscription: CreditCard,
  usage: Chart2,
  support: CircleHelp,
};
export function SettingsShell({
  activeTab,
  actions,
  children,
  description,
  ownsHeader = false,
}: {
  activeTab: SettingsTabId;
  actions?: ReactNode;
  children: ReactNode;
  description?: ReactNode;
  /** The page draws its own header, so the frame's title is not shown. */
  ownsHeader?: boolean;
}) {
  const session = useSessionSnapshot();
  const { permissions } = useCompanyPermissions();
  const tabs = getVisibleSettingsTabs(permissions);
  const current = tabs.find((tab) => tab.id === activeTab)!;
  const groups = [
    {
      title: "Personal",
      ids: ["profile", "appearance", "notifications", "support"],
    },
    {
      title: session.company?.workspaceName ?? "Company",
      ids: [
        "general",
        "activity",
        "agents",
        "plugins",
        "subscription",
        "usage",
      ],
    },
  ];
  return (
    <div className="settings-workspace" data-tour-id="settings-overview">
      <aside className="settings-workspace-nav">
        <div className="settings-workspace-heading">Settings</div>
        <nav aria-label="Settings sections">
          {groups.map((group) => {
            const visible = tabs.filter((tab) => group.ids.includes(tab.id));
            return visible.length ? (
              <section key={group.title}>
                <h2>{group.title}</h2>
                {visible.map((tab) => {
                  const Icon = icons[tab.id];
                  return (
                    <NavLink
                      key={tab.id}
                      to={tab.href}
                      className={tab.id === activeTab ? "is-active" : undefined}
                    >
                      <Icon size={16} />
                      {tab.id === "profile"
                        ? "My Account"
                        : tab.id === "general"
                          ? "Company"
                          : tab.label}
                    </NavLink>
                  );
                })}
              </section>
            ) : null;
          })}
        </nav>
      </aside>
      <WorkspacePageFrame
        actions={actions}
        ariaLabel="Settings"
        className={[
          "settings-shell settings-shell--current",
          `settings-shell--${activeTab}`,
          ownsHeader ? "settings-shell--page-header" : "",
        ]
          .filter(Boolean)
          .join(" ")}
        contentWidth="contained"
        title={
          activeTab === "profile"
            ? "My Account"
            : activeTab === "general"
              ? "Company settings"
              : current.title
        }
        description={
          description ??
          (activeTab === "profile"
            ? "Your profile and preferences, across companies."
            : current.description)
        }
      >
        <div className="settings-current-content" key={activeTab}>
          {children}
        </div>
      </WorkspacePageFrame>
    </div>
  );
}
