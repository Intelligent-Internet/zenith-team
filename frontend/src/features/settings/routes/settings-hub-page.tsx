import { NotificationsPage } from "@/features/notifications/routes/notifications-page";
import { LegacyUsageRedirect } from "@/features/analytics/routes/legacy-usage-redirect";
import { ActivitySettingsPage } from "@/features/settings/routes/activity-settings-page";
import { Link, Navigate, useLocation } from "react-router";

import { useCompanyPermissions } from "@/shared/permissions";
import { ArrowRight } from "@/shared/ui/icons";
import { SettingsShell } from "@/features/settings/components/settings-shell";
import {
  canAccessSettingsTab,
  getSettingsTab,
  resolveSettingsRoute,
  type SettingsTabId,
} from "@/features/settings/model/settings-navigation";
import { AgentSettingsPage } from "@/features/settings/routes/agent-settings-page";
import {
  AppearanceSaveStatus,
  AppearanceSettingsPage,
} from "@/features/settings/routes/appearance-settings-page";
import { CompanySettingsPage } from "@/features/settings/routes/company-settings-page";
import { NotificationPreferencesPage } from "@/features/settings/routes/notification-preferences-page";
import { PluginDetailPage } from "@/features/settings/routes/plugin-detail-page";
import { PluginsSettingsPage } from "@/features/settings/routes/plugins-settings-page";
import { ProfileSettingsPage } from "@/features/settings/routes/profile-settings-page";
import { RuntimeDetailPage } from "@/features/settings/routes/runtime-detail-page";
import { SubscriptionSettingsPage } from "@/features/settings/routes/subscription-settings-page";
import { SupportSettingsPage } from "@/features/settings/routes/support-settings-page";

export function SettingsHubPage({ forcedTab }: { forcedTab?: SettingsTabId }) {
  const location = useLocation();
  const { permissions } = useCompanyPermissions();
  const route = resolveSettingsRoute(location.pathname);
  const routeTab = route.tab;

  if (
    !forcedTab &&
    (routeTab === "general" || routeTab === "company") &&
    location.hash === "#company-email"
  ) {
    return <Navigate replace to={`/mailbox${location.search}`} />;
  }

  if (!forcedTab && routeTab === "company") {
    return (
      <Navigate
        replace
        to={`/settings/general${location.search}${location.hash}`}
      />
    );
  }

  if (!forcedTab && routeTab === "connectors") {
    return (
      <Navigate
        replace
        to={`/settings/plugins${location.search}${location.hash}`}
      />
    );
  }

  if (!forcedTab && !getSettingsTab(routeTab)) {
    return <Navigate replace to="/settings/profile" />;
  }

  const activeTab = forcedTab ?? (routeTab as SettingsTabId);
  const notificationViewParams = new URLSearchParams(location.search);
  const notificationPreferences =
    notificationViewParams.get("view") === "preferences";
  notificationViewParams.set(
    "view",
    notificationPreferences ? "inbox" : "preferences",
  );

  if (!canAccessSettingsTab(activeTab, permissions)) {
    return <Navigate replace to="/settings/profile" />;
  }

  const pluginId = forcedTab ? undefined : route.pluginId;

  // The Plugins pages draw their own header: the catalog puts its computer
  // and refresh beside the title, a plugin's page its breadcrumb, logo and
  // name.
  return (
    <SettingsShell
      activeTab={activeTab}
      ownsHeader={activeTab === "plugins"}
      description={
        activeTab === "notifications" && notificationPreferences
          ? "Choose which desktop alerts you receive."
          : undefined
      }
      actions={
        activeTab === "appearance" ? (
          <AppearanceSaveStatus />
        ) : activeTab === "notifications" ? (
          <Link
            className={
              notificationPreferences
                ? "notification-preferences-inbox-link"
                : "workspace-action-button"
            }
            to={`/settings/notifications?${notificationViewParams}`}
          >
            {notificationPreferences ? "View inbox" : "Preferences"}
            {notificationPreferences ? (
              <ArrowRight size={16} aria-hidden="true" />
            ) : null}
          </Link>
        ) : undefined
      }
    >
      <SettingsTabContent
        activeTab={activeTab}
        notificationPreferences={notificationPreferences}
        pluginId={pluginId}
        runtimeId={forcedTab ? undefined : route.runtimeId}
      />
    </SettingsShell>
  );
}

function SettingsTabContent({
  activeTab,
  notificationPreferences,
  pluginId,
  runtimeId,
}: {
  activeTab: SettingsTabId;
  notificationPreferences: boolean;
  pluginId: string | undefined;
  runtimeId: string | undefined;
}) {
  switch (activeTab) {
    case "profile":
      return <ProfileSettingsPage />;
    case "notifications":
      return notificationPreferences ? (
        <NotificationPreferencesPage />
      ) : (
        <NotificationsPage />
      );
    case "activity":
      return <ActivitySettingsPage />;
    case "appearance":
      return <AppearanceSettingsPage />;
    case "agents":
      return runtimeId ? (
        <RuntimeDetailPage runtimeId={runtimeId} />
      ) : (
        <AgentSettingsPage />
      );
    case "plugins":
      return pluginId ? (
        <PluginDetailPage pluginId={pluginId} />
      ) : (
        <PluginsSettingsPage />
      );
    case "general":
      return <CompanySettingsPage />;
    case "subscription":
      return <SubscriptionSettingsPage />;
    case "usage":
      return <LegacyUsageRedirect />;
    case "support":
      return <SupportSettingsPage />;
  }
}

export default SettingsHubPage;
