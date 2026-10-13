import { useMemo, useState } from "react";

import { useCan } from "@/shared/permissions";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  Archive,
  Building2,
  Check,
  ChevronDown,
  EyeOff,
  type IconComponent,
} from "@/shared/ui/icons";

import {
  useListManagedProjectsQuery,
  useListProjectsQuery,
  type Project,
} from "../../api/project-api";
import { ProjectLogo } from "../../components/project-files";
import { ProjectAccessReport } from "../access-report";
import { ProjectAccessPanel } from "./project-access-panel";
import { AccessLoading, LoadFailed } from "./project-access-status";
import "./project-access.css";

// The Team page's "Project access" view: pick a Project in the toolbar,
// then manage who has access to it, Groups and people in one list. The
// Projects a person can see come first; Projects they manage without
// seeing come after a divider, each with a lock, to join. Company owners
// and admins also get the report of people cut off from their own work.
export function TeamProjectAccess({
  companyId,
  projectId,
  onSelectProject,
}: {
  companyId: string;
  projectId: string | null;
  onSelectProject: (projectId: string) => void;
}) {
  const managesProjects = useCan("projects.manage");
  const readable = useListProjectsQuery(companyId);
  const managed = useListManagedProjectsQuery(companyId, {
    skip: !managesProjects,
  });
  const groups = useMemo(() => {
    const byTitle = (left: Project, right: Project) =>
      Number(Boolean(left.archivedAt)) - Number(Boolean(right.archivedAt)) ||
      left.title.localeCompare(right.title);
    const readableItems = readable.currentData?.items ?? [];
    // A Project someone just joined may still sit in the managed list
    // until it reloads; it is listed once, as one they can see.
    const readableIds = new Set(readableItems.map((project) => project.id));
    return {
      readable: [...readableItems].sort(byTitle),
      managed: (managed.currentData?.items ?? [])
        .filter((project) => !readableIds.has(project.id))
        .sort(byTitle),
    };
  }, [readable.currentData, managed.currentData]);
  const all = [...groups.readable, ...groups.managed];
  // The Project on screen. A change that moves it from one list to the
  // other (a manager let into it) reloads both, and the one that lands
  // first may not list it yet: it stays on screen until both have
  // reloaded, so the page neither flashes "No projects yet" nor jumps.
  const [shown, setShown] = useState<Project | null>(null);
  const held = shown?.companyId === companyId ? shown : null;
  const wanted = projectId ?? held?.id ?? null;
  const reloading = readable.isFetching || managed.isFetching;
  const selected =
    all.find((project) => project.id === wanted) ??
    (reloading && held && held.id === wanted ? held : null) ??
    all[0] ??
    null;
  if (selected !== shown) setShown(selected);
  // Both lists answer before the page says there is no Project.
  const managedLoading =
    managesProjects && !managed.currentData && !managed.isError;

  if (readable.isError) {
    return (
      <LoadFailed
        message="Couldn't load projects"
        onRetry={() => void readable.refetch()}
      />
    );
  }
  if (!readable.currentData || managedLoading) return <AccessLoading />;
  if (!selected) {
    return (
      <p className="m-0 p-[28px_0] text-workspace-muted text-center">
        No projects yet
      </p>
    );
  }
  return (
    <div className="team-project-access flex flex-col gap-[16px]">
      {managesProjects ? (
        <ProjectAccessReport companyId={companyId} onOpen={onSelectProject} />
      ) : null}
      <ProjectAccessPanel
        key={selected.id}
        companyId={companyId}
        project={selected}
        toolbarStart={
          <ProjectPicker
            companyId={companyId}
            groups={groups}
            selected={selected}
            onSelect={onSelectProject}
          />
        }
      />
    </div>
  );
}

function ProjectPicker({
  companyId,
  groups,
  selected,
  onSelect,
}: {
  companyId: string;
  groups: { readable: Project[]; managed: Project[] };
  selected: Project;
  onSelect: (projectId: string) => void;
}) {
  const item = (project: Project, joined: boolean) => (
    <DropdownMenuItem
      key={project.id}
      className="team-project-picker__item"
      onSelect={() => onSelect(project.id)}
    >
      <ProjectLogo
        companyId={companyId}
        file={project.files?.logo}
        title={project.title}
      />
      <span className="team-project-picker__text flex min-w-0 flex-1 flex-col gap-[1px]">
        <strong>{project.title}</strong>
        <ProjectNote joined={joined} project={project} />
      </span>
      {project.id === selected.id ? (
        <Check size={14} aria-hidden="true" />
      ) : null}
    </DropdownMenuItem>
  );
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          aria-label={`Project: ${selected.title}`}
          className="team-project-picker"
          type="button"
        >
          <ProjectLogo
            companyId={companyId}
            file={selected.files?.logo}
            title={selected.title}
          />
          <span className="team-project-picker__text flex min-w-0 flex-1 flex-col gap-[1px]">
            <strong>{selected.title}</strong>
            {/* The Join button beside the picker carries the lock. */}
            <ProjectNote joined project={selected} />
          </span>
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="team-project-picker__menu"
        collisionPadding={12}
      >
        {groups.readable.map((project) => item(project, true))}
        {groups.managed.length > 0 ? (
          <>
            {groups.readable.length > 0 ? <DropdownMenuSeparator /> : null}
            {groups.managed.map((project) => item(project, false))}
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// Under a Project's title, as icons only: open to everyone, archived, and,
// in the menu only, not joined (managed without reading it); nothing for
// a plain Project.
function ProjectNote({
  joined,
  project,
}: {
  joined: boolean;
  project: Project;
}) {
  const marks: { Icon: IconComponent; label: string }[] = [];
  if (project.visibility === "company") {
    marks.push({ Icon: Building2, label: "Open to everyone" });
  }
  if (project.archivedAt) marks.push({ Icon: Archive, label: "Archived" });
  if (!joined) marks.push({ Icon: EyeOff, label: "Not joined" });
  if (marks.length === 0) return null;
  return (
    <small>
      {marks.map(({ Icon, label }) => (
        <span key={label} aria-label={label} role="img" title={label}>
          <Icon aria-hidden="true" size={12} />
        </span>
      ))}
    </small>
  );
}
