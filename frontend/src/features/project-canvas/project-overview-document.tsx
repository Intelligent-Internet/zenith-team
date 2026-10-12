import { PageDocumentLoading } from "./page-document-loading";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import { useCallback, useState } from "react";
import {
  useAppDispatch,
  useAppSelector,
  selectOverviewViews,
  setOverviewViews,
  type OverviewTaskView,
  type OverviewResultView,
} from "@/state";
import { OverviewSection } from "./overview-section";
import { Icon } from "@/shared/ui/icon";
import { PageDocument } from "./page-document";
import { ProjectOverviewWork } from "./project-overview-work";
import {
  useProjectOverviewQuery,
  useProjectPagesQuery,
  useUpdateOverviewMutation,
} from "./page-api";
import { useGetCanvasQuery, type CanvasScope } from "./canvas-api";
import { ProjectResultList } from "./work-blocks";
import type { Project } from "@/features/company-plan/api/project-api";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";

export default function ProjectOverviewDocument({
  project,
  ...scope
}: CanvasScope & { project: Project }) {
  const dispatch = useAppDispatch();
  const views = useAppSelector((state) =>
    selectOverviewViews(state, scope.companyId, scope.projectId),
  );
  const setViews = (next: typeof views) =>
    dispatch(setOverviewViews({ ...scope, views: next }));
  const overview = useProjectOverviewQuery(scope);
  const pages = useProjectPagesQuery(scope);
  const results = useGetCanvasQuery(scope);
  const work = useListWorkspaceTasksQuery({
    companyId: scope.companyId,
    options: { project_id: scope.projectId },
  });
  const [update, updating] = useUpdateOverviewMutation();
  const [sourceId, setSourceId] = useState("");
  const [error, setError] = useState("");
  const data = overview.currentData;
  const [readyPageId, setReadyPageId] = useState<string>();
  const [actionContainer, setActionContainer] = useState<HTMLDivElement | null>(
    null,
  );
  const handleReady = useCallback(() => {
    setReadyPageId(data?.pageId ?? undefined);
  }, [data?.pageId]);
  const loading =
    Boolean(data?.pageId && readyPageId !== data.pageId) ||
    work.isLoading ||
    results.isLoading;
  const designate = async (pageId?: string) => {
    setError("");
    try {
      await update({
        ...scope,
        input: {
          action: pageId ? "designate" : "create",
          pageId,
          requestKey: crypto.randomUUID(),
        },
      }).unwrap();
    } catch {
      setError(
        "The overview could not be updated. Your access or connection may have changed.",
      );
    }
  };
  if (overview.isLoading) return <PageDocumentLoading overview />;
  if (overview.isError || !data)
    return (
      <div className="po-state" role="alert">
        <h2>The overview could not be loaded</h2>
        <button onClick={() => void overview.refetch()}>Try again</button>
      </div>
    );
  return (
    <div className="po-overview-loading-boundary" aria-busy={loading}>
      <div className="pc-review-floating-actions" ref={setActionContainer} />
      {loading && (
        <div className="po-overview-loading-cover">
          <PageDocumentLoading overview />
        </div>
      )}
      <div
        className="po-document po-project-home"
        data-loading={loading || undefined}
        inert={loading}
        aria-hidden={loading || undefined}
      >
        {error && (
          <p role="alert" className="po-error">
            {error}
          </p>
        )}
        {data.pageId ? (
          <PageDocument
            actionContainer={loading ? null : actionContainer}
            key={data.pageId}
            onReady={handleReady}
            scope={{ ...scope, pageId: data.pageId }}
            reportContent={
              <>
                <section className="pc-pdf-project-section">
                  <h2>Plan and progress</h2>
                  {work.isFetching ? (
                    <p role="status">Loading Project work…</p>
                  ) : work.isError ? (
                    <p role="alert">Project work is unavailable.</p>
                  ) : (
                    <table>
                      <thead>
                        <tr>
                          <th scope="col">Task</th>
                          <th scope="col">Status</th>
                          <th scope="col">Responsible</th>
                        </tr>
                      </thead>
                      <tbody>
                        {work.currentData?.items.map((task) => (
                          <tr key={task.id}>
                            <td>{task.title}</td>
                            <td>{taskWorkStatusLabel(task)}</td>
                            <td>
                              {task.assignee.name}
                              {task.execution.agent
                                ? ` · ${task.execution.agent.name}`
                                : ""}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </section>
                <section className="pc-pdf-project-section">
                  <h2>Results</h2>
                  {results.isFetching ? (
                    <p role="status">Loading Project results…</p>
                  ) : results.isError ? (
                    <p role="alert">Results are unavailable.</p>
                  ) : (
                    <ProjectResultList
                      scope={scope}
                      artifacts={(results.currentData?.artifacts ?? []).filter(
                        (artifact) =>
                          artifact.latestVersion.outputId ||
                          artifact.latestVersion.sourceOutputId,
                      )}
                      display="table"
                    />
                  )}
                </section>
              </>
            }
            settingsContent={
              data.canEdit ? (
                <>
                  <label>
                    Project overview
                    <select
                      aria-label="Overview Page"
                      value={sourceId || data.pageId}
                      onChange={(event) => setSourceId(event.target.value)}
                      disabled={updating.isLoading}
                    >
                      <option value={data.pageId}>{data.title}</option>
                      {pages.currentData
                        ?.filter((page) => page.pageId !== data.pageId)
                        .map((page) => (
                          <option key={page.pageId} value={page.pageId ?? ""}>
                            {page.title}
                          </option>
                        ))}
                    </select>
                  </label>
                  {sourceId && sourceId !== data.pageId && (
                    <button
                      disabled={updating.isLoading}
                      onClick={() => void designate(sourceId)}
                    >
                      Use as overview
                    </button>
                  )}
                  {pages.isError && (
                    <p role="alert">
                      Pages could not be loaded.{" "}
                      <button onClick={() => void pages.refetch()}>
                        Retry
                      </button>
                    </p>
                  )}
                </>
              ) : undefined
            }
          />
        ) : (
          <section className="po-project-intro" aria-label="Project overview">
            <h1>{project.title}</h1>
            {project.description && (
              <p className="po-project-description">{project.description}</p>
            )}
            <div className="po-overview-prompt">
              <Icon name="document-text" className="size-5" />
              <div>
                <h2>
                  {data.canEdit ? "Add a project overview" : "Project overview"}
                </h2>
                <p>
                  {data.canEdit
                    ? "Keep the brief, key decisions and useful links together."
                    : "No overview yet. Follow the plan and progress below."}
                </p>
              </div>
              {data.canEdit && (
                <button
                  disabled={updating.isLoading}
                  onClick={() => void designate()}
                >
                  {updating.isLoading ? "Creating…" : "Create overview"}
                </button>
              )}
            </div>
          </section>
        )}
        <div className="po-overview-report">
          <section
            className="po-overview-work"
            aria-label="Project work and results"
          >
            <OverviewSection
              title="Plan and progress"
              value={views.tasks}
              options={[
                ["list", "List"],
                ["gallery", "Cards"],
                ["table", "Table"],
                ["board", "Board"],
              ]}
              onChange={(tasks) =>
                setViews({ ...views, tasks: tasks as OverviewTaskView })
              }
            >
              <ProjectOverviewWork {...scope} display={views.tasks} />
            </OverviewSection>
            <OverviewSection
              title="Results"
              value={views.results}
              options={[
                ["list", "List"],
                ["gallery", "Cards"],
                ["table", "Table"],
                ["expanded", "Featured"],
              ]}
              onChange={(results) =>
                setViews({ ...views, results: results as OverviewResultView })
              }
            >
              {results.isError ? (
                <p role="alert">
                  Results could not be loaded.{" "}
                  <button onClick={() => void results.refetch()}>Retry</button>
                </p>
              ) : results.isLoading ? (
                <p role="status">Loading results…</p>
              ) : (
                <ProjectResultList
                  scope={scope}
                  display={views.results}
                  artifacts={(results.currentData?.artifacts ?? []).filter(
                    (artifact) =>
                      artifact.latestVersion.outputId ||
                      artifact.latestVersion.sourceOutputId,
                  )}
                />
              )}
            </OverviewSection>
          </section>
        </div>
      </div>
    </div>
  );
}
