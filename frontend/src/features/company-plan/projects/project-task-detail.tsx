import { useListWorkspaceTaskMessagesQuery } from "@/shared/api/task-api";
import { TaskPages } from "@/features/project-canvas/documents/task-pages";
import { useTaskAttention } from "../hooks/use-task-attention";
import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type Ref,
} from "react";
import {
  Clock3,
  Maximize2,
  X,
  FileText,
  ChevronDown,
  ArrowUpRight,
  MessagesSquare,
} from "@/shared/ui/icons";
import { useTaskLabel } from "@/shared/lib/task-label";
import type { TaskDto } from "@/shared/api/types";

import { useTaskDependencyPresentation } from "@/features/task-dependencies/model/use-task-dependency-presentation";
import { TaskDependencySummary } from "@/features/task-dependencies/components/task-dependency-summary";
import { TaskActivity, TaskFileList } from "../components/task-detail-page";
import { OutsideProjectBadge } from "../components/outside-project-badge";
import { TaskDescription } from "../components/task-description";
import { TaskProperties } from "../components/task-properties";
import { TaskStatusIcon } from "../components/task-metadata-icons";
import { milestoneLabel } from "../model/milestone-catalog";
import { useTaskAwaitsViewerReview } from "../hooks/use-project-task-reviews";
import { createTaskPanelAttachments } from "../model/task-panel-attachments";
import {
  taskWaitsForPrerequisites,
  taskWorkStatusLabel,
} from "../model/task-work-state";
import "./project-task-detail.css";

const detailTabs = [
  { id: "details", label: "Details" },
  { id: "activity", label: "Activity" },
  { id: "dependencies", label: "Dependencies" },
] as const;

export function ProjectTaskDetail({
  task,
  panelRef,
  onClose,
  onExpand,
  onOpenChat,
  onSelectTask,
  insight,
  actions,
  canEdit = false,
}: {
  task: TaskDto;
  panelRef?: Ref<HTMLElement>;
  onClose: () => void;
  onExpand?: () => void;
  onOpenChat?: () => void;
  onSelectTask: (taskId: string) => void;
  insight?: ReactNode;
  actions?: ReactNode;
  canEdit?: boolean;
}) {
  const [tab, setTab] = useState<(typeof detailTabs)[number]["id"]>("details");
  const [filesOpen, setFilesOpen] = useState(false);
  const attention = useTaskAttention(task.companyId);
  const needsResponse =
    attention.taskIds.has(task.id) &&
    task.status !== "done" &&
    task.status !== "cancelled";
  // Only someone who may decide the review is asked for it, and only when
  // an Agent's Run left work in it; anyone else reads the plain status
  // above, "In review".
  const awaitsReview = useTaskAwaitsViewerReview(task);
  const statusLabel = taskWorkStatusLabel(task, awaitsReview);
  const labelTask = useTaskLabel(task.companyId);
  const dependencies = useTaskDependencyPresentation(task.companyId, task.id);
  const waiting = taskWaitsForPrerequisites(task);
  const prerequisite = dependencies.blockedBy.find((item) => item.isUnresolved);
  const messages = useListWorkspaceTaskMessagesQuery(
    { taskId: task.id, conversationId: task.conversationId },
    { skip: !filesOpen },
  );
  const attachments = createTaskPanelAttachments(messages.currentData ?? []);
  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const currentIndex = detailTabs.findIndex(({ id }) => id === tab);
    const nextIndex =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? detailTabs.length - 1
          : (currentIndex +
              (event.key === "ArrowRight" ? 1 : -1) +
              detailTabs.length) %
            detailTabs.length;
    const next = detailTabs[nextIndex].id;
    setTab(next);
    event.currentTarget.parentElement
      ?.querySelector<HTMLButtonElement>(`[data-tab="${next}"]`)
      ?.focus();
  };
  return (
    <aside
      className="project-task-detail"
      ref={panelRef}
      tabIndex={-1}
      aria-labelledby={`project-task-title-${task.id}`}
      onKeyDown={(event) => {
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <header className="project-task-detail-header">
        <div className="project-task-detail-topline">
          <span>{labelTask(task)}</span>
          <div>
            {onOpenChat ? (
              <button
                type="button"
                aria-label="Open task chat"
                title="Open task chat"
                onClick={onOpenChat}
              >
                <MessagesSquare size={17} />
              </button>
            ) : null}
            {onExpand ? (
              <button
                type="button"
                aria-label="Expand task details"
                title="Expand task details"
                onClick={onExpand}
              >
                <Maximize2 size={17} />
              </button>
            ) : null}
            <button
              type="button"
              aria-label="Close task details"
              title="Close task details"
              onClick={onClose}
            >
              <X size={18} />
            </button>
          </div>
        </div>
        <h2 id={`project-task-title-${task.id}`}>{task.title}</h2>
        <div className="project-task-detail-metadata">
          <span className="project-task-state" title={statusLabel}>
            <TaskStatusIcon status={task.status} />
            {waiting ? "Waiting" : statusLabel}
          </span>
          <span>{milestoneLabel(task.milestoneType)}</span>
          <OutsideProjectBadge task={task} />
        </div>
      </header>
      <div className="project-task-detail-scroll">
        {(needsResponse || awaitsReview) && onOpenChat ? (
          <section className="project-task-response">
            <strong>
              {needsResponse ? "Needs your response" : "Needs your review"}
            </strong>
            <button
              type="button"
              className="dashboard-button dashboard-button-primary"
              onClick={onOpenChat}
            >
              {needsResponse ? "Respond in chat" : "Review in chat"}{" "}
              <ArrowUpRight size={14} />
            </button>
          </section>
        ) : null}
        <div
          className="project-task-detail-tabs"
          role="tablist"
          aria-label="Task detail sections"
        >
          {detailTabs.map(({ id, label }) => (
            <button
              key={id}
              type="button"
              role="tab"
              data-tab={id}
              id={`project-task-${task.id}-${id}-tab`}
              aria-controls={`project-task-${task.id}-${id}`}
              aria-selected={tab === id}
              tabIndex={tab === id ? 0 : -1}
              onClick={() => setTab(id)}
              onKeyDown={onTabKeyDown}
            >
              {label}
            </button>
          ))}
        </div>
        <div
          role="tabpanel"
          id={`project-task-${task.id}-${tab}`}
          aria-labelledby={`project-task-${task.id}-${tab}-tab`}
          className="project-task-detail-content"
        >
          {tab === "dependencies" ? (
            (insight ?? (
              <section className="project-task-prerequisites">
                <h3>Prerequisites</h3>
                <TaskDependencySummary
                  blockedBy={dependencies.blockedBy}
                  isError={dependencies.isError}
                  isLoading={dependencies.isLoading}
                  onTaskSelect={(item) => onSelectTask(item.taskId)}
                />
              </section>
            ))
          ) : tab === "activity" ? (
            <TaskActivity companyId={task.companyId} taskId={task.id} />
          ) : (
            <>
              <TaskProperties task={task} variant="compact" canEdit={canEdit} />
              <ProjectTaskBrief task={task} />
              <details
                className="project-task-files"
                open={filesOpen}
                onToggle={(event) => setFilesOpen(event.currentTarget.open)}
              >
                <summary>
                  <FileText size={16} />
                  Pages &amp; files
                  <ChevronDown size={15} />
                </summary>
                {filesOpen ? (
                  <TaskPages
                    companyId={task.companyId}
                    projectId={task.projectId}
                    taskId={task.id}
                  />
                ) : null}
                <h3>Deliverables</h3>
                <TaskFileList
                  files={attachments.deliverables}
                  isLoading={messages.isFetching && !messages.currentData}
                  isError={messages.isError}
                  onRetry={() => void messages.refetch()}
                  emptyLabel="No deliverables attached. Check the task conversation for saved results."
                />
                <h3>Acceptance criteria</h3>
                <TaskFileList
                  files={attachments.criteria}
                  isLoading={messages.isFetching && !messages.currentData}
                  isError={messages.isError}
                  onRetry={() => void messages.refetch()}
                  emptyLabel="See the task brief for requirements."
                />
              </details>
            </>
          )}
        </div>
      </div>
      {waiting ? (
        <footer className="project-task-waiting">
          <span>
            <Clock3 size={16} />
            Waiting for prerequisites to complete
          </span>
          {prerequisite && !dependencies.isError && !dependencies.isLoading ? (
            <button
              className="dashboard-button"
              type="button"
              onClick={() => onSelectTask(prerequisite.taskId)}
            >
              View prerequisite
            </button>
          ) : null}
        </footer>
      ) : actions ? (
        <div className="project-task-detail-actions">{actions}</div>
      ) : null}
    </aside>
  );
}

function ProjectTaskBrief({ task }: { task: TaskDto }) {
  const contentRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflows, setOverflows] = useState(false);
  useEffect(() => {
    const content = contentRef.current;
    if (!content) return;
    const observer = new ResizeObserver(() => {
      const lineHeight = parseFloat(getComputedStyle(content).lineHeight);
      setOverflows(content.scrollHeight > lineHeight * 6 + 1);
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, []);
  return (
    <section className="project-task-brief">
      <h3>Brief</h3>
      <div
        id={`project-task-brief-${task.id}`}
        className={`project-task-brief-body${!expanded && overflows ? " is-collapsed" : ""}`}
        onFocusCapture={() => setExpanded(true)}
      >
        <div ref={contentRef}>
          <TaskDescription description={task.description} />
        </div>
      </div>
      {overflows ? (
        <button
          type="button"
          className="project-task-brief-toggle"
          aria-expanded={expanded}
          aria-controls={`project-task-brief-${task.id}`}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? "Show less" : "Show more"}
          <ChevronDown size={14} />
        </button>
      ) : null}
    </section>
  );
}
