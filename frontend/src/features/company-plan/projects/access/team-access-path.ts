// Project access is managed on the Team page: its "Project access" view,
// scoped to one Project. Every link to a Project's access goes through here.
export const TEAM_PROJECT_ACCESS_VIEW = "projects";

/**
 * The other views that open Project access: the tab's own name, and the
 * Team page's former Groups view, since Groups are listed in Project
 * access.
 */
const TEAM_PROJECT_ACCESS_ALIASES: readonly string[] = [
  "project-access",
  "groups",
];

/** Whether a `view` query value opens the Project access tab. */
export function opensProjectAccess(view: string | null): boolean {
  return (
    view === TEAM_PROJECT_ACCESS_VIEW ||
    (view !== null && TEAM_PROJECT_ACCESS_ALIASES.includes(view))
  );
}

export function teamProjectAccessPath(projectId?: string): string {
  const search = new URLSearchParams({ view: TEAM_PROJECT_ACCESS_VIEW });
  if (projectId) search.set("project", projectId);
  return `/team?${search.toString()}`;
}
