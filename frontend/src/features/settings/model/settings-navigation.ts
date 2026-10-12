import type { CompanyPermission } from "@/shared/permissions";

export type SettingsTabId =
  | "profile"
  | "appearance"
  | "notifications"
  | "activity"
  | "agents"
  | "plugins"
  | "general"
  | "subscription"
  | "usage"
  | "support";

export type SettingsTabDefinition = {
  description: string;
  href: string;
  icon: string;
  id: SettingsTabId;
  label: string;
  title: string;
  // The permission the tab needs; a tab without one is open to every
  // member. Affordances inside a tab are gated by the page itself.
  requires?: CompanyPermission;
};

export const SETTINGS_TABS: readonly SettingsTabDefinition[] = [
  {
    description:
      "Manage your identity, contact details, profile image, and personal preferences.",
    href: "/settings/profile",
    icon: "user-circle",
    id: "profile",
    label: "Profile",
    title: "Profile Settings",
  },
  {
    description: "Make Zenith Team feel like yours.",
    href: "/settings/appearance",
    icon: "palette",
    id: "appearance",
    label: "Appearance",
    title: "Appearance",
  },
  {
    description:
      "Updates, reminders, and decisions across your calendars and companies.",
    href: "/settings/notifications?view=preferences",
    icon: "notification",
    id: "notifications",
    label: "Notifications",
    title: "Notifications",
  },
  {
    description: "Changes across your company.",
    href: "/settings/activity",
    icon: "note",
    id: "activity",
    label: "Activity",
    title: "Company activity",
  },
  {
    description: "Manage the computers your agents run on.",
    href: "/settings/agents",
    icon: "monitor",
    id: "agents",
    label: "Computers",
    title: "Computers",
  },
  {
    description: "Work with your Agents across your favorite tools",
    href: "/settings/plugins",
    icon: "puzzle",
    id: "plugins",
    requires: "plugins.manage",
    label: "Plugins",
    title: "Plugins",
  },
  {
    description: "Manage your company profile.",
    href: "/settings/general",
    icon: "setting",
    id: "general",
    requires: "company.profile.update",
    label: "General",
    title: "General Settings",
  },
  {
    description:
      "Check billing availability and find usage, agent access, and support.",
    href: "/settings/subscription",
    icon: "receipt-2",
    id: "subscription",
    requires: "usage.read",
    label: "Subscription",
    title: "Subscription",
  },
  {
    description: "Usage, cost, and reliability across your agents.",
    href: "/analytics",
    icon: "chart",
    id: "usage",
    requires: "usage.read",
    label: "Analytics",
    title: "Analytics",
  },
  {
    description: "Keep Zenith Team up to date and get help.",
    href: "/settings/support",
    icon: "info",
    id: "support",
    label: "Updates & Support",
    title: "Updates & Support",
  },
] as const;

// Computers is open to every member. Each member sees and manages only
// the computers they registered (ADR 0014).
export function getVisibleSettingsTabs(
  permissions: readonly CompanyPermission[],
) {
  return SETTINGS_TABS.filter(
    (tab) => !tab.requires || permissions.includes(tab.requires),
  );
}

export function canAccessSettingsTab(
  tabId: SettingsTabId,
  permissions: readonly CompanyPermission[],
) {
  const tab = getSettingsTab(tabId);
  return Boolean(tab) && (!tab?.requires || permissions.includes(tab.requires));
}

export function getSettingsTab(
  tabId: string | undefined,
): SettingsTabDefinition | null {
  return SETTINGS_TABS.find((tab) => tab.id === tabId) ?? null;
}

export type SettingsRoute = {
  // Set only for `/settings/plugins/<pluginId>`, the plugin page.
  pluginId: string | undefined;
  // Set only for `/settings/agents/<runtimeId>`, the runtime detail.
  runtimeId: string | undefined;
  // The raw segment after `settings`; the hub judges whether it is a tab,
  // a retired name it redirects, or nothing.
  tab: string | undefined;
};

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function runtimeDetailHref(runtimeId: string): string {
  return `/settings/agents/${runtimeId}`;
}

// The plugin page reads the Plugin as `runtimeId` observed it; without one it
// uses the same default Runtime as the catalog.
export function pluginDetailPath(pluginId: string, runtimeId?: string): string {
  const path = `/settings/plugins/${pluginId}`;
  return runtimeId
    ? `${path}?runtimeId=${encodeURIComponent(runtimeId)}`
    : path;
}

// A catalog row opens the plugin page with the catalog's own URL (view,
// search, computer and Workflow app panel) in the navigation state, so the
// page's breadcrumb returns to exactly that catalog.
export function pluginCatalogLinkState(catalogHref: string) {
  return { pluginCatalogHref: catalogHref };
}

export function readPluginCatalogHref(state: unknown): string | undefined {
  if (!state || typeof state !== "object") return undefined;
  const href = (state as { pluginCatalogHref?: unknown }).pluginCatalogHref;
  return typeof href === "string" && /^\/settings\/plugins(\?|$)/.test(href)
    ? href
    : undefined;
}

// The settings hub is mounted under `settings/*` (the desktop hash router
// yields the same pathname), so the tab is the segment after `settings` and
// the only deeper routes are the runtime detail under the agents tab and the
// plugin page under the plugins tab. Any other trailing segment is ignored, so
// an old or malformed link still lands on its tab.
export function resolveSettingsRoute(pathname: string): SettingsRoute {
  const segments = pathname.split("/").filter(Boolean);
  if (segments[0] !== "settings") {
    return { pluginId: undefined, runtimeId: undefined, tab: undefined };
  }
  const tab = segments[1];
  const detailId =
    segments.length === 3 && UUID_PATTERN.test(segments[2])
      ? segments[2]
      : undefined;
  return {
    pluginId: tab === "plugins" ? detailId : undefined,
    runtimeId: tab === "agents" ? detailId : undefined,
    tab,
  };
}
