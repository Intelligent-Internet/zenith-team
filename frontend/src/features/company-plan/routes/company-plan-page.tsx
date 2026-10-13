import {
  useGetWorkspaceTaskDependencyGraphQuery,
  useListWorkspaceTaskMessagesQuery,
  useListWorkspaceTasksQuery,
  useUpdateWorkspaceTaskMutation,
} from "@/shared/api/task-api";
import {
  useGetWorkspaceProactiveModeQuery,
  useUpdateWorkspaceProactiveModeMutation,
} from "@/shared/api/company-api";
import { ProjectControls } from "../components/project-controls";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { NewTaskButton } from "@/features/company-plan/components/new-task-button";
import { WorkViewSelector } from "@/features/company-plan/components/work-view-selector";
import { WorkTaskTable } from "@/features/company-plan/components/work-task-table";
import {
  WorkProactiveMode,
  type ProactiveModeStatus,
} from "@/features/company-plan/components/work-proactive-mode";
import { WorkCompletionNotice } from "@/features/company-plan/components/work-completion-notice";
import {
  WorkDependencyCanvas,
  type WorkDependencyCanvasProps,
} from "@/features/company-plan/components/work-dependency-canvas";
import {
  WorkNextUp,
  type WorkNextUpProps,
} from "@/features/company-plan/components/work-next-up";
import { WorkNextUpInsight } from "@/features/company-plan/components/work-next-up-insight";
import {
  createWorkNextUpModel,
  type WorkNextUpItem,
} from "@/features/company-plan/model/work-next-up-model";
import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import {
  useAppDispatch,
  useAppSelector,
  selectWorkView,
  setWorkView,
  selectLatestWorkCompletion,
  dismissWorkCompletion,
  type WorkView as CompanyPlanView,
} from "@/state";
import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";
import {
  taskWaitsForPrerequisites,
  taskWorkStatusLabel,
} from "@/features/company-plan/model/task-work-state";
import { TaskDescription } from "@/features/company-plan/components/task-description";
import {
  TaskAssigneeField,
  TaskExecutorField,
} from "@/features/company-plan/components/task-assignment-fields";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ChevronDown, Funnel, Layers, Maximize2 } from "@/shared/ui/icons";
import { useNavigate, useSearchParams } from "react-router";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import { useTaskRun } from "@/features/company-plan/hooks/use-task-run";
import {
  cannotRunTasksHereLabel,
  firstName,
  taskRunAction,
  taskStartAction,
  taskStartNotes,
} from "@/features/company-plan/model/task-execution";
import { hasProjectPermission, useCan } from "@/shared/permissions";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import {
  WorkspaceAccessState,
  WorkspacePageFrame,
} from "@/features/common/components/workspace-page-frame";
import { TaskDetailPage } from "@/features/company-plan/components/task-detail-page";
import { ProjectTaskDetail } from "../projects/project-task-detail";
import { WorkTaskDetailPanel } from "@/features/company-plan/components/work-task-detail-panel";
import { WorkspaceModalPortal } from "@/features/common/components/workspace-modal-portal";
import { MilestoneBoard } from "@/features/company-plan/components/milestone-board";
import { StatusBoard } from "@/features/company-plan/components/status-board";
import {
  createCompanyPlanMilestones,
  type CompanyPlanMilestoneTask,
} from "@/features/company-plan/model/company-plan-milestone-model";
import {
  createCompanyPlanViewModel,
  type CompanyPlanTaskLane,
} from "@/features/company-plan/model/company-plan-view-model";
import { TaskReviewPanel } from "@/features/company-plan/components/task-review-panel";
import { TaskAccessFixLink } from "@/features/company-plan/components/task-access-fix-link";
import {
  useTaskDependencyPresentation,
  type TaskDependencyPresentationItem,
} from "@/features/task-dependencies/model/use-task-dependency-presentation";

import type { TaskDto } from "@/shared/api/types";
import type { ChatConversationTarget } from "@/shared/chat/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { createTaskPanelAttachments } from "@/features/company-plan/model/task-panel-attachments";
import { milestoneLabel } from "@/features/company-plan/model/milestone-catalog";
import { cx } from "@/shared/lib/cx";
import { useTaskLabel, type LabelTask } from "@/shared/lib/task-label";
import { GlassDivider } from "@/shared/ui/glass-divider";
import { Icon } from "@/shared/ui/icon";
import { toast } from "@/shared/ui/sonner";
import "@/features/company-plan/styles/company-plan.css";

const workViews: CompanyPlanView[] = [
  "board",
  "table",
  "next_up",
  "dependencies",
];
const workViewLabels = {
  board: "Board",
  table: "Table",
  next_up: "Focus",
  dependencies: "Dependencies",
};
type CompanyPlanGrouping = "status" | "milestones" | "agents";

const taskStatusAppearance: Record<
  TaskDto["status"],
  {
    icon: string;
    label: string;
    statusClassName: string;
  }
> = {
  backlog: {
    icon: "hourglass",
    label: "Backlog",
    statusClassName: "bg-[#98a2b3] text-white",
  },
  blocked: {
    icon: "alert",
    label: "Blocked",
    statusClassName: "bg-[#ffb454] text-[#212121]",
  },
  error: {
    icon: "alert",
    label: "Technical error",
    statusClassName: "bg-[#ff8a57] text-white",
  },
  failed: {
    icon: "close-circle",
    label: "Failed",
    statusClassName: "bg-[#ff6b75] text-white",
  },
  cancelled: {
    icon: "close-circle",
    label: "Cancelled",
    statusClassName: "bg-[#ff6b75] text-white",
  },
  done: {
    icon: "checkmark",
    label: "Completed",
    statusClassName: "bg-[#69d267] text-white",
  },
  in_progress: {
    icon: "arrow-right",
    label: "Working",
    statusClassName: "bg-[#6aa9ff] text-white",
  },
  in_review: {
    icon: "eye",
    label: "In review",
    statusClassName: "bg-[#b388ff] text-white",
  },
  todo: {
    icon: "play",
    label: "Ready",
    statusClassName: "bg-yellow text-[#212121]",
  },
};

export function CompanyPlanPage() {
  const session = useSessionSnapshot();
  const canManageProjects = useCan("projects.manage");
  const companyId = session.company?.id;

  if (!companyId) {
    return (
      <WorkspaceAccessState
        description="Choose an active Company before opening its Tasks."
        title="Company unavailable"
      />
    );
  }

  if (!canManageProjects) {
    return <MemberWorkPage companyId={companyId} />;
  }

  return <OwnerCompanyPlanPage companyId={companyId} />;
}

type WorkPageProps = {
  companyId: string;
  projectScopeId?: string;
  inspectorOnly?: boolean;
  onOpenProjectGraph?: () => void;
};

export function WorkTaskInspector({
  canManage,
  companyId,
  projectScopeId,
  onOpenProjectGraph,
}: WorkPageProps & { canManage: boolean }) {
  return canManage ? (
    <OwnerCompanyPlanPage
      companyId={companyId}
      projectScopeId={projectScopeId}
      inspectorOnly
      onOpenProjectGraph={onOpenProjectGraph}
    />
  ) : (
    <MemberWorkPage
      companyId={companyId}
      projectScopeId={projectScopeId}
      inspectorOnly
    />
  );
}

function OwnerCompanyPlanPage({
  companyId,
  projectScopeId,
  inspectorOnly = false,
  onOpenProjectGraph,
}: WorkPageProps) {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const dispatch = useAppDispatch();
  const preferredView = useAppSelector(selectWorkView);
  const requestedView = searchParams.get("view");
  const workView = workViews.includes(requestedView as CompanyPlanView)
    ? (requestedView as CompanyPlanView)
    : preferredView;
  const changeWorkView = useCallback(
    (view: CompanyPlanView) => {
      dispatch(setWorkView(view));
      const params = new URLSearchParams(searchParams);
      params.set("view", view);
      setSearchParams(params, { replace: true });
    },
    [dispatch, searchParams, setSearchParams],
  );
  // The Work page remembers the view it was last opened on. A Project's
  // Tasks tab keeps its own view in the same URL parameter, so the Task
  // inspector it mounts reads that view but never saves it.
  useEffect(() => {
    if (inspectorOnly) return;
    if (workViews.includes(requestedView as CompanyPlanView))
      dispatch(setWorkView(requestedView as CompanyPlanView));
  }, [dispatch, inspectorOnly, requestedView]);
  const completion = useAppSelector((state) =>
    selectLatestWorkCompletion(state, companyId),
  );
  // The one label every view shows for a task.
  const labelTask = useTaskLabel(companyId);
  const actionInFlight = useRef(false);
  const [groupBy, setGroupBy] = useState<CompanyPlanGrouping>("status");
  const [assigneeFilter, setAssigneeFilter] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const [nextUpNavigation, setNextUpNavigation] = useState(0);
  const selectedTaskId = searchParams.get("taskId") ?? undefined;
  const taskExpanded = Boolean(
    selectedTaskId && searchParams.get("taskView") === "full",
  );
  const [isTaskReviewOpen, setIsTaskReviewOpen] = useState(false);
  const [taskActionPending, setTaskActionPending] = useState<{
    action: "complete";
    taskId: string;
  }>();
  const [taskRatings, setTaskRatings] = useState<Record<string, number>>({});
  const proactiveModeQuery = useGetWorkspaceProactiveModeQuery(companyId, {
    skip: !companyId,
  });
  const [updateWorkspaceProactiveMode, proactiveModeMutation] =
    useUpdateWorkspaceProactiveModeMutation();
  const [updateWorkspaceTask] = useUpdateWorkspaceTaskMutation();
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  // Starting a Task is the assignee's own act, on their own computer
  // (ADR 0014); the hook sets the Agent up first when they have not.
  const viewerUserId = useSessionSnapshot().userId;
  const {
    dialog: bindDialog,
    pendingTaskId: runPendingTaskId,
    runTask: startTaskRun,
  } = useTaskRun(companyId);
  const [projectParams, setProjectParams] = useSearchParams();
  const projectId = projectScopeId ?? projectParams.get("projectId") ?? "";
  const tasksQuery = useListWorkspaceTasksQuery(
    { companyId, options: projectId ? { project_id: projectId } : undefined },
    { skip: !companyId },
  );
  const questionsQuery = useListQuestionsQuery({
    companyId,
    allPages: true,
    status: ["pending"],
  });
  const approvalsQuery = useListApprovalsQuery({
    companyId,
    allPages: true,
    status: ["pending"],
  });
  // Every pending request the viewer can see holds its Task: nothing starts
  // it or marks it done. Only those the viewer may answer (ADR 0017) ask for
  // their response.
  const pendingRequestTaskIds = useMemo(
    () =>
      new Set(
        [
          ...(questionsQuery.currentData?.items ?? []).map(
            (item) => item.taskId,
          ),
          ...(approvalsQuery.currentData?.items ?? []).map(
            (item) => item.taskId,
          ),
        ].filter((id): id is string => Boolean(id)),
      ),
    [questionsQuery.currentData, approvalsQuery.currentData],
  );
  const respondRequests = useTaskAttention(companyId);
  const respondTaskIds = respondRequests.taskIds;
  const dependencyGraphQuery = useGetWorkspaceTaskDependencyGraphQuery(
    { companyId },
    {
      skip: !companyId,
    },
  );
  const activeRunsQuery = useListWorkActiveRunsQuery(companyId);
  const nextUpModel = useMemo(
    () =>
      createWorkNextUpModel({
        tasks: tasksQuery.currentData?.items ?? [],
        dependencyGraph: dependencyGraphQuery.isError
          ? undefined
          : dependencyGraphQuery.currentData,
        activeRuns: activeRunsQuery.currentData ?? [],
        pendingRequestTaskIds,
        respondTaskIds,
        viewerUserId,
      }),
    [
      tasksQuery.currentData,
      dependencyGraphQuery.currentData,
      dependencyGraphQuery.isError,
      activeRunsQuery.currentData,
      pendingRequestTaskIds,
      respondTaskIds,
      viewerUserId,
    ],
  );
  // Without the decide list, the viewer's own request would read as
  // someone else's, so Next up waits for it too.
  const nextUpActionsUnavailable =
    !activeRunsQuery.currentData ||
    activeRunsQuery.isError ||
    !questionsQuery.currentData ||
    questionsQuery.isError ||
    !approvalsQuery.currentData ||
    approvalsQuery.isError ||
    respondRequests.isLoading ||
    respondRequests.isError ||
    tasksQuery.isError;
  const plan = useMemo(
    () =>
      createCompanyPlanViewModel(
        tasksQuery.currentData?.items ?? [],
        agentsQuery.currentData?.items ?? [],
      ),
    [agentsQuery.currentData?.items, tasksQuery.currentData?.items],
  );
  const assignees = useMemo(
    () =>
      Array.from(
        new Map(
          plan.lanes
            .flatMap((lane) => lane.tasks)
            .map((task) => [
              `${task.assignee.kind}:${task.assignee.id}`,
              task.assignee,
            ]),
        ).values(),
      ).sort((left, right) => left.name.localeCompare(right.name)),
    [plan.lanes],
  );
  useEffect(() => setAssigneeFilter(""), [companyId]);
  useEffect(() => {
    if (
      assigneeFilter &&
      !assignees.some(
        (assignee) => `${assignee.kind}:${assignee.id}` === assigneeFilter,
      )
    ) {
      setAssigneeFilter("");
    }
  }, [assigneeFilter, assignees]);
  const normalizedSearchQuery = searchQuery.trim().toLocaleLowerCase();
  const visibleLanes = useMemo(
    () =>
      filterTaskLanes(
        plan.lanes.map((lane) => ({
          ...lane,
          tasks: lane.tasks.filter(
            (task) =>
              !assigneeFilter ||
              `${task.assignee.kind}:${task.assignee.id}` === assigneeFilter,
          ),
        })),
        normalizedSearchQuery,
        labelTask,
      ),
    [normalizedSearchQuery, plan.lanes, assigneeFilter, labelTask],
  );
  const visibleTaskIds = useMemo(
    () =>
      new Set(
        visibleLanes.flatMap((lane) => lane.tasks.map((task) => task.id)),
      ),
    [visibleLanes],
  );
  const scopeTaskIds = useMemo(
    () =>
      new Set(plan.lanes.flatMap((lane) => lane.tasks.map((task) => task.id))),
    [plan.lanes],
  );
  const visibleTaskCount = visibleLanes.reduce(
    (count, lane) => count + lane.tasks.length,
    0,
  );
  const milestones = useMemo(
    () =>
      workView === "board" && groupBy === "milestones"
        ? createCompanyPlanMilestones(visibleLanes)
        : [],
    [workView, groupBy, visibleLanes],
  );
  const inspectorLanes = plan.lanes;
  const selectedTask = selectedTaskId
    ? inspectorLanes
        .flatMap((lane) => lane.tasks)
        .find((task) => task.id === selectedTaskId)
    : undefined;
  const selectedTaskUnavailable = Boolean(
    selectedTaskId && tasksQuery.currentData && !selectedTask,
  );
  useEffect(() => {
    setIsTaskReviewOpen(false);
  }, [companyId, selectedTaskId, selectedTask?.status]);
  const selectedTaskDependencies = useTaskDependencyPresentation(
    companyId,
    selectedTask?.id,
  );
  const selectedTaskAgent = selectedTask
    ? inspectorLanes.find((lane) =>
        lane.tasks.some((task) => task.id === selectedTask.id),
      )?.agent
    : undefined;
  const taskInspectorRef = useRef<HTMLElement>(null);
  const taskInspectorIsRendered = Boolean(
    (selectedTask && selectedTaskAgent) || selectedTaskUnavailable,
  );
  useEffect(() => {
    if (!selectedTaskId || !taskInspectorIsRendered) return;
    if (
      !inspectorOnly &&
      !taskExpanded &&
      workView === "dependencies" &&
      window.matchMedia("(min-width: 761px)").matches
    )
      return;
    const frame = window.requestAnimationFrame(() => {
      focusWorkInspector(taskInspectorRef.current);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [
    selectedTaskId,
    taskInspectorIsRendered,
    workView,
    taskExpanded,
    inspectorOnly,
  ]);
  const selectedTaskMessagesQuery = useListWorkspaceTaskMessagesQuery(
    {
      conversationId: selectedTask?.conversationId ?? "",
      taskId: selectedTask?.id ?? "",
    },
    { skip: !selectedTask },
  );
  const selectedTaskAttachments = useMemo(
    () =>
      createTaskPanelAttachments(selectedTaskMessagesQuery.currentData ?? []),
    [selectedTaskMessagesQuery.currentData],
  );
  const selectedTaskHasChat = selectedTask
    ? taskHasRun(
        selectedTask,
        selectedTaskMessagesQuery.currentData?.length ?? 0,
      )
    : false;
  const isLoading =
    (agentsQuery.currentData === undefined && agentsQuery.isFetching) ||
    (tasksQuery.currentData === undefined && tasksQuery.isFetching);
  const hasError = agentsQuery.isError || tasksQuery.isError;
  const proactiveMode = proactiveModeQuery.currentData;
  const proactiveModeStatus: ProactiveModeStatus =
    proactiveModeMutation.isLoading
      ? { type: "saving" }
      : proactiveMode === undefined && proactiveModeQuery.isFetching
        ? { type: "loading" }
        : proactiveModeQuery.isError
          ? {
              message: getWorkspaceErrorMessage(
                proactiveModeQuery.error,
                "Proactive Mode could not be loaded.",
              ),
              type: "error",
            }
          : proactiveMode && !proactiveMode.canManage
            ? { type: "read-only" }
            : null;
  const updateProactiveMode = useCallback(
    async (enabled: boolean) => {
      if (
        !companyId ||
        !proactiveMode?.canManage ||
        proactiveModeQuery.isFetching ||
        proactiveModeQuery.isError ||
        proactiveModeMutation.isLoading
      ) {
        return;
      }

      try {
        await updateWorkspaceProactiveMode({
          companyId,
          request: {
            enabled,
            expectedRevision: proactiveMode.revision,
          },
        }).unwrap();
      } catch (error) {
        toast.error(
          getWorkspaceErrorMessage(
            error,
            "Proactive Mode could not be updated.",
          ),
        );
      }
    },
    [
      companyId,
      proactiveMode,
      proactiveModeMutation.isLoading,
      proactiveModeQuery.isError,
      proactiveModeQuery.isFetching,
      updateWorkspaceProactiveMode,
    ],
  );
  const selectTask = useCallback(
    (taskId: string | undefined) => {
      const nextSearchParams = new URLSearchParams(searchParams);
      if (taskId) nextSearchParams.set("taskId", taskId);
      else {
        nextSearchParams.delete("taskId");
        nextSearchParams.delete("taskView");
      }
      setSearchParams(nextSearchParams);
    },
    [searchParams, setSearchParams],
  );
  const toggleTaskExpanded = useCallback(() => {
    const params = new URLSearchParams(searchParams);
    if (taskExpanded) params.delete("taskView");
    else params.set("taskView", "full");
    setSearchParams(params);
  }, [searchParams, setSearchParams, taskExpanded]);
  const openTaskGraph = useCallback(() => {
    if (onOpenProjectGraph) {
      onOpenProjectGraph();
      return;
    }
    const params = new URLSearchParams(searchParams);
    params.delete("taskView");
    params.set("view", "dependencies");
    dispatch(setWorkView("dependencies"));
    setSearchParams(params);
  }, [dispatch, searchParams, setSearchParams, onOpenProjectGraph]);
  const openTaskDetail = useCallback(
    (task: TaskDto) => selectTask(task.id),
    [selectTask],
  );
  const openMilestoneTask = useCallback(
    (item: CompanyPlanMilestoneTask) => selectTask(item.task.id),
    [selectTask],
  );
  const closeTaskDetail = useCallback(() => {
    selectTask(undefined);
  }, [selectTask]);
  const openSelectedTaskChat = useCallback(() => {
    if (!selectedTask) {
      return;
    }
    navigate(createChatWorkspaceHref(taskChatTarget(selectedTask)));
  }, [navigate, selectedTask]);
  const openDependencyTaskDetail = useCallback(
    (item: TaskDependencyPresentationItem) => {
      selectTask(item.taskId);
    },
    [selectTask],
  );
  const openTaskChat = useCallback(
    (task: TaskDto) => {
      navigate(createChatWorkspaceHref(taskChatTarget(task)));
    },
    [navigate],
  );
  const runTask = useCallback(
    async (task: TaskDto) => {
      const item = nextUpModel.byTaskId.get(task.id);
      if (
        actionInFlight.current ||
        nextUpActionsUnavailable ||
        !item ||
        (!["start", "retry"].includes(item.action) &&
          !(
            taskStartAction(task, viewerUserId)?.kind === "set_up" &&
            task.dependenciesResolved &&
            !item.runStatus &&
            !pendingRequestTaskIds.has(task.id)
          ))
      )
        return;
      actionInFlight.current = true;
      try {
        const startedTask = await startTaskRun(task);
        if (!startedTask) return;
        void Promise.allSettled([
          activeRunsQuery.refetch(),
          tasksQuery.refetch(),
        ]);
        openTaskChat(startedTask);
      } finally {
        actionInFlight.current = false;
      }
    },
    [
      nextUpModel,
      nextUpActionsUnavailable,
      viewerUserId,
      pendingRequestTaskIds,
      startTaskRun,
      activeRunsQuery,
      tasksQuery,
      openTaskChat,
    ],
  );
  const handleNextUpAction = useCallback(
    (item: WorkNextUpItem) => {
      if (item.action === "start" || item.action === "retry")
        void runTask(item.task);
      else if (respondTaskIds.has(item.task.id) || item.category === "running")
        openTaskChat(item.task);
      else selectTask(item.task.id);
    },
    [respondTaskIds, openTaskChat, runTask, selectTask],
  );
  const markSelectedTaskDone = useCallback(async () => {
    if (
      actionInFlight.current ||
      !companyId ||
      !selectedTask ||
      pendingRequestTaskIds.has(selectedTask.id) ||
      !selectedTask.dependenciesResolved ||
      nextUpActionsUnavailable ||
      Boolean(nextUpModel.byTaskId.get(selectedTask.id)?.runStatus) ||
      selectedTask.executedBy.kind !== "user" ||
      !(
        isRunnableTaskStatus(selectedTask.status) ||
        selectedTask.status === "in_review"
      ) ||
      taskActionPending?.taskId === selectedTask.id
    ) {
      return;
    }

    actionInFlight.current = true;
    setTaskActionPending({ action: "complete", taskId: selectedTask.id });
    try {
      await updateWorkspaceTask({
        companyId,
        request: {
          expectedRevision: selectedTask.revision,
          status: "done",
        },
        taskId: selectedTask.id,
      }).unwrap();
      toast.success("Task marked as done.");
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(error, "Task could not be marked as done."),
      );
    } finally {
      actionInFlight.current = false;
      setTaskActionPending(undefined);
    }
  }, [
    pendingRequestTaskIds,
    companyId,
    selectedTask,
    taskActionPending?.taskId,
    updateWorkspaceTask,
    nextUpModel,
    nextUpActionsUnavailable,
  ]);
  const updateSelectedTaskRating = useCallback(
    (rating: number) => {
      if (!selectedTask) {
        return;
      }
      setTaskRatings((current) => ({
        ...current,
        [selectedTask.id]: Math.min(5, Math.max(0, Math.round(rating))),
      }));
    },
    [selectedTask],
  );
  // Marking done is this page's own action; a run's pending state is the
  // run hook's.
  const actionPending =
    taskActionPending ??
    (runPendingTaskId
      ? { action: "run" as const, taskId: runPendingTaskId }
      : undefined);
  const selectedNextUpEntry = selectedTask
    ? nextUpModel.byTaskId.get(selectedTask.id)
    : undefined;
  // The side panel explains the Task in the same words as the queue.
  const selectedNextUpItem = selectedNextUpEntry
    ? viewerNextUpItem(selectedNextUpEntry, viewerUserId)
    : undefined;
  const inspector =
    selectedTask && selectedTaskAgent ? (
      <WorkTaskDetailPanel
        projectPresentation={Boolean(projectScopeId && inspectorOnly)}
        onSelectTask={selectTask}
        companyId={companyId}
        expanded={taskExpanded}
        onExpand={toggleTaskExpanded}
        onOpenGraph={openTaskGraph}
        reviewOpen={isTaskReviewOpen}
        insight={
          <WorkNextUpInsight
            compact={Boolean(projectScopeId && inspectorOnly)}
            item={selectedNextUpItem}
            isLoading={
              !dependencyGraphQuery.currentData &&
              dependencyGraphQuery.isFetching
            }
            isError={dependencyGraphQuery.isError}
            onSelectTask={selectTask}
            onRetry={() => void dependencyGraphQuery.refetch()}
          />
        }
        actionUnavailable={
          nextUpActionsUnavailable ||
          Boolean(actionPending) ||
          Boolean(nextUpModel.byTaskId.get(selectedTask.id)?.runStatus) ||
          pendingRequestTaskIds.has(selectedTask.id)
        }
        readiness={
          workView === "next_up"
            ? (selectedNextUpEntry?.readiness ?? undefined)
            : undefined
        }
        actionPending={
          actionPending?.taskId === selectedTask.id
            ? actionPending.action
            : null
        }
        agent={selectedTaskAgent}
        criteria={selectedTaskAttachments.criteria}
        dependencyBlockedBy={selectedTaskDependencies.blockedBy}
        dependencyHasUnresolvedDependencies={
          selectedTaskDependencies.hasUnresolvedDependencies
        }
        dependencyIsError={selectedTaskDependencies.isError}
        dependencyIsLoading={selectedTaskDependencies.isLoading}
        deliverables={selectedTaskAttachments.deliverables}
        hasAttachmentError={selectedTaskMessagesQuery.isError}
        hasTaskChat={selectedTaskHasChat}
        isAttachmentLoading={
          selectedTaskMessagesQuery.currentData === undefined &&
          selectedTaskMessagesQuery.isFetching
        }
        rating={taskRatings[selectedTask.id] ?? 0}
        task={selectedTask}
        taskNumber={String(selectedTask.number)}
        onClose={closeTaskDetail}
        onMarkDone={() => void markSelectedTaskDone()}
        onOpenChat={openSelectedTaskChat}
        panelRef={taskInspectorRef}
        onRatingChange={updateSelectedTaskRating}
        onReview={() => setIsTaskReviewOpen(true)}
        onRunTask={() => void runTask(selectedTask)}
        onSelectDependency={openDependencyTaskDetail}
      />
    ) : selectedTaskUnavailable ? (
      <aside
        ref={taskInspectorRef}
        className="work-task-inspector workspace-matte-panel rounded-2xl"
        tabIndex={-1}
      >
        <MemberWorkState
          action={
            <RetryWorkButton
              label="Close inspector"
              onClick={closeTaskDetail}
            />
          }
          label="This Task was not found or is no longer visible."
        />
      </aside>
    ) : taskExpanded ? (
      <MemberWorkState
        label={
          hasError
            ? "Task details could not be loaded."
            : "Loading task details…"
        }
        action={
          <RetryWorkButton
            label={hasError ? "Retry" : "All tasks"}
            onClick={
              hasError
                ? () => {
                    void tasksQuery.refetch();
                    void agentsQuery.refetch();
                  }
                : closeTaskDetail
            }
          />
        }
      />
    ) : null;
  const review =
    isTaskReviewOpen &&
    selectedTask?.status === "in_review" &&
    selectedTaskAgent ? (
      <WorkspaceModalPortal>
        <div aria-hidden="true" className="fixed inset-0 z-[71] bg-black/35" />
        <TaskReviewPanel
          key={`${companyId}:${selectedTask.id}`}
          agent={{
            avatarSrc: selectedTaskAgent.avatarSrc,
            title: selectedTaskAgent.name,
          }}
          agentId={
            selectedTask.execution.agent
              ? selectedTask.execution.agent.id
              : undefined
          }
          canEditTask={hasProjectPermission(
            selectedTask.access,
            "project.tasks.write",
          )}
          canMerge={hasProjectPermission(
            selectedTask.access,
            "project.tasks.execute",
          )}
          companyId={companyId}
          task={{
            taskId: selectedTask.id,
            taskStatus: selectedTask.status,
            taskVersion: selectedTask.revision,
            title: selectedTask.title,
          }}
          onClose={() => setIsTaskReviewOpen(false)}
          onOpenChat={openSelectedTaskChat}
        />
      </WorkspaceModalPortal>
    ) : null;
  if (inspectorOnly)
    return (
      <>
        {inspector ?? (
          <MemberWorkState
            label={
              hasError
                ? "Task details could not be loaded."
                : "Loading task details…"
            }
            action={
              <RetryWorkButton
                label={hasError ? "Retry" : "Close inspector"}
                onClick={
                  hasError
                    ? () => {
                        void tasksQuery.refetch();
                        void agentsQuery.refetch();
                      }
                    : closeTaskDetail
                }
              />
            }
          />
        )}
        {review}
        {bindDialog}
      </>
    );
  return (
    <WorkspacePageFrame
      ariaLabel="Company work"
      className={cx(
        "company-work-page",
        taskExpanded && "company-work-page--task-expanded",
      )}
      title="Work"
      description="A clear view of what your company is building."
      contentClassName="company-plan-scroll"
      actions={
        <>
          <WorkProactiveMode
            enabled={proactiveMode?.enabled ?? false}
            disabled={
              !proactiveMode?.canManage ||
              proactiveModeQuery.isFetching ||
              proactiveModeQuery.isError ||
              proactiveModeMutation.isLoading
            }
            status={proactiveModeStatus}
            onEnabledChange={updateProactiveMode}
            onRetry={() => void proactiveModeQuery.refetch()}
          />
          <ProjectControls
            companyId={companyId}
            selected={projectId}
            canManage={proactiveMode?.canManage ?? false}
            onSelect={(id) => {
              const next = new URLSearchParams(projectParams);
              if (id) next.set("projectId", id);
              else next.delete("projectId");
              setProjectParams(next);
            }}
          />
          <NewTaskButton companyId={companyId} />
        </>
      }
    >
      <section className="pointer-events-auto flex min-h-0 flex-1 text-white">
        <div
          className="company-plan-layout grid min-h-0 flex-1 gap-3"
          data-work-view={workView}
          data-inspector-open={Boolean(
            (selectedTask && selectedTaskAgent) || selectedTaskUnavailable,
          )}
        >
          {!taskExpanded ? (
            <WorkWorkspace
              dependencyCanvas={{
                tasks: tasksQuery.currentData?.items ?? [],
                graph: dependencyGraphQuery.currentData,
                labelTask,
                scopeTaskIds,
                matchingTaskIds: visibleTaskIds,
                hasFilters: Boolean(assigneeFilter || normalizedSearchQuery),
                selectedTaskId,
                lanes: plan.lanes,
                isLoading:
                  !dependencyGraphQuery.currentData &&
                  dependencyGraphQuery.isFetching,
                isError: dependencyGraphQuery.isError,
                onRetry: () => void dependencyGraphQuery.refetch(),
                onSelectTask: selectTask,
              }}
              dependencyCanvasKey={companyId}
              nextUp={{
                contextKey: [
                  assigneeFilter,
                  normalizedSearchQuery,
                  nextUpNavigation,
                ].join(":"),
                verificationError:
                  activeRunsQuery.isError ||
                  questionsQuery.isError ||
                  approvalsQuery.isError ||
                  respondRequests.isError,
                onRetryVerification: () => {
                  void activeRunsQuery.refetch();
                  void questionsQuery.refetch();
                  void approvalsQuery.refetch();
                  respondRequests.retry();
                },
                items: nextUpModel.items
                  .filter((item) => visibleTaskIds.has(item.task.id))
                  .map((item) => viewerNextUpItem(item, viewerUserId)),
                lanes: plan.lanes,
                selectedTaskId,
                newlyReadyTaskIds: new Set(
                  completion &&
                    !completion.isReconciling &&
                    !completion.isUnavailable
                    ? completion.newlyReadyTaskIds
                    : [],
                ),
                pendingTaskId: actionPending?.taskId,
                actionsUnavailable: nextUpActionsUnavailable,
                labelTask,
                onSelectTask: selectTask,
                onAction: handleNextUpAction,
              }}
              completionNotice={
                completion ? (
                  <WorkCompletionNotice
                    completion={completion}
                    model={nextUpModel}
                    onDismiss={() =>
                      dispatch(dismissWorkCompletion(completion.id))
                    }
                    onViewNext={() => {
                      setAssigneeFilter("");
                      setSearchQuery("");
                      setNextUpNavigation((value) => value + 1);
                      dispatch(setWorkView("next_up"));
                      const params = new URLSearchParams(searchParams);
                      params.set("view", "next_up");
                      params.delete("taskId");
                      setSearchParams(params);
                    }}
                  />
                ) : null
              }
              workView={workView}
              groupBy={groupBy}
              assignees={assignees}
              assigneeFilter={assigneeFilter}
              attentionTaskIds={respondTaskIds}
              hasError={hasError}
              isLoading={isLoading}
              lanes={visibleLanes}
              milestones={milestones}
              searchQuery={searchQuery}
              selectedTaskId={selectedTask?.id}
              visibleTaskCount={visibleTaskCount}
              onWorkViewChange={changeWorkView}
              onGroupByChange={setGroupBy}
              onAssigneeFilterChange={setAssigneeFilter}
              onMilestoneTaskSelect={openMilestoneTask}
              onSearchQueryChange={setSearchQuery}
              onTaskSelect={openTaskDetail}
            />
          ) : null}

          {inspector}
        </div>
      </section>
      {review}
      {bindDialog}
    </WorkspacePageFrame>
  );
}

type MemberWorkFilter = "all" | "active" | "review" | "blocked" | "done";

function MemberWorkPage({
  companyId,
  projectScopeId,
  inspectorOnly = false,
}: WorkPageProps) {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const taskInspectorRef = useRef<HTMLElement>(null);
  const [filter, setFilter] = useState<MemberWorkFilter>("active");
  const [searchQuery, setSearchQuery] = useState("");
  const projectId = projectScopeId ?? searchParams.get("projectId") ?? "";
  const tasksQuery = useListWorkspaceTasksQuery({
    companyId,
    options: projectId ? { project_id: projectId } : undefined,
  });
  const labelTask = useTaskLabel(companyId);
  const session = useSessionSnapshot();
  const [updateWorkspaceTask, updateWorkspaceTaskState] =
    useUpdateWorkspaceTaskMutation();
  const {
    dialog: bindDialog,
    pendingTaskId: runPendingTaskId,
    runTask,
  } = useTaskRun(companyId);
  const tasks = tasksQuery.currentData?.items ?? [];
  const visibleTasks = useMemo(
    () => filterMemberTasks(tasks, filter, searchQuery, labelTask),
    [filter, labelTask, searchQuery, tasks],
  );
  const selectedTaskId = searchParams.get("taskId") ?? undefined;
  const taskExpanded = Boolean(
    selectedTaskId && searchParams.get("taskView") === "full",
  );
  const selectedTask = selectedTaskId
    ? tasks.find((task) => task.id === selectedTaskId)
    : visibleTasks[0];
  // A member finishes their own work here, or starts a Run with the Agent
  // executing or assisting it (ADR 0014). The owner detail panel has the
  // same actions; someone else's task stays read-only.
  const canCompleteSelected = Boolean(
    selectedTask &&
    selectedTask.executedBy.kind === "user" &&
    selectedTask.assignee.id === session.userId &&
    selectedTask.dependenciesResolved &&
    (isRunnableTaskStatus(selectedTask.status) ||
      selectedTask.status === "in_review") &&
    // Marking done is a Task edit (ADR 0017).
    hasProjectPermission(selectedTask.access, "project.tasks.write"),
  );
  const markSelectedTaskDone = async () => {
    if (
      !selectedTask ||
      !canCompleteSelected ||
      Boolean(runPendingTaskId) ||
      updateWorkspaceTaskState.isLoading
    )
      return;
    try {
      await updateWorkspaceTask({
        companyId,
        request: { expectedRevision: selectedTask.revision, status: "done" },
        taskId: selectedTask.id,
      }).unwrap();
      toast.success("Task marked as done.");
    } catch (error) {
      toast.error(
        getBoardlyQueryErrorMessage(error, "Task could not be marked as done."),
      );
    }
  };
  const completeAction = canCompleteSelected ? (
    <button
      className={
        selectedTask?.status === "in_review"
          ? "dashboard-button dashboard-button-primary"
          : "dashboard-button"
      }
      type="button"
      disabled={updateWorkspaceTaskState.isLoading || Boolean(runPendingTaskId)}
      onClick={() => void markSelectedTaskDone()}
    >
      {updateWorkspaceTaskState.isLoading ? "Marking as Done…" : "Mark as Done"}
    </button>
  ) : null;

  const selectTask = (taskId: string) => {
    const nextSearchParams = new URLSearchParams(searchParams);
    nextSearchParams.set("taskId", taskId);
    setSearchParams(nextSearchParams);
  };

  const closeTaskInspector = () => {
    const nextSearchParams = new URLSearchParams(searchParams);
    nextSearchParams.delete("taskId");
    nextSearchParams.delete("taskView");
    setSearchParams(nextSearchParams);
  };

  useEffect(() => {
    if (!selectedTaskId) return;
    const frame = window.requestAnimationFrame(() => {
      focusWorkInspector(taskInspectorRef.current);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [selectedTaskId, taskExpanded, selectedTask?.id]);

  const openTaskChat = (task: TaskDto) => {
    navigate(createChatWorkspaceHref(taskChatTarget(task)));
  };

  // Starting runs the Task in its Project, which needs "Run tasks" there.
  const startAction =
    selectedTask?.dependenciesResolved &&
    hasProjectPermission(selectedTask.access, "project.tasks.execute")
      ? taskStartAction(selectedTask, session.userId)
      : null;
  const runSelectedTask = async () => {
    if (!selectedTask || updateWorkspaceTaskState.isLoading) return;
    const startedTask = await runTask(selectedTask);
    if (startedTask) openTaskChat(startedTask);
  };
  const primaryAction = startAction ? (
    <button
      className="dashboard-button dashboard-button-primary"
      type="button"
      disabled={Boolean(runPendingTaskId) || updateWorkspaceTaskState.isLoading}
      onClick={() => void runSelectedTask()}
    >
      {runPendingTaskId === selectedTask?.id
        ? "Starting task…"
        : startAction.label}
    </button>
  ) : null;
  // Explain unavailable actions; ready tasks need no extra caption.
  const startNotes =
    selectedTask && isRunnableTaskStatus(selectedTask.status)
      ? taskStartNotes(selectedTask, session.userId)
      : [];
  // The expanded page and a project's task panel take the button and its
  // lines as one footer, as the owner's panel hands them over.
  const detailActions =
    primaryAction || completeAction || startNotes.length > 0 ? (
      <footer className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {primaryAction}
        {completeAction}
        {startNotes.map((note) => (
          <p
            key={note}
            className="m-0 flex basis-full flex-wrap items-center gap-x-2 text-xs text-[var(--workspace-native-text-muted)]"
          >
            {note}
            {selectedTask ? <TaskAccessFixLink task={selectedTask} /> : null}
          </p>
        ))}
      </footer>
    ) : null;

  const toggleTaskExpanded = () => {
    if (!selectedTask) return;
    const params = new URLSearchParams(searchParams);
    params.set("taskId", selectedTask.id);
    if (taskExpanded) params.delete("taskView");
    else params.set("taskView", "full");
    setSearchParams(params);
  };

  if (taskExpanded && selectedTask) {
    return (
      <main className="member-task-detail-page" aria-label="Task details">
        <TaskDetailPage
          key={selectedTask.id}
          actions={detailActions}
          companyId={companyId}
          task={selectedTask}
          onCollapse={toggleTaskExpanded}
          onOpenChat={() => openTaskChat(selectedTask)}
          onSelectDependency={(item) => selectTask(item.taskId)}
          panelRef={taskInspectorRef}
        />
        {bindDialog}
      </main>
    );
  }

  const inspector =
    projectScopeId && inspectorOnly && selectedTask ? (
      <ProjectTaskDetail
        key={selectedTask.id}
        task={selectedTask}
        panelRef={taskInspectorRef}
        onClose={closeTaskInspector}
        onExpand={toggleTaskExpanded}
        onOpenChat={() => openTaskChat(selectedTask)}
        onSelectTask={selectTask}
        actions={detailActions}
        canEdit={hasProjectPermission(
          selectedTask.access,
          "project.tasks.write",
        )}
      />
    ) : (
      <aside
        ref={taskInspectorRef}
        className="member-task-inspector workspace-matte-panel"
        aria-live="polite"
        tabIndex={-1}
      >
        {selectedTask ? (
          <>
            <header>
              <span>{labelTask(selectedTask)}</span>
              <StatusPill task={selectedTask} />
              <button
                className="task-inspector-expand"
                type="button"
                aria-label="Expand task details"
                title="Expand task details"
                onClick={toggleTaskExpanded}
              >
                <Maximize2 size={17} />
              </button>
              {selectedTaskId ? (
                <button
                  aria-label="Close Task inspector"
                  className="member-task-inspector__close"
                  title="Close"
                  type="button"
                  onClick={closeTaskInspector}
                >
                  <Icon name="close" className="size-4" aria-hidden="true" />
                </button>
              ) : null}
            </header>
            <h2>{selectedTask.title}</h2>
            <TaskDescription description={selectedTask.description} />
            <dl>
              {/* Whose Task it is and who does the work (ADR 0014); the
                  assignee may hand it over from here. */}
              <TaskInspectorFact
                label="Assigned to"
                value={<TaskAssigneeField task={selectedTask} />}
              />
              <TaskInspectorFact
                label="Executed by"
                value={<TaskExecutorField task={selectedTask} />}
              />
              <TaskInspectorFact
                label="Milestone"
                value={milestoneLabel(selectedTask.milestoneType)}
              />
              <TaskInspectorFact
                label="Dependencies"
                value={
                  selectedTask.dependenciesResolved
                    ? "Resolved"
                    : `${selectedTask.blockedByCount} unresolved`
                }
              />
              <TaskInspectorFact
                label="Updated"
                value={formatTaskTimestamp(selectedTask.updatedAt)}
              />
            </dl>
            {primaryAction}
            {completeAction}
            {startNotes.map((note) => (
              <p key={note} className="member-task-inspector__start-note">
                {note} <TaskAccessFixLink task={selectedTask} />
              </p>
            ))}
            <button
              className="member-task-inspector__chat"
              type="button"
              onClick={() => openTaskChat(selectedTask)}
            >
              Open conversation
              <Icon name="arrow-right-2" className="size-4" />
            </button>
          </>
        ) : (
          <MemberWorkState
            action={
              selectedTaskId ? (
                <RetryWorkButton
                  label="Close inspector"
                  onClick={closeTaskInspector}
                />
              ) : undefined
            }
            label={
              selectedTaskId
                ? "This task doesn't exist or you can't see it."
                : "Select a Task to inspect it."
            }
          />
        )}
      </aside>
    );
  if (inspectorOnly)
    return (
      <>
        {inspector}
        {bindDialog}
      </>
    );
  return (
    <WorkspacePageFrame
      ariaLabel="Company work"
      description="Follow the Tasks visible to you, inspect their status and dependencies, and open the corresponding Run conversation in Chat."
      title="Work"
      actions={
        <>
          <ProjectControls
            companyId={companyId}
            selected={projectId}
            canManage={false}
            onSelect={(id) => {
              const next = new URLSearchParams(searchParams);
              if (id) next.set("projectId", id);
              else next.delete("projectId");
              setSearchParams(next);
            }}
          />
          <NewTaskButton companyId={companyId} />
        </>
      }
      contentClassName={cx(
        "member-work-layout",
        selectedTaskId && "member-work-layout--inspector-open",
      )}
    >
      <section className="member-work-list workspace-matte-panel">
        <div className="member-work-toolbar">
          <label className="member-work-search">
            <Icon name="search" className="size-4" aria-hidden="true" />
            <span className="sr-only">Search visible tasks</span>
            <input
              placeholder="Search visible Tasks"
              type="search"
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
            />
          </label>
          <div className="member-work-filters" aria-label="Task status filters">
            {(["all", "active", "review", "blocked", "done"] as const).map(
              (item) => (
                <button
                  aria-pressed={filter === item}
                  data-active={filter === item}
                  key={item}
                  type="button"
                  onClick={() => setFilter(item)}
                >
                  {memberWorkFilterLabel[item]}
                </button>
              ),
            )}
          </div>
        </div>

        {tasksQuery.currentData === undefined && tasksQuery.isFetching ? (
          <MemberWorkState label="Loading Tasks…" />
        ) : tasksQuery.isError ? (
          <MemberWorkState
            action={
              <RetryWorkButton onClick={() => void tasksQuery.refetch()} />
            }
            label="Tasks could not be loaded. Check your connection and try again."
          />
        ) : visibleTasks.length === 0 ? (
          <MemberWorkState label="No visible Tasks match this view." />
        ) : (
          <div className="min-h-0 overflow-y-auto p-[8px]">
            {visibleTasks.map((task) => {
              const appearance = {
                ...taskStatusAppearance[task.status],
                label: taskWorkStatusLabel(task),
              };
              return (
                <button
                  aria-pressed={selectedTask?.id === task.id}
                  className="member-work-row"
                  data-selected={selectedTask?.id === task.id}
                  key={task.id}
                  type="button"
                  onClick={() => selectTask(task.id)}
                >
                  <span className="member-work-row__key">
                    {labelTask(task)}
                  </span>
                  <span className="member-work-row__copy">
                    <strong>{task.title}</strong>
                    <small>{task.assignee.name}</small>
                  </span>
                  <span
                    className="member-work-row__status"
                    data-status={
                      taskWaitsForPrerequisites(task) ? "waiting" : task.status
                    }
                  >
                    {appearance.label}
                  </span>
                  <Icon name="arrow-right-2" className="size-4 text-white/28" />
                </button>
              );
            })}
          </div>
        )}
      </section>

      {inspector}
      {bindDialog}
    </WorkspacePageFrame>
  );
}

function focusWorkInspector(inspector: HTMLElement | null) {
  if (!inspector) return;
  inspector.focus({ preventScroll: true });
  if (window.matchMedia("(max-width: 1180px)").matches) {
    inspector.scrollIntoView({ block: "start" });
  }
}

const memberWorkFilterLabel: Record<MemberWorkFilter, string> = {
  active: "Active",
  all: "All",
  blocked: "Blocked",
  done: "Done",
  review: "Review",
};

function filterMemberTasks(
  tasks: TaskDto[],
  filter: MemberWorkFilter,
  searchQuery: string,
  labelTask: LabelTask,
) {
  const query = searchQuery.trim().toLocaleLowerCase();
  return tasks.filter((task) => {
    const matchesFilter =
      filter === "all" ||
      (filter === "active" &&
        ["backlog", "todo", "in_progress", "error"].includes(task.status)) ||
      (filter === "review" && task.status === "in_review") ||
      (filter === "blocked" &&
        ["blocked", "failed", "error"].includes(task.status)) ||
      (filter === "done" && ["done", "cancelled"].includes(task.status));
    if (!matchesFilter) return false;
    if (!query) return true;
    return [
      labelTask(task),
      task.key,
      task.title,
      task.description,
      task.assignee.name,
    ]
      .join(" ")
      .toLocaleLowerCase()
      .includes(query);
  });
}

function MemberWorkState({
  action,
  label,
}: {
  action?: ReactNode;
  label: string;
}) {
  return (
    <div className="member-work-state" role="status">
      <Icon
        name="task-square"
        className="size-8 text-white/24"
        aria-hidden="true"
      />
      <p>{label}</p>
      {action}
    </div>
  );
}

function RetryWorkButton({
  label = "Try again",
  onClick,
}: {
  label?: string;
  onClick: () => void;
}) {
  return (
    <button className="member-work-retry" type="button" onClick={onClick}>
      {label}
    </button>
  );
}

function StatusPill({ task }: { task: TaskDto }) {
  return (
    <span
      className="member-work-row__status"
      data-status={taskWaitsForPrerequisites(task) ? "waiting" : task.status}
    >
      {taskWorkStatusLabel(task)}
    </span>
  );
}

function TaskInspectorFact({
  label,
  value,
}: {
  label: string;
  value: ReactNode;
}) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function formatTaskTimestamp(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "Unavailable"
    : new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(date);
}

export const Component = CompanyPlanPage;

function isRunnableTaskStatus(status: TaskDto["status"]) {
  return (
    status === "backlog" ||
    status === "todo" ||
    status === "blocked" ||
    status === "error"
  );
}

function taskHasRun(
  task: Pick<TaskDto, "executedBy" | "status">,
  messageCount: number,
) {
  if (messageCount > 0) {
    return true;
  }
  // Work the assignee does themself never runs.
  if (task.executedBy.kind === "user") {
    return false;
  }
  return (
    task.status === "in_progress" ||
    task.status === "in_review" ||
    task.status === "done" ||
    task.status === "failed" ||
    task.status === "error"
  );
}

function getWorkspaceErrorMessage(error: unknown, fallback: string) {
  if (!error || typeof error !== "object") {
    return fallback;
  }

  if (
    "body" in error &&
    error.body &&
    typeof error.body === "object" &&
    "detail" in error.body &&
    typeof error.body.detail === "string" &&
    error.body.detail.trim()
  ) {
    return error.body.detail.trim();
  }

  if (
    "message" in error &&
    typeof error.message === "string" &&
    error.message.trim()
  ) {
    return error.message.trim();
  }

  return fallback;
}

function WorkWorkspace({
  dependencyCanvas,
  dependencyCanvasKey,
  nextUp,
  completionNotice,
  workView,
  groupBy,
  assignees,
  assigneeFilter,
  attentionTaskIds,
  hasError,
  isLoading,
  lanes,
  milestones,
  onWorkViewChange,
  onGroupByChange,
  onAssigneeFilterChange,
  onMilestoneTaskSelect,
  onSearchQueryChange,
  onTaskSelect,
  searchQuery,
  selectedTaskId,
  visibleTaskCount,
}: {
  dependencyCanvas: WorkDependencyCanvasProps;
  dependencyCanvasKey: string;
  nextUp: WorkNextUpProps;
  completionNotice: ReactNode;
  workView: CompanyPlanView;
  groupBy: CompanyPlanGrouping;
  assignees: TaskDto["assignee"][];
  assigneeFilter: string;
  attentionTaskIds: ReadonlySet<string>;
  hasError: boolean;
  isLoading: boolean;
  lanes: CompanyPlanTaskLane[];
  milestones: ReturnType<typeof createCompanyPlanMilestones>;
  onWorkViewChange: (view: CompanyPlanView) => void;
  onGroupByChange: (grouping: CompanyPlanGrouping) => void;
  onAssigneeFilterChange: (assignee: string) => void;
  onMilestoneTaskSelect: (item: CompanyPlanMilestoneTask) => void;
  onSearchQueryChange: (query: string) => void;
  onTaskSelect: (task: TaskDto) => void;
  searchQuery: string;
  selectedTaskId: string | undefined;
  visibleTaskCount: number;
}) {
  const hasFilters = searchQuery.trim().length > 0 || Boolean(assigneeFilter);
  let content: ReactNode;
  if (isLoading) content = <WorkspaceMessage message="Loading tasks…" />;
  else if (hasError)
    content = (
      <WorkspaceMessage
        error
        message="Tasks could not be loaded. Check your connection and try again."
      />
    );
  else if (workView === "dependencies")
    content = (
      <WorkDependencyCanvas key={dependencyCanvasKey} {...dependencyCanvas} />
    );
  else if (visibleTaskCount === 0)
    content = (
      <WorkspaceMessage
        message={
          hasFilters
            ? "No tasks match these filters. Change the assignee or search."
            : "No tasks yet. Select New task to get started."
        }
      />
    );
  else if (workView === "next_up")
    content = <WorkNextUp key={nextUp.contextKey} {...nextUp} />;
  else if (workView === "table")
    content = (
      <WorkTaskTable
        attentionTaskIds={attentionTaskIds}
        lanes={lanes}
        selectedTaskId={selectedTaskId}
        onTaskSelect={onTaskSelect}
      />
    );
  else if (groupBy === "milestones")
    content = (
      <MilestoneBoard
        milestones={milestones}
        selectedTaskId={selectedTaskId}
        onTaskSelect={onMilestoneTaskSelect}
      />
    );
  else if (groupBy === "agents")
    content = (
      <TaskLaneBoard
        lanes={
          hasFilters ? lanes.filter((lane) => lane.tasks.length > 0) : lanes
        }
        selectedTaskId={selectedTaskId}
        onTaskSelect={onTaskSelect}
      />
    );
  else
    content = (
      <StatusBoard
        lanes={lanes}
        hideEmptyColumns={hasFilters}
        selectedTaskId={selectedTaskId}
        onTaskSelect={onTaskSelect}
        attentionTaskIds={attentionTaskIds}
      />
    );

  return (
    <section className="work-workspace workspace-matte-panel pointer-events-auto h-full w-full flex-col overflow-hidden rounded-2xl p-4">
      <WorkToolbar
        taskCount={lanes.reduce((count, lane) => count + lane.tasks.length, 0)}
        workView={workView}
        groupBy={groupBy}
        assignees={assignees}
        assigneeFilter={assigneeFilter}
        onWorkViewChange={onWorkViewChange}
        onGroupByChange={onGroupByChange}
        onAssigneeFilterChange={onAssigneeFilterChange}
        searchQuery={searchQuery}
        onSearchQueryChange={onSearchQueryChange}
      />
      {completionNotice}
      <div
        className="flex-1 min-h-0 min-w-0 overflow-hidden"
        role="region"
        id={`company-work-${workView}-panel`}
        aria-label={`${workViewLabels[workView]} view`}
        tabIndex={0}
      >
        {content}
      </div>
    </section>
  );
}

function WorkToolbar({
  taskCount,
  workView,
  groupBy,
  assignees,
  assigneeFilter,
  onWorkViewChange,
  onGroupByChange,
  onAssigneeFilterChange,
  onSearchQueryChange,
  searchQuery,
}: {
  taskCount: number;
  workView: CompanyPlanView;
  groupBy: CompanyPlanGrouping;
  assignees: TaskDto["assignee"][];
  assigneeFilter: string;
  onWorkViewChange: (view: CompanyPlanView) => void;
  onGroupByChange: (grouping: CompanyPlanGrouping) => void;
  onAssigneeFilterChange: (assignee: string) => void;
  onSearchQueryChange: (query: string) => void;
  searchQuery: string;
}) {
  return (
    <div className="work-list-toolbar work-toolbar">
      <div className="work-toolbar__controls">
        <label className="work-search">
          <Icon name="search" className="size-4 shrink-0" />
          <span className="sr-only">Search tasks</span>
          <input
            aria-label="Search tasks"
            placeholder="Search tasks…"
            type="search"
            value={searchQuery}
            onChange={(event) => onSearchQueryChange(event.target.value)}
          />
        </label>
        <div
          className="flex min-w-0 flex-wrap items-center gap-[8px]"
          role="group"
          aria-label="Task view and filters"
        >
          <WorkViewSelector value={workView} onChange={onWorkViewChange} />
          <label className="work-select">
            <span className="sr-only">Assignee</span>
            <span className="work-select__control">
              <Funnel
                className="absolute top-[50%] left-[9px] [transform:translateY(-50%)] pointer-events-none text-workspace-muted"
                aria-hidden
                size={14}
              />
              <select
                aria-label="Filter tasks by assignee"
                value={assigneeFilter}
                onChange={(event) => onAssigneeFilterChange(event.target.value)}
              >
                <option value="">Everyone</option>
                {assignees.map((assignee) => (
                  <option
                    key={`${assignee.kind}:${assignee.id}`}
                    value={`${assignee.kind}:${assignee.id}`}
                  >
                    {assignee.name}
                  </option>
                ))}
              </select>
              <ChevronDown
                aria-hidden="true"
                className="absolute top-[50%] [inset-inline-end:12px] [transform:translateY(-50%)] pointer-events-none text-workspace-muted"
                size={16}
              />
            </span>
          </label>
          {workView === "board" ? (
            <label className="work-select">
              <span className="sr-only">Group by</span>
              <span className="work-select__control">
                <Layers
                  className="absolute top-[50%] left-[9px] [transform:translateY(-50%)] pointer-events-none text-workspace-muted"
                  aria-hidden
                  size={14}
                />
                <select
                  aria-label="Group board tasks by"
                  value={groupBy}
                  onChange={(event) =>
                    onGroupByChange(event.target.value as CompanyPlanGrouping)
                  }
                >
                  <option value="status">Status</option>
                  <option value="milestones">Milestone</option>
                  <option value="agents">Agent</option>
                </select>
                <ChevronDown
                  aria-hidden="true"
                  className="absolute top-[50%] [inset-inline-end:12px] [transform:translateY(-50%)] pointer-events-none text-workspace-muted"
                  size={16}
                />
              </span>
            </label>
          ) : null}
        </div>
        <span className="work-toolbar__count">
          {taskCount} {taskCount === 1 ? "task" : "tasks"}
        </span>
      </div>
    </div>
  );
}

function TaskLaneBoard({
  lanes,
  onTaskSelect,
  selectedTaskId,
}: {
  lanes: CompanyPlanTaskLane[];
  onTaskSelect: (task: TaskDto) => void;
  selectedTaskId: string | undefined;
}) {
  return (
    <section
      className="workspace-work-lane-board flex h-full min-h-0 gap-3 overflow-x-auto overflow-y-hidden pb-1"
      aria-label="Tasks by Agent"
    >
      {lanes.map((lane) => (
        <TaskLane
          key={lane.agent.id}
          lane={lane}
          selectedTaskId={selectedTaskId}
          onTaskSelect={onTaskSelect}
        />
      ))}
    </section>
  );
}

function TaskLane({
  lane,
  onTaskSelect,
  selectedTaskId,
}: {
  lane: CompanyPlanTaskLane;
  onTaskSelect: (task: TaskDto) => void;
  selectedTaskId: string | undefined;
}) {
  const { agent, tasks } = lane;

  return (
    <article className="workspace-work-lane flex h-full min-h-0 w-[360px] shrink-0 flex-col overflow-hidden border">
      <header className="workspace-work-lane__header flex h-[78px] shrink-0 items-start gap-2 border-b p-4">
        {agent.avatarSrc ? (
          <img
            alt=""
            className="workspace-work-lane__avatar size-[46px] shrink-0 bg-white object-cover object-top"
            draggable={false}
            src={agent.avatarSrc}
          />
        ) : (
          <span
            aria-hidden="true"
            className="workspace-work-lane__avatar grid size-[46px] shrink-0 place-items-center overflow-hidden bg-white"
          >
            <FoldAvatar agentId={agent.id} paused size={46} />
          </span>
        )}
        <div className="min-w-0 pl-1">
          <h2 className="m-0 truncate text-sm font-bold leading-[18px] text-[var(--workspace-native-text)]">
            {agent.name}
          </h2>
          <span className="workspace-work-lane__role mt-1.5 inline-flex max-w-full truncate px-3 py-1 text-[12px] font-bold leading-[18px]">
            {agent.title}
          </span>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-[15px] pb-4">
        {tasks.length > 0 ? (
          <div className="grid gap-3">
            {tasks.map((task) => (
              <TaskCard
                key={task.id}
                task={task}
                selected={task.id === selectedTaskId}
                onSelect={onTaskSelect}
              />
            ))}
          </div>
        ) : (
          <div className="workspace-work-lane__empty grid min-h-32 place-items-center border border-dashed px-5 text-center text-xs leading-5">
            No tasks for this Agent.
          </div>
        )}
      </div>
    </article>
  );
}

function TaskCard({
  task,
  onSelect,
  selected,
}: {
  task: TaskDto;
  onSelect: (task: TaskDto) => void;
  selected: boolean;
}) {
  const appearance = {
    ...taskStatusAppearance[task.status],
    ...(taskWaitsForPrerequisites(task)
      ? {
          icon: "lock",
          statusClassName:
            "bg-white/10 text-[var(--workspace-native-text-muted)]",
        }
      : {}),
    label: taskWorkStatusLabel(task),
  };
  return (
    <button
      aria-label={`View task ${task.title}`}
      aria-pressed={selected}
      className={cx(
        "workspace-work-task-card workspace-work-task-card--assignee relative h-[132px] w-full cursor-pointer overflow-hidden border text-left text-[var(--workspace-native-text)] transition",
        selected && "is-selected",
      )}
      type="button"
      onClick={() => onSelect(task)}
    >
      <div className="absolute inset-x-3 top-3 grid grid-cols-[32px_minmax(0,1fr)_24px] items-start gap-2">
        <span className="workspace-work-task-card__icon grid size-8 place-items-center text-[var(--workspace-native-text-muted)]">
          <Icon name="task-square" className="size-[22px]" />
        </span>
        <div className="min-w-0">
          <h3 className="m-0 line-clamp-2 text-sm font-bold leading-[18px] text-[var(--workspace-native-text)]">
            {task.title}
          </h3>
          <p className="m-0 mt-1 line-clamp-2 text-xs leading-4 text-[var(--workspace-native-text-muted)]">
            {task.description || "No task description yet."}
          </p>
        </div>
        <span
          aria-label={appearance.label}
          className={cx(
            "workspace-task-status grid size-6 place-items-center rounded-full",
            appearance.statusClassName,
          )}
          title={appearance.label}
          data-status={
            taskWaitsForPrerequisites(task) ? "waiting" : task.status
          }
        >
          <Icon name={appearance.icon} className="size-4" />
        </span>
      </div>

      <GlassDivider className="workspace-work-task-divider absolute inset-x-3 top-[98px] w-auto" />

      <div className="absolute inset-x-3 top-[107px] flex h-[18px] items-center justify-between gap-3">
        <span
          className="workspace-work-task-key block h-5 min-w-0 max-w-[60%] shrink truncate px-2 text-center font-mono text-[12px] leading-[18px]"
          title={appearance.label}
        >
          {appearance.label}
        </span>
        <span className="min-w-0 flex-1 truncate text-right text-[12px] leading-none text-[var(--workspace-native-text-muted)]">
          {milestoneLabel(task.milestoneType)}
        </span>
      </div>
    </button>
  );
}

function WorkspaceMessage({
  error = false,
  message,
}: {
  error?: boolean;
  message: string;
}) {
  return (
    <div
      className={cx(
        "grid h-full min-h-[320px] place-items-center rounded-xl border border-dashed px-8 text-center text-sm leading-6",
        error
          ? "border-red-300/24 bg-red-300/8 text-red-50"
          : "border-white/14 bg-white/5 text-white/60",
      )}
      role="status"
    >
      {message}
    </div>
  );
}

// Next Up ranks work for the whole Company, but only a Task's assignee starts
// it, on their own computer (ADR 0014). A start or retry the viewer cannot
// make opens the Task instead, and its line says who can start it, six
// words at most. Work a member does themself reads as theirs, not the
// viewer's.
function viewerNextUpItem(
  item: WorkNextUpItem,
  viewerUserId: string | undefined,
): WorkNextUpItem {
  if (item.action === "founder" && item.task.assignee.id !== viewerUserId) {
    return {
      ...item,
      actionLabel: "View task",
      reason: `${firstName(item.task.assignee.name)} does this themself`,
    };
  }
  if (item.action !== "start" && item.action !== "retry") return item;
  const action = taskRunAction(item.task, viewerUserId);
  if (
    action.kind === "run" ||
    action.kind === "retry" ||
    action.kind === "set_up"
  ) {
    // Starting also needs "Run tasks" on the Task's Project (ADR 0017).
    return hasProjectPermission(item.task.access, "project.tasks.execute")
      ? item
      : {
          ...item,
          action: "inspect",
          actionLabel: "View task",
          reason: cannotRunTasksHereLabel(),
        };
  }
  if (action.kind === "not_yours") {
    return {
      ...item,
      action: "inspect",
      actionLabel: "View task",
      reason: action.label,
    };
  }
  return { ...item, action: "inspect", actionLabel: action.label };
}

/** The chat target for a Task: its conversation, with the Agent doing it when there is one. */
function taskChatTarget(task: TaskDto): ChatConversationTarget {
  return {
    ...(task.execution.agent ? { agentId: task.execution.agent.id } : {}),
    conversationId: task.conversationId,
    kind: "task",
    taskId: task.id,
  };
}

function filterTaskLanes(
  lanes: CompanyPlanTaskLane[],
  normalizedQuery: string,
  labelTask: LabelTask,
) {
  if (!normalizedQuery) {
    return lanes;
  }

  return lanes
    .map((lane) => {
      const agentSearchText = [lane.agent.name, lane.agent.title]
        .join(" ")
        .toLocaleLowerCase();
      const agentMatches = agentSearchText.includes(normalizedQuery);

      return {
        ...lane,
        tasks: agentMatches
          ? lane.tasks
          : lane.tasks.filter((task) =>
              [
                labelTask(task),
                task.description,
                task.key,
                task.title,
                task.assignee.name,
                task.milestoneType.replaceAll("_", " "),
                milestoneLabel(task.milestoneType),
              ]
                .join(" ")
                .toLocaleLowerCase()
                .includes(normalizedQuery),
            ),
      };
    })
    .filter((lane) => lane.tasks.length > 0);
}
