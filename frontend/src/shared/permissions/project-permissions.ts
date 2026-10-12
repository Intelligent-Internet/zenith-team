import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

/**
 * One action a person may hold on one Project (ADR 0017). The server decides
 * each person's set and returns it as `access.permissions` on every Project
 * and Task read. Gate on that set, never on a Company role: a role does not
 * open a Project's content, and there are no Project roles at all.
 */
export type ProjectPermission = ApiComponents["schemas"]["ProjectPermission"];
export type ProjectVisibility = ApiComponents["schemas"]["ProjectVisibility"];

/** Whatever carries a server verdict: a Project's or a Task's `access`. */
export type ProjectAccessContext = {
  permissions?: readonly ProjectPermission[] | null;
};

export const PROJECT_PERMISSION_LABELS: Record<ProjectPermission, string> = {
  "project.read": "View tasks, chats and files",
  "project.tasks.write": "Create and edit tasks",
  "project.tasks.execute": "Run tasks with agents",
  "project.files.manage": "Manage project files",
  "project.approvals.resolve": "Answer approvals and questions",
  "project.visibility.update": "Change who can see the project",
  "project.settings.update": "Edit project details",
  "project.grants.manage": "Manage people's access",
  "project.archive": "Archive the project",
};

/** One line on what each permission lets a person do, for editors. */
export const PROJECT_PERMISSION_HELP: Record<ProjectPermission, string> = {
  "project.read": "See the project, its tasks, chats and files.",
  "project.tasks.write": "Create tasks, edit them and post in their chats.",
  "project.tasks.execute": "Start tasks with an agent on their own computer.",
  "project.files.manage": "Add and replace the project's documents.",
  "project.approvals.resolve":
    "Answer agents' questions and approvals on tasks.",
  "project.visibility.update":
    "Choose between people added and everyone in the company.",
  "project.settings.update": "Rename the project and edit its brief.",
  "project.grants.manage": "Add people and change what they can do.",
  "project.archive":
    "Archive it for good, or let a plan delete its open tasks.",
};

/** The permissions in the order every surface lists them. */
export const PROJECT_PERMISSION_GROUPS: readonly {
  label: string;
  permissions: readonly ProjectPermission[];
}[] = [
  {
    label: "Work",
    permissions: [
      "project.read",
      "project.tasks.write",
      "project.tasks.execute",
      "project.files.manage",
    ],
  },
  {
    label: "Decisions",
    permissions: ["project.approvals.resolve"],
  },
  {
    label: "Management",
    permissions: [
      "project.settings.update",
      "project.grants.manage",
      "project.archive",
      "project.visibility.update",
    ],
  },
];

export const PROJECT_PERMISSIONS: readonly ProjectPermission[] =
  PROJECT_PERMISSION_GROUPS.flatMap((group) => group.permissions);

/**
 * What everyone in the Company holds without a grant while a Project is open
 * to the Company (docs/api/projects.md). Used only to explain where an
 * effective permission comes from, never to decide a gate.
 */
export const PROJECT_COMPANY_FLOOR: readonly ProjectPermission[] = [
  "project.read",
  "project.tasks.write",
  "project.tasks.execute",
];

/**
 * What a Company `projects.manage` holder holds on every Project without a
 * grant (docs/api/projects.md). Explanation only, like the floor.
 */
export const PROJECT_MANAGEMENT_BRIDGE: readonly ProjectPermission[] = [
  "project.settings.update",
  "project.grants.manage",
  "project.archive",
];

export function hasProjectPermission(
  access: ProjectAccessContext | null | undefined,
  permission: ProjectPermission,
): boolean {
  return access?.permissions?.includes(permission) ?? false;
}

/** The sentence a disabled control shows: what is missing, never a role. */
export function missingProjectPermissionTitle(
  permission: ProjectPermission,
): string {
  return `You need "${PROJECT_PERMISSION_LABELS[permission]}" on this project.`;
}
