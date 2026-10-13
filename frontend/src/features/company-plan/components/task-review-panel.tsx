import type { DesktopTaskReview } from "../../../../electron/shared/desktop-bridge.types";
import { boardlyApi } from "@/shared/api/boardly-api";
import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronRight,
  FileCode2,
  GitBranch,
  Minus,
  Plus,
  RefreshCw,
  X,
} from "@/shared/ui/icons";
import { Diff, Hunk, parseDiff } from "react-diff-view";
import "react-diff-view/style/index.css";
import { reviewDesktopTaskWorkspace } from "@/shared/desktop/desktop-environment.service";
import { iconButtonClassName } from "@/features/chat-panel/constants";
import { runService } from "@/shared/api";
import { conversationService } from "@/shared/api/conversation.service";
import { taskService } from "@/shared/api/task.service";

import { cx } from "@/shared/lib/cx";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";
import { Icon } from "@/shared/ui/icon";
import { ContentPanel } from "@/shared/ui/content-panel";
import { toast } from "@/shared/ui/sonner";
import { useAppDispatch } from "@/state";
import type { TaskStatusDto } from "@/shared/api/types";
import "@/features/company-plan/styles/task-review-panel.css";

type TaskReviewPanelProps = {
  agentId?: string;
  /** The assignee Agent's look when no `agentId` renders a generated avatar. */
  agent: { avatarSrc?: string; title: string };
  /**
   * The viewer holds "Create and edit tasks" on the Task's Project (ADR
   * 0017). Marking it done without a review and asking the Agent to fix a
   * conflict are Task edits, so only they are offered them.
   */
  canEditTask: boolean;
  /**
   * The viewer holds "Run tasks with agents" on the Task's Project (ADR
   * 0017). Accepting merges the Run's work, which the server allows only
   * then, so only they are offered it.
   */
  canMerge: boolean;
  companyId: string;
  onClose: () => void;
  onOpenChat: () => void;
  task: {
    taskId: string;
    taskStatus: TaskStatusDto;
    taskVersion: number;
    title: string;
  };
};

type ReviewState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  // No diff can be shown for the Run — its retained workspace is gone, or
  // it ran before this version kept a record — but the Task still needs a
  // way forward (TK-AC7: a re-review or repair path, never a silent done).
  | { kind: "unavailable"; message: string }
  | { kind: "ready"; review: DesktopTaskReview; binding: ReviewBinding }
  | { kind: "blocked"; review: DesktopTaskReview; binding: ReviewBinding };

type ReviewBinding = {
  companyId: string;
  expectedExecutionGeneration: number;
  expectedRevision: number;
  runId: string;
  runtimeId: string;
  taskId: string;
};

const conflictStatuses = new Set(["conflict", "main_dirty"]);
const autoResolveMessage = "rebase and resolve conflict from main";

export function TaskReviewPanel({
  agentId,
  canEditTask,
  canMerge,
  companyId,
  agent,
  onClose,
  onOpenChat,
  task,
}: TaskReviewPanelProps) {
  const dispatch = useAppDispatch();
  const [dialogElement, setDialogElement] = useState<HTMLDivElement | null>(
    null,
  );
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);
  useEffect(() => {
    if (!dialogElement) return;
    const previousFocus = document.activeElement;
    dialogElement
      .querySelector<HTMLButtonElement>(".task-review-panel__close")
      ?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = Array.from(
        dialogElement.querySelectorAll<HTMLElement>(
          'button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((element) => element.getClientRects().length > 0);
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) {
        event.preventDefault();
        dialogElement.focus();
      } else if (!dialogElement.contains(document.activeElement)) {
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
    document.addEventListener("keydown", handleKeyDown, true);
    return () => {
      document.removeEventListener("keydown", handleKeyDown, true);
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected) {
        previousFocus.focus();
      }
    };
  }, [dialogElement]);
  const [state, setState] = useState<ReviewState>({ kind: "loading" });
  const [isAccepting, setIsAccepting] = useState(false);
  const [isResolving, setIsResolving] = useState(false);
  const [collapsedFileKeys, setCollapsedFileKeys] = useState<Set<string>>(
    () => new Set(),
  );
  const [isReviewSidebarCollapsed, setIsReviewSidebarCollapsed] =
    useState(false);
  const reviewLoadGeneration = useRef(0);
  const titleId = `task-review-title-${task.taskId}`;

  const loadReview = useCallback(async () => {
    const generation = ++reviewLoadGeneration.current;
    const commitState = (nextState: ReviewState) => {
      if (generation === reviewLoadGeneration.current) {
        setState(nextState);
      }
    };
    if (!companyId) {
      commitState({ kind: "error", message: "Task review is unavailable." });
      return;
    }
    commitState({ kind: "loading" });
    try {
      const run = await resolveLatestReviewRun(companyId, task.taskId);
      if (!run) {
        commitState({
          kind: "error",
          message: "No completed task run is available for review.",
        });
        return;
      }
      const executionGeneration = run.taskExecutionGeneration;
      if (
        run.companyId !== companyId ||
        run.taskId !== task.taskId ||
        typeof executionGeneration !== "number" ||
        !Number.isSafeInteger(executionGeneration) ||
        executionGeneration < 1
      ) {
        commitState({
          kind: "error",
          message: "The completed task run is no longer reviewable.",
        });
        return;
      }
      // The run's files stay on the computer that ran it, and only its owner
      // is told which runtime that is (ADR 0014).
      const runtimeId = run.runtimeId;
      if (!runtimeId) {
        commitState({
          kind: "unavailable",
          message: "This run's files are on another member's computer.",
        });
        return;
      }
      // The desktop review bridge is not the API: it addresses a run by its
      // decimal string, so the id is rendered once here.
      const runId = String(run.id);
      const result = await reviewDesktopTaskWorkspace({
        action: "prepare",
        runId,
      });
      if (!result.ok) {
        commitState(
          result.code === "workspace_unavailable"
            ? { kind: "unavailable", message: result.reason }
            : { kind: "error", message: result.reason },
        );
        return;
      }
      const binding: ReviewBinding = {
        companyId,
        expectedExecutionGeneration: executionGeneration,
        expectedRevision: task.taskVersion,
        runId,
        runtimeId,
        taskId: task.taskId,
      };
      commitState(
        conflictStatuses.has(result.value.status)
          ? { kind: "blocked", review: result.value, binding }
          : { kind: "ready", review: result.value, binding },
      );
    } catch (error) {
      commitState({
        kind: "error",
        message: getBoardlyQueryErrorMessage(
          error,
          "Task review is unavailable.",
        ),
      });
    }
  }, [companyId, task.taskId, task.taskStatus, task.taskVersion]);

  useEffect(() => {
    void loadReview();
    return () => {
      reviewLoadGeneration.current += 1;
    };
  }, [loadReview]);

  const files = useMemo(
    () =>
      state.kind === "ready" && state.review.diff
        ? parseReviewDiff(state.review.diff)
        : [],
    [state],
  );
  const reviewSummary = useMemo(
    () => (state.kind === "ready" ? summarizeReviewFiles(files) : null),
    [files, state.kind],
  );

  const toggleFileCollapsed = useCallback((fileKey: string) => {
    setCollapsedFileKeys((currentKeys) => {
      const nextKeys = new Set(currentKeys);
      if (nextKeys.has(fileKey)) {
        nextKeys.delete(fileKey);
      } else {
        nextKeys.add(fileKey);
      }
      return nextKeys;
    });
  }, []);

  // The repair path when no diff can be shown: the person decides from the
  // conversation and the project itself, and the Task records that decision
  // as an ordinary status change rather than a reviewed merge.
  const [confirmingDone, setConfirmingDone] = useState(false);
  const [isMarkingDone, setIsMarkingDone] = useState(false);
  const markDoneWithoutReview = useCallback(async () => {
    setIsMarkingDone(true);
    try {
      await taskService.updateTask(companyId, task.taskId, {
        expectedRevision: task.taskVersion,
        status: "done",
      });
      dispatch(
        boardlyApi.util.invalidateTags([
          { id: companyId, type: "Run" },
          { id: companyId, type: "Task" },
        ]),
      );
      toast.success("Task marked as done without a reviewed diff.");
      onClose();
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(error, "Task could not be marked as done."),
      );
    } finally {
      setIsMarkingDone(false);
      setConfirmingDone(false);
    }
  }, [companyId, dispatch, onClose, task.taskId, task.taskVersion]);

  const acceptReview = useCallback(async () => {
    if (state.kind !== "ready") {
      return;
    }
    setIsAccepting(true);
    try {
      const merge = await reviewDesktopTaskWorkspace({
        action: "accept",
        ...state.binding,
      });
      if (!merge.ok) {
        throw new Error(merge.reason);
      }
      dispatch(
        boardlyApi.util.invalidateTags([
          { id: state.binding.companyId, type: "Run" },
          { id: state.binding.companyId, type: "Task" },
        ]),
      );
      toast.success("Task merged.");
      onClose();
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(error, "Task could not be merged."),
      );
    } finally {
      setIsAccepting(false);
    }
  }, [dispatch, onClose, state]);

  const autoResolveConflict = useCallback(async () => {
    if (state.kind !== "blocked") {
      return;
    }
    setIsResolving(true);
    try {
      const latest = await taskService.getTask(companyId, task.taskId);
      await conversationService.postMessage(latest.conversationId, {
        attachmentIds: [],
        content: autoResolveMessage,
        requestKey: generateUuidV7(),
      });
      dispatch(
        boardlyApi.util.invalidateTags([
          { id: latest.conversationId, type: "Message" },
          { id: companyId, type: "Run" },
          { id: companyId, type: "Task" },
        ]),
      );
      toast.success("Conflict resolution sent to the task agent.");
      onOpenChat();
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(
          error,
          "Conflict resolution could not be started.",
        ),
      );
    } finally {
      setIsResolving(false);
    }
  }, [companyId, dispatch, onOpenChat, state.kind, task.taskId]);

  return (
    <ContentPanel
      className="task-review-panel-frame pointer-events-none fixed bottom-4 left-4 right-4 top-16 z-[72] overflow-hidden rounded-3xl transition-[bottom,left,right,top,width,height] duration-300 ease-out sm:bottom-6 sm:left-6 sm:right-6 sm:top-20 xl:left-[max(32px,calc((100vw-1440px)/2))] xl:right-[max(32px,calc((100vw-1440px)/2))]"
      contentRef={setDialogElement}
      contentClassName="task-review-panel pointer-events-auto h-full w-full flex-col overflow-hidden rounded-3xl text-white"
      role="dialog"
      aria-labelledby={titleId}
      aria-modal="true"
      tabIndex={-1}
    >
      <header className="task-review-panel__header relative flex min-h-[104px] w-full shrink-0 flex-wrap items-start gap-3 px-5 pt-5 sm:px-7">
        <span
          className="grid size-[48px] shrink-0 place-items-center overflow-hidden rounded-xl bg-white"
          aria-hidden="true"
        >
          {agentId ? (
            <FoldAvatar agentId={agentId} size={48} />
          ) : (
            <img
              alt=""
              className="size-[48px] max-w-none object-cover object-[50%_12%]"
              draggable={false}
              src={agent.avatarSrc}
            />
          )}
        </span>
        <div className="min-w-0 flex-1 pt-0.5 pr-10">
          <h2
            className="task-review-panel__title m-0 truncate text-base font-bold leading-6 text-white"
            id={titleId}
          >
            Review source changes
          </h2>
          <p className="task-review-panel__task-name mt-1 inline-flex h-7 max-w-full items-center truncate rounded-full bg-white/10 px-3 text-xs font-bold leading-none text-white">
            {task.title}
          </p>
        </div>
        {state.kind === "ready" ? (
          <div className="flex w-full flex-wrap items-center gap-2 pl-0 sm:pl-[60px]">
            <ReviewMetric
              icon={<FileCode2 className="size-3.5" aria-hidden="true" />}
              label={`${reviewSummary?.fileCount ?? 0} files`}
            />
            <ReviewMetric
              icon={<Plus className="size-3.5" aria-hidden="true" />}
              label={`+${reviewSummary?.additions ?? 0}`}
              tone="add"
            />
            <ReviewMetric
              icon={<Minus className="size-3.5" aria-hidden="true" />}
              label={`-${reviewSummary?.deletions ?? 0}`}
              tone="delete"
            />
            <ReviewMetric
              icon={<GitBranch className="size-3.5" aria-hidden="true" />}
              label={
                reviewedInPlace(state.review)
                  ? `committed in place ${state.review.baseRef} -> ${state.review.headRef}`
                  : `${state.review.baseRef} -> ${state.review.headRef}`
              }
            />
          </div>
        ) : null}
        <button
          className={cx(
            iconButtonClassName,
            "task-review-panel__close absolute right-5 top-5",
          )}
          type="button"
          aria-label="Close task review"
          title="Close"
          onClick={onClose}
        >
          <X aria-hidden="true" className="size-5" />
        </button>
        <span
          aria-hidden="true"
          className="task-review-panel__divider pointer-events-none absolute bottom-0 left-4 right-4 h-px bg-[linear-gradient(90deg,rgba(255,255,255,0)_0%,#FFF_50%,rgba(255,255,255,0)_100%)]"
        />
      </header>

      <div className="min-h-0 flex-1 overflow-hidden px-5 py-5 sm:px-7">
        {state.kind === "loading" ? (
          <ReviewNotice
            icon={<GitBranch className="size-5" aria-hidden="true" />}
            tone="neutral"
            title="Checking task changes"
          >
            Preparing the review against main.
          </ReviewNotice>
        ) : state.kind === "error" ? (
          <ReviewNotice
            icon={<AlertTriangle className="size-5" aria-hidden="true" />}
            tone="danger"
            title="Review unavailable"
          >
            {state.message}
          </ReviewNotice>
        ) : state.kind === "unavailable" ? (
          <ReviewNotice
            icon={<AlertTriangle className="size-5" aria-hidden="true" />}
            tone="neutral"
            title="No diff to show for this run"
          >
            Zenith Team cannot show what this run changed: its retained
            workspace is gone, or the run happened before this version kept a
            record of work done in place. The agent&apos;s work may still be in
            your project. Read the conversation and check the project, then
            either mark the task done or keep it in review.
          </ReviewNotice>
        ) : state.kind === "blocked" ? (
          <ReviewNotice
            icon={<AlertTriangle className="size-5" aria-hidden="true" />}
            tone="danger"
            title="Merge is blocked"
          >
            {state.review.message || "Task changes conflict with main."}
          </ReviewNotice>
        ) : files.length === 0 ? (
          <ReviewNotice
            icon={<CheckCircle2 className="size-5" aria-hidden="true" />}
            tone="neutral"
            title="No file changes"
          >
            {reviewedInPlace(state.review)
              ? "This run committed nothing in your project."
              : "This task has no file diff against main."}
          </ReviewNotice>
        ) : (
          <div
            className={cx(
              "grid h-full min-h-0 gap-4 transition-[grid-template-columns] duration-200 ease-out",
              isReviewSidebarCollapsed
                ? "grid-cols-[56px_minmax(0,1fr)]"
                : "lg:grid-cols-[280px_minmax(0,1fr)]",
            )}
          >
            <aside
              className={cx(
                "task-review-panel__sidebar min-h-0 overflow-hidden rounded-2xl border border-white/12 bg-black/22 transition-[width] duration-200 ease-out",
              )}
            >
              {isReviewSidebarCollapsed ? (
                <div className="flex h-full flex-col items-center gap-3 px-2 py-3">
                  <button
                    aria-label="Show changed files"
                    className="task-review-panel__sidebar-toggle grid size-10 cursor-pointer place-items-center rounded-xl border border-white/14 bg-white/8 text-white/72 transition-[background-color,scale] hover:bg-white/14 hover:text-white active:scale-[0.96]"
                    title="Show changed files"
                    type="button"
                    onClick={() => setIsReviewSidebarCollapsed(false)}
                  >
                    <ChevronRight aria-hidden="true" className="size-4" />
                  </button>
                  <div className="task-review-panel__sidebar-icon grid size-10 place-items-center rounded-xl bg-white/6 text-white/46">
                    <FileCode2 aria-hidden="true" className="size-4" />
                  </div>
                  <span className="task-review-panel__muted text-[11px] font-bold leading-none text-white/54 [writing-mode:vertical-rl]">
                    {reviewSummary?.fileCount ?? files.length} files
                  </span>
                </div>
              ) : (
                <>
                  <div className="task-review-panel__sidebar-header flex items-start gap-3 border-b border-white/10 px-4 py-3">
                    <div className="min-w-0 flex-1">
                      <p className="task-review-panel__muted m-0 text-[11px] font-bold uppercase leading-none tracking-normal text-white/42">
                        Changed files
                      </p>
                      <p className="task-review-panel__text m-0 mt-1 text-sm font-bold text-white">
                        {reviewSummary?.fileCount ?? files.length} files
                        reviewed
                      </p>
                    </div>
                    <button
                      aria-label="Hide changed files"
                      className="task-review-panel__sidebar-toggle grid size-8 shrink-0 cursor-pointer place-items-center rounded-lg border border-white/12 bg-white/6 text-white/60 transition-[background-color,scale] hover:bg-white/12 hover:text-white active:scale-[0.96]"
                      title="Hide changed files"
                      type="button"
                      onClick={() => setIsReviewSidebarCollapsed(true)}
                    >
                      <ChevronRight
                        aria-hidden="true"
                        className="size-4 rotate-180"
                      />
                    </button>
                  </div>
                  <div className="max-h-[180px] overflow-y-auto p-2 [scrollbar-width:thin] lg:max-h-none lg:h-[calc(100%-65px)]">
                    {files.map((file) => {
                      const fileKey = getReviewFileKey(file);
                      return (
                        <ReviewFileNavItem
                          file={file}
                          isCollapsed={collapsedFileKeys.has(fileKey)}
                          key={fileKey}
                          onToggle={toggleFileCollapsed}
                        />
                      );
                    })}
                  </div>
                </>
              )}
            </aside>
            <div className="min-h-0 overflow-y-auto pr-1 [scrollbar-width:thin]">
              <div className="space-y-4">
                {files.map((file) => (
                  <ReviewFileDiff
                    file={file}
                    isCollapsed={collapsedFileKeys.has(getReviewFileKey(file))}
                    key={getReviewFileKey(file)}
                    onToggle={toggleFileCollapsed}
                  />
                ))}
              </div>
            </div>
          </div>
        )}
      </div>

      <footer className="task-review-panel__footer flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-white/12 bg-black/16 px-5 py-3 sm:px-7">
        <button
          className="task-review-panel__secondary inline-flex h-10 cursor-pointer items-center justify-center whitespace-nowrap rounded-lg border border-white/20 bg-white/8 px-4 text-sm font-semibold text-white transition-[background-color,scale] hover:bg-white/14 active:scale-[0.96]"
          type="button"
          onClick={onClose}
        >
          Close review
        </button>
        {state.kind === "error" ? (
          <GradientCtaButton
            className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
            hideIcon
            size="auto"
            type="button"
            onClick={loadReview}
          >
            <RefreshCw className="size-4 shrink-0" aria-hidden="true" />
            Try again
          </GradientCtaButton>
        ) : state.kind === "blocked" &&
          state.review.status === "conflict" &&
          canEditTask ? (
          <GradientCtaButton
            className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
            disabled={isResolving}
            hideIcon
            size="auto"
            type="button"
            onClick={autoResolveConflict}
          >
            <Icon name="refresh" className="size-4 shrink-0" />
            {isResolving ? "Starting resolver..." : "Auto Resolve Conflict"}
          </GradientCtaButton>
        ) : state.kind === "blocked" ? (
          <GradientCtaButton
            className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
            hideIcon
            size="auto"
            type="button"
            onClick={loadReview}
          >
            <Icon name="refresh" className="size-4 shrink-0" />
            Check again
          </GradientCtaButton>
        ) : state.kind === "unavailable" ? (
          confirmingDone && canEditTask ? (
            <>
              <GradientCtaButton
                className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
                disabled={isMarkingDone}
                hideIcon
                size="auto"
                type="button"
                onClick={markDoneWithoutReview}
              >
                <Icon name="checkmark" className="size-4 shrink-0" />
                {isMarkingDone ? "Marking as done…" : "Confirm: mark as done"}
              </GradientCtaButton>
              <button
                className="task-review-panel__secondary inline-flex h-10 cursor-pointer items-center justify-center whitespace-nowrap rounded-lg border border-white/20 bg-white/8 px-4 text-sm font-semibold text-white transition-[background-color,scale] hover:bg-white/14 active:scale-[0.96]"
                disabled={isMarkingDone}
                type="button"
                onClick={() => setConfirmingDone(false)}
              >
                Keep in review
              </button>
            </>
          ) : (
            <>
              {canEditTask ? (
                <GradientCtaButton
                  className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
                  hideIcon
                  size="auto"
                  type="button"
                  onClick={() => setConfirmingDone(true)}
                >
                  <Icon name="checkmark" className="size-4 shrink-0" />
                  Mark as done without review
                </GradientCtaButton>
              ) : null}
              <button
                className="task-review-panel__secondary inline-flex h-10 cursor-pointer items-center justify-center whitespace-nowrap rounded-lg border border-white/20 bg-white/8 px-4 text-sm font-semibold text-white transition-[background-color,scale] hover:bg-white/14 active:scale-[0.96]"
                type="button"
                onClick={onOpenChat}
              >
                Open conversation
              </button>
            </>
          )
        ) : canMerge ? (
          <GradientCtaButton
            className="task-review-panel__primary !h-10 justify-center whitespace-nowrap !px-4 [&>span]:gap-2"
            disabled={state.kind !== "ready" || isAccepting}
            hideIcon
            size="auto"
            type="button"
            onClick={acceptReview}
          >
            <Icon name="checkmark" className="size-4 shrink-0" />
            {state.kind === "ready" && reviewedInPlace(state.review)
              ? isAccepting
                ? "Accepting..."
                : "Approve"
              : isAccepting
                ? "Merging..."
                : "Approve & merge"}
          </GradientCtaButton>
        ) : null}
      </footer>
    </ContentPanel>
  );
}

type ReviewFile = ReturnType<typeof parseDiff>[number];

// A Run that executed in place (a bound local directory) reviews the commits
// it made there, so its refs are commits rather than main and a branch, and
// accepting records the work already in the tree instead of merging it.
function reviewedInPlace(review: Pick<DesktopTaskReview, "baseRef">) {
  return review.baseRef !== "main";
}

function parseReviewDiff(diff: string): ReviewFile[] {
  // gitdiff-parser needs a blank line to finish reading metadata before it
  // recognizes Git's binary summary. Only normalize the displayed diff.
  return parseDiff(diff.replace(/^Binary files /gm, "\nBinary files "), {
    nearbySequences: "zip",
  }).map((file) => {
    if (
      file.isBinary &&
      file.type === "modify" &&
      file.oldPath !== file.newPath
    ) {
      return { ...file, type: "rename" };
    }
    return file;
  });
}

function ReviewMetric({
  icon,
  label,
  tone = "neutral",
}: {
  icon: ReactNode;
  label: string;
  tone?: "add" | "delete" | "neutral";
}) {
  return (
    <span
      className={cx(
        "task-review-panel__metric inline-flex h-7 max-w-full items-center gap-1.5 rounded-full px-3 text-[11px] font-bold leading-none tracking-normal",
        tone === "add"
          ? "bg-emerald-400/12 text-emerald-100 ring-1 ring-emerald-300/20"
          : tone === "delete"
            ? "bg-red-400/12 text-red-100 ring-1 ring-red-300/20"
            : "bg-white/8 text-white/72 ring-1 ring-white/12",
      )}
      title={label}
      data-tone={tone}
    >
      <span className="shrink-0">{icon}</span>
      <span className="min-w-0 truncate">{label}</span>
    </span>
  );
}

function ReviewFileNavItem({
  file,
  isCollapsed,
  onToggle,
}: {
  file: ReviewFile;
  isCollapsed: boolean;
  onToggle: (fileKey: string) => void;
}) {
  const fileKey = getReviewFileKey(file);
  const fileName = getReviewFileName(file);
  const status = getReviewFileStatus(file);
  const stats = getReviewFileStats(file);

  return (
    <button
      className="task-review-panel__file-nav group flex w-full cursor-pointer items-center gap-2 rounded-xl border-0 bg-transparent px-3 py-2.5 text-left text-white transition-[background-color,scale] hover:bg-white/8 active:scale-[0.99]"
      type="button"
      title={fileName}
      aria-expanded={!isCollapsed}
      onClick={() => onToggle(fileKey)}
    >
      <ChevronRight
        aria-hidden="true"
        className={cx(
          "task-review-panel__muted size-3.5 shrink-0 text-white/46 transition-transform duration-150 ease-out",
          !isCollapsed && "rotate-90",
        )}
      />
      <FileCode2
        aria-hidden="true"
        className="task-review-panel__muted size-4 shrink-0 text-white/42 group-hover:text-white/70"
      />
      <span className="min-w-0 flex-1">
        <span className="task-review-panel__text block truncate text-xs font-bold leading-4 text-white/84">
          {fileName}
        </span>
        <span className="mt-1 flex items-center gap-2 text-[11px] font-bold leading-none">
          <span
            className={cx(
              "task-review-panel__file-status-text",
              status.textClassName,
            )}
            data-file-type={file.type}
          >
            {status.label}
          </span>
          <span className="task-review-panel__additions text-emerald-200/80">
            +{stats.additions}
          </span>
          <span className="task-review-panel__deletions text-red-200/80">
            -{stats.deletions}
          </span>
        </span>
      </span>
    </button>
  );
}

function ReviewFileDiff({
  file,
  isCollapsed,
  onToggle,
}: {
  file: ReviewFile;
  isCollapsed: boolean;
  onToggle: (fileKey: string) => void;
}) {
  const fileKey = getReviewFileKey(file);
  const fileName = getReviewFileName(file);
  const status = getReviewFileStatus(file);
  const stats = getReviewFileStats(file);

  return (
    <section className="task-review-panel__file overflow-hidden rounded-2xl border border-white/12 bg-[#0d1218]/94 shadow-[0_18px_60px_rgba(0,0,0,0.24)]">
      <button
        aria-expanded={!isCollapsed}
        className="task-review-panel__file-header flex w-full cursor-pointer items-center gap-3 border-0 border-b border-white/10 bg-[#121720]/92 px-4 py-3 text-left text-white transition-[background-color,scale] hover:bg-white/7 active:scale-[0.99] sm:px-5"
        type="button"
        onClick={() => onToggle(fileKey)}
      >
        <ChevronRight
          aria-hidden="true"
          className={cx(
            "task-review-panel__muted size-4 shrink-0 text-white/60 transition-transform duration-150 ease-out",
            !isCollapsed && "rotate-90",
          )}
        />
        <p className="task-review-panel__text m-0 min-w-0 flex-1 truncate text-sm font-bold text-white/88">
          {fileName}
        </p>
        <span className="hidden shrink-0 items-center gap-2 text-xs font-bold leading-none sm:inline-flex">
          <span className="task-review-panel__additions text-emerald-200/86">
            +{stats.additions}
          </span>
          <span className="task-review-panel__deletions text-red-200/86">
            -{stats.deletions}
          </span>
        </span>
        <span
          className={cx(
            "task-review-panel__file-status inline-flex h-6 shrink-0 items-center rounded-full px-2.5 text-[10px] font-bold uppercase leading-none tracking-normal",
            status.className,
          )}
          data-file-type={file.type}
        >
          {status.label}
        </span>
      </button>
      {!isCollapsed ? (
        <div className="task-review-diff max-h-[min(64dvh,760px)] overflow-auto text-[12px] leading-5 [scrollbar-width:thin]">
          {file.hunks.length === 0 ? (
            <p className="task-review-panel__muted m-0 px-4 py-5 text-sm text-white/60 sm:px-5">
              {file.isBinary
                ? "Binary file. This change will be applied when you accept."
                : "No text changes. This change will be applied when you accept."}
            </p>
          ) : (
            <Diff
              diffType={file.type}
              gutterType="default"
              hunks={file.hunks}
              viewType="split"
            >
              {(hunks) =>
                hunks.map((hunk) => <Hunk hunk={hunk} key={hunk.content} />)
              }
            </Diff>
          )}
        </div>
      ) : null}
    </section>
  );
}

function getReviewFileKey(file: ReviewFile) {
  return `${file.oldPath}-${file.newPath}-${file.type}`;
}

function getReviewFileName(file: ReviewFile) {
  if (
    (file.type === "rename" || file.type === "copy") &&
    file.oldPath &&
    file.newPath &&
    file.oldPath !== file.newPath
  ) {
    return `${file.oldPath} -> ${file.newPath}`;
  }
  return file.newPath || file.oldPath;
}

function getReviewFileStatus(file: ReviewFile) {
  switch (file.type) {
    case "add":
      return {
        className:
          "bg-emerald-400/16 text-emerald-100 ring-1 ring-emerald-300/28",
        label: "Added",
        textClassName: "text-emerald-100",
      };
    case "delete":
      return {
        className: "bg-red-400/16 text-red-100 ring-1 ring-red-300/28",
        label: "Deleted",
        textClassName: "text-red-100",
      };
    case "rename":
      return {
        className: "bg-sky-400/16 text-sky-100 ring-1 ring-sky-300/28",
        label: "Renamed",
        textClassName: "text-sky-100",
      };
    case "copy":
      return {
        className: "bg-cyan-400/16 text-cyan-100 ring-1 ring-cyan-300/28",
        label: "Copied",
        textClassName: "text-cyan-100",
      };
    default:
      return {
        className: "bg-amber-300/14 text-amber-100 ring-1 ring-amber-200/24",
        label: "Modified",
        textClassName: "text-amber-100",
      };
  }
}

function getReviewFileStats(file: ReviewFile) {
  let additions = 0;
  let deletions = 0;
  for (const hunk of file.hunks) {
    for (const change of hunk.changes) {
      if (change.type === "insert") additions++;
      if (change.type === "delete") deletions++;
    }
  }
  return { additions, deletions };
}

function summarizeReviewFiles(files: ReviewFile[]) {
  return files.reduce(
    (summary, file) => {
      const stats = getReviewFileStats(file);
      summary.additions += stats.additions;
      summary.deletions += stats.deletions;
      return summary;
    },
    { additions: 0, deletions: 0, fileCount: files.length },
  );
}

function ReviewNotice({
  children,
  icon,
  title,
  tone,
}: {
  children: string;
  icon: ReactNode;
  title: string;
  tone: "danger" | "neutral";
}) {
  return (
    <section
      className={cx(
        "task-review-panel__notice flex max-w-3xl items-start gap-4 rounded-2xl border p-5",
        tone === "danger"
          ? "border-red-300/24 bg-red-500/12"
          : "border-white/14 bg-white/7",
      )}
      data-tone={tone}
    >
      <span
        className={cx(
          "task-review-panel__notice-icon grid size-10 shrink-0 place-items-center rounded-xl",
          tone === "danger"
            ? "bg-red-400/14 text-red-100"
            : "bg-white/8 text-white/74",
        )}
      >
        {icon}
      </span>
      <div className="min-w-0">
        <h3 className="task-review-panel__text m-0 text-sm font-bold text-white">
          {title}
        </h3>
        <p className="task-review-panel__muted m-0 mt-2 text-sm font-medium leading-6 text-white/64">
          {children}
        </p>
      </div>
    </section>
  );
}

async function resolveLatestReviewRun(companyId: string, taskId: string) {
  const page = await runService.listRuns(companyId, {
    limit: 1,
    status: "completed",
    taskId,
  });
  return page.items[0] ?? null;
}
