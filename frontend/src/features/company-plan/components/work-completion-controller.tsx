import { useEffect, useMemo, useRef } from "react";
import { ArrowRight, CheckCircle2, X } from "@/shared/ui/icons";
import { Link, useLocation } from "react-router";

import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";
import { parseChatWorkspaceRoute } from "@/features/chat-workspace/model/chat-workspace-route";
import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import {
  createWorkCompletionUpdate,
  createWorkNextUpModel,
  type WorkNextUpModel,
} from "@/features/company-plan/model/work-next-up-model";
import {
  useGetWorkspaceTaskDependencyGraphQuery,
  useListWorkspaceTasksQuery,
} from "@/shared/api/workspace-api";
import { useAppDispatch, useAppSelector } from "@/state/store";
import {
  dismissWorkCompletion,
  upsertWorkCompletion,
  type WorkCompletionReceipt,
} from "@/state/slice/work";
import "@/features/company-plan/styles/work-completion.css";

type PendingCompletion = {
  id: string;
  before: WorkNextUpModel;
  completedTaskIds: string[];
  isReconciling: boolean;
  isUnavailable: boolean;
};

export function WorkCompletionController({ companyId }: { companyId: string }) {
  const dispatch = useAppDispatch();
  const location = useLocation();
  const tasksQuery = useListWorkspaceTasksQuery({ companyId });
  const graphQuery = useGetWorkspaceTaskDependencyGraphQuery({ companyId });
  const runsQuery = useListWorkActiveRunsQuery(companyId);
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
  // The same two sets as the Work page, so both Next up models agree.
  const pendingRequestTaskIds = useMemo(
    () =>
      new Set(
        [
          ...(questionsQuery.currentData?.items ?? []),
          ...(approvalsQuery.currentData?.items ?? []),
        ]
          .map((item) => item.taskId)
          .filter((taskId): taskId is string => Boolean(taskId)),
      ),
    [questionsQuery.currentData, approvalsQuery.currentData],
  );
  const respondRequests = useTaskAttention(companyId);
  const respondTaskIds = respondRequests.taskIds;
  const verificationQueries = [
    tasksQuery,
    graphQuery,
    runsQuery,
    questionsQuery,
    approvalsQuery,
  ];
  // The decide list is part of what the model rests on: without it a
  // Task waiting on the viewer reads as waiting on someone else.
  const hasAllVerificationData =
    verificationQueries.every((query) => query.currentData !== undefined) &&
    respondRequests.hasData;
  const hasVerificationError =
    verificationQueries.some((query) => query.isError) ||
    respondRequests.isError;
  const isVerificationFetching =
    verificationQueries.some((query) => query.isFetching) ||
    respondRequests.isFetching;
  const currentVerification = useRef({
    hasAllData: hasAllVerificationData,
    hasError: hasVerificationError,
  });
  const previous = useRef<WorkNextUpModel | null>(null);
  const pending = useRef<PendingCompletion | null>(null);
  const mounted = useRef(true);
  const model = useMemo(
    () =>
      createWorkNextUpModel({
        tasks: tasksQuery.currentData?.items ?? [],
        dependencyGraph: graphQuery.currentData,
        activeRuns: runsQuery.currentData ?? [],
        pendingRequestTaskIds,
        respondTaskIds,
      }),
    [
      tasksQuery.currentData,
      graphQuery.currentData,
      runsQuery.currentData,
      pendingRequestTaskIds,
      respondTaskIds,
    ],
  );
  const currentModel = useRef(model);
  const refetchTasks = tasksQuery.refetch;
  const refetchGraph = graphQuery.refetch;
  const refetchRuns = runsQuery.refetch;
  const refetchQuestions = questionsQuery.refetch;
  const refetchApprovals = approvalsQuery.refetch;
  const refetchRespondRequests = respondRequests.refetch;

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    currentModel.current = model;
    currentVerification.current = {
      hasAllData: hasAllVerificationData,
      hasError: hasVerificationError,
    };
    if (!tasksQuery.currentData) return;
    const before = previous.current;
    previous.current = model;
    if (!before) return;

    if (pending.current && !pending.current.isReconciling) {
      if (
        hasAllVerificationData &&
        !hasVerificationError &&
        !isVerificationFetching
      ) {
        pending.current.isUnavailable = false;
      } else if (hasVerificationError) {
        pending.current.isUnavailable = true;
      }
    }

    const completion = createWorkCompletionUpdate(before, model);
    if (completion) {
      if (pending.current) {
        pending.current.isReconciling = false;
        publishCompletion(pending.current, before);
      }
      const id = `${companyId}:${completion.completedTaskIds
        .map(
          (taskId) => `${taskId}:${model.byTaskId.get(taskId)?.task.revision}`,
        )
        .join(",")}`;
      pending.current = {
        id,
        before,
        completedTaskIds: completion.completedTaskIds,
        isReconciling: true,
        isUnavailable: false,
      };

      // A task update can arrive before the refreshed dependent tasks. Keep
      // the pre-completion snapshot until the next completion so late server
      // dependency resolution updates amend the same receipt.
      void Promise.allSettled([
        refetchTasks().unwrap(),
        refetchGraph().unwrap(),
        refetchRuns().unwrap(),
        refetchQuestions().unwrap(),
        refetchApprovals().unwrap(),
        refetchRespondRequests(),
      ]).then((results) => {
        const observation = pending.current;
        if (!mounted.current || observation?.id !== id) return;
        observation.isReconciling = false;
        observation.isUnavailable = results.some(
          (result) => result.status === "rejected",
        );
        publishCompletion(observation, currentModel.current);
      });
    }
    if (pending.current) publishCompletion(pending.current, model);

    function publishCompletion(
      observation: PendingCompletion,
      current: WorkNextUpModel,
    ) {
      const update = createWorkCompletionUpdate(
        observation.before,
        current,
        observation.completedTaskIds,
      );
      if (!update) {
        dispatch(dismissWorkCompletion(observation.id));
        return;
      }
      dispatch(
        upsertWorkCompletion({
          ...update,
          id: observation.id,
          companyId,
          isReconciling:
            observation.isReconciling ||
            (!currentVerification.current.hasAllData &&
              !currentVerification.current.hasError),
          isUnavailable:
            observation.isUnavailable || currentVerification.current.hasError,
        }),
      );
    }
  }, [
    companyId,
    dispatch,
    hasAllVerificationData,
    hasVerificationError,
    isVerificationFetching,
    model,
    refetchTasks,
    refetchGraph,
    refetchRuns,
    refetchQuestions,
    refetchApprovals,
    refetchRespondRequests,
    tasksQuery.currentData,
  ]);

  const route = parseChatWorkspaceRoute(location.search);
  const taskId =
    route.target?.kind === "task" ? route.target.taskId : undefined;
  const receipt = useAppSelector((state) =>
    state.work.completionReceipts.find(
      (candidate) =>
        candidate.companyId === companyId &&
        !candidate.dismissed &&
        Boolean(taskId && candidate.completedTaskIds.includes(taskId)),
    ),
  );
  if (location.pathname !== "/app" || !taskId || !receipt) return null;

  return (
    <WorkChatCompletionNotice
      receipt={receipt}
      model={model}
      completedTaskId={taskId}
      onDismiss={() => dispatch(dismissWorkCompletion(receipt.id))}
    />
  );
}

function WorkChatCompletionNotice({
  receipt,
  model,
  completedTaskId,
  onDismiss,
}: {
  receipt: WorkCompletionReceipt;
  model: WorkNextUpModel;
  completedTaskId: string;
  onDismiss: () => void;
}) {
  const completedTask = model.byTaskId.get(completedTaskId)?.task;
  const readyTasks = receipt.newlyReadyTaskIds.flatMap((taskId) => {
    const item = model.byTaskId.get(taskId);
    return item && ["start", "retry", "founder"].includes(item.action)
      ? [item.task]
      : [];
  });
  const remainingPrerequisites = new Map(
    receipt.affectedWaitingTaskIds.flatMap((taskId) =>
      (model.byTaskId.get(taskId)?.prerequisites ?? [])
        .filter((task) => task.status !== "done")
        .map((task) => [task.id, task.title] as const),
    ),
  );
  const params = new URLSearchParams({ view: "next_up" });
  if (readyTasks[0]) params.set("taskId", readyTasks[0].id);
  const description = receipt.isReconciling
    ? "Updating next steps…"
    : receipt.isUnavailable
      ? "Next steps could not be verified. Open Work to check the latest task state."
      : readyTasks.length > 0
        ? `${readyTasks.length} task${readyTasks.length === 1 ? " is" : "s are"} now actionable: ${readyTasks
            .slice(0, 2)
            .map((task) => task.title)
            .join(", ")}${readyTasks.length > 2 ? ", …" : ""}.`
        : remainingPrerequisites.size > 0
          ? `No tasks newly ready. Following tasks still need: ${[...remainingPrerequisites.values()].slice(0, 2).join(", ")}${remainingPrerequisites.size > 2 ? ", …" : ""}.`
          : "No tasks newly ready. Open Work to see the remaining work and next actions.";

  return (
    <aside
      className="work-chat-completion"
      aria-label="Task completed: next steps"
    >
      <CheckCircle2 size={18} aria-hidden="true" />
      <div className="work-chat-completion__copy" role="status">
        <strong>{completedTask?.title ?? "Task"} completed</strong>
        <span>{description}</span>
      </div>
      <Link className="work-chat-completion__link" to={`/tasks?${params}`}>
        See next tasks <ArrowRight size={15} aria-hidden="true" />
      </Link>
      <button
        type="button"
        className="work-chat-completion__dismiss"
        aria-label="Dismiss task completion update"
        onClick={onDismiss}
      >
        <X size={16} aria-hidden="true" />
      </button>
    </aside>
  );
}
