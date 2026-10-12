import { PageDocumentLoading } from "@/features/project-canvas/page-document-loading";
import { ProjectUploadedFilesLoading } from "@/features/project-canvas/project-uploaded-files-loading";
import sidebarStyles from "./project-sidebar.module.css";
import { ProjectLogo } from "../components/project-files";
import "./project-overview.css";
import { NewTaskButton } from "@/features/company-plan/components/new-task-button";
import { WorkTaskInspector } from "../routes/company-plan-page";
import { ProjectPerformance } from "./project-performance";
import { ProjectTasks } from "./project-tasks";
import { ProjectApprovals } from "./project-approvals";
import { teamProjectAccessPath } from "./access/team-access-path";
import type { Project } from "../api/project-api";
import { useProjectAccessLoss } from "../hooks/use-project-access-loss";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import { useProjectAccess, type ProjectAccessView } from "@/shared/permissions";
import { ProjectVisibilityLabel } from "../components/project-controls";
import { useCanvasFullscreen } from "@/app/shell/workspace-layout-context";

import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";
import { usePanelResize } from "@/shared/ui/use-panel-resize";
import {
  Link,
  Navigate,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import { lazy, Suspense, useEffect, useRef, type CSSProperties } from "react";
const ProjectCanvas = lazy(
  () => import("@/features/project-canvas/project-canvas"),
);

const ProjectUploadedFiles = lazy(
  () => import("@/features/project-canvas/project-uploaded-files"),
);

const ProjectLibrary = lazy(
  () => import("@/features/project-canvas/project-library"),
);

const ProjectOverviewDocument = lazy(
  () => import("@/features/project-canvas/project-overview-document"),
);

type ProjectSection =
  | "overview"
  | "tasks"
  | "approvals"
  | "analysis"
  | "pages"
  | "artifacts"
  | "settings";

type ProjectWorkspaceProps = {
  /** Which Work view the Task inspector mounts; it grants nothing here. */
  canManage: boolean;
  companyId: string;
  onEdit: () => void;
  /** Asks "Archive {Project}?" for someone who may archive but not edit. */
  onArchive: () => void;
  project: Project;
};

export function ProjectWorkspace({
  canManage,
  companyId,
  onEdit,
  onArchive,
  project,
}: ProjectWorkspaceProps) {
  const navigate = useNavigate();
  const { section } = useParams<{ section?: string }>();
  // Everything inside a Project is gated on the caller's own permissions on
  // it (ADR 0017). Access itself is managed on the Team page: the old
  // Access section and a manager who can't read the Project go there, where
  // they may join.
  const access = useProjectAccess(project);
  // Edit project opens for the details or the files; someone who may only
  // archive gets the Archive question instead of a greyed form.
  const editable =
    access.can("project.settings.update") || access.can("project.files.manage");
  const toTeam = !access.reads || section === "access";
  const activeSection: ProjectSection =
    section === "tasks" ||
    section === "pages" ||
    section === "artifacts" ||
    section === "settings" ||
    section === "approvals" ||
    section === "analysis"
      ? section
      : "overview";
  const canvasFullscreen = useCanvasFullscreen();
  const [searchParams, setSearchParams] = useSearchParams();
  const isCanvasView =
    activeSection === "artifacts" && searchParams.get("view") !== "gallery";
  useEffect(() => {
    if (section !== "documents") return;
    navigate(
      {
        pathname: `/projects/${project.id}/pages`,
        search: searchParams.toString(),
      },
      { replace: true },
    );
  }, [navigate, project.id, searchParams, section]);
  const selectedTaskId = searchParams.get("taskId") ?? undefined;
  const expanded = !!selectedTaskId && searchParams.get("taskView") === "full";
  const workspaceRef = useRef<HTMLElement>(null);
  const inspectorRef = useRef<HTMLDivElement>(null);
  const inspectorResize = usePanelResize(inspectorRef, {
    side: "right",
    visible: Boolean(selectedTaskId) && !expanded,
    defaultWidth: 480,
    minWidth: 400,
    remainingWidth: 0,
    widthLimit: 640,
    label: "Resize task details",
  });
  const previousTaskId = useRef(selectedTaskId);
  useEffect(() => {
    const closedTaskId = previousTaskId.current;
    previousTaskId.current = selectedTaskId;
    if (selectedTaskId || !closedTaskId) return;
    const frame = requestAnimationFrame(() => {
      const trigger = workspaceRef.current?.querySelector<HTMLButtonElement>(
        `button[data-task-id="${CSS.escape(closedTaskId)}"]`,
      );
      (
        trigger ??
        workspaceRef.current?.querySelector<HTMLAnchorElement>(
          ".project-workspace-nav-item.is-active",
        )
      )?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [selectedTaskId]);

  const setSelectedTaskId = (taskId: string | undefined) => {
    const next = new URLSearchParams(searchParams);
    if (taskId) next.set("taskId", taskId);
    else {
      next.delete("taskId");
      next.delete("taskView");
    }
    setSearchParams(next);
  };
  const tasksQuery = useListWorkspaceTasksQuery(
    {
      companyId,
      options: { project_id: project.id },
    },
    {
      skip:
        !access.reads ||
        (!["tasks", "approvals", "analysis"].includes(activeSection) &&
          !selectedTaskId),
    },
  );
  const tasks = tasksQuery.currentData?.items ?? [];
  // A 404 here may mean the viewer just lost the Project: nothing shows
  // until the Project is read again, and then they leave or see the error.
  const checkingAccess = useProjectAccessLoss(
    companyId,
    project.id,
    tasksQuery.error,
  );
  const style = {
    "--project-accent": "var(--workspace-native-accent)",
    "--project-task-inspector-width": `${inspectorResize.width}px`,
  } as CSSProperties;

  if (toTeam)
    return <Navigate replace to={teamProjectAccessPath(project.id)} />;
  if (section === "files") {
    return (
      <Navigate
        replace
        to={{
          pathname: `/projects/${project.id}/settings`,
          search: searchParams.toString(),
          hash: "#uploaded-files",
        }}
      />
    );
  }
  if (section === "canvas") {
    const next = new URLSearchParams(searchParams);
    next.set("view", "canvas");
    return (
      <Navigate
        replace
        to={{
          pathname: `/projects/${project.id}/artifacts`,
          search: next.toString(),
        }}
      />
    );
  }

  return (
    <section
      ref={workspaceRef}
      aria-label={`${project.title} project workspace`}
      className="project-workspace-page"
      style={style}
    >
      <div className="project-workspace-frame">
        <ProjectSidebar
          activeSection={activeSection}
          access={access}
          companyId={companyId}
          hidden={isCanvasView && canvasFullscreen.active}
          project={project}
        />
        <main className="project-workspace-main">
          <div
            className={cx(
              "project-workspace-body",
              selectedTaskId && "has-task-inspector",
              expanded && "is-task-expanded",
            )}
          >
            {!expanded ? (
              <div className="project-workspace-view">
                {["tasks", "approvals", "analysis"].includes(activeSection) ? (
                  <ProjectReadState
                    checkingAccess={checkingAccess}
                    query={tasksQuery}
                  />
                ) : null}
                {isCanvasView ? (
                  <Suspense
                    fallback={
                      <p className="project-workspace-state">Opening canvas…</p>
                    }
                  >
                    <ProjectCanvas
                      companyId={companyId}
                      projectId={project.id}
                    />
                  </Suspense>
                ) : activeSection === "overview" ? (
                  <Suspense fallback={<PageDocumentLoading overview />}>
                    <ProjectOverviewDocument
                      key={`${companyId}:${project.id}`}
                      companyId={companyId}
                      projectId={project.id}
                      project={project}
                    />
                  </Suspense>
                ) : activeSection === "pages" ||
                  activeSection === "artifacts" ? (
                  <Suspense
                    fallback={
                      <p className="project-workspace-state">
                        Opening {activeSection}…
                      </p>
                    }
                  >
                    <ProjectLibrary
                      key={`${companyId}:${project.id}:${activeSection}`}
                      companyId={companyId}
                      projectId={project.id}
                      kind={activeSection}
                    />
                  </Suspense>
                ) : activeSection === "settings" ? (
                  <section className="project-settings-surface">
                    <h2>Project settings</h2>
                    <dl>
                      <dt>Project key</dt>
                      <dd>{project.key}</dd>
                      <dt>Purpose</dt>
                      <dd>
                        {project.description ||
                          "Add a purpose to guide this Project."}
                      </dd>
                      <dt>Status</dt>
                      <dd>{project.archivedAt ? "Archived" : "Active"}</dd>
                    </dl>
                    <div className="project-settings-actions">
                      <Link
                        className="dashboard-button"
                        to={teamProjectAccessPath(project.id)}
                      >
                        <Icon name="security" className="size-4" />
                        {access.can("project.grants.manage")
                          ? "Manage access"
                          : "Access"}
                      </Link>
                      {project.archivedAt ? null : editable ? (
                        <button
                          className="dashboard-button"
                          type="button"
                          onClick={onEdit}
                        >
                          <Icon name="edit" className="size-4" />
                          Edit project
                        </button>
                      ) : access.can("project.archive") ? (
                        <button
                          className="dashboard-button"
                          type="button"
                          onClick={onArchive}
                        >
                          <Icon name="archive" className="size-4" />
                          Archive project
                        </button>
                      ) : null}
                    </div>
                    <Suspense fallback={<ProjectUploadedFilesLoading />}>
                      <ProjectUploadedFiles
                        key={`${companyId}:${project.id}`}
                        companyId={companyId}
                        documents={project.files?.documents ?? []}
                        canManageFiles={
                          access.can("project.files.manage") &&
                          !project.archivedAt
                        }
                        onManageFiles={onEdit}
                      />
                    </Suspense>
                  </section>
                ) : tasksQuery.currentData && !checkingAccess ? (
                  activeSection === "tasks" ? (
                    <ProjectTasks
                      companyId={companyId}
                      project={project}
                      tasks={tasks}
                      onSelectTask={setSelectedTaskId}
                      selectedTaskId={selectedTaskId}
                    />
                  ) : activeSection === "approvals" ? (
                    <ProjectApprovals companyId={companyId} tasks={tasks} />
                  ) : (
                    <ProjectPerformance
                      companyId={companyId}
                      project={project}
                      tasks={tasks}
                    />
                  )
                ) : null}
              </div>
            ) : null}
            {selectedTaskId ? (
              <div
                className="project-workspace-inspector"
                id={`project-task-inspector-${project.id}`}
                ref={inspectorRef}
              >
                {!expanded ? (
                  <div
                    className="project-task-inspector-resize"
                    aria-controls={`project-task-inspector-${project.id}`}
                    aria-description="Drag or use arrow keys to resize. Double-click to reset."
                    {...inspectorResize.separatorProps}
                  />
                ) : null}
                <WorkTaskInspector
                  canManage={canManage}
                  companyId={companyId}
                  projectScopeId={project.id}
                  onOpenProjectGraph={() => {
                    const next = new URLSearchParams(searchParams);
                    next.delete("taskView");
                    next.set("view", "dependencies");
                    navigate(`/projects/${project.id}/tasks?${next}`);
                  }}
                />
              </div>
            ) : null}
          </div>
        </main>
      </div>
    </section>
  );
}

function ProjectSidebar({
  activeSection,
  access,
  companyId,
  hidden,
  project,
}: {
  activeSection: ProjectSection;
  access: ProjectAccessView;
  companyId: string;
  hidden: boolean;
  project: Project;
}) {
  const navRef = useRef<HTMLElement>(null);
  // On a phone the sections are one sideways row; keep the open one in
  // view. Only the row scrolls, never the page.
  useEffect(() => {
    const nav = navRef.current;
    const item = nav?.querySelector<HTMLElement>(
      ".project-workspace-nav-item.is-active",
    );
    if (!nav || !item || nav.scrollWidth <= nav.clientWidth) return;
    const navBox = nav.getBoundingClientRect();
    const itemBox = item.getBoundingClientRect();
    if (itemBox.left >= navBox.left && itemBox.right <= navBox.right) return;
    nav.scrollTo({
      left:
        nav.scrollLeft +
        itemBox.left -
        navBox.left -
        (navBox.width - itemBox.width) / 2,
    });
  }, [activeSection]);
  return (
    <aside
      id="project-workspace-sections"
      className="project-workspace-sidebar"
      aria-label="Project sections"
      hidden={hidden}
    >
      <div className={sidebarStyles["project-workspace-sidebar-project"]}>
        <Link
          className={sidebarStyles["project-workspace-sidebar-project-name"]}
          to={`/projects/${project.id}`}
          title={project.title}
        >
          <ProjectLogo
            companyId={companyId}
            file={project.files?.logo}
            title={project.title}
          />
          <span>{project.title}</span>
        </Link>
      </div>
      <nav ref={navRef} className="project-workspace-nav">
        <ProjectNavLink
          active={activeSection === "overview"}
          icon="category"
          label="Overview"
          to={`/projects/${project.id}`}
        />
        <ProjectNavLink
          active={activeSection === "tasks"}
          icon="task"
          label="Tasks"
          to={`/projects/${project.id}/tasks`}
        />
        <ProjectNavLink
          active={activeSection === "pages"}
          icon="document-text"
          label="Pages"
          to={`/projects/${project.id}/pages`}
        />
        <ProjectNavLink
          active={activeSection === "artifacts"}
          icon="box"
          label="Results"
          to={`/projects/${project.id}/artifacts`}
        />
        <ProjectNavLink
          active={activeSection === "settings"}
          icon="setting-2"
          label="Settings"
          to={`/projects/${project.id}/settings`}
        />
      </nav>
      <div className={sidebarStyles["project-workspace-sidebar-footer"]}>
        {access.can("project.tasks.write") && !project.archivedAt ? (
          <NewTaskButton companyId={companyId} projectId={project.id} />
        ) : null}
        <div className={sidebarStyles["project-workspace-sidebar-status"]}>
          <ProjectVisibilityLabel visibility={project.visibility} />
          {project.archivedAt ? <span>Archived</span> : null}
        </div>
      </div>
    </aside>
  );
}

function ProjectNavLink({
  active = false,
  icon,
  label,
  to,
}: {
  active?: boolean;
  icon: string;
  label: string;
  to: string;
}) {
  return (
    <Link
      aria-label={label}
      title={label}
      aria-current={active ? "page" : undefined}
      className={cx("project-workspace-nav-item", active && "is-active")}
      to={to}
    >
      <Icon name={icon} className="size-5" />
      <span>{label}</span>
    </Link>
  );
}

function ProjectReadState({
  checkingAccess,
  query,
}: {
  checkingAccess: boolean;
  query: ReturnType<typeof useListWorkspaceTasksQuery>;
}) {
  // Losing the Project mid-view is no load error: the access check takes
  // the viewer to Projects in a moment.
  if (query.isLoading || checkingAccess) {
    return (
      <p className="project-workspace-state" role="status">
        Loading project work…
      </p>
    );
  }
  if (query.isError) {
    return (
      <p className="project-workspace-state" role="alert">
        Project work could not be loaded.{" "}
        <button type="button" onClick={() => void query.refetch()}>
          Try again
        </button>
      </p>
    );
  }
  return null;
}
