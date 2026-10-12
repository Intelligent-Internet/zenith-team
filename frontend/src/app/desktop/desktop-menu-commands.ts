import type {
  DesktopMenuCommand,
  DesktopRouteKey,
} from "@/app/desktop/desktop-environment.service";
import type { CompanyPermission } from "@/shared/permissions";
import {
  BookOpen,
  Bot,
  Building2,
  Calendar,
  CalendarClock,
  Chart2,
  CheckSquare,
  CreditCard,
  FolderOpen,
  Info,
  Mail,
  MessagesSquare,
  Palette,
  Share,
  UserRound,
  Users,
  Zap,
  type IconComponent,
} from "@/shared/ui/icons";

export type DesktopCommandAction = {
  id:
    | DesktopRouteKey
    | "wiki"
    | "members"
    | "workflows"
    | "calendar"
    | "mailbox"
    | "social"
    | "settings-appearance"
    | "settings-subscription"
    | "settings-support";
  group: "Workspace" | "Tools" | "Settings";
  icon: IconComponent;
  label: string;
  description: string;
  route: string;
  shortcut?: string;
  keywords: string[];
  // The permission the destination needs; an action without one is open
  // to every member.
  requires?: CompanyPermission;
};

export const desktopCommandActions: DesktopCommandAction[] = [
  {
    id: "cockpit",
    group: "Workspace",
    icon: MessagesSquare,
    label: "Open Chat",
    description: "Conversations with your team and Agents",
    route: "/app",
    shortcut: "1",
    keywords: ["home", "company", "chat", "conversation", "ceo"],
  },
  {
    id: "work-search",
    group: "Workspace",
    icon: FolderOpen,
    label: "Open Projects",
    description: "Projects, tasks, and their progress",
    route: "/projects",
    shortcut: "2",
    keywords: ["projects", "tasks", "runs", "plan", "board"],
  },
  {
    id: "team",
    group: "Workspace",
    icon: Bot,
    label: "Open Agents",
    description: "Agent work, instructions, and capabilities",
    route: "/agents",
    shortcut: "3",
    keywords: ["agents", "instructions", "capabilities"],
  },
  {
    id: "wiki",
    group: "Workspace",
    icon: BookOpen,
    label: "Open Wiki",
    description: "Company knowledge and shared pages",
    route: "/wiki",
    keywords: ["knowledge", "docs", "documentation", "pages"],
  },
  {
    id: "members",
    group: "Workspace",
    icon: Users,
    label: "Open Team",
    description: "Company members and invitations",
    route: "/team",
    keywords: ["people", "members", "invites"],
  },
  {
    id: "analytics",
    group: "Workspace",
    icon: Chart2,
    label: "Open Analytics",
    description: "Usage, cost, and reliability",
    route: "/analytics",
    keywords: ["usage dashboard", "cost", "analytics", "tokens"],
  },
  {
    id: "workflows",
    group: "Tools",
    icon: Zap,
    label: "Open Workflows",
    description: "Ready-to-run workflows for your Agents",
    route: "/workflows",
    keywords: ["templates", "playbooks"],
  },
  {
    id: "calendar",
    group: "Tools",
    icon: Calendar,
    label: "Open Calendar",
    description: "Events, meetings, and upcoming work",
    route: "/calendar",
    keywords: ["events", "meetings", "agenda"],
  },
  {
    id: "mailbox",
    group: "Tools",
    icon: Mail,
    label: "Open Mailbox",
    description: "Company email and Agent drafts",
    route: "/mailbox",
    requires: "mailbox.manage",
    keywords: ["email", "mail", "drafts"],
  },
  {
    id: "social",
    group: "Tools",
    icon: Share,
    label: "Open Social accounts",
    description: "Connected social accounts and publishing",
    route: "/settings/plugins",
    keywords: ["social media", "publish", "channels"],
  },
  {
    id: "assets",
    group: "Tools",
    icon: FolderOpen,
    label: "Open Assets",
    description: "Company outputs, assets, and files",
    route: "/assets",
    shortcut: "4",
    keywords: ["outputs", "files", "deliverables"],
  },
  {
    id: "approvals",
    group: "Tools",
    icon: CheckSquare,
    label: "Open Approvals",
    description: "Pending decisions and review queue",
    route: "/approvals",
    keywords: ["decisions", "review", "blocked work"],
  },
  {
    id: "scheduled",
    group: "Tools",
    icon: CalendarClock,
    label: "Open Schedules",
    description: "Recurring and one-time Agent work",
    route: "/scheduled",
    requires: "schedules.manage",
    keywords: ["automation", "cron", "daily brief", "recurring", "schedule"],
  },
  {
    id: "settings-profile",
    group: "Settings",
    icon: UserRound,
    label: "Open My Account",
    description: "Your profile and account settings",
    route: "/settings/profile",
    shortcut: ",",
    keywords: ["profile", "account", "settings"],
  },
  {
    id: "settings-appearance",
    group: "Settings",
    icon: Palette,
    label: "Open Appearance",
    description: "Theme and display preferences",
    route: "/settings/appearance",
    keywords: ["theme", "dark", "light", "settings"],
  },
  {
    id: "settings-company",
    group: "Settings",
    icon: Building2,
    label: "Open Company settings",
    description: "Company profile",
    route: "/settings/general",
    requires: "company.profile.update",
    keywords: ["company", "settings", "general", "sharing"],
  },
  {
    id: "settings-subscription",
    group: "Settings",
    icon: CreditCard,
    label: "Open Subscription",
    description: "Plan and billing information",
    route: "/settings/subscription",
    requires: "usage.read",
    keywords: ["billing", "subscription", "settings"],
  },
  {
    id: "settings-support",
    group: "Settings",
    icon: Info,
    label: "Open Updates & Support",
    description: "App updates and help",
    route: "/settings/support",
    keywords: ["version", "update", "help", "support", "settings"],
  },
];

const routeByKey = new Map(
  desktopCommandActions.map((action) => [action.id, action.route]),
);

export function getDesktopRouteForMenuCommand(
  command: DesktopMenuCommand,
): string | null {
  if (command.type === "route:settings") {
    return routeByKey.get("settings-profile") ?? null;
  }

  if (command.type !== "route:navigate") {
    return null;
  }

  return routeByKey.get(command.route) ?? null;
}
