import { useCallback, useMemo } from "react";
import { useAgentRealtimeChat } from "@/features/chat-panel/hooks/use-agent-realtime-chat";
import { runFailureMessageForViewer } from "@/features/company-plan/model/task-execution";
import type { TeamNodeData } from "@/features/home/types";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import { runService } from "@/shared/api/run.service";
import { workspaceApi } from "@/shared/api/workspace-api";
import type { TaskDto } from "@/shared/api/types";
import { useTaskAccess } from "@/shared/permissions";
import {
  appendTaskChatMessage,
  selectTaskChatDraft,
  selectTaskChatMessages,
  setTaskChatMessages,
  setTaskDraft,
  updateTaskChatMessage,
  useAppDispatch,
  useAppSelector,
  type ConversationDockTab,
} from "@/state";

const emptyTaskChatMessages: ChatMessage[] = [];
const taskConversationUnavailableMessage =
  "This item conversation is not connected right now.";

export function useTaskConversationController({
  activeTeam,
  listedTask,
  tab,
  teams,
}: {
  activeTeam: TeamNodeData;
  // The Task as the caller's list has it, when it has one. It wins over the
  // task workspace's copy when it is newer.
  listedTask?: TaskDto;
  tab: ConversationDockTab | undefined;
  teams: TeamNodeData[];
}) {
  const dispatch = useAppDispatch();
  const companyId = requireActiveCompanyId();
  const backendTaskId = tab?.kind === "task" ? tab.taskId : undefined;
  const taskChatId = backendTaskId ? `task:${backendTaskId}` : "";
  const [loadTaskWorkspaceQuery, taskWorkspaceQuery] =
    workspaceApi.useLazyGetWorkspaceTaskWorkspaceQuery();

  const draftMessage = useAppSelector((state) =>
    taskChatId ? selectTaskChatDraft(state, taskChatId) : "",
  );
  const storedMessages = useAppSelector((state) =>
    taskChatId
      ? selectTaskChatMessages(state, taskChatId)
      : emptyTaskChatMessages,
  );
  const handleAppendMessage = useCallback(
    (message: ChatMessage) => {
      dispatch(appendTaskChatMessage({ message, taskChatId }));
    },
    [dispatch, taskChatId],
  );
  const handleDraftMessageChange = useCallback(
    (message: string) => {
      dispatch(setTaskDraft({ message, taskChatId }));
    },
    [dispatch, taskChatId],
  );
  const loadTaskWorkspace = useCallback(async () => {
    if (!backendTaskId) {
      return null;
    }

    const value = await loadTaskWorkspaceQuery({
      companyId,
      taskId: backendTaskId,
    }).unwrap();
    dispatch(
      workspaceApi.util.upsertQueryData(
        "getWorkspaceTaskDependencies",
        { companyId, taskId: backendTaskId },
        value.dependencies,
      ),
    );
    return value;
  }, [backendTaskId, companyId, dispatch, loadTaskWorkspaceQuery]);
  const taskWorkspace =
    taskWorkspaceQuery.currentData?.task.id === backendTaskId
      ? taskWorkspaceQuery.currentData
      : null;
  const resolveTaskConversation = useCallback(async () => {
    const workspace = await loadTaskWorkspace();
    return workspace?.conversation.id ?? null;
  }, [loadTaskWorkspace]);
  const loadActiveTaskRunIds = useCallback(
    async (conversationId: string) => {
      if (!backendTaskId) {
        return [];
      }
      const runs = await runService.listAllRuns(companyId, {
        active: true,
        conversationId,
        taskId: backendTaskId,
      });
      return runs.map((run) => run.id);
    },
    [backendTaskId, companyId],
  );
  const messageStore = useMemo(
    () => ({
      appendMessage: (message: ChatMessage) => {
        dispatch(appendTaskChatMessage({ message, taskChatId }));
      },
      setMessages: (_team: ChatTeamId, nextMessages: ChatMessage[]) => {
        dispatch(setTaskChatMessages({ messages: nextMessages, taskChatId }));
      },
      updateMessage: (
        _team: ChatTeamId,
        id: number,
        changes: Partial<Omit<ChatMessage, "id" | "team">>,
      ) => {
        dispatch(updateTaskChatMessage({ changes, id, taskChatId }));
      },
    }),
    [dispatch, taskChatId],
  );
  // The Agent doing the work answers in the chat; the assignee is always
  // the member it runs for (ADR 0014).
  const workspaceExecutor = taskWorkspace?.task.execution.agent;
  const assistantActor =
    workspaceExecutor?.kind === "agent"
      ? workspaceExecutor
      : tab?.agentId
        ? {
            avatarUrl: null,
            id: tab.agentId,
            kind: "agent" as const,
            name: activeTeam.label,
          }
        : undefined;
  const {
    activeConversationId,
    connectionStatus,
    hasOlderMessages,
    historyLoadError,
    isLoadingOlderMessages,
    loadOlderMessages,
    sendAgentMessage,
  } = useAgentRealtimeChat({
    assistantActor,
    conversationKey: taskChatId || null,
    loadInitialTaskWorkspace: loadTaskWorkspace,
    loadActiveRunIds: loadActiveTaskRunIds,
    messageStore,
    resolveConversation: resolveTaskConversation,
    taskId: backendTaskId,
    team: activeTeam.teamId,
    unavailableMessage: taskConversationUnavailableMessage,
  });
  const hasAppliedTaskWorkspace =
    taskWorkspace?.conversation.id === activeConversationId;
  const conversationTab = useMemo(() => {
    if (!tab) {
      return undefined;
    }

    return {
      ...tab,
      ...(hasAppliedTaskWorkspace && activeConversationId
        ? { conversationId: activeConversationId }
        : {}),
      title: taskWorkspace?.task.title ?? tab.title,
    };
  }, [activeConversationId, hasAppliedTaskWorkspace, tab, taskWorkspace]);
  const resolvedTaskStatus = taskWorkspace?.task.status ?? listedTask?.status;
  const task =
    listedTask &&
    (!taskWorkspace || listedTask.revision > taskWorkspace.task.revision)
      ? listedTask
      : (taskWorkspace?.task ?? null);
  const taskAccess = useTaskAccess(task);
  // A failed run's card speaks to the person at the computer that ran it.
  // Here, in the one place both task chats read, everyone who is not
  // provably that person reads that the fix is on the computer that ran it
  // (runFailureMessageForViewer). The Task's current assignee is no proof:
  // a handoff keeps the chat, and the earlier runs ran elsewhere.
  const messages = useMemo(
    () =>
      storedMessages.map((message) =>
        message.runFailure && !message.runFailure.ranOnViewerComputer
          ? {
              ...message,
              runFailure: {
                ...message.runFailure,
                message: runFailureMessageForViewer(message.runFailure, false),
              },
            }
          : message,
      ),
    [storedMessages],
  );

  return {
    activeTeam,
    allowAgentMailActions: taskAccess.can("project.tasks.write"),
    connectionStatus,
    conversationId: hasAppliedTaskWorkspace ? activeConversationId : null,
    conversationTab,
    draftMessage,
    hasOlderMessages,
    historyLoadError,
    initialRunQueue: hasAppliedTaskWorkspace
      ? taskWorkspace.runQueue
      : undefined,
    isLoadingOlderMessages,
    messages,
    onAppendMessage: handleAppendMessage,
    onDraftMessageChange: handleDraftMessageChange,
    onLoadOlderMessages: loadOlderMessages,
    readOnly:
      resolvedTaskStatus === "done" || resolvedTaskStatus === "cancelled",
    sendAgentMessage,
    selectedTeam: activeTeam.teamId,
    task,
    teams,
  };
}
