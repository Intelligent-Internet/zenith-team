import { TaskProperties } from "@/features/company-plan/components/task-properties";
import { TaskDescription } from "@/features/company-plan/components/task-description";
import { taskWaitsForPrerequisites } from "@/features/company-plan/model/task-work-state";
import { useTaskWorkStatusLabel } from "@/features/company-plan/hooks/use-project-task-reviews";
import {
  ArrowUpRight,
  CircleAlert,
  Clock3,
  FileText,
  LoaderCircle,
  MessageSquareText,
  X,
} from "@/shared/ui/icons";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";

import { useGetWorkspaceTaskWorkspaceQuery } from "@/shared/api/workspace-api";
import { useTaskLabel } from "@/shared/lib/task-label";
import { conversationService } from "@/shared/api/conversation.service";
import { createTaskPanelAttachments } from "@/features/company-plan/model/task-panel-attachments";
import { WorkspaceModalPortal } from "@/features/common/components/workspace-modal-portal";
import type {
  ActorRefDto,
  ResourceAttachmentSnapshot,
  TaskDto,
} from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { personInitials } from "@/shared/ui/person-initials";

import "@/features/chat-workspace/styles/task-peek.css";

type TaskPeekProps = {
  presentation?: "responsive" | "modal";
  companyId: string;
  fallbackReturnFocusRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  onOpenTask: (taskId: string) => void;
  onPeekTask: (taskId: string, trigger: HTMLElement) => void;
  onOpenTaskChat: (task: {
    agentId?: string;
    conversationId: string;
    taskId: string;
  }) => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  suppressReturnFocusRef?: RefObject<boolean>;
  taskId: string;
};

export function TaskPeek({
  presentation = "responsive",
  companyId,
  fallbackReturnFocusRef,
  onClose,
  onOpenTask,
  onPeekTask,
  onOpenTaskChat,
  returnFocusRef,
  suppressReturnFocusRef,
  taskId,
}: TaskPeekProps) {
  const [activeTab, setActiveTab] = useState<"activity" | "details">(
    "activity",
  );
  const [isNarrowViewport, setIsNarrowViewport] = useState(
    () => window.matchMedia("(max-width: 1279px)").matches,
  );
  const isModal = presentation === "modal" || isNarrowViewport;
  const panelRef = useRef<HTMLElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const workspaceQuery = useGetWorkspaceTaskWorkspaceQuery(
    { companyId, taskId },
    { skip: !companyId || !taskId },
  );
  const workspace = workspaceQuery.currentData;
  const task = workspace?.task;
  const labelTask = useTaskLabel(companyId || undefined);
  const latestAgentMessage = workspace?.messages
    .slice()
    .reverse()
    .find((message) => message.author.kind === "agent");
  const attachments = useMemo(
    () =>
      workspace
        ? createTaskPanelAttachments(
            conversationService.displayItemsFromMessageViews(
              workspace.messages,
            ),
          )
        : { criteria: [], deliverables: [] },
    [workspace],
  );
  useEffect(() => setActiveTab("activity"), [taskId]);

  useEffect(() => {
    const query = window.matchMedia("(max-width: 1279px)");
    const handleChange = () => setIsNarrowViewport(query.matches);
    query.addEventListener("change", handleChange);
    return () => query.removeEventListener("change", handleChange);
  }, []);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      closeButtonRef.current?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [isModal]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      // A dialog or list opened from the peek, such as the handoff question,
      // owns its keys: Escape closes only it, and Tab stays inside it.
      if (
        event.defaultPrevented ||
        inAnotherLayer(event.target, panelRef.current)
      ) {
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onClose();
        return;
      }
      if (event.key !== "Tab" || !isModal || !panelRef.current) return;
      const focusable = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(
          'button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((element) => element.tabIndex >= 0 && !element.hidden);
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) {
        event.preventDefault();
        return;
      }
      if (!panelRef.current.contains(document.activeElement)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isModal, onClose]);

  useEffect(
    () => () => {
      window.requestAnimationFrame(() => {
        if (suppressReturnFocusRef?.current) return;
        const returnTarget = returnFocusRef.current?.isConnected
          ? returnFocusRef.current
          : fallbackReturnFocusRef.current?.isConnected
            ? fallbackReturnFocusRef.current
            : document.querySelector<HTMLElement>(
                '[aria-label="Active conversation"] textarea:not([disabled])',
              );
        if (returnTarget?.isConnected) returnTarget.focus();
      });
    },
    [fallbackReturnFocusRef, returnFocusRef, suppressReturnFocusRef],
  );

  const handleTabKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (
      event.key !== "ArrowLeft" &&
      event.key !== "ArrowRight" &&
      event.key !== "Home" &&
      event.key !== "End"
    ) {
      return;
    }
    event.preventDefault();
    const nextTab =
      event.key === "ArrowLeft" || event.key === "Home"
        ? "activity"
        : "details";
    setActiveTab(nextTab);
    window.requestAnimationFrame(() => {
      panelRef.current
        ?.querySelector<HTMLButtonElement>(`#task-peek-${nextTab}-tab`)
        ?.focus();
    });
  };

  const content = (
    <>
      {isModal ? (
        <button
          aria-hidden="true"
          className="absolute inset-0 z-20 bg-black/25"
          tabIndex={-1}
          type="button"
          onClick={onClose}
        />
      ) : null}
      <aside
        ref={panelRef}
        aria-label="Task peek"
        aria-modal={isModal || undefined}
        data-presentation={presentation}
        className="workspace-task-peek workspace-room-task-peek absolute inset-y-0 right-0 z-30 flex w-[400px] max-w-full min-w-0 flex-col border-l border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] text-[var(--workspace-native-text)] min-[1280px]:static min-[1280px]:z-auto min-[1280px]:w-[360px] min-[1280px]:shrink-0 max-[599px]:w-full"
        role={isModal ? "dialog" : "complementary"}
      >
        <header className="flex h-[58px] shrink-0 items-center gap-3 border-b border-[var(--workspace-divider)] px-4">
          <div className="min-w-0 flex-1">
            <p className="m-0 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
              Task peek
            </p>
            <h2 className="m-0 truncate font-mono text-sm font-bold text-[var(--workspace-native-text)]">
              {task ? labelTask(task) : "Task"}
            </h2>
          </div>
          <button
            ref={closeButtonRef}
            aria-label="Close task peek"
            className="grid size-8 place-items-center rounded-lg text-[var(--workspace-native-text-muted)] transition hover:bg-[var(--workspace-control-bg)] hover:text-[var(--workspace-native-text)]"
            type="button"
            onClick={onClose}
          >
            <X size={16} />
          </button>
        </header>

        {workspaceQuery.isFetching && !workspace ? (
          <div className="grid min-h-0 flex-1 place-items-center text-[var(--workspace-native-text-muted)]">
            <LoaderCircle className="animate-spin" size={22} />
          </div>
        ) : workspaceQuery.isError || !workspace || !task ? (
          <div className="grid min-h-0 flex-1 place-items-center px-8 text-center">
            <div>
              <CircleAlert
                className="mx-auto mb-3 text-[var(--workspace-native-approval)]"
                size={22}
              />
              <p className="m-0 text-sm font-bold text-[var(--workspace-native-text)]">
                Task unavailable
              </p>
              <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                This task could not be loaded. It may have moved or you may no
                longer have access.
              </p>
              <button
                className="mt-4 rounded-lg border border-[var(--workspace-divider)] px-3 py-2 text-xs font-bold text-[var(--workspace-native-text)] hover:bg-[var(--workspace-control-bg)]"
                type="button"
                onClick={() => void workspaceQuery.refetch()}
              >
                Try again
              </button>
            </div>
          </div>
        ) : (
          <>
            <div className="shrink-0 border-b border-[var(--workspace-divider)] px-4 pt-3">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <TaskPill
                  tone={
                    taskWaitsForPrerequisites(task)
                      ? "muted"
                      : statusTone(task.status)
                  }
                >
                  <TaskPeekStatus task={task} />
                </TaskPill>
                <TaskPill tone={priorityTone(task.priority)}>
                  {formatLabel(task.priority)}
                </TaskPill>
              </div>
              <h3 className="m-0 break-words text-base font-bold leading-6 text-[var(--workspace-native-text)]">
                {task.title}
              </h3>
              <div
                aria-label="Task peek views"
                className="mt-3 flex gap-4"
                role="tablist"
                onKeyDown={handleTabKeyDown}
              >
                <TaskPeekTab
                  active={activeTab === "activity"}
                  controls="task-peek-activity"
                  id="task-peek-activity-tab"
                  label="Activity"
                  onSelect={() => setActiveTab("activity")}
                />
                <TaskPeekTab
                  active={activeTab === "details"}
                  controls="task-peek-details"
                  id="task-peek-details-tab"
                  label="Details"
                  onSelect={() => setActiveTab("details")}
                />
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 py-4">
              {activeTab === "activity" ? (
                <div
                  aria-labelledby="task-peek-activity-tab"
                  id="task-peek-activity"
                  role="tabpanel"
                >
                  {taskWaitsForPrerequisites(task) ? (
                    <section className="mb-4 border-b border-[var(--workspace-divider)] pb-4">
                      <h3 className="mb-2 text-sm font-semibold">
                        Waiting for prerequisites
                      </h3>
                      <p className="mb-3 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                        These tasks must complete before this task can start.
                      </p>
                      <ul className="grid gap-2">
                        {workspace.dependencies.blockedBy
                          .filter((item) => item.task.status !== "done")
                          .map((item) => (
                            <li key={item.task.id}>
                              <button
                                className="w-full rounded-lg border border-[var(--workspace-divider)] p-3 text-left text-xs"
                                onClick={(event) =>
                                  onPeekTask(item.task.id, event.currentTarget)
                                }
                                type="button"
                              >
                                <span className="block font-semibold">
                                  {item.task.title}
                                </span>
                                <span className="mt-1 block text-[var(--workspace-native-text-muted)]">
                                  {formatLabel(item.task.status)} · Open
                                  prerequisite
                                </span>
                              </button>
                            </li>
                          ))}
                      </ul>
                    </section>
                  ) : null}
                  {task.status === "done" ? (
                    <section className="mb-4 border-b border-[var(--workspace-divider)] pb-4">
                      <h3 className="mb-3 text-sm font-semibold">
                        Task completed
                      </h3>
                      <p className="mb-3 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                        Recent files and messages. Open the conversation for the
                        full history.
                      </p>
                      {attachments.deliverables.length ? (
                        <TaskAttachmentList
                          attachments={attachments.deliverables}
                          emptyLabel=""
                          label="Recent Agent files"
                        />
                      ) : null}
                      {latestAgentMessage?.content.trim() ? (
                        <details
                          className="mt-3 text-xs"
                          open={!attachments.deliverables.length}
                        >
                          <DisclosureSummary>
                            Latest Agent message
                          </DisclosureSummary>
                          <TaskDescription
                            description={latestAgentMessage.content}
                          />
                        </details>
                      ) : !attachments.deliverables.length ? (
                        <p className="text-xs text-[var(--workspace-native-text-muted)]">
                          No Agent files or message text in this preview.
                        </p>
                      ) : null}
                    </section>
                  ) : null}
                  <section className="border-b border-[var(--workspace-divider)] pb-4">
                    <h3 className="mb-3 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
                      Execution
                    </h3>
                    <div className="grid gap-2">
                      {workspace.activeRuns.map((run) => (
                        <div
                          className="flex items-center gap-3 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2.5"
                          key={run.id}
                        >
                          <span className="size-2 shrink-0 animate-pulse rounded-full bg-[var(--workspace-accent-fill)]" />
                          <span className="min-w-0 flex-1">
                            <span className="block font-mono text-xs font-bold text-[var(--workspace-native-text)]">
                              Run #{run.id}
                            </span>
                            <span className="block text-[11px] text-[var(--workspace-native-text-muted)]">
                              {formatLabel(run.status)}
                            </span>
                          </span>
                        </div>
                      ))}
                      {workspace.runQueue.currentRun &&
                      !workspace.activeRuns.some(
                        (run) => run.id === workspace.runQueue.currentRun?.id,
                      ) ? (
                        <div className="flex items-center gap-3 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2.5">
                          <Clock3
                            className="shrink-0 text-[var(--workspace-native-text-muted)]"
                            size={15}
                          />
                          <span className="min-w-0 flex-1">
                            <span className="block font-mono text-xs font-bold text-[var(--workspace-native-text)]">
                              Run #{workspace.runQueue.currentRun.id}
                            </span>
                            <span className="block text-[11px] text-[var(--workspace-native-text-muted)]">
                              {formatLabel(
                                workspace.runQueue.currentRun.status,
                              )}
                            </span>
                          </span>
                        </div>
                      ) : null}
                      {workspace.runQueue.items.map((item, index) => (
                        <div
                          className="rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2.5"
                          key={item.id}
                        >
                          <div className="flex items-center gap-2">
                            <span className="text-[10px] font-black uppercase tracking-wide text-[var(--workspace-native-accent)]">
                              Up next {index + 1}
                            </span>
                            <span className="ml-auto text-[10px] text-[var(--workspace-native-text-muted)]">
                              Waiting for promotion
                            </span>
                          </div>
                          <p className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                            {item.content || "Attachment prompt"}
                          </p>
                        </div>
                      ))}
                      {!workspace.activeRuns.length &&
                      !workspace.runQueue.currentRun &&
                      !workspace.runQueue.items.length ? (
                        <p className="m-0 text-xs text-[var(--workspace-native-text-muted)]">
                          No active execution.
                        </p>
                      ) : null}
                    </div>
                  </section>

                  <section className="py-4">
                    <h3 className="mb-3 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
                      Recent conversation
                    </h3>
                    <div className="grid gap-3">
                      {workspace.messages.slice(-6).map((message) => (
                        <article
                          className="flex min-w-0 gap-2.5"
                          key={message.id}
                        >
                          <ActorAvatar actor={message.author} />
                          <div className="min-w-0 flex-1">
                            <div className="flex items-baseline gap-2">
                              <span className="truncate text-[11px] font-bold text-[var(--workspace-native-text)]">
                                {message.author.name}
                              </span>
                              <span className="shrink-0 text-[9px] uppercase text-[var(--workspace-native-text-muted)]">
                                {message.author.kind}
                              </span>
                            </div>
                            <p className="mt-0.5 line-clamp-3 whitespace-pre-wrap text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                              {message.content}
                            </p>
                          </div>
                        </article>
                      ))}
                      {!workspace.messages.length ? (
                        <div className="rounded-xl border border-dashed border-[var(--workspace-divider)] px-4 py-6 text-center">
                          <MessageSquareText
                            className="mx-auto mb-2 text-[var(--workspace-native-text-muted)]"
                            size={18}
                          />
                          <p className="m-0 text-xs text-[var(--workspace-native-text-muted)]">
                            No messages yet.
                          </p>
                        </div>
                      ) : null}
                    </div>
                  </section>
                </div>
              ) : (
                <div
                  aria-labelledby="task-peek-details-tab"
                  className="grid gap-5"
                  id="task-peek-details"
                  role="tabpanel"
                >
                  <section>
                    <h4 className="mb-2 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
                      Brief
                    </h4>
                    <TaskDescription description={task.description} />
                    <TaskProperties task={task} />
                  </section>
                  <section>
                    <h4 className="mb-3 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
                      Dependencies
                    </h4>
                    <p className="m-0 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                      {task.blockedByCount
                        ? `${task.blockedByCount} prerequisite${task.blockedByCount === 1 ? "" : "s"}; ${task.dependenciesResolved ? "all resolved" : "work is blocked"}.`
                        : "No prerequisites."}
                    </p>
                  </section>
                  <section className="grid gap-4">
                    <TaskAttachmentList
                      attachments={attachments.criteria}
                      emptyLabel="No criteria attached."
                      label="Success criteria"
                    />
                    <TaskAttachmentList
                      attachments={attachments.deliverables}
                      emptyLabel="No deliverables yet."
                      label="Deliverables"
                    />
                  </section>
                </div>
              )}
            </div>

            <footer className="grid shrink-0 grid-cols-2 gap-2 border-t border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] p-3">
              <button
                className="flex h-10 items-center justify-center gap-2 rounded-xl border border-[var(--workspace-divider)] text-xs font-bold text-[var(--workspace-native-text)] transition hover:bg-[var(--workspace-control-bg)] hover:text-[var(--workspace-native-text)]"
                type="button"
                onClick={() => onOpenTask(task.id)}
              >
                Open task <ArrowUpRight size={14} />
              </button>
              <button
                className="flex h-10 items-center justify-center gap-2 rounded-xl bg-[var(--workspace-accent-fill)] text-xs font-semibold text-[var(--workspace-accent-foreground)] transition hover:bg-[var(--workspace-accent-hover)]"
                title="Open this task’s conversation"
                type="button"
                onClick={() => {
                  onOpenTaskChat({
                    ...(task.execution.agent
                      ? { agentId: task.execution.agent.id }
                      : {}),
                    conversationId: workspace.conversation.id,
                    taskId: task.id,
                  });
                }}
              >
                Open conversation <MessageSquareText size={14} />
              </button>
            </footer>
          </>
        )}
      </aside>
    </>
  );

  return isModal ? (
    <WorkspaceModalPortal>{content}</WorkspaceModalPortal>
  ) : (
    content
  );
}

function TaskPeekTab({
  active,
  controls,
  id,
  label,
  onSelect,
}: {
  active: boolean;
  controls: string;
  id: string;
  label: string;
  onSelect: () => void;
}) {
  return (
    <button
      aria-controls={controls}
      aria-selected={active}
      className={cx(
        "border-b-2 px-0 pb-2 text-xs font-bold transition",
        active
          ? "border-[var(--workspace-native-accent)] text-[var(--workspace-native-text)]"
          : "border-transparent text-[var(--workspace-native-text-muted)] hover:text-[var(--workspace-native-text)]",
      )}
      id={id}
      role="tab"
      tabIndex={active ? 0 : -1}
      type="button"
      onClick={onSelect}
    >
      {label}
    </button>
  );
}

function TaskAttachmentList({
  attachments,
  emptyLabel,
  label,
}: {
  attachments: ResourceAttachmentSnapshot[];
  emptyLabel: string;
  label: string;
}) {
  return (
    <div>
      <h4 className="mb-2 text-[10px] font-bold uppercase tracking-[0.14em] text-[var(--workspace-native-text-muted)]">
        {label}
      </h4>
      {attachments.length ? (
        <ul className="grid gap-2">
          {attachments.map((attachment) => {
            const content = (
              <>
                <FileText
                  className="shrink-0 text-[var(--workspace-native-text-muted)]"
                  size={14}
                />
                <span className="min-w-0 flex-1 truncate text-xs font-semibold text-[var(--workspace-native-text)]">
                  {attachment.file_name || "Attachment"}
                </span>
              </>
            );
            return (
              <li key={attachment.id}>
                {attachment.url ? (
                  <a
                    className="flex min-h-10 items-center gap-2 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 transition hover:border-[var(--workspace-divider-strong)] hover:bg-[var(--workspace-control-bg)]"
                    href={attachment.url}
                    rel="noreferrer"
                    target="_blank"
                  >
                    {content}
                  </a>
                ) : (
                  <div className="flex min-h-10 items-center gap-2 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3">
                    {content}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="m-0 rounded-xl border border-dashed border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-3 text-[11px] leading-4 text-[var(--workspace-native-text-muted)]">
          {emptyLabel}
        </p>
      )}
    </div>
  );
}

// The Task's status for this viewer: "Needs review" only when its review
// waits on them, "In review" for everyone else.
function TaskPeekStatus({ task }: { task: TaskDto }) {
  const label = useTaskWorkStatusLabel(task);
  return <>{label}</>;
}

function TaskPill({
  children,
  tone,
}: {
  children: ReactNode;
  tone: "danger" | "muted" | "status" | "success" | "warning";
}) {
  return (
    <span
      className={cx(
        "rounded-full border px-2 py-1 text-[10px] font-bold uppercase tracking-wide",
        tone === "danger" &&
          "border-transparent bg-[var(--workspace-danger-soft)] text-[var(--workspace-native-danger)]",
        tone === "warning" &&
          "border-transparent bg-[var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]",
        tone === "status" &&
          "border-transparent bg-[var(--workspace-info-soft)] text-[var(--workspace-native-info)]",
        tone === "success" &&
          "border-transparent bg-[var(--workspace-success-soft)] text-[var(--workspace-native-success)]",
        tone === "muted" &&
          "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]",
      )}
    >
      {children}
    </span>
  );
}

function ActorAvatar({ actor }: { actor: ActorRefDto }) {
  if (actor.kind === "agent") {
    return <FoldAvatar agentId={actor.id} paused size={24} />;
  }
  return actor.avatarUrl ? (
    <img
      alt=""
      className="size-6 shrink-0 rounded-lg border border-[var(--workspace-divider)] object-cover"
      src={actor.avatarUrl}
    />
  ) : (
    <span className="grid size-6 shrink-0 place-items-center rounded-lg bg-[var(--workspace-control-bg)] text-[9px] font-black text-[var(--workspace-native-text-muted)]">
      {personInitials(actor.name)}
    </span>
  );
}

// Whether a key was pressed in a dialog or popover of its own, not in the
// peek or in something that holds it. Those are portaled out of the peek,
// so the peek's Tab trap would pull focus away from them.
function inAnotherLayer(
  target: EventTarget | null,
  panel: HTMLElement | null,
): boolean {
  const layer =
    target instanceof Element
      ? target.closest('[role="dialog"], [role="alertdialog"]')
      : null;
  return layer !== null && !(panel && layer.contains(panel));
}

function priorityTone(priority: string) {
  if (priority === "urgent") return "danger" as const;
  if (priority === "high") return "warning" as const;
  return "muted" as const;
}

function statusTone(status: string) {
  if (status === "done") return "success" as const;
  if (status === "error" || status === "failed") return "danger" as const;
  if (status === "in_review" || status === "blocked") return "warning" as const;
  if (status === "in_progress") return "status" as const;
  return "muted" as const;
}

function formatLabel(value: string) {
  return value.replaceAll("_", " ");
}
