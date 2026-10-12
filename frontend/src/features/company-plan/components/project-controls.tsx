import { Link } from "react-router";
import { useListProjectsQuery } from "../api/project-api";
import type { ProjectVisibility } from "@/shared/permissions";
import { Building2 } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

const EVERYONE_HELP = "Everyone in the company can see it";

// The Project a Task belongs to, as a link to it. Standing alone it says
// "Project:"; a property row already names the field, so it shows only the
// title.
export function TaskProjectLabel({
  companyId,
  projectId,
  withLabel = true,
}: {
  companyId: string;
  projectId: string;
  withLabel?: boolean;
}) {
  const query = useListProjectsQuery(companyId);
  const project = query.data?.items.find((p) => p.id === projectId);
  return (
    <Link
      to={`/projects/${encodeURIComponent(projectId)}`}
      className="text-sm text-muted-foreground"
    >
      {withLabel ? "Project: " : null}
      {project?.title ?? projectId}
    </Link>
  );
}
// Who can see a Task: exactly who can see its Project (ADR 0017). Only
// the people added is the norm and shows nothing; a Project open to the
// whole Company shows its icon, the words in the tooltip.
export function TaskProjectVisibility({
  companyId,
  projectId,
  className,
}: {
  companyId: string;
  projectId: string;
  className?: string;
}) {
  const query = useListProjectsQuery(companyId);
  const project = query.data?.items.find((p) => p.id === projectId);
  if (project?.visibility !== "company") return null;
  return (
    <Tooltip label={EVERYONE_HELP}>
      <span
        aria-label={EVERYONE_HELP}
        className={className}
        role="img"
        tabIndex={0}
      >
        <Building2 size={15} aria-hidden="true" />
      </span>
    </Tooltip>
  );
}

// A Project open to the whole Company, as an icon and a word; one only for
// the people added shows nothing.
export function ProjectVisibilityLabel({
  visibility,
  className,
}: {
  visibility: ProjectVisibility;
  className?: string;
}) {
  if (visibility !== "company") return null;
  return (
    <Tooltip label={EVERYONE_HELP}>
      <span className={className} tabIndex={0}>
        <Building2 size={15} aria-hidden="true" /> Everyone
      </span>
    </Tooltip>
  );
}
export function ProjectControls({
  companyId,
  selected,
  onSelect,
}: {
  companyId: string;
  selected: string;
  onSelect: (id: string) => void;
  canManage: boolean;
}) {
  const query = useListProjectsQuery(companyId);
  return (
    <select
      aria-label="Filter by Project"
      className="workspace-action-button"
      value={selected}
      onChange={(event) => onSelect(event.target.value)}
    >
      <option value="">All projects</option>
      {query.data?.items.map((project) => (
        <option key={project.id} value={project.id}>
          {project.title}
          {project.archivedAt ? " (archived)" : ""}
        </option>
      ))}
    </select>
  );
}
