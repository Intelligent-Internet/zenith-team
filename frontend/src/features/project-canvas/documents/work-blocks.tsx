import { DocumentViewMode } from "./document-view-mode";
import { taskBlockSummary } from "./task-summary";
import { useScope, useValue } from "./intelligent-ui/reactive-values";
import { useListWorkspaceTasksQuery } from "@/shared/api/task-api";
import { BlockSettings, useBlockEditable } from "./block-settings";
import { PageReportMode } from "./page-report-context";
import { BlockViewSelect } from "./block-view-select";
import { useContext, createContext, type ReactNode } from "react";
import { NodeViewWrapper, type NodeViewProps } from "@tiptap/react";
import { useNavigate } from "react-router";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useTaskRun } from "@/features/company-plan/hooks/use-task-run";
import { taskStartAction } from "@/features/company-plan/model/task-execution";
import { Icon } from "@/shared/ui/icon";
import { useGetCanvasQuery, type CanvasScope } from "../api/canvas-api";
import type { components } from "@/shared/api/generated/api.generated";

export const WorkBlockScope = createContext<CanvasScope | null>(null);
type Task = components["schemas"]["OverviewTask"];

function workLane(status: string) {
  if (["backlog", "todo"].includes(status)) return "To do";
  if (status === "in_review") return "In review";
  if (status === "done") return "Completed";
  if (["error", "failed", "cancelled"].includes(status)) return "Stopped";
  return "In progress";
}

export function ProjectTaskCollection({
  tasks: visible,
  display,
  open,
  status,
  assignee,
  action,
}: {
  tasks: Task[];
  display: string;
  open: (id: string) => void;
  status: (task: Task) => ReactNode;
  assignee: (task: Task) => ReactNode;
  action?: (task: Task) => ReactNode;
}) {
  const row = (task: Task) => (
    <div className="pc-work-row" key={task.id}>
      <Icon name="task-square" className="size-4" />
      <button className="pc-work-title" onClick={() => open(task.id)}>
        {task.title}
      </button>
      {status(task)}
      {assignee(task)}
      {action?.(task)}
    </div>
  );
  return (
    <div className={`pc-work-${display}`}>
      {display === "table" ? (
        <div className="pc-work-table">
          <table>
            <caption className="sr-only">Project work</caption>
            <thead>
              <tr>
                <th scope="col">Task</th>
                <th scope="col">Status</th>
                <th scope="col">Responsible</th>
                {action && (
                  <th scope="col">
                    <span className="sr-only">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {visible.map((task) => (
                <tr key={task.id}>
                  <td>
                    <button
                      className="pc-work-title"
                      onClick={() => open(task.id)}
                    >
                      {task.title}
                    </button>
                  </td>
                  <td>{status(task)}</td>
                  <td>{assignee(task)}</td>
                  {action && (
                    <td className="pc-work-action-cell">{action(task)}</td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : display === "board" ? (
        <div className="pc-work-lanes">
          {["To do", "In progress", "In review", "Completed", "Stopped"].map(
            (lane) => {
              const laneTasks = visible.filter(
                (task) => workLane(task.status) === lane,
              );
              return laneTasks.length ? (
                <section className="pc-work-lane" key={lane}>
                  <h4>
                    {lane}
                    <span>{laneTasks.length}</span>
                  </h4>
                  {laneTasks.map(row)}
                </section>
              ) : null;
            },
          )}
        </div>
      ) : (
        <div className="pc-work-rows">{visible.map(row)}</div>
      )}
    </div>
  );
}

function matches(status: string, filter: string) {
  if (filter === "active") return !["done", "cancelled"].includes(status);
  if (filter === "review") return status === "in_review";
  if (filter === "done") return status === "done";
  return true;
}

export function WorkBlock(props: NodeViewProps) {
  const scope = useContext(WorkBlockScope);
  const mode = useContext(DocumentViewMode);
  const reactive = useScope(props);
  const expression =
    props.node.type.name === "taskCollection" &&
    typeof props.node.attrs.filter === "object" &&
    props.node.attrs.filter !== null
      ? props.node.attrs.filter
      : undefined;
  const filterResult = useValue(
    reactive.template ? undefined : expression,
    reactive.snapshot,
    mode,
  );
  if (!scope)
    return (
      <NodeViewWrapper className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
        Project work is unavailable here.
      </NodeViewWrapper>
    );
  return (
    <ProjectWorkBlock
      {...props}
      scope={scope}
      filterResult={filterResult}
      template={reactive.template}
    />
  );
}

function ProjectWorkBlock({
  node,
  editor,
  updateAttributes,
  scope,
  filterResult,
  template,
}: NodeViewProps & {
  scope: CanvasScope;
  filterResult: { status: string; value?: unknown };
  template: boolean;
}) {
  const editable = useBlockEditable(editor);
  const report = useContext(PageReportMode);
  const mode = useContext(DocumentViewMode);
  const captured = mode === "snapshot" || Boolean(node.attrs.capturedAt);
  const navigate = useNavigate();
  const session = useSessionSnapshot();
  const reactiveFilter =
    node.type.name === "taskCollection" &&
    node.attrs.filter !== null &&
    typeof node.attrs.filter === "object";
  const filterReady =
    !reactiveFilter ||
    (filterResult.status === "ready" &&
      typeof filterResult.value === "string" &&
      ["all", "active", "review", "done"].includes(filterResult.value));
  const query = useListWorkspaceTasksQuery(
    { companyId: scope.companyId, options: { project_id: scope.projectId } },
    {
      skip: captured || template || !filterReady,
      refetchOnMountOrArgChange: true,
      refetchOnFocus: true,
      refetchOnReconnect: true,
      pollingInterval: reactiveFilter ? 60000 : 0,
      skipPollingIfUnfocused: true,
    },
  );
  const runner = useTaskRun(scope.companyId);
  const filter = reactiveFilter
    ? String(filterResult.value ?? "")
    : String(node.attrs.filter || "all");
  const display = String(node.attrs.display || "list");
  const tasks: Task[] =
    template ||
    !filterReady ||
    (reactiveFilter && !captured && (query.isFetching || query.isError))
      ? []
      : captured
        ? (node.attrs.capturedTasks ?? [])
        : (query.currentData?.items ?? []).map(taskBlockSummary);
  const rows = tasks.filter(
    (task) =>
      (node.type.name !== "taskReference" || task.id === node.attrs.taskId) &&
      matches(task.status, filter),
  );
  const visible = report ? rows : rows.slice(0, 50);
  const open = (id: string) =>
    navigate(`/projects/${scope.projectId}?taskId=${encodeURIComponent(id)}`);
  const status = (task: Task) => (
    <span className={`pc-work-status is-${task.status}`}>
      {task.status.replaceAll("_", " ")}
    </span>
  );
  const assignee = (task: Task) => (
    <span className="pc-work-assignee">
      {task.assigneeName}
      {task.agentName && <small>With {task.agentName}</small>}
    </span>
  );
  const action = (task: Task) => {
    const live = query.currentData?.items.find((row) => row.id === task.id);
    const start =
      mode === "live" && !report && !captured && live?.dependenciesResolved
        ? taskStartAction(live, session.userId)
        : null;
    return start && live ? (
      <button
        className="pc-work-action"
        disabled={!!runner.pendingTaskId}
        onClick={() => void runner.runTask(live)}
      >
        {runner.pendingTaskId === task.id ? "Starting…" : start.label}
      </button>
    ) : (
      <button className="pc-work-action" onClick={() => open(task.id)}>
        {captured ? "Open current Task" : "Open Task"}
      </button>
    );
  };
  return (
    <NodeViewWrapper
      className={`pc-has-settings pc-work-block pc-work-${display}`}
      data-block-id={node.attrs.blockId}
      contentEditable={false}
    >
      <header className="pc-collection-heading">
        <span>
          <Icon name="task-square" className="size-4" />
          {node.type.name === "taskReference" ? "Task" : "Project work"}
        </span>
        <small>
          {rows.length} {rows.length === 1 ? "task" : "tasks"} ·{" "}
          {rows.filter((task) => task.status === "done").length} complete
        </small>
      </header>
      {mode === "comparison" && (
        <p className="text-[var(--pc-muted)] text-[length:11px] leading-[1.4] m-[0_0_8px]">
          {node.type.name === "taskReference"
            ? "Task reference"
            : "Project work"}{" "}
          · {filter} · {display}
        </p>
      )}
      {editable && (
        <BlockSettings label="Work options">
          {node.type.name === "taskReference" ? (
            <label>
              Task
              <select
                aria-label="Referenced Task"
                value={node.attrs.taskId || ""}
                onChange={(event) =>
                  updateAttributes({ taskId: event.target.value })
                }
              >
                <option value="">Choose a Task…</option>
                {tasks.map((task) => (
                  <option key={task.id} value={task.id}>
                    {task.title}
                  </option>
                ))}
              </select>
            </label>
          ) : (
            <label>
              Show
              <select
                aria-label={
                  reactiveFilter
                    ? "Scenario-controlled Task filter"
                    : "Task filter"
                }
                disabled={reactiveFilter}
                value={filter}
                onChange={(event) =>
                  updateAttributes({ filter: event.target.value })
                }
              >
                {["all", "active", "review", "done"].map((value) => (
                  <option key={value} value={value}>
                    {value === "review"
                      ? "In review"
                      : value === "all"
                        ? "All work"
                        : value === "active"
                          ? "Active work"
                          : "Completed"}
                  </option>
                ))}
              </select>
            </label>
          )}
          <BlockViewSelect
            value={display}
            options={[
              ["list", "List"],
              ["gallery", "Cards"],
              ["table", "Table"],
              ["board", "Board"],
            ]}
            onChange={(display) => updateAttributes({ display })}
          />
        </BlockSettings>
      )}
      {template ? (
        <p role="status">Task collection template</p>
      ) : !filterReady ? (
        <p role="status">
          {filterResult.status === "loading"
            ? "Updating filter…"
            : "The scenario filter is unavailable. Choose all, active, review, or done."}
        </p>
      ) : !captured &&
        (query.isLoading || (reactiveFilter && query.isFetching)) ? (
        <p role="status">Loading work…</p>
      ) : !captured && query.isError ? (
        <p role="alert">
          Work is unavailable.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : visible.length ? (
        <ProjectTaskCollection
          tasks={visible}
          display={display}
          open={open}
          status={status}
          assignee={assignee}
          action={action}
        />
      ) : (
        <p className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
          {node.type.name === "taskReference"
            ? node.attrs.taskId
              ? "This Task is unavailable in this view."
              : "Choose a Task from this Project."
            : "No work matches this view."}
        </p>
      )}
      {rows.length > visible.length && (
        <button
          className="pc-work-more"
          onClick={() => navigate(`/projects/${scope.projectId}/tasks`)}
        >
          Open all {rows.length} Tasks
        </button>
      )}
      {node.attrs.capturedAt && (
        <small>
          Captured {new Date(node.attrs.capturedAt).toLocaleString()}
        </small>
      )}
      {runner.dialog}
    </NodeViewWrapper>
  );
}

export function OutputCollectionBlock(props: NodeViewProps) {
  const scope = useContext(WorkBlockScope);
  const { template } = useScope(props);
  if (!scope)
    return (
      <NodeViewWrapper className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
        Project results are unavailable here.
      </NodeViewWrapper>
    );
  return (
    <ProjectOutputCollectionBlock
      {...props}
      scope={scope}
      template={template}
    />
  );
}

function ProjectOutputCollectionBlock({
  node,
  editor,
  updateAttributes,
  scope,
  template,
}: NodeViewProps & { scope: CanvasScope; template: boolean }) {
  const editable = useBlockEditable(editor);
  const mode = useContext(DocumentViewMode);
  const captured = mode === "snapshot" || Boolean(node.attrs.capturedAt);
  const query = useGetCanvasQuery(scope, { skip: captured || template });
  const included = new Set<string>();
  editor.state.doc.descendants((block) => {
    if (block.type.name === "artifactReference") {
      included.add(String(block.attrs.artifactId));
      included.add(String(block.attrs.versionId));
    }
  });
  const all = template
    ? []
    : captured
      ? ((node.attrs.capturedOutputs ??
          []) as components["schemas"]["CanvasArtifact"][])
      : (query.currentData?.artifacts ?? []);
  const rows = all.filter(
    (artifact) =>
      (captured ||
        artifact.latestVersion.outputId ||
        artifact.latestVersion.sourceOutputId) &&
      (node.attrs.filter !== "new" ||
        (!included.has(artifact.id) &&
          !included.has(artifact.latestVersionId))),
  );
  return (
    <NodeViewWrapper
      className={`pc-has-settings pc-output-collection is-${node.attrs.display || "list"}`}
      contentEditable={false}
      data-block-id={node.attrs.blockId}
    >
      <header className="pc-collection-heading">
        <span>
          <Icon name="document-text" className="size-4" />
          Results
        </span>
        <small>
          {rows.length} {rows.length === 1 ? "result" : "results"}
        </small>
      </header>
      {mode === "comparison" && (
        <p className="text-[var(--pc-muted)] text-[length:11px] leading-[1.4] m-[0_0_8px]">
          Results · {node.attrs.filter || "all"} ·{" "}
          {node.attrs.display || "list"}
        </p>
      )}
      {editable && (
        <BlockSettings label="Result options">
          <BlockViewSelect
            value={node.attrs.display || "list"}
            options={[
              ["list", "List"],
              ["gallery", "Gallery"],
              ["table", "Table"],
              ["expanded", "Featured"],
            ]}
            onChange={(display) => updateAttributes({ display })}
          />
          <label>
            Show
            <select
              aria-label="Result filter"
              value={node.attrs.filter || "all"}
              onChange={(event) =>
                updateAttributes({ filter: event.target.value })
              }
            >
              <option value="all">All results</option>
              <option value="new">New results</option>
            </select>
          </label>
        </BlockSettings>
      )}
      {template ? (
        <p role="status">Results collection template</p>
      ) : !captured && query.isError ? (
        <p role="alert">
          Results could not be loaded.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : !captured && query.isLoading ? (
        <p role="status">Loading results…</p>
      ) : rows.length ? (
        <ProjectResultList
          scope={scope}
          artifacts={rows}
          display={String(node.attrs.display || "list")}
        />
      ) : (
        <p className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
          {node.attrs.filter === "new"
            ? "No new results yet."
            : "No results yet."}{" "}
          Submitted Task Artifacts appear here immediately.
        </p>
      )}
    </NodeViewWrapper>
  );
}

export function ProjectResultList({
  scope,
  artifacts,
  display = "list",
}: {
  scope: CanvasScope;
  display?: string;
  artifacts: components["schemas"]["CanvasArtifact"][];
}) {
  const navigate = useNavigate();
  const report = useContext(PageReportMode);
  const visible = report ? artifacts : artifacts.slice(0, 24);
  if (!artifacts.length)
    return (
      <p className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
        No results yet. Submitted Task results appear here automatically.
      </p>
    );
  if (display === "table")
    return (
      <div className="pc-work-table pc-result-table">
        <table>
          <caption className="sr-only">Project results</caption>
          <thead>
            <tr>
              <th scope="col">Result</th>
              <th scope="col">Version</th>
              <th scope="col">Review</th>
              <th scope="col">Created</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((artifact) => (
              <tr key={artifact.id}>
                <td>
                  <button
                    className="pc-work-title"
                    onClick={() =>
                      navigate(
                        `/projects/${scope.projectId}/artifacts?artifactId=${artifact.id}&versionId=${artifact.latestVersionId}`,
                      )
                    }
                  >
                    {artifact.title}
                  </button>
                </td>
                <td>v{artifact.latestVersion.number}</td>
                <td>{artifact.pageId ? "Page" : "Saved Output"}</td>
                <td>
                  {new Date(
                    artifact.latestVersion.createdAt,
                  ).toLocaleDateString()}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  return (
    <div className={`pc-output-rows pc-results-${display}`}>
      {visible.map((artifact) => (
        <button
          key={artifact.id}
          className="pc-result-card"
          onClick={() =>
            navigate(
              `/projects/${scope.projectId}/artifacts?artifactId=${artifact.id}&versionId=${artifact.latestVersionId}`,
            )
          }
        >
          <span className="grid place-items-center flex-none w-[40px] h-[46px] [border:1px_solid_var(--pc-line)] rounded-[8px] [background:var(--pc-raised)] text-[var(--pc-accent)]">
            <Icon name="document-text" className="size-5" />
          </span>
          <span className="pc-result-copy">
            <strong>{artifact.title}</strong>
            {display === "expanded" && artifact.latestVersion.excerpt && (
              <p className="pc-result-excerpt">
                {artifact.latestVersion.excerpt}
              </p>
            )}
            <small>
              Version {artifact.latestVersion.number} ·{" "}
              {artifact.pageId ? "Page" : "Saved Output"}
            </small>
          </span>
          <Icon
            name="arrow-right"
            className="flex-none text-[var(--pc-soft)] size-4"
          />
        </button>
      ))}
    </div>
  );
}
