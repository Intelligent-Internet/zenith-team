import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { Link } from "react-router";

import { getBoardlyDesktopBridge } from "@/app/desktop/desktop-environment.service";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useCan, useTaskAccess } from "@/shared/permissions";

import { sendWithAgentSetup } from "@/features/agents/model/agent-binding";
import { formatTaskLabel } from "@/shared/lib/task-label";
import { TaskReportQueueBanner } from "@/features/chat-panel/components/task-report-queue-banner";
import { useContextChatPanel } from "@/features/chat-panel/hooks/use-context-chat-panel";
import { useAgentRealtimeChat } from "@/features/chat-panel/hooks/use-agent-realtime-chat";
import { useTaskConversationController } from "@/features/chat-panel/hooks/use-task-conversation-controller";
import type { ChatFocusedMessageTarget } from "@/features/chat-panel/types";
import { WorkspaceModalPortal } from "@/features/common/components/workspace-modal-portal";
import { TaskReviewPanel } from "@/features/company-plan/components/task-review-panel";
import { TaskAccessFixLink } from "@/features/company-plan/components/task-access-fix-link";
import {
  taskAwaitsViewerReview,
  unreadyReasonLabel,
} from "@/features/company-plan/model/task-execution";
import type { TeamNodeData } from "@/features/home/types";
import { resolveDirectConversation } from "@/shared/api/direct-conversation";
import { runService } from "@/shared/api/run.service";
import { refreshWorkspaceChatConversations } from "@/shared/api/workspace-api";
import type { ActorRefDto, AgentDto, TaskDto } from "@/shared/api/types";
import { PencilLine } from "@/shared/ui/icons";
import { directConversationTitle } from "@/features/chat-workspace/model/chat-navigator-browse";
import { chatConversationTargetKey } from "@/shared/chat/conversation-target";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatConversationTarget, ChatMessage } from "@/shared/chat/types";
import {
  appendConversationChatMessage,
  selectConversationChatMessages,
  selectConversationWorkspaceDraft,
  setConversationChatMessages,
  setConversationWorkspaceDraft,
  updateConversationChatMessage,
  useAppDispatch,
  useAppSelector,
  type ConversationDockTab,
} from "@/state";

type ActiveConversationPaneProps = {
  archived?: boolean;
  agent?: ChatWorkspaceAgent;
  agents: ChatWorkspaceAgent[];
  allowStructuredActions: boolean;
  availableAgentIds?: ReadonlySet<string>;
  companyId: string;
  conversationTitle?: string | null;
  focusedMessage?: ChatFocusedMessageTarget;
  focusRequestKey: number;
  isCeoConversation?: boolean;
  onConversationResolved: (target: ChatConversationTarget) => void;
  onExitFocusedHistory: () => void;
  onNewChat?: () => void;
  // Opens the setup of an Agent on the viewer's own computer and answers
  // whether they finished it (ADR 0014).
  onSetUpAgent?: (agentId: string) => Promise<boolean>;
  roomNavigationAction?: ReactNode;
  target: ChatConversationTarget;
  task?: TaskDto;
  taskPrefix?: string;
  team: TeamNodeData;
  teams: TeamNodeData[];
};

export type ChatWorkspaceAgent = {
  accentColor?: string | null;
  provider?: AgentDto["provider"];
  archivedAt?: string | null;
  avatarSrc?: string | null;
  id: string;
  // The viewer's own computer for this Agent (ADR 0014), where Studio runs.
  runtimeId?: string;
  name: string;
  // Whether this Agent can run for the VIEWER right now: on a computer of
  // theirs, online and signed in (ADR 0014).
  runtimeAvailable?: boolean;
  // Whether the viewer has set the Agent up on a computer of their own. A
  // send to one they have not is refused, so the composer offers the setup
  // instead.
  bound?: boolean;
  title: string;
};

// True when the viewer has to set this Agent up before it can answer them.
function agentNeedsSetup(
  agent: ChatWorkspaceAgent | undefined | null,
): boolean {
  return Boolean(agent && agent.bound === false);
}

export function ActiveConversationPane(props: ActiveConversationPaneProps) {
  return props.target.kind === "task" ? (
    <TaskConversationPane {...props} target={props.target} />
  ) : (
    <DirectConversationPane {...props} target={props.target} />
  );
}

function DirectConversationPane({
  archived,
  agent,
  agents,
  allowStructuredActions,
  availableAgentIds,
  companyId,
  conversationTitle,
  focusedMessage,
  focusRequestKey,
  isCeoConversation,
  onConversationResolved,
  onExitFocusedHistory,
  onNewChat,
  onSetUpAgent,
  roomNavigationAction,
  target,
  team,
  teams,
}: ActiveConversationPaneProps & {
  target: Extract<ChatConversationTarget, { kind: "direct" }>;
}) {
  const dispatch = useAppDispatch();
  const resolvedAgent =
    agent ?? agents.find((candidate) => candidate.id === target.agentId);
  const agentArchived = Boolean(
    resolvedAgent?.archivedAt ||
    (availableAgentIds && !availableAgentIds.has(target.agentId)),
  );
  const needsSetup = agentNeedsSetup(resolvedAgent);
  const targetKey = chatConversationTargetKey(target);
  const conversationSessionKey = `${companyId}:${targetKey}`;
  const messages = useAppSelector((state) =>
    selectConversationChatMessages(state, conversationSessionKey),
  );
  const draftMessage = useAppSelector((state) =>
    selectConversationWorkspaceDraft(state, companyId, targetKey),
  );
  const resolveConversation = useCallback(async () => {
    if (target.conversationId) return target.conversationId;

    const conversationId = await resolveDirectConversation(
      target.agentId,
      `${resolvedAgent?.name ?? team.label} conversation`,
    );
    await refreshWorkspaceChatConversations(dispatch, companyId);
    return conversationId;
  }, [
    companyId,
    dispatch,
    resolvedAgent?.name,
    target.agentId,
    target.conversationId,
    team.label,
  ]);
  const loadActiveRunIds = useCallback(
    async (conversationId: string) => {
      const runs = await runService.listAllRuns(companyId, {
        active: true,
        conversationId,
      });
      return runs.map((run) => run.id);
    },
    [companyId],
  );
  const messageStore = useMemo(
    () => ({
      appendMessage: (message: ChatMessage) => {
        dispatch(
          appendConversationChatMessage({
            conversationKey: conversationSessionKey,
            message,
          }),
        );
      },
      setMessages: (_team: ChatTeamId, nextMessages: ChatMessage[]) => {
        dispatch(
          setConversationChatMessages({
            conversationKey: conversationSessionKey,
            messages: nextMessages,
          }),
        );
      },
      updateMessage: (
        _team: ChatTeamId,
        id: number,
        changes: Partial<Omit<ChatMessage, "id" | "team">>,
      ) => {
        dispatch(
          updateConversationChatMessage({
            changes,
            conversationKey: conversationSessionKey,
            id,
          }),
        );
      },
    }),
    [conversationSessionKey, dispatch],
  );
  const assistantActor = useMemo<ActorRefDto>(
    () => ({
      avatarUrl: null,
      id: resolvedAgent?.id ?? target.agentId,
      kind: "agent",
      name: resolvedAgent?.name ?? team.label,
    }),
    [resolvedAgent?.id, resolvedAgent?.name, target.agentId, team.label],
  );
  const realtimeChat = useAgentRealtimeChat({
    assistantActor,
    conversationKey: conversationSessionKey,
    loadActiveRunIds,
    loadInitialConversation: resolveConversation,
    messageStore,
    resolveConversation: () => resolveConversation(),
    team: team.teamId,
    unavailableMessage: `${resolvedAgent?.name ?? team.label} is not available right now.`,
  });
  const activeConversationId = realtimeChat.activeConversationId;
  const agentName = resolvedAgent?.name ?? team.label;
  const setUpAgent = useCallback(
    () => onSetUpAgent?.(target.agentId) ?? Promise.resolve(false),
    [onSetUpAgent, target.agentId],
  );
  // A send to an Agent the viewer has not set up offers the setup first and
  // then sends the same words, so the draft survives the detour (ADR 0014).
  const { sendAgentMessage } = realtimeChat;
  const sendAfterSetup = useCallback<typeof sendAgentMessage>(
    (message, options) =>
      sendWithAgentSetup(
        () => sendAgentMessage(message, options),
        { bound: !needsSetup, name: agentName },
        setUpAgent,
      ),
    [agentName, needsSetup, sendAgentMessage, setUpAgent],
  );
  useEffect(() => {
    if (
      !activeConversationId ||
      activeConversationId === target.conversationId
    ) {
      return;
    }
    onConversationResolved({
      ...target,
      conversationId: activeConversationId,
    });
  }, [activeConversationId, onConversationResolved, target]);

  const conversationTab = useMemo<ConversationDockTab>(
    () => ({
      agentId: target.agentId,
      avatarSrc: resolvedAgent?.avatarSrc || team.iconSrc,
      ...(activeConversationId ? { conversationId: activeConversationId } : {}),
      id: targetKey,
      kind: "direct",
      subtitle: `${resolvedAgent?.title ?? team.agentRole} · Direct message`,
      teamId: team.teamId,
      title: directConversationTitle(
        conversationTitle,
        resolvedAgent?.name ?? team.label,
      ),
    }),
    [
      activeConversationId,
      conversationTitle,
      resolvedAgent?.avatarSrc,
      resolvedAgent?.name,
      resolvedAgent?.title,
      target.agentId,
      targetKey,
      team,
    ],
  );
  const handleAppendMessage = useCallback(
    (message: ChatMessage) =>
      dispatch(
        appendConversationChatMessage({
          conversationKey: conversationSessionKey,
          message,
        }),
      ),
    [conversationSessionKey, dispatch],
  );
  const handleDraftChange = useCallback(
    (message: string) => {
      dispatch(
        setConversationWorkspaceDraft({ companyId, message, targetKey }),
      );
    },
    [companyId, dispatch, targetKey],
  );

  return useContextChatPanel({
    activeTeam: team,
    allowStructuredActions,
    appendUserMessageOnSend: false,
    workspaceContext: {
      agentName: resolvedAgent?.name ?? team.label,
      provider: resolvedAgent?.provider,
    },
    studioRuntimeId: resolvedAgent?.runtimeId,
    studioAgentId: resolvedAgent?.id,
    // An archived Agent's chat is read-only and the Agent cannot be set up
    // (the bind is refused), so neither notice about sending to it applies.
    composerAccessory: (
      <div className="grid gap-2">
        {isCeoConversation &&
        resolvedAgent &&
        !agentArchived &&
        activeConversationId ? (
          <TaskReportQueueBanner
            key={`${companyId}:${activeConversationId}`}
            companyId={companyId}
            conversationId={activeConversationId}
            onRunCreated={realtimeChat.openConversation}
          />
        ) : null}

        {agentArchived ? null : needsSetup && resolvedAgent ? (
          <NotBoundNotice
            agentName={resolvedAgent.name}
            onSetUp={onSetUpAgent ? () => void setUpAgent() : undefined}
          />
        ) : resolvedAgent?.runtimeAvailable === false ? (
          <RunnerOfflineNotice />
        ) : null}
      </div>
    ),
    composerAriaLabel: `Message ${resolvedAgent?.name ?? team.label}`,
    composerPlaceholder: needsSetup
      ? `Set ${resolvedAgent?.name ?? team.label} up on your computer to chat`
      : `Message ${resolvedAgent?.name ?? team.label}…`,
    connectionStatus: realtimeChat.connectionStatus,
    conversationId: activeConversationId,
    conversationTab,
    dockAware: false,
    draftMessage,
    enabled: true,
    focusRequestKey,
    focusedMessage,
    hasOlderMessages: realtimeChat.hasOlderMessages,
    headerActions: (
      <>
        {roomNavigationAction}
        {onNewChat && resolvedAgent && !agentArchived ? (
          <button
            aria-label={"New chat with " + resolvedAgent.name}
            className="workspace-chat-action workspace-chat-new-conversation"
            onClick={onNewChat}
            title={"New chat with " + resolvedAgent.name}
            type="button"
          >
            <PencilLine size={18} aria-hidden />
          </button>
        ) : null}
      </>
    ),
    historyLoadError: realtimeChat.historyLoadError,
    isLoadingOlderMessages: realtimeChat.isLoadingOlderMessages,
    layout: "workspace",
    messages,
    onAppendMessage: handleAppendMessage,
    onClose: noop,
    onDraftMessageChange: handleDraftChange,
    onExitFocusedHistory,
    onLoadOlderMessages: realtimeChat.loadOlderMessages,
    onSelectTeam: noop,
    readOnly: archived || !resolvedAgent || agentArchived,
    readOnlyPlaceholder: archived
      ? "This conversation is archived. Restore it from Archived chats to send a message."
      : resolvedAgent
        ? "This Agent is archived. The conversation is read-only."
        : "This Agent is no longer available. The conversation is read-only.",
    selectedTeam: team.teamId,
    sendAgentMessage: sendAfterSetup,
    showApprovals: allowStructuredActions,
    teams,
  });
}

function TaskConversationPane({
  archived,
  agents,
  availableAgentIds,
  companyId,
  focusedMessage,
  focusRequestKey,
  onConversationResolved,
  onExitFocusedHistory,
  onSetUpAgent,
  target,
  task: listedTask,
  taskPrefix,
  team,
  teams,
}: ActiveConversationPaneProps & {
  target: Extract<ChatConversationTarget, { kind: "task" }>;
}) {
  const dispatch = useAppDispatch();
  const viewerUserId = useSessionSnapshot().userId;
  const targetKey = chatConversationTargetKey(target);
  const draftMessage = useAppSelector((state) =>
    selectConversationWorkspaceDraft(state, companyId, targetKey),
  );
  const tab = useMemo<ConversationDockTab>(
    () => ({
      ...(target.agentId ? { agentId: target.agentId } : {}),
      avatarSrc: team.iconSrc,
      ...(target.conversationId
        ? { conversationId: target.conversationId }
        : {}),
      id: targetKey,
      kind: "task",
      subtitle: `Task · ${team.label}`,
      taskId: target.taskId,
      teamId: team.teamId,
      title: listedTask?.title ?? "Task conversation",
    }),
    [listedTask?.title, target, targetKey, team],
  );
  const taskConversation = useTaskConversationController({
    activeTeam: team,
    listedTask,
    tab,
    teams,
  });
  const task = taskConversation.task;
  const [isTaskReviewOpen, setIsTaskReviewOpen] = useState(false);
  // The review decides the work the assignee's computer did, so it is
  // theirs or an owner's or admin's (ADR 0014). A member the Task was
  // handed away from keeps no review of it.
  const canAssignTasks = useCan("tasks.assign");
  // Inside a Project the Task's own access decides (ADR 0017): posting
  // needs "Create and edit tasks", and the chat's questions, approvals and
  // plan cards "Answer approvals and questions", apart from posting; the
  // Company permissions are not consulted for them. The review offers what
  // the server allows: merging needs "Run tasks with agents", marking done
  // "Create and edit tasks". Until a read brings the Task's access (a
  // `task.created` frame leaves it out) nothing is offered; the frame
  // already asked for that read.
  const taskAccess = useTaskAccess(task);
  const accessKnown = Boolean(task?.access);
  const canPost = accessKnown && taskAccess.can("project.tasks.write");
  const canDecide = accessKnown && taskAccess.can("project.approvals.resolve");
  const canMergeTask = accessKnown && taskAccess.can("project.tasks.execute");
  const canReviewTask = Boolean(
    task &&
    taskAwaitsViewerReview(task, viewerUserId, canAssignTasks) &&
    getBoardlyDesktopBridge()?.reviewTaskWorkspace,
  );
  const closeTaskReview = useCallback(() => setIsTaskReviewOpen(false), []);

  useEffect(() => {
    if (!canReviewTask) closeTaskReview();
  }, [canReviewTask, closeTaskReview]);

  // The Agent doing the work. The assignee is always a member, the one who
  // runs it on their own computer (ADR 0014), so it never names the Agent.
  const taskExecutor = task?.execution.agent;
  const executorAgent = agents.find(
    (candidate) => candidate.id === taskExecutor?.id,
  );
  const taskAgent =
    task && taskExecutor
      ? (executorAgent ?? {
          id: taskExecutor.id,
          name: taskExecutor.name,
          title: team.agentRole,
        })
      : null;
  const hasBlockedDependencies = task?.dependenciesResolved === false;
  const taskAgentNeedsSetup = agentNeedsSetup(taskAgent);
  // A Task runs on its assignee's computer, so anybody else's message is
  // saved for them rather than starting work (ADR 0014).
  const viewerIsAssignee = Boolean(
    task && viewerUserId && task.assignee.id === viewerUserId,
  );
  // A Task a person does themself, with no Agent helping, has nobody to
  // answer in its chat; that is not a missing assignment.
  const personWithoutAgent = Boolean(
    task && task.executedBy.kind === "user" && !taskExecutor,
  );
  const lacksAgent = Boolean(
    task &&
    (!taskExecutor ||
      (availableAgentIds && !availableAgentIds.has(taskExecutor.id))),
  );
  // Every Run this chat starts is the assignee's (ADR 0014), so when they
  // may not run tasks in the Project the server refuses every message here,
  // a colleague's too (ADR 0017). The chat gives that reason, never the
  // Agent's setup, which would not help.
  const assigneeCannotRun =
    task?.execution.unreadyReason === "assignee_cannot_run";
  const composerReadOnly = Boolean(
    archived ||
    !canPost ||
    taskConversation.readOnly ||
    lacksAgent ||
    hasBlockedDependencies ||
    assigneeCannotRun,
  );
  // The read-only line names that reason only when nothing the server asks
  // first (the right to post, the Task's state) already explains it.
  const showsCannotRunReason = Boolean(
    assigneeCannotRun &&
    !archived &&
    canPost &&
    !taskConversation.readOnly &&
    !hasBlockedDependencies &&
    !personWithoutAgent,
  );

  useEffect(() => {
    if (
      !taskConversation.conversationId ||
      taskConversation.conversationId === target.conversationId
    ) {
      return;
    }
    onConversationResolved({
      ...target,
      ...(taskAgent ? { agentId: taskAgent.id } : {}),
      conversationId: taskConversation.conversationId,
    });
  }, [
    onConversationResolved,
    target,
    taskAgent,
    taskConversation.conversationId,
  ]);

  // The assignee's message starts a run on their own setup of the Agent, so
  // it offers that setup first. Anybody else's is saved for the assignee
  // and is never refused for it (ADR 0014). An assignee who may not run
  // tasks here is never sent to the setup: the permission is what is
  // missing (ADR 0017).
  const taskAgentId = taskAgent?.id;
  const taskAgentName = taskAgent?.name ?? "The agent";
  const setUpTaskAgent = useCallback(
    () =>
      taskAgentId && onSetUpAgent
        ? onSetUpAgent(taskAgentId)
        : Promise.resolve(false),
    [onSetUpAgent, taskAgentId],
  );
  const { sendAgentMessage: sendTaskMessage } = taskConversation;
  const sendTaskMessageAfterSetup = useCallback<typeof sendTaskMessage>(
    (message, options) =>
      viewerIsAssignee && taskAgentId && !assigneeCannotRun
        ? sendWithAgentSetup(
            () => sendTaskMessage(message, options),
            { bound: !taskAgentNeedsSetup, name: taskAgentName },
            setUpTaskAgent,
          )
        : sendTaskMessage(message, options),
    [
      assigneeCannotRun,
      sendTaskMessage,
      setUpTaskAgent,
      taskAgentId,
      taskAgentName,
      taskAgentNeedsSetup,
      viewerIsAssignee,
    ],
  );

  const handleDraftChange = useCallback(
    (message: string) => {
      dispatch(
        setConversationWorkspaceDraft({ companyId, message, targetKey }),
      );
    },
    [companyId, dispatch, targetKey],
  );

  const sendingNotice =
    task && !viewerIsAssignee && taskAgent ? (
      <NonAssigneeNotice
        agentName={taskAgent.name}
        assigneeName={task.assignee.name}
      />
    ) : taskAgentNeedsSetup && taskAgent ? (
      <NotBoundNotice
        agentName={taskAgent.name}
        onSetUp={onSetUpAgent ? () => void setUpTaskAgent() : undefined}
      />
    ) : taskAgent?.runtimeAvailable === false ? (
      <RunnerOfflineNotice />
    ) : null;

  const panel = useContextChatPanel({
    ...taskConversation,
    workspaceContext: {
      task: task ?? undefined,
      taskLabel: task ? formatTaskLabel(task, taskPrefix) : undefined,
      agentName: taskAgent?.name ?? task?.assignee.name ?? team.label,
      provider: executorAgent?.provider,
    },
    studioRuntimeId: executorAgent?.runtimeId,
    studioAgentId: executorAgent?.id,
    activeTeam: team,
    allowStructuredActions: canDecide,
    // Questions are decided apart from the composer: a person who may answer
    // but not post still answers, and one who may not reads why on the
    // card. Until the Task's access is read they follow the composer, which
    // offers nothing yet.
    canDecide: accessKnown ? canDecide : undefined,
    // Whenever the composer is read-only — no right to post, access still
    // loading, the Task closed, the chat archived, prerequisites open, the
    // assignee may not run tasks here, or no available Agent — no notice
    // about sending applies: not the setup or runner notice, and not the
    // promise that the Agent reads a non-assignee's message on the next run.
    // The one thing above a read-only message box: when the assignee may not
    // run tasks here and the viewer manages the Project's access, the link
    // that fixes it.
    composerAccessory: (
      <div className="grid gap-2">
        {!composerReadOnly ? (
          sendingNotice
        ) : task && showsCannotRunReason ? (
          <TaskAccessFixLink
            className="justify-self-start text-xs"
            task={task}
          />
        ) : null}
      </div>
    ),
    composerAriaLabel: `Send to ${taskAgent?.name ?? "the task Agent"}`,
    // Anyone but the assignee sends a message that is kept for the
    // assignee's next run (the notice says so), so only the assignee is
    // asked to set the Agent up; everyone else gets the same "Message …"
    // as the Send button beside it.
    composerPlaceholder: hasBlockedDependencies
      ? "Complete prerequisite tasks to continue"
      : personWithoutAgent
        ? "No agent works on this task"
        : lacksAgent
          ? "Assign this task to its agent to continue"
          : viewerIsAssignee && taskAgentNeedsSetup
            ? `Set ${taskAgent?.name ?? "the agent"} up on your computer to start it`
            : `Message ${taskAgent?.name ?? "agent"} about #${task ? formatTaskLabel(task, taskPrefix) : "task"}…`,
    conversationTab: {
      ...tab,
      ...(taskConversation.conversationId
        ? { conversationId: taskConversation.conversationId }
        : {}),
      title: task?.title ?? tab.title,
    },
    dockAware: false,
    draftMessage,
    enabled: true,
    focusRequestKey,
    focusedMessage,
    layout: "workspace",
    onClose: noop,
    onDraftMessageChange: handleDraftChange,
    onExitFocusedHistory,
    onReviewTask: canReviewTask ? () => setIsTaskReviewOpen(true) : undefined,
    readOnly: composerReadOnly,
    // In the server's order: the right to post, the Task's state, then the
    // reason the server gives for its assignee not being able to start it.
    // One line each, six words at most, so it fits the box at 390 px.
    readOnlyPlaceholder: archived
      ? "Chat archived"
      : !accessKnown
        ? "Checking your access…"
        : !canPost
          ? "View only"
          : taskConversation.readOnly
            ? "Task closed"
            : hasBlockedDependencies
              ? "Waiting for prerequisites"
              : personWithoutAgent
                ? "No agent on this task"
                : task && showsCannotRunReason
                  ? unreadyReasonLabel(task, viewerUserId)
                  : "Agent unavailable",
    // Every reader sees the chat's approvals; each card decides whether
    // this viewer may act on it, and says so when they may not.
    showApprovals: accessKnown,
    sendAgentMessage: sendTaskMessageAfterSetup,
  });

  return (
    <>
      {panel}
      {isTaskReviewOpen && canReviewTask && task ? (
        <WorkspaceModalPortal>
          <div
            aria-hidden="true"
            className="fixed inset-0 z-[71] bg-black/35"
          />
          <TaskReviewPanel
            agent={{ avatarSrc: team.iconSrc, title: team.label }}
            agentId={taskExecutor?.id}
            canEditTask={canPost}
            canMerge={canMergeTask}
            companyId={companyId}
            onClose={closeTaskReview}
            onOpenChat={closeTaskReview}
            task={{
              taskId: task.id,
              taskStatus: task.status,
              taskVersion: task.revision,
              title: task.title,
            }}
          />
        </WorkspaceModalPortal>
      ) : null}
    </>
  );
}

function noop() {
  return undefined;
}

function RunnerOfflineNotice() {
  return (
    <div className="rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-[12px] leading-4 text-[var(--workspace-native-text-muted)]">
      Runner offline. You can still send; the prompt will wait in the queue
      until a compatible runner is online.
    </div>
  );
}

// The viewer has not set this Agent up on a computer of their own, so a send
// would be refused with agent_binding_required (ADR 0014). The composer says
// what is missing and offers the setup right here; a send opens the same
// setup first and keeps the draft.
function NotBoundNotice({
  agentName,
  onSetUp,
}: {
  agentName: string;
  onSetUp?: () => void;
}) {
  return (
    <div
      className="rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-[12px] leading-4 text-[var(--workspace-native-text-muted)]"
      role="note"
    >
      {agentName} runs on your own computer and is not set up there yet.{" "}
      {onSetUp ? (
        <button
          className="font-semibold text-[var(--workspace-native-accent)] underline"
          onClick={onSetUp}
          type="button"
        >
          Set up {agentName} on your computer
        </button>
      ) : (
        <Link className="underline" to="/agents">
          Agents
        </Link>
      )}
    </div>
  );
}

// A Task belongs to one member and runs on their computer. Anybody else can
// still write in its chat: the words are saved, and the assignee's next run
// reads them (ADR 0014).
function NonAssigneeNotice({
  agentName,
  assigneeName,
}: {
  agentName: string;
  assigneeName: string;
}) {
  return (
    <div
      className="rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-[12px] leading-4 text-[var(--workspace-native-text-muted)]"
      role="note"
    >
      {agentName} runs on {assigneeName}&apos;s computer. Your message is saved
      here, and {agentName} reads it the next time {assigneeName} runs this
      task.
    </div>
  );
}
