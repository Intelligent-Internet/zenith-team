import { TaskPages } from "@/features/project-canvas/documents/task-pages";
import { ProjectTaskDetail } from "../projects/project-task-detail";
import { TaskProperties } from "@/features/company-plan/components/task-properties";
import { TaskDescription } from "@/features/company-plan/components/task-description";
import { milestoneLabel } from "@/features/company-plan/model/milestone-catalog";
import { taskWaitsForPrerequisites } from "@/features/company-plan/model/task-work-state";
import { useTaskWorkStatusLabel } from "@/features/company-plan/hooks/use-project-task-reviews";
import {
  taskAwaitsViewerReview,
  taskCanMarkDone,
  taskStartAction,
  unreadyReasonLabel,
} from "@/features/company-plan/model/task-execution";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useCan, useTaskAccess } from "@/shared/permissions";
import { useEffect, type MouseEvent, type ReactNode, type Ref } from "react";
import { Maximize2, MessagesSquare, X } from "@/shared/ui/icons";
import { OutsideProjectBadge } from "./outside-project-badge";
import { TaskAccessFixLink } from "./task-access-fix-link";
import { TaskDetailPage } from "./task-detail-page";
import { TaskDependencySummary } from "@/features/task-dependencies/components/task-dependency-summary";
import type { TaskDependencyPresentationItem } from "@/features/task-dependencies/model/use-task-dependency-presentation";
import type { WorkNextUpReadiness } from "@/features/company-plan/model/work-next-up-model";
import type {
  ResourceAttachmentSnapshot,
  TaskDto,
  TaskStatusDto,
} from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { GlassDivider } from "@/shared/ui/glass-divider";
import { Icon } from "@/shared/ui/icon";

type WorkTaskDetailPanelProps = {
  companyId?: string;
  projectPresentation?: boolean;
  onSelectTask?: (taskId: string) => void;
  expanded?: boolean;
  onExpand?: () => void;
  onOpenGraph?: () => void;
  reviewOpen?: boolean;
  insight?: ReactNode;
  actionUnavailable?: boolean;
  readiness?: WorkNextUpReadiness;
  actionPending: "complete" | "run" | null;
  agent: { avatarSrc?: string; name: string; title: string };
  criteria: ResourceAttachmentSnapshot[];
  dependencyBlockedBy: TaskDependencyPresentationItem[];
  dependencyHasUnresolvedDependencies: boolean;
  dependencyIsError: boolean;
  dependencyIsLoading: boolean;
  deliverables: ResourceAttachmentSnapshot[];
  hasAttachmentError: boolean;
  hasTaskChat: boolean;
  isAttachmentLoading: boolean;
  onClose: () => void;
  onMarkDone: () => void;
  onOpenChat: () => void;
  panelRef?: Ref<HTMLElement>;
  onRatingChange: (rating: number) => void;
  onReview: () => void;
  onRunTask: () => void;
  onSelectDependency: (item: TaskDependencyPresentationItem) => void;
  rating: number;
  task: TaskDto;
  taskNumber: string;
};

type TaskPanelStatusAppearance = {
  badges: Array<{
    className: string;
    label: string;
  }>;
};

const taskStatusAppearance: Record<TaskStatusDto, TaskPanelStatusAppearance> = {
  backlog: {
    badges: [
      { className: "bg-[#a7b1c3] text-white", label: "Pending" },
      { className: "bg-[#8dff66] text-[#212121]", label: "Available" },
    ],
  },
  blocked: {
    badges: [
      { className: "bg-[#a7b1c3] text-white", label: "Pending" },
      { className: "bg-[#fc413d] text-white", label: "Blocked" },
    ],
  },
  error: {
    badges: [
      { className: "bg-[#ff8a57] text-white", label: "Technical Error" },
    ],
  },
  failed: {
    badges: [{ className: "bg-[#fc413d] text-white", label: "Failed" }],
  },
  cancelled: {
    badges: [{ className: "bg-[#fc413d] text-white", label: "Cancelled" }],
  },
  done: {
    badges: [{ className: "bg-[#5de381] text-white", label: "Completed" }],
  },
  in_progress: {
    badges: [{ className: "bg-[#6aa9ff] text-white", label: "In Progress" }],
  },
  in_review: {
    badges: [
      {
        className: "bg-[#b17aff] text-white",
        label: "Ready to Review",
      },
    ],
  },
  todo: {
    badges: [
      { className: "bg-[#a7b1c3] text-white", label: "Pending" },
      { className: "bg-[#8dff66] text-[#212121]", label: "Available" },
    ],
  },
};

const satisfactionLabels = [
  "Give your satisfaction point for this task",
  "Needs improvement",
  "Fair",
  "Good",
  "Excellent",
  "Outstanding",
] as const;

export function WorkTaskDetailPanel({
  companyId,
  projectPresentation = false,
  onSelectTask,
  expanded = false,
  onExpand,
  onOpenGraph,
  reviewOpen = false,
  insight,
  actionUnavailable = false,
  readiness,
  actionPending,
  criteria,
  dependencyBlockedBy,
  dependencyHasUnresolvedDependencies,
  dependencyIsError,
  dependencyIsLoading,
  deliverables,
  hasAttachmentError,
  hasTaskChat,
  isAttachmentLoading,
  onClose,
  onMarkDone,
  onOpenChat,
  panelRef,
  onRatingChange,
  onReview,
  onRunTask,
  onSelectDependency,
  rating,
  task,
}: WorkTaskDetailPanelProps) {
  const titleId = `work-task-detail-title-${task.id}`;
  const appearance = taskStatusAppearance[task.status];
  // A Task Next up says can't start yet looks like one waiting for its
  // prerequisites, not like one ready to start.
  const waiting = readiness?.waiting ?? taskWaitsForPrerequisites(task);
  const isAvailable =
    task.status === "backlog" ||
    task.status === "todo" ||
    task.status === "error";
  // Who may start this, and where it would run (ADR 0014): only the member
  // the Task belongs to, on a computer of their own.
  const viewerUserId = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  // Inside a Project each move also needs the caller's own permission on
  // it (ADR 0017): starting and merging run the Task, marking done edits
  // it. A move they cannot make is not offered.
  const access = useTaskAccess(task);
  const startAction = access.can("project.tasks.execute")
    ? taskStartAction(task, viewerUserId)
    : null;
  // The review is the assignee's, or an owner's or admin's, like the start.
  // It merges ("Run tasks with agents") or marks done ("Create and edit
  // tasks"), so either one opens it: the rule the board's "Needs your
  // review" follows too.
  const canReview = taskAwaitsViewerReview(task, viewerUserId, canAssign);
  // "Needs review" only when the review waits on the viewer and a Run left
  // work in it; anyone else reads "In review".
  const statusLabel = useTaskWorkStatusLabel(task);
  const canMarkDone =
    access.can("project.tasks.write") &&
    taskCanMarkDone(task, viewerUserId, canAssign);
  // Readiness is about the Agent doing the work; a Task a person does
  // themself, with no Agent helping, has nothing to report here.
  const executionNote =
    isAvailable &&
    !task.execution.assigneeReady &&
    (task.executedBy.kind === "agent" || task.execution.agent)
      ? unreadyReasonLabel(task, viewerUserId)
      : null;

  const closePanel = (event: MouseEvent<HTMLButtonElement>) => {
    event.preventDefault();
    event.stopPropagation();
    onClose();
  };

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented && !reviewOpen) {
        if (expanded && onExpand) onExpand();
        else onClose();
      }
    };

    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [expanded, onClose, onExpand, reviewOpen]);

  const actions =
    startAction ||
    canReview ||
    canMarkDone ||
    (!expanded && !projectPresentation) ||
    executionNote ? (
      <footer className="flex min-h-16 shrink-0 flex-wrap items-center gap-x-3 gap-y-1 px-4 pb-4">
        {startAction ? (
          <TaskPrimaryAction
            disabled={
              actionUnavailable ||
              !task.dependenciesResolved ||
              actionPending !== null ||
              dependencyHasUnresolvedDependencies ||
              dependencyIsError ||
              dependencyIsLoading
            }
            icon="play"
            label={
              dependencyHasUnresolvedDependencies
                ? "Waiting for prerequisite"
                : actionPending === "run"
                  ? "Starting task…"
                  : startAction.label
            }
            onClick={onRunTask}
          />
        ) : null}
        {canReview ? (
          <TaskPrimaryAction
            disabled={actionPending !== null}
            icon="tick"
            label="Review source changes"
            onClick={onReview}
          />
        ) : null}
        {canMarkDone ? (
          <TaskPrimaryAction
            disabled={
              actionUnavailable ||
              !task.dependenciesResolved ||
              actionPending !== null ||
              dependencyHasUnresolvedDependencies ||
              dependencyIsError ||
              dependencyIsLoading
            }
            icon="tick"
            label={
              dependencyHasUnresolvedDependencies
                ? "Waiting for prerequisite"
                : actionPending === "complete"
                  ? "Marking as Done…"
                  : "Mark as Done"
            }
            secondary={task.status !== "in_review"}
            onClick={onMarkDone}
          />
        ) : null}
        {!expanded && !projectPresentation ? (
          <button
            className="inline-flex h-10 cursor-pointer items-center justify-center gap-2 rounded-xl border border-white/30 bg-white/10 px-[22px] text-xs text-white transition hover:bg-white/16"
            type="button"
            onClick={onOpenChat}
          >
            <MessagesSquare aria-hidden="true" size={16} />
            Open conversation
          </button>
        ) : null}
        {executionNote ? (
          <p className="m-0 flex basis-full flex-wrap items-center gap-x-2 text-xs text-[var(--workspace-native-text-muted)]">
            {executionNote}
            <TaskAccessFixLink task={task} />
          </p>
        ) : startAction &&
          task.executedBy.kind === "user" &&
          task.execution.agent ? (
          <p className="m-0 basis-full text-xs text-[var(--workspace-native-text-muted)]">
            Work through this task with {task.execution.agent.name} in chat.
          </p>
        ) : null}
      </footer>
    ) : null;

  if (expanded && companyId && onExpand && onOpenGraph) {
    return (
      <TaskDetailPage
        key={task.id}
        actions={actions}
        companyId={companyId}
        task={task}
        onCollapse={onExpand}
        onOpenChat={onOpenChat}
        onOpenGraph={onOpenGraph}
        onSelectDependency={onSelectDependency}
        panelRef={panelRef}
      />
    );
  }

  if (projectPresentation && onSelectTask) {
    return (
      <ProjectTaskDetail
        key={task.id}
        task={task}
        panelRef={panelRef}
        onClose={onClose}
        onExpand={onExpand}
        onOpenChat={hasTaskChat ? onOpenChat : undefined}
        onSelectTask={onSelectTask}
        insight={insight}
        actions={actions}
        canEdit={access.can("project.tasks.write")}
      />
    );
  }

  return (
    <aside
      ref={panelRef}
      className="work-task-inspector workspace-matte-panel pointer-events-auto flex h-full w-full flex-col overflow-hidden rounded-2xl text-white"
      role="complementary"
      aria-labelledby={titleId}
      tabIndex={-1}
    >
      <header className="shrink-0 px-4 pt-4">
        <div className="grid grid-cols-[46px_minmax(0,1fr)_auto] items-start gap-3">
          <span
            aria-hidden="true"
            className="grid size-[46px] place-items-center rounded-lg bg-white/10 text-white"
          >
            <Icon name="task-square" className="size-8" />
          </span>
          <h2
            className="m-0 line-clamp-2 text-lg font-bold leading-[21px] text-white"
            id={titleId}
          >
            {task.title}
          </h2>
          <div className="flex items-center gap-2">
            {onExpand ? (
              <button
                type="button"
                aria-label="Expand task details"
                title="Expand task details"
                className="task-inspector-expand"
                onClick={onExpand}
              >
                <Maximize2 size={17} />
              </button>
            ) : null}
            <button
              aria-label="Close task details"
              className="pointer-events-auto relative z-20 grid size-6 touch-manipulation cursor-pointer place-items-center text-white transition [-webkit-app-region:no-drag] hover:text-white/64"
              title="Close"
              type="button"
              onClick={closePanel}
              onPointerDown={(event) => event.stopPropagation()}
            >
              <X aria-hidden="true" className="size-6" />
            </button>
          </div>
        </div>

        <div className="mt-3 flex min-h-8 flex-wrap items-center gap-3">
          {appearance.badges.slice(-1).map((badge) => (
            <span
              className={cx(
                "workspace-task-status inline-flex h-6 items-center justify-center rounded-xl px-[22px] text-xs font-bold",
                waiting
                  ? "bg-white/10 text-[var(--workspace-native-text-muted)]"
                  : badge.className,
              )}
              key={badge.label}
              data-status={waiting ? "waiting" : task.status}
            >
              {readiness?.label ?? statusLabel}
            </span>
          ))}
          <TaskMetadataBadge label={milestoneLabel(task.milestoneType)} />
          <OutsideProjectBadge task={task} />
        </div>

        <GlassDivider className="mt-3 opacity-[0.18]" />
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {insight}
        <TaskPanelSection className="pt-3" label="Brief">
          <TaskDescription description={task.description} />
        </TaskPanelSection>
        <TaskProperties
          task={task}
          canEdit={access.can("project.tasks.write")}
        />

        <TaskPages
          companyId={task.companyId}
          projectId={task.projectId}
          taskId={task.id}
        />
        <TaskPanelSection label="Deliverables">
          <TaskAttachmentArea
            attachments={deliverables}
            emptyLabel={
              task.status === "done"
                ? "No files were attached. Open the conversation to read the completed work."
                : "Deliverables will appear here when work is submitted."
            }
            hasError={hasAttachmentError}
            isLoading={isAttachmentLoading}
          />
        </TaskPanelSection>

        {!insight ? (
          <TaskPanelSection label="Dependencies">
            <TaskDependencySummary
              blockedBy={dependencyBlockedBy}
              isError={dependencyIsError}
              isLoading={dependencyIsLoading}
              onTaskSelect={onSelectDependency}
            />
          </TaskPanelSection>
        ) : null}

        <TaskPanelSection label="Criteria">
          <TaskAttachmentArea
            attachments={criteria}
            emptyLabel="No criteria attached."
            hasError={hasAttachmentError}
            isLoading={isAttachmentLoading}
          />
        </TaskPanelSection>

        <TaskPanelSection label="Satisfaction">
          {task.status === "done" ? (
            <TaskSatisfaction rating={rating} onRatingChange={onRatingChange} />
          ) : (
            <div className="grid min-h-[85px] place-items-center text-xs text-white/56">
              Point it after done
            </div>
          )}
        </TaskPanelSection>
      </div>

      {actions}
    </aside>
  );
}

function TaskMetadataBadge({ label }: { label: string }) {
  return (
    <span className="inline-flex h-6 items-center rounded-xl border border-white/20 bg-white/10 px-3 text-[12px] font-bold uppercase tracking-wide text-white/72">
      {label}
    </span>
  );
}

function TaskPanelSection({
  children,
  className,
  label,
}: {
  children: ReactNode;
  className?: string;
  label: string;
}) {
  return (
    <section className={cx("mt-3", className)}>
      <h3 className="m-0 text-xs font-normal uppercase tracking-[2.4px] text-white/56">
        {label}
      </h3>
      <div className="mt-2">{children}</div>
    </section>
  );
}

function TaskAttachmentArea({
  attachments,
  emptyLabel,
  hasError,
  isLoading,
}: {
  attachments: ResourceAttachmentSnapshot[];
  emptyLabel: string;
  hasError: boolean;
  isLoading: boolean;
}) {
  if (isLoading) {
    return (
      <div className="grid min-h-[120px] place-items-center text-xs text-white/56">
        Loading files…
      </div>
    );
  }
  if (hasError) {
    return (
      <div className="grid min-h-[120px] place-items-center text-xs text-white/56">
        Files could not be loaded.
      </div>
    );
  }
  if (attachments.length === 0) {
    return (
      <div className="grid min-h-[120px] place-items-center text-xs text-white/56">
        {emptyLabel}
      </div>
    );
  }

  return (
    <div className="flex min-h-[120px] gap-3 overflow-x-auto pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
      {attachments.map((attachment) => (
        <TaskAttachmentCard attachment={attachment} key={attachment.id} />
      ))}
    </div>
  );
}

function TaskAttachmentCard({
  attachment,
}: {
  attachment: ResourceAttachmentSnapshot;
}) {
  const filename = attachment.file_name || "attachment";
  const { extension, stem } = splitFilename(filename);
  const className =
    "flex h-[120px] w-[98px] shrink-0 items-end overflow-hidden rounded-md bg-white p-2 text-left text-[7px] leading-[9px] text-black shadow-[4px_20px_32px_rgba(0,0,0,0.36)]";
  const label = (
    <span className="w-full [overflow-wrap:anywhere]">
      <strong className="font-bold">{stem}</strong>
      {extension ? (
        <span className="font-normal text-[#737373]">{extension}</span>
      ) : null}
    </span>
  );

  return attachment.url ? (
    <a
      aria-label={`Open ${filename}`}
      className={cx(
        className,
        "cursor-pointer transition-transform hover:-translate-y-0.5",
      )}
      href={attachment.url}
      rel="noreferrer"
      target="_blank"
      title={filename}
    >
      {label}
    </a>
  ) : (
    <div className={className} title={filename}>
      {label}
    </div>
  );
}

function TaskSatisfaction({
  onRatingChange,
  rating,
}: {
  onRatingChange: (rating: number) => void;
  rating: number;
}) {
  const normalizedRating = Math.min(5, Math.max(0, rating));

  return (
    <div className="flex min-h-[85px] items-center gap-3">
      <label className="relative min-w-0 flex-1 cursor-pointer">
        <span className="sr-only">Task satisfaction rating</span>
        <span className="block h-2 overflow-hidden rounded-full bg-white/10">
          <span
            className="block h-full rounded-full bg-[#ffd84d] transition-[width] duration-200"
            style={{ width: `${(normalizedRating / 5) * 100}%` }}
          />
        </span>
        <input
          aria-valuetext={`${normalizedRating} out of 5, ${satisfactionLabels[normalizedRating]}`}
          className="absolute inset-x-0 top-1/2 h-8 w-full -translate-y-1/2 cursor-pointer opacity-0"
          max={5}
          min={0}
          step={1}
          type="range"
          value={normalizedRating}
          onChange={(event) => onRatingChange(Number(event.target.value))}
        />
      </label>
      <div className="w-[106px] shrink-0 text-right">
        <p className="m-0 text-2xl font-bold leading-6 text-white">
          {normalizedRating}/5
        </p>
        <p className="m-0 mt-2 text-[12px] leading-[18px] text-white">
          {satisfactionLabels[normalizedRating]}
        </p>
      </div>
    </div>
  );
}

function TaskPrimaryAction({
  disabled,
  secondary = false,
  icon,
  label,
  onClick,
}: {
  disabled: boolean;
  secondary?: boolean;
  icon: string;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      className={cx(
        "inline-flex h-10 items-center gap-3 rounded-xl px-[22px] text-xs font-bold transition",
        secondary
          ? "border border-[var(--workspace-divider)] bg-transparent text-[var(--workspace-native-text)] hover:bg-[var(--workspace-control-bg)]"
          : "workspace-task-primary-action bg-[linear-gradient(97deg,#99b51b_0%,#97aa1c_50%,#265f05_100%)] text-white shadow-[4px_38px_62px_rgba(0,0,0,0.5),inset_-3px_-4px_7px_rgba(255,255,255,0.15)]",
        disabled
          ? "cursor-not-allowed opacity-60"
          : "cursor-pointer hover:brightness-110",
      )}
      disabled={disabled}
      type="button"
      onClick={onClick}
    >
      <Icon name={icon} className="size-[19px]" />
      {label}
    </button>
  );
}

function splitFilename(filename: string) {
  const extensionIndex = filename.lastIndexOf(".");
  if (extensionIndex <= 0 || extensionIndex === filename.length - 1) {
    return { extension: "", stem: filename };
  }
  return {
    extension: filename.slice(extensionIndex),
    stem: filename.slice(0, extensionIndex),
  };
}
