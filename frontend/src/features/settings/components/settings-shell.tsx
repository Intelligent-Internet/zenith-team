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
import { cx } from "@/shared/lib/cx";
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
    <div
      className="grid h-full min-h-0 w-full grid-cols-[208px_minmax(0,1fr)] text-workspace-text [background:var(--workspace-panel-bg)] [@media(max-width:800px)]:grid-cols-[minmax(0,1fr)] [@media(max-width:800px)]:grid-rows-[auto_minmax(0,1fr)]"
      data-tour-id="settings-overview"
    >
      <aside className="overflow-auto p-[20px_12px] [border-right:1px_solid_var(--workspace-divider)] [background:var(--workspace-sidebar-bg)] [@media(max-width:800px)]:p-[8px_12px] [@media(max-width:800px)]:[border-right:0] [@media(max-width:800px)]:[border-bottom:1px_solid_var(--workspace-divider)]">
        <div className="flex items-center gap-[9px] p-[0_12px_24px] text-workspace-body leading-workspace-body font-[600] [@media(max-width:800px)]:hidden">
          Settings
        </div>
        <nav
          aria-label="Settings sections"
          className="[@media(max-width:800px)]:flex [@media(max-width:800px)]:overflow-auto"
        >
          {groups.map((group) => {
            const visible = tabs.filter((tab) => group.ids.includes(tab.id));
            return visible.length ? (
              <section
                key={group.title}
                className="[&+section]:mt-[28px] [@media(max-width:800px)]:flex [@media(max-width:800px)]:shrink-0 [@media(max-width:800px)]:[&+section]:m-0"
              >
                <h2 className="m-[0_12px_8px] text-workspace-label leading-workspace-label font-[500] text-workspace-muted [@media(max-width:800px)]:hidden">
                  {group.title}
                </h2>
                {visible.map((tab) => {
                  const Icon = icons[tab.id];
                  return (
                    <NavLink
                      key={tab.id}
                      to={tab.href}
                      className={cx(
                        "flex items-center gap-[10px] rounded-workspace-control p-[7px_12px] text-workspace-body leading-workspace-body text-workspace-text [text-decoration:none]",
                        tab.id === activeTab
                          ? "is-active [background:var(--workspace-selected-bg)] font-[500]"
                          : "[&:hover]:[background:var(--workspace-hover-bg)]",
                      )}
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
        <div className="w-full min-w-0" key={activeTab}>
          {children}
        </div>
      </WorkspacePageFrame>
    </div>
  );
}
