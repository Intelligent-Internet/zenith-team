import { skipToken } from "@reduxjs/toolkit/query";
import {
  useGetTaskResultSelectionQuery,
  useGetTaskSelectedOutputsQuery,
} from "../api/task-result-selection-api";
import { TaskResultSelection } from "./task-result-selection";
import { TaskPages } from "@/features/project-canvas/task-pages";
import { count, duration } from "@/features/analytics/model/format";
import { OutsideProjectBadge } from "./outside-project-badge";
import { TaskProjectLabel, TaskProjectVisibility } from "./project-controls";
import { ActivitySnapshotDetails } from "@/shared/ui/activity-snapshot-details";
import { Link, useNavigate } from "react-router";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { TaskLatestUpdate } from "./task-latest-update";
import { TaskWikiSources } from "./task-wiki-sources";
import {
  formatTaskEventTime,
  taskActivityDescription,
} from "../model/task-detail-presentation";
import { useState, type ReactNode, type Ref } from "react";
import {
  ArrowRight,
  ArrowUpRight,
  FileText,
  GitBranch,
  MessagesSquare,
  Minimize2,
  Paperclip,
  Settings,
} from "@/shared/ui/icons";
import { personInitials } from "@/shared/ui/person-initials";
import { useListWorkspaceTaskMessagesQuery } from "@/shared/api/workspace-api";
import {
  useTaskDependencyPresentation,
  type TaskDependencyPresentationItem,
} from "@/features/task-dependencies/model/use-task-dependency-presentation";
import { TaskDependencySummary } from "@/features/task-dependencies/components/task-dependency-summary";
import { useTaskLabel } from "@/shared/lib/task-label";
import {
  activityActorName,
  activityPersonName,
} from "@/shared/lib/activity-actor";
import type { ResourceAttachmentSnapshot, TaskDto } from "@/shared/api/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import { createTaskPanelAttachments } from "../model/task-panel-attachments";
import { TaskDetailResults } from "./task-detail-results";
import { useTaskWorkStatusLabel } from "../hooks/use-project-task-reviews";
import {
  useGetTaskActivityQuery,
  useGetTaskUsageQuery,
} from "../api/task-detail-api";
import { TaskMemberAvatar } from "./task-assignee";
import { TaskDescription } from "./task-description";
import { TaskProperties } from "./task-properties";
import { TaskStatusIcon } from "./task-metadata-icons";
import "../styles/task-detail-page.css";

type TaskDetailPageProps = {
  actions?: ReactNode;
  companyId: string;
  task: TaskDto;
  onCollapse: () => void;
  onOpenChat: () => void;
  onOpenGraph?: () => void;
  onSelectDependency: (item: TaskDependencyPresentationItem) => void;
  panelRef?: Ref<HTMLElement>;
};

export function TaskDetailPage({
  actions,
  companyId,
  task,
  onCollapse,
  onOpenChat,
  onOpenGraph,
  onSelectDependency,
  panelRef,
}: TaskDetailPageProps) {
  const [tab, setTab] = useState<"overview" | "files">("overview");
  const labelTask = useTaskLabel(companyId);
  const navigate = useNavigate();
  const selectResult = (artifact: ConversationArtifact, fileId?: string) => {
    if (!artifact.runId) {
      const item = artifact.artifact.items.find((item) => item.id === fileId);
      navigate(
        `/companies/${companyId}/artifacts/${artifact.artifact.id}${item ? `?item=${item.ordinal}` : ""}`,
      );
      return;
    }
    navigate(
      createChatWorkspaceHref(
        {
          kind: "task",
          taskId: task.id,
          conversationId: task.conversationId,
          ...(task.execution.agent ? { agentId: task.execution.agent.id } : {}),
        },
        {
          artifact: {
            artifactId: artifact.artifact.id,
            runId: artifact.runId,
            fileId,
          },
        },
      ),
    );
  };
  // What the Run presented is the saved result; message attachments are the
  // brief's acceptance material and any files people shared by hand.
  const results = useConversationArtifacts({
    companyId,
    conversationId: task.conversationId,
  });
  const selection = useGetTaskResultSelectionQuery({
    companyId,
    taskId: task.id,
  });
  const selectedIds = selection.isError
    ? []
    : (selection.currentData?.outputIds ?? []);
  const selected = useGetTaskSelectedOutputsQuery(
    selectedIds.length ? { companyId, ids: selectedIds } : skipToken,
  );
  const selectedItems = selected.isError
    ? []
    : (selected.currentData?.items ?? []);
  const allResults = {
    ...results,
    items: [
      ...results.items,
      ...selectedItems.filter(
        (item) =>
          !results.items.some(
            (existing) => existing.artifact.id === item.artifact.id,
          ),
      ),
    ],
  };
  const messages = useListWorkspaceTaskMessagesQuery({
    conversationId: task.conversationId,
    taskId: task.id,
  });
  const attachments = createTaskPanelAttachments(messages.currentData ?? []);
  const dependencies = useTaskDependencyPresentation(companyId, task.id);
  const label = labelTask(task);
  const statusLabel = useTaskWorkStatusLabel(task);
  const referenceFiles = attachments.criteria.filter(
    (file) => !attachments.deliverables.some((result) => result.id === file.id),
  );
  const resultCount = results.count + attachments.deliverables.length;
  const renderFiles = (
    items: ResourceAttachmentSnapshot[],
    emptyLabel: string,
  ) => (
    <TaskFileList
      files={items}
      isLoading={messages.isFetching && !messages.currentData}
      isError={messages.isError}
      onRetry={() => void messages.refetch()}
      emptyLabel={emptyLabel}
    />
  );

  return (
    <article
      className="task-detail-page"
      ref={panelRef}
      tabIndex={-1}
      aria-labelledby="task-detail-title"
    >
      <header className="task-detail-header">
        <nav className="task-detail-breadcrumb" aria-label="Task location">
          <Link to="/projects">Projects</Link>
          <span aria-hidden="true">/</span>
          <TaskProjectLabel
            companyId={companyId}
            projectId={task.projectId}
            withLabel={false}
          />
          <span aria-hidden="true">/</span>
          <span aria-current="page">{label}</span>
        </nav>
        <h1 id="task-detail-title">{task.title}</h1>
        <div className="task-detail-metadata">
          <span className="task-detail-status" data-status={task.status}>
            <TaskStatusIcon status={task.status} /> {statusLabel}
          </span>
          <TaskProjectVisibility
            className="task-detail-visibility"
            companyId={companyId}
            projectId={task.projectId}
          />
          <OutsideProjectBadge task={task} />
        </div>
        <div className="task-detail-actions">
          {actions}
          <button
            type="button"
            className="task-detail-open-chat"
            data-secondary={actions ? true : undefined}
            onClick={onOpenChat}
          >
            <MessagesSquare size={16} /> Open task chat
          </button>
          <button
            type="button"
            className="task-detail-icon-button"
            aria-label="Collapse task details"
            title="Collapse task details"
            onClick={onCollapse}
          >
            <Minimize2 size={16} />
          </button>
        </div>
      </header>
      <nav className="task-detail-tabs" aria-label="Task detail sections">
        <button
          type="button"
          aria-current={tab === "overview" ? "page" : undefined}
          onClick={() => setTab("overview")}
        >
          Overview
        </button>
        <button
          type="button"
          aria-current={tab === "files" ? "page" : undefined}
          onClick={() => setTab("files")}
        >
          Results{" "}
          <span className="task-detail-count">
            {results.isLoading ||
            results.error ||
            (messages.isFetching && !messages.currentData) ||
            messages.isError
              ? "—"
              : `${resultCount}${results.hasMore ? "+" : ""}`}
          </span>
        </button>
      </nav>
      <div className="task-detail-body">
        <div className="task-detail-content">
          {tab === "overview" ? (
            <>
              <TaskLatestUpdate
                companyId={companyId}
                task={task}
                onOpenChat={onOpenChat}
              />
              <section className="task-detail-brief">
                <h2>Task brief</h2>
                <TaskDescription description={task.description} />
              </section>
              <TaskWikiSources companyId={companyId} taskId={task.id} />
            </>
          ) : null}
          <section>
            <h2>Results</h2>
            <p className="task-results-description">
              Saved outputs from this task. Project documents and files on your
              computer are listed separately.
            </p>
            <TaskResultSelection task={task} items={allResults.items} />
            <TaskDetailResults
              companyId={companyId}
              sourceLabel={`${label} · ${task.title}`}
              results={allResults}
              selectedIds={selectedIds}
              latestOnly={tab === "overview"}
              emptyLabel={
                attachments.deliverables.length ? "" : "No results shared yet."
              }
              onSelect={selectResult}
            />
            {attachments.deliverables.length
              ? renderFiles(attachments.deliverables, "")
              : null}
            {tab === "files" && results.hasMore ? (
              <button
                type="button"
                className="task-detail-text-button"
                disabled={results.isLoadingMore}
                onClick={() => void results.loadMore()}
              >
                {results.isLoadingMore
                  ? "Loading results…"
                  : "Load more results"}
              </button>
            ) : null}
            {tab === "overview" && resultCount > 0 ? (
              <button
                type="button"
                className="task-detail-text-button"
                onClick={() => setTab("files")}
              >
                View all results <ArrowRight size={14} />
              </button>
            ) : null}
          </section>
          <TaskPages
            companyId={companyId}
            projectId={task.projectId}
            taskId={task.id}
          />
          <details className="task-detail-references">
            <DisclosureSummary count={referenceFiles.length}>
              <Paperclip size={16} /> Reference files
            </DisclosureSummary>
            {renderFiles(referenceFiles, "No reference files attached.")}
          </details>
          {tab === "overview" ? (
            <TaskActivity companyId={companyId} taskId={task.id} task={task} />
          ) : null}
        </div>
        <aside className="task-detail-properties" aria-label="Task context">
          <TaskProperties task={task} variant="detail" />
          <section className="task-detail-dependencies">
            <h2>
              Dependencies{" "}
              <span className="task-detail-count">{task.blockedByCount}</span>
            </h2>
            <TaskDependencySummary
              blockedBy={dependencies.blockedBy}
              isError={dependencies.isError}
              isLoading={dependencies.isLoading}
              onTaskSelect={onSelectDependency}
            />
            {onOpenGraph ? (
              <button
                type="button"
                className="task-detail-graph"
                onClick={onOpenGraph}
              >
                <GitBranch size={14} /> View graph <ArrowUpRight size={14} />
              </button>
            ) : null}
          </section>
          <TaskUsagePanel companyId={companyId} taskId={task.id} />
        </aside>
      </div>
    </article>
  );
}

export function TaskFileList({
  files,
  isLoading,
  isError,
  onRetry,
  emptyLabel,
}: {
  files: ResourceAttachmentSnapshot[];
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  emptyLabel: string;
}) {
  if (isError)
    return (
      <p className="task-detail-empty" role="status">
        Files could not be loaded.{" "}
        <button type="button" onClick={onRetry}>
          Retry
        </button>
      </p>
    );
  if (isLoading)
    return (
      <p className="task-detail-empty" role="status">
        Loading files…
      </p>
    );
  if (!files.length) return <p className="task-detail-empty">{emptyLabel}</p>;
  return (
    <div className="task-detail-files">
      {files.map((file) => {
        const content = (
          <>
            <span className="task-detail-file-icon">
              <FileText size={20} />
            </span>
            <span className="task-detail-file-name">
              <strong>{file.file_name}</strong>
              <small>{file.content_type || "Task attachment"}</small>
            </span>
            <small>
              {file.file_size === null
                ? ""
                : `${Math.max(1, Math.ceil(file.file_size / 1024))} KB`}
            </small>
            {file.url ? <ArrowUpRight size={16} /> : <small>Unavailable</small>}
          </>
        );
        return file.url ? (
          <a
            className="task-detail-file"
            key={file.id}
            href={file.url}
            target="_blank"
            rel="noreferrer"
          >
            {content}
          </a>
        ) : (
          <div className="task-detail-file" key={file.id}>
            {content}
          </div>
        );
      })}
    </div>
  );
}

export function TaskActivity({
  companyId,
  taskId,
  task,
}: {
  companyId: string;
  taskId: string;
  task?: TaskDto;
}) {
  const query = useGetTaskActivityQuery({ companyId, taskId });
  const [showHistory, setShowHistory] = useState(false);
  const activities = query.currentData?.activities ?? [];
  const visible = showHistory ? activities : activities.slice(0, 3);
  return (
    <section className="task-detail-activity">
      <h2>Activity</h2>
      {query.isError ? (
        <p className="task-detail-empty">
          Activity could not be loaded.{" "}
          <button type="button" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      ) : !query.currentData ? (
        <p className="task-detail-empty" role="status">
          Loading activity…
        </p>
      ) : activities.length ? (
        <>
          <ol>
            {visible.map((event) => {
              const member = [task?.assignee, task?.creator].find(
                (actor) =>
                  actor?.kind === "user" && actor.id === event.actor.id,
              );
              return (
                <li key={event.event_id}>
                  {event.actor.kind === "agent" && event.actor.id ? (
                    <FoldAvatar paused agentId={event.actor.id} size={27} />
                  ) : member ? (
                    <TaskMemberAvatar member={member} />
                  ) : (
                    <span className="task-detail-person">
                      {event.actor.kind === "system" ? (
                        <Settings size={15} />
                      ) : (
                        personInitials(event.actor.displayName) || "?"
                      )}
                    </span>
                  )}
                  <div className="task-detail-event-content">
                    <div className="task-detail-event-summary">
                      <p>
                        <strong>{activityActorName(event.actor)}</strong>
                        {event.on_behalf_of
                          ? ` for ${activityPersonName(event.on_behalf_of)}`
                          : null}{" "}
                        {taskActivityDescription(event)}
                      </p>
                      <time
                        dateTime={event.created_at}
                        title={new Date(event.created_at).toLocaleString()}
                      >
                        {formatTaskEventTime(event.created_at)}
                      </time>
                    </div>
                    {showHistory ? (
                      <ActivitySnapshotDetails event={event} />
                    ) : null}
                  </div>
                </li>
              );
            })}
          </ol>
          <button
            type="button"
            className="task-detail-text-button"
            aria-expanded={showHistory}
            onClick={() => setShowHistory((value) => !value)}
          >
            {showHistory ? "Hide history" : "View history"}
          </button>
        </>
      ) : (
        <p className="task-detail-empty">No recent task events.</p>
      )}
    </section>
  );
}

function TaskUsagePanel({
  companyId,
  taskId,
}: {
  companyId: string;
  taskId: string;
}) {
  const query = useGetTaskUsageQuery({ companyId, taskId });
  const usage = query.currentData;
  return (
    <section className="task-detail-latest-run" aria-label="Task usage">
      <h2>Usage</h2>
      {query.isError ? (
        <p className="task-detail-empty">Usage could not be loaded.</p>
      ) : !usage ? (
        <p className="task-detail-empty" role="status">
          Loading usage…
        </p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-2 text-sm">
            <span>
              Runs <strong>{usage.runs}</strong>
            </span>
            <span>
              Agent time <strong>{duration(usage.agentTimeMs)}</strong>
            </span>
            <span>
              Lead time <strong>{duration(usage.leadTimeMs)}</strong>
            </span>
            <span>
              Tokens{" "}
              <strong>
                {usage.tokens
                  ? count(usage.tokens)
                  : usage.usageRestricted
                    ? "Restricted"
                    : "—"}
              </strong>
            </span>
          </div>
          <div className="mt-3 divide-y divide-[var(--workspace-native-border)]">
            {usage.items.slice(0, 5).map((run) => (
              <div
                className="flex items-center justify-between gap-3 py-2 text-xs"
                key={run.id}
              >
                <span>
                  {run.agent} · {run.model ?? "Unknown model"}
                </span>
                <span>
                  {duration(run.durationMs)} ·{" "}
                  {run.usageRestricted
                    ? "Restricted"
                    : run.tokens
                      ? count(run.tokens)
                      : "—"}
                </span>
              </div>
            ))}
          </div>
        </>
      )}
    </section>
  );
}
