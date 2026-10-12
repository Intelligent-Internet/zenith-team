import { ProjectFilesEditor, ProjectLogo } from "../components/project-files";
import type { components } from "@/shared/api/generated/api.generated";
import { useState, useEffect, useRef, useId } from "react";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { ProjectWorkspace } from "../projects/project-workspace";
import {
  ProjectAccessConfirmDialog,
  type AccessConfirmation,
} from "../projects/access/project-access-confirm-dialog";
import "../projects/project-workspace.css";
import { Link, useParams } from "react-router";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { hasProjectPermission, useCan } from "@/shared/permissions";
import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import {
  Archive,
  FolderOpen,
  Plus,
  Pencil,
  MoreHorizontal,
  LockKeyhole,
  Building2,
} from "@/shared/ui/icons";
import { ViewSwitch } from "@/shared/ui/collection-controls/controls";
import { cx } from "@/shared/lib/cx";
import {
  useListProjectsQuery,
  useGetProjectQuery,
  useCreateProjectMutation,
  useUpdateProjectMutation,
  useReplaceProjectFilesMutation,
  useArchiveProjectMutation,
  type Project,
} from "../api/project-api";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/shared/ui/dropdown-menu";
import {
  useAppDispatch,
  useAppSelector,
  selectProjectsView,
  setProjectsView,
} from "@/state";
import { Tooltip } from "@/shared/ui/tooltip";
import { toast } from "@/shared/ui/sonner";
import "../styles/projects.css";

const EVERYONE_HELP = "Everyone in the company can see it";

export function ProjectsPage() {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const canManage = useCan("projects.manage");
  return <ProjectsWorkspace companyId={companyId} canManage={canManage} />;
}

export function ProjectsWorkspace({
  companyId,
  canManage,
}: {
  companyId: string;
  canManage: boolean;
}) {
  const { projectId } = useParams();
  const dispatch = useAppDispatch();
  const view = useAppSelector(selectProjectsView);
  const projects = useListProjectsQuery(companyId, { skip: !companyId });
  const tasks = useListWorkspaceTasksQuery({ companyId }, { skip: !companyId });
  const [editor, setEditor] = useState<Project | "new" | null>(null);
  const editorTrigger = useRef<HTMLElement | null>(null);
  const projectMenuTriggers = useRef(new Map<string, HTMLButtonElement>());
  // Someone who may archive but not edit gets only the question, never a
  // greyed Edit project around one button.
  const [archive] = useArchiveProjectMutation();
  const [question, setQuestion] = useState<AccessConfirmation | null>(null);
  const askArchive = (project: Project) =>
    setQuestion({
      title: `Archive ${project.title}?`,
      action: "Archive",
      destructive: true,
      icon: "archive",
      pending: "Archiving…",
      run: async () => {
        try {
          await archive({
            companyId,
            id: project.id,
            expectedRevision: project.revision,
          }).unwrap();
        } catch (err) {
          toast.error(
            getBoardlyQueryErrorMessage(err, "Couldn't archive project"),
          );
        }
      },
    });
  const archiveQuestion = (
    <ProjectAccessConfirmDialog
      confirmation={question}
      onClose={() => setQuestion(null)}
    />
  );
  const [showArchived, setShowArchived] = useState(false);
  const openEditor = (
    project: Project | "new",
    trigger: HTMLElement | null = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null,
  ) => {
    editorTrigger.current = trigger;
    setEditor(project);
  };
  const restoreEditorFocus = () => {
    const trigger = editorTrigger.current;
    if (trigger?.isConnected && trigger.getClientRects().length) {
      trigger.focus();
    }
  };
  const items =
    projects.currentData?.items.filter(
      (project) => !!project.archivedAt === showArchived,
    ) ?? [];
  // A Project the caller manages without reading it is not in the list; it
  // is read on its own, and a 404 is the same "unavailable" as a missing one.
  const listed = projects.currentData?.items.find(
    (item) => item.id === projectId,
  );
  const single = useGetProjectQuery(
    { companyId, id: projectId ?? "" },
    { skip: !projectId || !projects.currentData || Boolean(listed) },
  );
  if (projectId) {
    const project = listed ?? single.currentData;
    if (!project)
      return (
        <WorkspacePageFrame
          className="projects-workspace-page"
          title="Project"
          ariaLabel="Project"
        >
          {projects.isLoading || single.isFetching ? (
            <p role="status">Loading project…</p>
          ) : projects.isError ? (
            <div className="project-unavailable" role="alert">
              <strong>Project could not be loaded.</strong>
              <button
                className="dashboard-button"
                type="button"
                onClick={() => void projects.refetch()}
              >
                Retry
              </button>
            </div>
          ) : (
            // A hidden Project and a missing one read the same, so this
            // says nothing about whether it exists.
            <div className="project-unavailable" role="status">
              <LockKeyhole size={28} aria-hidden="true" />
              <strong>You can't open this project.</strong>
              <span>It doesn't exist, or you don't have access to it.</span>
              <Link className="dashboard-button" to="/projects">
                Back to Projects
              </Link>
            </div>
          )}
        </WorkspacePageFrame>
      );
    return (
      <>
        <ProjectWorkspace
          key={project.id}
          companyId={companyId}
          project={project}
          canManage={canManage}
          onEdit={() => openEditor(project)}
          onArchive={() => askArchive(project)}
        />
        {editor && editor !== "new" ? (
          <ProjectEditor
            key={editor.id}
            companyId={companyId}
            project={editor}
            onClose={() => setEditor(null)}
            onRestoreFocus={restoreEditorFocus}
          />
        ) : null}
        {archiveQuestion}
      </>
    );
  }
  return (
    <WorkspacePageFrame
      className="projects-workspace-page projects-directory-page collection-page"
      contentWidth="contained"
      ariaLabel="Projects"
      tourId="projects-overview"
      title="Projects"
      actions={
        canManage ? (
          <button
            className="dashboard-button dashboard-button-primary"
            onClick={(event) => openEditor("new", event.currentTarget)}
          >
            <Plus size={16} /> New project
          </button>
        ) : undefined
      }
    >
      <div className="projects-page">
        <div className="projects-page__toolbar">
          <div className="projects-page__tabs" aria-label="Project status">
            <button
              aria-pressed={!showArchived}
              onClick={() => setShowArchived(false)}
            >
              Active{" "}
              {projects.currentData ? (
                <span>
                  {
                    projects.currentData.items.filter(
                      (project) => !project.archivedAt,
                    ).length
                  }
                </span>
              ) : null}
            </button>
            <button
              aria-pressed={showArchived}
              onClick={() => setShowArchived(true)}
            >
              Archived{" "}
              {projects.currentData ? (
                <span>
                  {
                    projects.currentData.items.filter(
                      (project) => !!project.archivedAt,
                    ).length
                  }
                </span>
              ) : null}
            </button>
          </div>
          <ViewSwitch
            ariaLabel="Project view"
            value={view}
            onChange={(nextView) => dispatch(setProjectsView(nextView))}
          />
        </div>
        {editor ? (
          <ProjectEditor
            key={editor === "new" ? "new" : editor.id}
            companyId={companyId}
            project={editor === "new" ? null : editor}
            onClose={() => setEditor(null)}
            onRestoreFocus={restoreEditorFocus}
          />
        ) : null}
        {archiveQuestion}
        {projects.isLoading ? (
          <p className="projects-page__message" role="status">
            Loading projects…
          </p>
        ) : projects.isError ? (
          <div className="projects-page__message" role="alert">
            Projects could not be loaded.{" "}
            <button
              className="dashboard-button"
              onClick={() => void projects.refetch()}
            >
              Retry
            </button>
          </div>
        ) : items.length === 0 ? (
          <div className="projects-page__empty">
            <FolderOpen size={32} />
            <h2>
              {showArchived
                ? "No archived projects"
                : "Your projects start here"}
            </h2>
            <p>
              {showArchived
                ? "Archived projects will stay here for reference."
                : canManage
                  ? "Create a project to bring related tasks together."
                  : "Projects you have access to will appear here."}
            </p>
          </div>
        ) : (
          <div
            className={cx(
              "projects-page__list",
              view === "list" && "collection-list",
            )}
            data-view={view}
          >
            {view === "list" ? (
              <div
                className="projects-page__columns collection-list__header"
                aria-hidden="true"
              >
                <span>Project</span>
                <span>Progress</span>
                <span />
              </div>
            ) : null}
            {items.map((project) => {
              const owned =
                tasks.currentData?.items.filter(
                  (task) => task.projectId === project.id,
                ) ?? [];
              const done = owned.filter(
                (task) => task.status === "done",
              ).length;
              const editable = canEditProject(project);
              const archivable = hasProjectPermission(
                project.access,
                "project.archive",
              );
              return (
                <article
                  className={cx(
                    "projects-page__row",
                    view === "list" && "collection-list__row",
                  )}
                  key={project.id}
                >
                  <div className="projects-page__identity collection-list__identity">
                    <span className="projects-page__icon">
                      <ProjectLogo
                        companyId={companyId}
                        file={project.files?.logo}
                        title={project.title}
                      />
                    </span>
                    <div className="projects-page__details">
                      <span className="projects-page__title-row">
                        <Link
                          className="projects-page__title"
                          title={project.title}
                          to={`/projects/${project.id}`}
                        >
                          {project.title}
                        </Link>
                        {project.visibility === "company" ? (
                          <Tooltip label={EVERYONE_HELP}>
                            <span
                              aria-label={EVERYONE_HELP}
                              className="projects-page__everyone"
                              role="img"
                              tabIndex={0}
                            >
                              <Building2 size={13} aria-hidden="true" />
                            </span>
                          </Tooltip>
                        ) : null}
                      </span>
                      {project.description ? (
                        view === "list" ? (
                          <p
                            className="collection-list__secondary"
                            title={project.description}
                          >
                            {project.description}
                          </p>
                        ) : (
                          <ProjectDescription text={project.description} />
                        )
                      ) : null}
                    </div>
                  </div>
                  <div
                    className="projects-page__progress"
                    data-complete={owned.length > 0 && done === owned.length}
                  >
                    <span className="projects-page__count">
                      {tasks.currentData ? (
                        owned.length === 0 ? (
                          "No tasks yet"
                        ) : done === owned.length ? (
                          "Completed"
                        ) : (
                          <>
                            <strong>
                              {done}/{owned.length}
                            </strong>{" "}
                            tasks completed
                          </>
                        )
                      ) : tasks.isError ? (
                        "Progress unavailable"
                      ) : (
                        "Loading progress…"
                      )}
                    </span>
                    {tasks.currentData && owned.length > 0 ? (
                      <div
                        className="projects-page__progress-track"
                        role="progressbar"
                        aria-label={`Completed tasks in ${project.title}`}
                        aria-valuemin={0}
                        aria-valuemax={owned.length}
                        aria-valuenow={done}
                      >
                        <span
                          style={{ width: `${(done / owned.length) * 100}%` }}
                        />
                      </div>
                    ) : null}
                  </div>
                  {(editable || archivable) && !project.archivedAt ? (
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          ref={(element) => {
                            if (element) {
                              projectMenuTriggers.current.set(
                                project.id,
                                element,
                              );
                            } else {
                              projectMenuTriggers.current.delete(project.id);
                            }
                          }}
                          type="button"
                          className="projects-page__menu"
                          aria-label={`Actions for ${project.title}`}
                        >
                          <MoreHorizontal size={18} />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        {editable ? (
                          <DropdownMenuItem
                            onSelect={() =>
                              openEditor(
                                project,
                                projectMenuTriggers.current.get(project.id) ??
                                  null,
                              )
                            }
                          >
                            <Pencil size={16} /> Edit project
                          </DropdownMenuItem>
                        ) : (
                          <DropdownMenuItem
                            onSelect={() => askArchive(project)}
                          >
                            <Archive size={16} /> Archive project
                          </DropdownMenuItem>
                        )}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  ) : null}
                </article>
              );
            })}
          </div>
        )}
      </div>
    </WorkspacePageFrame>
  );
}

// The editor holds the title and description, the files and Archive, each
// enabled by its own Project permission. It opens for someone who may
// change the details or the files; someone who may only archive gets the
// Archive question straight from the menu instead.
function canEditProject(project: Project) {
  return (
    hasProjectPermission(project.access, "project.settings.update") ||
    hasProjectPermission(project.access, "project.files.manage")
  );
}

function ProjectDescription({ text }: { text: string }) {
  const [expanded, setExpanded] = useState(false);
  const [overflows, setOverflows] = useState(false);
  const paragraph = useRef<HTMLParagraphElement>(null);
  const id = useId();
  useEffect(() => {
    const element = paragraph.current;
    if (!element) return;
    const measure = () => {
      const lineHeight = Number.parseFloat(
        getComputedStyle(element).lineHeight,
      );
      setOverflows(element.scrollHeight > lineHeight * 3 + 1);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [text]);
  return (
    <div className="projects-page__description">
      <p ref={paragraph} id={id} data-expanded={expanded}>
        {text}
      </p>
      {overflows ? (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={id}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      ) : null}
    </div>
  );
}

function ProjectEditor({
  companyId,
  project,
  onClose,
  onRestoreFocus,
}: {
  companyId: string;
  project: Project | null;
  onClose: () => void;
  onRestoreFocus: () => void;
}) {
  const existingProjects = useListProjectsQuery(companyId);
  const [files, setFiles] = useState<
    components["schemas"]["ProjectFilesInput"]
  >({
    logoId: project?.files?.logo?.id ?? null,
    documentIds: project?.files?.documents.map((file) => file.id) ?? [],
  });
  const [filesReady, setFilesReady] = useState(true);

  const [title, setTitle] = useState(project?.title ?? "");
  const [description, setDescription] = useState(project?.description ?? "");
  const baseKey = title
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[đĐ]/g, "d")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "");
  const normalizedKey = (
    baseKey.match(/^[a-z]/) ? baseKey : `project_${baseKey}`
  )
    .slice(0, 64)
    .replace(/_+$/g, "");
  let suggestedKey = normalizedKey;
  const taken = new Set(
    (existingProjects.currentData?.items ?? []).map((item) => item.key),
  );
  for (let index = 2; taken.has(suggestedKey); index++) {
    const suffix = `_${index}`;
    suggestedKey = normalizedKey.slice(0, 64 - suffix.length) + suffix;
  }
  const projectKey = project?.key ?? (title.trim() ? suggestedKey : "");

  const [error, setError] = useState("");
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  // Nothing brings an archived Project back, so Archive asks first. The
  // question stands in for the footer and the close button, so it is
  // Cancel and Archive only; it opens focused on itself, scrolled into
  // view, with no focus ring.
  const [confirmArchive, setConfirmArchive] = useState(false);
  const questionRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (confirmArchive) questionRef.current?.focus();
  }, [confirmArchive]);
  const filesChanged =
    files.logoId !== (project?.files?.logo?.id ?? null) ||
    JSON.stringify(files.documentIds) !==
      JSON.stringify(project?.files?.documents.map((file) => file.id) ?? []);
  const dirty =
    title !== (project?.title ?? "") ||
    description !== (project?.description ?? "") ||
    filesChanged ||
    !filesReady;
  const requestClose = () => {
    if (!dirty) {
      onClose();
      return;
    }
    setConfirmArchive(false);
    setConfirmDiscard(true);
  };
  const [create, creating] = useCreateProjectMutation();
  const [update, updating] = useUpdateProjectMutation();
  const [replaceFiles, replacingFiles] = useReplaceProjectFilesMutation();
  const [archive, archiving] = useArchiveProjectMutation();
  const saving =
    creating.isLoading || updating.isLoading || replacingFiles.isLoading;
  const pending = saving || archiving.isLoading;
  // Each part of an existing Project is its own permission (ADR 0017); the
  // creator of a new one holds them all.
  const canEditText =
    !project || hasProjectPermission(project.access, "project.settings.update");
  const canEditFiles =
    !project || hasProjectPermission(project.access, "project.files.manage");
  const canArchive =
    !!project && hasProjectPermission(project.access, "project.archive");
  // Someone who may only archive has nothing to save.
  const canSave = canEditText || canEditFiles;
  const archiveProject = async () => {
    if (!project) return;
    setError("");
    try {
      await archive({
        companyId,
        id: project.id,
        expectedRevision: project.revision,
      }).unwrap();
      onClose();
    } catch (err) {
      setConfirmArchive(false);
      setError(
        getBoardlyQueryErrorMessage(err, "Project could not be archived."),
      );
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (open || pending) return;
        // Escape while asking backs out of the question only.
        if (confirmArchive) setConfirmArchive(false);
        else requestClose();
      }}
    >
      <DialogContent
        className="project-editor-dialog"
        overlayClassName="project-editor-overlay"
        showCloseButton={!pending && !confirmArchive}
        onInteractOutside={(event) => event.preventDefault()}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          onRestoreFocus();
        }}
      >
        <form
          className="project-editor"
          onSubmit={async (event) => {
            event.preventDefault();
            setError("");
            if (!filesReady || !canSave || confirmArchive) return;
            try {
              if (project) {
                // Title and description, and the files, are separate
                // permissions on the server, so each is saved only when it
                // changed. Both check the Project revision, so the files
                // save names the revision the text save produced.
                let revision = project.revision;
                if (
                  title !== project.title ||
                  description !== project.description
                )
                  revision = (
                    await update({
                      companyId,
                      id: project.id,
                      request: {
                        expectedRevision: revision,
                        title: title.trim(),
                        description: description.trim(),
                      },
                    }).unwrap()
                  ).revision;
                if (filesChanged)
                  await replaceFiles({
                    companyId,
                    id: project.id,
                    request: { ...files, expectedRevision: revision },
                  }).unwrap();
              } else
                await create({
                  companyId,
                  request: {
                    key: projectKey,
                    files,
                    title: title.trim(),
                    description: description.trim(),
                  },
                }).unwrap();
              onClose();
            } catch (err) {
              void existingProjects.refetch();
              setError(
                getBoardlyQueryErrorMessage(err, "Project could not be saved."),
              );
            }
          }}
        >
          <DialogTitle>{project ? "Edit project" : "New project"}</DialogTitle>
          <DialogDescription className="sr-only">
            Set up your project and optionally add a logo and documents.
          </DialogDescription>
          <ProjectFilesEditor
            companyId={companyId}
            initial={project?.files}
            disabled={pending || !canEditFiles}
            onChange={setFiles}
            onReadyChange={setFilesReady}
            identity={
              <div className="project-editor__identity-fields">
                <label className="project-editor__field">
                  Project name
                  <input
                    autoFocus
                    required
                    maxLength={240}
                    value={title}
                    disabled={pending || !canEditText}
                    onChange={(event) => setTitle(event.target.value)}
                    placeholder="e.g. Website redesign"
                  />
                </label>
              </div>
            }
          >
            <label className="project-editor__field">
              <span>
                Description <small>Optional</small>
              </span>
              <textarea
                rows={3}
                maxLength={65536}
                value={description}
                disabled={pending || !canEditText}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="What are we building?"
              />
            </label>
          </ProjectFilesEditor>
          {error ? (
            <p role="alert" className="project-editor__error">
              {error}
            </p>
          ) : null}
          {confirmArchive && project ? (
            <section
              ref={questionRef}
              role="alertdialog"
              aria-modal="false"
              aria-labelledby="project-archive-title"
              className="project-discard-notice project-archive-notice"
              tabIndex={-1}
            >
              <h3 id="project-archive-title">
                <Archive size={16} aria-hidden="true" />
                <span>Archive {project.title}?</span>
              </h3>
              <div className="project-archive-notice__actions">
                <button
                  type="button"
                  className="dashboard-button"
                  disabled={pending}
                  onClick={() => setConfirmArchive(false)}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="dashboard-button project-discard-notice__danger"
                  disabled={pending}
                  onClick={() => void archiveProject()}
                >
                  {archiving.isLoading ? "Archiving…" : "Archive"}
                </button>
              </div>
            </section>
          ) : null}
          {confirmDiscard ? (
            <section
              role="alertdialog"
              aria-modal="false"
              aria-labelledby="project-discard-title"
              className="project-discard-notice"
            >
              <h3 id="project-discard-title">Discard this draft?</h3>
              <p>Your unsaved project details will be lost.</p>
              <button
                type="button"
                className="dashboard-button"
                autoFocus
                onClick={() => setConfirmDiscard(false)}
              >
                Keep editing
              </button>
              <button
                type="button"
                className="dashboard-button"
                onClick={onClose}
              >
                Discard draft
              </button>
            </section>
          ) : null}
          {confirmArchive ? null : (
            <div className="project-editor__footer">
              {project && canArchive ? (
                <button
                  type="button"
                  className="dashboard-button"
                  disabled={pending}
                  onClick={() => {
                    setError("");
                    setConfirmDiscard(false);
                    setConfirmArchive(true);
                  }}
                >
                  <Archive size={16} aria-hidden="true" />
                  Archive project
                </button>
              ) : (
                <span />
              )}
              <div>
                <button
                  type="button"
                  className="dashboard-button"
                  disabled={pending}
                  onClick={requestClose}
                >
                  Cancel
                </button>
                {canSave ? (
                  <button
                    className="dashboard-button dashboard-button-primary"
                    disabled={
                      pending ||
                      !filesReady ||
                      (!project &&
                        (!projectKey ||
                          existingProjects.isFetching ||
                          existingProjects.isError))
                    }
                  >
                    {saving
                      ? "Saving…"
                      : project
                        ? "Save changes"
                        : "Create project"}
                  </button>
                ) : null}
              </div>
            </div>
          )}
        </form>
      </DialogContent>
    </Dialog>
  );
}
