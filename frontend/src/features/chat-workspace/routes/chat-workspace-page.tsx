import { useWorkspaceSidebar } from "@/app/shell/workspace-sidebar-context";
import { usePanelResize } from "@/shared/ui/use-panel-resize";
import { WorkspacePageSidebar } from "@/app/shell/workspace-page-sidebar";
import { DiscussionMemberPresence } from "@/features/discussions/components/discussion-member-presence";
import { NewChatSessionPreview } from "@/features/chat-workspace/components/new-chat-session-preview";
import {
  directConversationTitle,
  initialChatNavigatorBrowse,
  initialChatNavigatorPreferences,
} from "@/features/chat-workspace/model/chat-navigator-browse";
import { useListWorkActiveRunsQuery } from "@/features/company-plan/api/work-next-up-api";
import { useTaskAttention } from "@/features/company-plan/hooks/use-task-attention";
import { useViewerReviewTaskIds } from "@/features/company-plan/hooks/use-project-task-reviews";
import {
  taskChatState,
  taskNeedsAttention,
} from "@/features/company-plan/model/task-work-state";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Bot,
  CircleAlert,
  Menu,
  MessageSquareText,
  RefreshCw,
} from "@/shared/ui/icons";
import { useLocation, useNavigate } from "react-router";

import { openDesktopCommandPalette } from "@/app/desktop/desktop-command-palette";
import { formatTaskLabel } from "@/shared/lib/task-label";
import { useGetWorkspaceCompanyQuery } from "@/shared/api/workspace-api";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import {
  discussionsApi,
  type DiscussionSummary,
} from "@/features/discussions/api/discussions-api";
import { useDiscussionRuntime } from "@/features/discussions/components/discussion-provider";
import {
  useDiscussionNavigation,
  type DiscussionSelection,
} from "@/features/discussions/model/use-discussion-navigation";
import {
  ChatNavigatorOptions,
  ChatNavigatorPageStatus,
} from "@/features/chat-workspace/components/chat-navigator-options";
import { DiscussionPane } from "@/features/discussions/components/discussion-pane";
import { CreateGroupDialog } from "@/features/discussions/components/create-group-dialog";
import { discussionError } from "@/features/discussions/model/discussion-runtime";
import "@/features/discussions/styles/discussions.css";
import { roleLabel, useCompanyPermissions } from "@/shared/permissions";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import {
  useListApprovalsQuery,
  useListQuestionsQuery,
} from "@/features/approvals/api/approvals-api";
import {
  ActiveConversationPane,
  type ChatWorkspaceAgent,
} from "@/features/chat-workspace/components/active-conversation-pane";
import {
  ChatNavigator,
  type ChatNavigatorItem,
} from "@/features/chat-workspace/components/chat-navigator";
import { TaskPeek } from "@/features/chat-workspace/components/task-peek";
import { RenameConversationDialog } from "@/features/chat-workspace/components/rename-conversation-dialog";
import {
  NewDirectMessageDialog,
  type DirectMessageRecipient,
} from "@/features/chat-workspace/components/new-direct-message-dialog";
import { WorkspaceModalPortal } from "@/features/common/components/workspace-modal-portal";
import {
  createTaskPeekNavigationState,
  createChatWorkspaceHref,
  parseChatWorkspaceRoute,
  readComposerDraftNavigationState,
  sameChatConversationTarget,
  taskPeekWasOpenedInApp,
} from "@/features/chat-workspace/model/chat-workspace-route";
import type { TeamNodeData } from "@/features/home/types";
import type {
  AgentDto,
  ApprovalDto,
  DirectConversationListItem,
  QuestionDto,
  TaskDto,
} from "@/shared/api/types";
import { findRootAgent } from "@/shared/api/adapters";
import {
  useGetWorkspaceCockpitQuery,
  useListWorkspaceChatConversationsQuery,
  useListWorkspaceConversationStatesQuery,
  useListWorkspaceTasksQuery,
  useLazyGetWorkspaceConversationStateQuery,
  useUpdateWorkspaceConversationStateMutation,
} from "@/shared/api/workspace-api";
import { chatConversationTargetKey } from "@/shared/chat/conversation-target";
import { CEO_CHAT_TEAM_ID, agentChatTeamId } from "@/shared/chat/chat-team";
import { ChatSessionMemoryScope } from "@/shared/chat/chat-session-memory";
import {
  ChatMemberProfileProvider,
  MemberProfilePanel,
  type ChatMemberProfile,
} from "@/shared/chat/member-profile";
import type { ChatConversationTarget } from "@/shared/chat/types";
import { cx } from "@/shared/lib/cx";
import { agentAccentColor } from "@/shared/ui/agent-avatar/agent-appearance";
import { toast } from "@/shared/ui/sonner";
import {
  activateConversationWorkspaceDiscussion,
  activateConversationWorkspaceTarget,
  rememberConversationWorkspaceTarget,
  rememberConversationWorkspaceTaskChats,
  removeConversationWorkspaceBackTarget,
  selectCompanyConversationWorkspace,
  setConversationWorkspaceNavigatorBrowse,
  setConversationWorkspaceNavigatorPreferences,
  showConversationWorkspaceDirectMessage,
  useAppDispatch,
  useAppSelector,
} from "@/state";

const defaultNavigatorWidth = 270;

export function ChatWorkspacePage({
  sidebarOnly = false,
}: {
  sidebarOnly?: boolean;
}) {
  const workspaceSidebar = useWorkspaceSidebar();
  const dispatch = useAppDispatch();
  const location = useLocation();
  const navigate = useNavigate();
  const session = useSessionSnapshot();
  const { can } = useCompanyPermissions();
  const companyId = session.company?.id ?? "";
  const discussionRuntime = useDiscussionRuntime();
  const peopleQuery = useListActiveCompanyMembersQuery(companyId, {
    skip: !companyId,
  });
  const [createHumanDiscussion] =
    discussionsApi.useCreateHumanDiscussionMutation();
  const openingHuman = useRef(false);
  // The Agent roster and cockpit are agents.manage reads; structured plan
  // actions need company.plan.apply. Approvals are listed to whoever the
  // server shows them to: a Task chat's are its Project's readers' (ADR
  // 0017), and each card says whether this viewer may decide it.
  // Every active member reads the Agent roster (ADR 0013); agents.manage is
  // for configuring Agents and the owner cockpit, not for seeing them.
  const hasAgentRoster = Boolean(companyId);
  const canManageAgents = can("agents.manage");
  const canApplyPlan = can("company.plan.apply");
  const companyQuery = useGetWorkspaceCompanyQuery(companyId, {
    skip: !companyId,
  });
  const taskPrefix = companyQuery.currentData?.taskPrefix;
  // Before the first Company Plan applies there is nothing else to open, so
  // the founder belongs in the CEO conversation. The stamp is server-owned
  // and written once, which makes it durable across reloads.
  const awaitingFirstPlan =
    companyQuery.currentData?.firstPlanAppliedAt === null;
  const route = useMemo(
    () => parseChatWorkspaceRoute(sidebarOnly ? "" : location.search),
    [location.search, sidebarOnly],
  );
  const activeDiscussionIdRef = useRef(route.discussion?.conversationId);
  activeDiscussionIdRef.current = route.discussion?.conversationId;
  const discussionSelection = useMemo<DiscussionSelection | undefined>(
    () =>
      route.discussion
        ? {
            conversationId: route.discussion.conversationId,
            type: route.discussion.discussionType,
            manage: route.discussion.manage,
            messageId: route.discussion.messageId,
            threadRootId: route.discussion.threadRootId,
          }
        : undefined,
    [route.discussion],
  );
  const newSessionRequested =
    !sidebarOnly &&
    new URLSearchParams(location.search).get("newSession") === "1";
  const activeTaskRunsQuery = useListWorkActiveRunsQuery(companyId, {
    skip: !companyId,
  });
  const runningTaskIds = useMemo(
    () =>
      new Set(
        (activeTaskRunsQuery.currentData ?? [])
          .filter((run) => run.status === "running")
          .flatMap((run) => (run.taskId ? [run.taskId] : [])),
      ),
    [activeTaskRunsQuery.currentData],
  );
  const pendingTaskWorkspaceRedirect = useMemo(() => {
    const search = new URLSearchParams(location.search);
    return (
      search.get("desktop_search") === "1" &&
      Boolean(search.get("taskId")?.trim())
    );
  }, [location.search]);
  const workspaceState = useAppSelector((state) =>
    selectCompanyConversationWorkspace(state, companyId),
  );
  const navigatorBrowse =
    workspaceState.navigatorBrowse ?? initialChatNavigatorBrowse;
  const navigatorPreferences =
    workspaceState.navigatorPreferences ?? initialChatNavigatorPreferences;
  const [focusRequestKey, setFocusRequestKey] = useState(0);
  const [mobileNavigatorOpen, setMobileNavigatorOpen] = useState(false);
  const [newDirectMessageOpen, setNewDirectMessageOpen] = useState(false);
  const [createGroupOpen, setCreateGroupOpen] = useState(false);
  const [renamingConversation, setRenamingConversation] = useState<{
    conversation: DirectConversationListItem;
    title: string;
    returnFocus: HTMLButtonElement;
    fallbackFocus: HTMLElement | null;
  } | null>(null);
  const [updatingTargetKey, setUpdatingTargetKey] = useState<string>();
  const stateMutationInFlightRef = useRef(false);
  const [profileSelection, setProfileSelection] = useState<{
    companyId: string;
    profile: ChatMemberProfile;
  } | null>(null);
  const profileReturnFocusRef = useRef<HTMLElement | null>(null);
  const profileHandoffRef = useRef(false);
  const [isMobileViewport, setIsMobileViewport] = useState(
    () => window.matchMedia("(max-width: 899px)").matches,
  );
  const navigatorCollapsed = workspaceSidebar.collapsed;
  const discussionNavigation = useDiscussionNavigation(
    navigatorBrowse.query,
    navigatorBrowse.view === "archived",
  );
  const chatLayoutRef = useRef<HTMLElement | null>(null);
  const desktopNavigatorRef = useRef<HTMLDivElement | null>(null);
  const navigatorResize = usePanelResize(desktopNavigatorRef, {
    side: "left",
    containerRef: chatLayoutRef,
    visible:
      Boolean(companyId) &&
      !sidebarOnly &&
      !isMobileViewport &&
      !navigatorCollapsed,
    defaultWidth: defaultNavigatorWidth,
    minWidth: 220,
    remainingWidth: 400,
    widthLimit: 480,
    label: "Resize conversation list",
  });
  const mobileNavigatorRef = useRef<HTMLDivElement | null>(null);
  const mobileMenuButtonRef = useRef<HTMLButtonElement | null>(null);
  const peekReturnFocusRef = useRef<HTMLElement | null>(null);
  const unavailableRouteRef = useRef<string | null>(null);
  const openDiscussion = useCallback(
    (selection: DiscussionSelection) => {
      setProfileSelection(null);
      setMobileNavigatorOpen(false);
      navigate(
        createChatWorkspaceHref({
          kind: "discussion",
          conversationId: selection.conversationId,
          discussionType: selection.type,
          manage: selection.manage,
          messageId: selection.messageId,
          threadRootId: selection.threadRootId,
        }),
      );
    },
    [navigate],
  );
  const openHumanDiscussion = useCallback(
    async (peerUserId: string | string[], agentIds: string[] = []) => {
      if (openingHuman.current) return false;
      openingHuman.current = true;
      const peerUserIds = Array.isArray(peerUserId) ? peerUserId : [peerUserId];
      const request = createHumanDiscussion({
        ...discussionRuntime.session,
        body: {
          ...(peerUserIds.length ? { peerUserIds } : {}),
          ...(agentIds.length ? { agentIds } : {}),
        },
      });
      try {
        const result = await request.unwrap();
        if (discussionRuntime.active)
          openDiscussion({
            conversationId: result.conversation.id,
            type: "human_direct",
          });
        return discussionRuntime.active;
      } catch (error) {
        if (discussionRuntime.active) toast.error(discussionError(error));
        return false;
      } finally {
        openingHuman.current = false;
        request.reset();
      }
    },
    [createHumanDiscussion, discussionRuntime, openDiscussion],
  );
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId || !hasAgentRoster,
  });
  const statesQuery = useListWorkspaceConversationStatesQuery(companyId, {
    skip: !companyId,
  });
  const directConversationsQuery = useListWorkspaceChatConversationsQuery(
    companyId,
    { skip: !companyId },
  );
  const tasksQuery = useListWorkspaceTasksQuery(
    { companyId },
    { skip: !companyId },
  );
  const questionsQuery = useListQuestionsQuery(
    { companyId, allPages: true, status: ["pending"] },
    { skip: !companyId },
  );
  const approvalsQuery = useListApprovalsQuery(
    { companyId, allPages: true, status: ["pending"] },
    { skip: !companyId },
  );
  // What this viewer may answer (ADR 0017): only these rows say "Needs
  // input" and rise to the top; the lists above keep every waiting chat
  // live.
  const respondRequests = useTaskAttention(companyId);
  const respondTaskIds = respondRequests.taskIds;
  const retryRespondRequests = respondRequests.retry;
  // The Task chats whose review waits on this viewer: only their rows say
  // "Needs review"; everyone else's read "In review".
  const reviewTaskIds = useViewerReviewTaskIds(
    companyId,
    tasksQuery.currentData?.items,
  ).taskIds;
  const cockpitQuery = useGetWorkspaceCockpitQuery(companyId, {
    refetchOnMountOrArgChange: 30,
    skip: !companyId || !canManageAgents,
  });
  const [getConversationState] = useLazyGetWorkspaceConversationStateQuery();
  const [updateConversationState] =
    useUpdateWorkspaceConversationStateMutation();
  const ownerAgents = agentsQuery.currentData?.items ?? [];
  const tasks = tasksQuery.currentData?.items ?? [];
  const directConversations = directConversationsQuery.currentData?.items ?? [];
  const agents = useMemo(
    () =>
      createChatWorkspaceAgents({
        directConversations,
        ownerAgents,
        tasks,
      }),
    [directConversations, ownerAgents, tasks],
  );
  const agentById = useMemo(
    () => new Map(agents.map((agent) => [agent.id, agent] as const)),
    [agents],
  );
  // One setup dialog for every chat surface here: the new-chat screen, a
  // direct chat's composer, a task chat's composer (ADR 0014). It answers
  // whether the viewer finished it, so a waiting send can go on.
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  const setUpAgent = useCallback(
    (agentId: string) =>
      new Promise<boolean>((resolve) => {
        const agent = ownerAgents.find((candidate) => candidate.id === agentId);
        // An archived Agent cannot be set up: the bind is refused.
        if (!agent || agent.archivedAt) {
          resolve(false);
          return;
        }
        openBindDialog(agent, {
          onBound: () => resolve(true),
          onDismiss: () => resolve(false),
        });
      }),
    [openBindDialog, ownerAgents],
  );
  const taskById = useMemo(
    () => new Map(tasks.map((task) => [task.id, task] as const)),
    [tasks],
  );
  const availableAgentIds = useMemo(
    () =>
      hasAgentRoster && agentsQuery.currentData
        ? new Set(
            ownerAgents
              .filter((agent) => !agent.archivedAt)
              .map((agent) => agent.id),
          )
        : undefined,
    [agentsQuery.currentData, hasAgentRoster, ownerAgents],
  );
  const navigatorAgents = useMemo(
    () =>
      agents.map((agent) => ({
        id: agent.id,
        name: agent.name,
        title: agent.title,
        archived: Boolean(agent.archivedAt),
        canStartChat:
          !agent.archivedAt &&
          (!hasAgentRoster || Boolean(availableAgentIds?.has(agent.id))),
      })),
    [agents, availableAgentIds, hasAgentRoster],
  );
  const ceoAgent = useMemo(() => {
    const rootAgentId = findRootAgent(ownerAgents)?.id;
    return (
      (rootAgentId ? agentById.get(rootAgentId) : undefined) ??
      findCeoPresentation(agents)
    );
  }, [agentById, agents, ownerAgents]);
  const directConversationByAgentId = useMemo(() => {
    const conversations = new Map<string, DirectConversationListItem>();
    directConversations.forEach((conversation) => {
      if (conversation.agent && !conversation.archivedAt) {
        conversations.set(conversation.agent.id, conversation);
      }
    });
    return conversations;
  }, [directConversations]);
  const teamNodes = useMemo(
    () =>
      agents.map((agent) => agentTeamNode(agent, agent.id === ceoAgent?.id)),
    [agents, ceoAgent?.id],
  );
  const cataloguesLoaded =
    directConversationsQuery.currentData !== undefined &&
    tasksQuery.currentData !== undefined &&
    (!hasAgentRoster || agentsQuery.currentData !== undefined) &&
    (!canManageAgents ||
      cockpitQuery.currentData !== undefined ||
      cockpitQuery.isError);
  const catalogueUnavailable =
    (directConversationsQuery.isError &&
      directConversationsQuery.currentData === undefined) ||
    (tasksQuery.isError && tasksQuery.currentData === undefined) ||
    (hasAgentRoster &&
      agentsQuery.isError &&
      agentsQuery.currentData === undefined);
  const awaitingConversationStates =
    statesQuery.isFetching && statesQuery.currentData === undefined;
  const hasWorkspaceDataWarning =
    activeTaskRunsQuery.isError ||
    directConversationsQuery.isError ||
    tasksQuery.isError ||
    questionsQuery.isError ||
    (hasAgentRoster && agentsQuery.isError) ||
    approvalsQuery.isError ||
    respondRequests.isError;
  const routeTargetIsAvailable = Boolean(
    route.target &&
    targetExists(route.target, taskById, directConversations, agentById),
  );
  const fallbackTarget = useMemo(() => {
    if (!cataloguesLoaded || awaitingConversationStates) return undefined;
    if (awaitingFirstPlan && ceoAgent) {
      return directTargetForAgent(
        ceoAgent,
        directConversationByAgentId.get(ceoAgent.id)?.id,
      );
    }
    const persistedTarget = workspaceState.activeTarget;
    if (
      persistedTarget &&
      !statesQuery.currentData?.items.some(
        (state) =>
          state.conversationId === persistedTarget.conversationId &&
          state.archivedAt,
      ) &&
      targetExists(persistedTarget, taskById, directConversations, agentById)
    ) {
      return persistedTarget;
    }
    if (ceoAgent) {
      return directTargetForAgent(
        ceoAgent,
        directConversationByAgentId.get(ceoAgent.id)?.id,
      );
    }
    const latestDirect = [...directConversations]
      .filter((conversation) => conversation.agent && !conversation.archivedAt)
      .sort((left, right) =>
        right.summary.updatedAt.localeCompare(left.summary.updatedAt),
      )[0];
    if (latestDirect?.agent) {
      return directTargetForAgent(latestDirect.agent, latestDirect.id);
    }
    const firstTask = tasks.find(
      (task) =>
        !statesQuery.currentData?.items.some(
          (state) =>
            state.conversationId === task.conversationId && state.archivedAt,
        ),
    );
    return firstTask ? targetForTask(firstTask) : undefined;
  }, [
    agentById,
    awaitingConversationStates,
    awaitingFirstPlan,
    cataloguesLoaded,
    ceoAgent,
    directConversationByAgentId,
    directConversations,
    taskById,
    tasks,
    workspaceState.activeTarget,
    statesQuery.currentData,
  ]);
  // Before the catalogues load, a task chat opens only once its Task is
  // known to be readable, so a link to one the viewer can't see never
  // mounts the pane or asks the server for its workspace.
  const routeTargetKnown =
    route.target?.kind !== "task" || taskById.has(route.target.taskId);
  const activeTarget =
    pendingTaskWorkspaceRedirect || discussionSelection
      ? undefined
      : route.target &&
          (cataloguesLoaded ? routeTargetIsAvailable : routeTargetKnown)
        ? route.target
        : fallbackTarget;
  const activeTask =
    activeTarget?.kind === "task"
      ? taskById.get(activeTarget.taskId)
      : undefined;
  const activeAgent = activeTarget
    ? resolveTargetAgent(activeTarget, activeTask, agentById)
    : undefined;
  const matchedActiveTeam = activeAgent
    ? teamNodes.find((team) => team.agentId === activeAgent.id)
    : undefined;
  const neutralAgentName =
    activeAgent?.name ??
    (activeTarget?.kind === "task" && activeTask?.execution.agent
      ? activeTask.execution.agent.name
      : activeTarget?.kind === "task"
        ? "Task Agent"
        : undefined);
  const neutralAgentRole = activeAgent?.title ?? "Agent conversation";
  const activeTeam =
    matchedActiveTeam ??
    (neutralAgentName
      ? {
          agentId: activeAgent?.id,
          agentRole: neutralAgentRole,
          available: activeAgent?.runtimeAvailable ?? true,
          currentWork: "Conversation",
          href: "/app",
          iconSrc: activeAgent?.avatarSrc ?? "",
          label: neutralAgentName,
          notificationCount: 0,
          status: "idle" as const,
          teamId: activeAgent?.id ?? activeTarget?.agentId ?? "agent",
        }
      : undefined) ??
    teamNodes.find((team) => team.teamId === CEO_CHAT_TEAM_ID) ??
    teamNodes[0];

  useEffect(() => {
    if (
      sidebarOnly ||
      !cataloguesLoaded ||
      awaitingConversationStates ||
      pendingTaskWorkspaceRedirect ||
      route.discussion
    )
      return;
    if (route.target && !routeTargetIsAvailable) {
      const unavailableRoute = `${companyId}:${location.key}:${location.search}`;
      if (unavailableRouteRef.current === unavailableRoute) return;
      unavailableRouteRef.current = unavailableRoute;
      toast.info(
        fallbackTarget
          ? "You can't open that chat. Opened your latest chat instead."
          : "You can't open that chat.",
      );
      navigate(
        fallbackTarget
          ? createChatWorkspaceHref(fallbackTarget, {
              ...(route.peekTaskId ? { peekTaskId: route.peekTaskId } : {}),
            })
          : "/app",
        { replace: true, state: location.state },
      );
      return;
    }
    if (!route.target && fallbackTarget) {
      navigate(
        createChatWorkspaceHref(fallbackTarget, {
          ...(route.peekTaskId ? { peekTaskId: route.peekTaskId } : {}),
        }),
        { replace: true, state: location.state },
      );
    }
  }, [
    awaitingConversationStates,
    cataloguesLoaded,
    companyId,
    fallbackTarget,
    location.key,
    location.search,
    navigate,
    route.peekTaskId,
    route.target,
    route.discussion,
    routeTargetIsAvailable,
    location.state,
    pendingTaskWorkspaceRedirect,
    sidebarOnly,
  ]);

  useEffect(() => {
    if (!companyId || !route.discussion) return;
    dispatch(
      activateConversationWorkspaceDiscussion({
        companyId,
        target: route.discussion,
      }),
    );
  }, [companyId, dispatch, route.discussion]);

  useEffect(() => {
    // Let the URL canonicalization above settle before persisting the target.
    // Dispatching a fallback target while /app still has no target feeds the
    // newly-created Redux object back into fallbackTarget and can loop before
    // React Router commits the replace navigation.
    if (
      !route.target ||
      !routeTargetIsAvailable ||
      !activeTarget ||
      !companyId ||
      !sameChatConversationTarget(route.target, activeTarget)
    ) {
      return;
    }
    dispatch(
      activateConversationWorkspaceTarget({ companyId, target: activeTarget }),
    );
  }, [activeTarget, companyId, dispatch, route.target, routeTargetIsAvailable]);

  useEffect(() => {
    const query = window.matchMedia("(max-width: 899px)");
    const handleChange = () => {
      setIsMobileViewport(query.matches);
      if (!query.matches) setMobileNavigatorOpen(false);
    };
    query.addEventListener("change", handleChange);
    return () => query.removeEventListener("change", handleChange);
  }, []);

  const openTarget = useCallback(
    (
      target: ChatConversationTarget,
      options: { focusComposer?: boolean; preservePeek?: boolean } = {},
    ) => {
      setProfileSelection(null);
      if (target.kind === "direct") {
        dispatch(
          showConversationWorkspaceDirectMessage({
            companyId,
            targetKey: chatConversationTargetKey(target),
          }),
        );
      }
      navigate(
        createChatWorkspaceHref(target, {
          ...(options.preservePeek && route.peekTaskId
            ? { peekTaskId: route.peekTaskId }
            : {}),
        }),
        { replace: !sidebarOnly, state: location.state },
      );
      setMobileNavigatorOpen(false);
      if (options.focusComposer) {
        setFocusRequestKey((key) => key + 1);
      }
    },
    [
      companyId,
      dispatch,
      location.state,
      navigate,
      route.peekTaskId,
      sidebarOnly,
    ],
  );

  const handleConversationResolved = useCallback(
    (target: ChatConversationTarget) => {
      // A mounted pane can still hold its previous ID while the fallback
      // resolver refreshes the catalogue. Do not put that ID back in the URL.
      if (
        target.kind === "direct" &&
        directConversationByAgentId.get(target.agentId)?.id !==
          target.conversationId
      ) {
        return;
      }
      dispatch(rememberConversationWorkspaceTarget({ companyId, target }));
      if (
        !sameChatConversationTarget(route.target, target) ||
        route.target?.conversationId === target.conversationId
      ) {
        return;
      }
      const focusedMessage =
        route.focusedMessage?.conversationId === target.conversationId
          ? route.focusedMessage
          : undefined;
      navigate(
        createChatWorkspaceHref(target, {
          ...(route.artifact ? { artifact: route.artifact } : {}),
          ...(route.runId ? { runId: route.runId } : {}),
          ...(route.reviewArtifacts ? { reviewArtifacts: true } : {}),
          ...(focusedMessage ? { focusedMessage } : {}),
          ...(route.peekTaskId ? { peekTaskId: route.peekTaskId } : {}),
        }),
        { replace: true, state: location.state },
      );
    },
    [
      companyId,
      directConversationByAgentId,
      dispatch,
      location.state,
      navigate,
      route,
    ],
  );

  const handleExitFocusedHistory = useCallback(() => {
    if (!activeTarget) return;
    navigate(
      createChatWorkspaceHref(activeTarget, {
        ...(route.artifact ? { artifact: route.artifact } : {}),
        ...(route.reviewArtifacts ? { reviewArtifacts: true } : {}),
        ...(route.peekTaskId ? { peekTaskId: route.peekTaskId } : {}),
      }),
      { replace: true, state: location.state },
    );
  }, [activeTarget, location.state, navigate, route]);

  const handleBack = useCallback(() => {
    const previousTarget = workspaceState.backStack.at(-1);
    if (!previousTarget) return;
    dispatch(
      removeConversationWorkspaceBackTarget({
        companyId,
        targetKey: chatConversationTargetKey(previousTarget),
      }),
    );
    dispatch(
      activateConversationWorkspaceTarget({
        companyId,
        recordHistory: false,
        target: previousTarget,
      }),
    );
    navigate(createChatWorkspaceHref(previousTarget), { replace: true });
  }, [companyId, dispatch, navigate, workspaceState.backStack]);

  useEffect(() => {
    if (sidebarOnly) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        openDesktopCommandPalette();
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key === "[") {
        if (!workspaceState.backStack.length) return;
        event.preventDefault();
        handleBack();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [handleBack, workspaceState.backStack.length, sidebarOnly]);

  useEffect(() => {
    if (!mobileNavigatorOpen) return undefined;
    const dialog = mobileNavigatorRef.current;
    const returnFocusTo = mobileMenuButtonRef.current;
    const frameId = window.requestAnimationFrame(() => {
      dialog?.querySelector<HTMLElement>("button")?.focus();
    });
    const handleMobileNavigatorKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      if (event.key === "Escape") {
        event.preventDefault();
        setMobileNavigatorOpen(false);
        return;
      }
      if (event.key !== "Tab" || !dialog) return;
      const focusable = Array.from(
        dialog.querySelectorAll<HTMLElement>(
          'button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
        ),
      );
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) {
        event.preventDefault();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", handleMobileNavigatorKeyDown);
    return () => {
      window.cancelAnimationFrame(frameId);
      window.removeEventListener("keydown", handleMobileNavigatorKeyDown);
      if (returnFocusTo?.isConnected) returnFocusTo.focus();
    };
  }, [mobileNavigatorOpen]);

  const navigatorModel = useMemo(
    () =>
      createNavigatorModel({
        archivedConversationIds: new Set(
          (statesQuery.currentData?.items ?? [])
            .filter((state) => state.archivedAt)
            .map((state) => state.conversationId),
        ),
        pinnedTaskConversationIds: new Set(
          (statesQuery.currentData?.items ?? [])
            .filter((state) => state.pinnedAt)
            .map((state) => state.conversationId),
        ),
        taskPrefix,
        agents,
        approvals: approvalsQuery.currentData?.items ?? [],
        ceoAgent,
        directConversations,
        pendingDirectAgentId:
          activeTarget?.kind === "direct" && !activeTarget.conversationId
            ? activeTarget.agentId
            : undefined,
        questions: questionsQuery.currentData?.items ?? [],
        recentTargets: workspaceState.recentTargets,
        respondTaskIds,
        reviewTaskIds,
        surfacedTaskIds: workspaceState.surfacedTaskIds ?? [],
        visibleDirectMessageKeys: workspaceState.visibleDirectMessageKeys ?? [],
        tasks,
        runningTaskIds,
      }),
    [
      taskPrefix,
      statesQuery.currentData,
      runningTaskIds,
      activeTarget,
      agents,
      approvalsQuery.currentData?.items,
      ceoAgent,
      directConversations,
      questionsQuery.currentData?.items,
      respondTaskIds,
      reviewTaskIds,
      tasks,
      workspaceState.recentTargets,
      workspaceState.surfacedTaskIds,
      workspaceState.visibleDirectMessageKeys,
    ],
  );

  useEffect(() => {
    const surfaced = new Set(workspaceState.surfacedTaskIds ?? []);
    const taskIds = navigatorModel.taskChats
      .flatMap((item) =>
        item.taskState?.active && item.target.kind === "task"
          ? [item.target.taskId]
          : [],
      )
      .slice(0, 100);
    if (taskIds.some((taskId) => !surfaced.has(taskId))) {
      dispatch(rememberConversationWorkspaceTaskChats({ companyId, taskIds }));
    }
  }, [
    companyId,
    dispatch,
    navigatorModel.taskChats,
    workspaceState.surfacedTaskIds,
  ]);

  const handlePeekTask = useCallback(
    (taskId: string, trigger: HTMLElement) => {
      if (!activeTarget) return;
      setProfileSelection(null);
      profileHandoffRef.current = false;
      peekReturnFocusRef.current = trigger;
      navigate(
        createChatWorkspaceHref(activeTarget, {
          ...(route.artifact ? { artifact: route.artifact } : {}),
          ...(route.reviewArtifacts ? { reviewArtifacts: true } : {}),
          ...(route.focusedMessage
            ? { focusedMessage: route.focusedMessage }
            : {}),
          peekTaskId: taskId,
        }),
        { state: createTaskPeekNavigationState() },
      );
    },
    [activeTarget, navigate, route],
  );
  const handleClosePeek = useCallback(() => {
    if (!activeTarget) return;
    if (taskPeekWasOpenedInApp(location.state)) {
      navigate(-1);
      return;
    }
    navigate(
      createChatWorkspaceHref(activeTarget, {
        ...(route.artifact ? { artifact: route.artifact } : {}),
        ...(route.reviewArtifacts ? { reviewArtifacts: true } : {}),
        ...(route.focusedMessage
          ? { focusedMessage: route.focusedMessage }
          : {}),
      }),
      { replace: true },
    );
  }, [activeTarget, location.state, navigate, route]);
  const handleOpenTaskChatFromPeek = useCallback(
    (task: { agentId?: string; conversationId: string; taskId: string }) => {
      peekReturnFocusRef.current = null;
      openTarget({
        ...(task.agentId ? { agentId: task.agentId } : {}),
        conversationId: task.conversationId,
        kind: "task",
        taskId: task.taskId,
      });
    },
    [openTarget],
  );
  const handleOpenStructuredTask = useCallback(
    (taskId: string) => {
      peekReturnFocusRef.current = null;
      navigate(`/tasks?${new URLSearchParams({ taskId }).toString()}`);
    },
    [navigate],
  );
  const handleToggleConversationPin = useCallback(
    async (item: ChatNavigatorItem) => {
      if (stateMutationInFlightRef.current || !item.target.conversationId)
        return false;
      const conversationId = item.target.conversationId;
      const targetKey = chatConversationTargetKey(item.target);
      const pinned = !item.pinned;
      stateMutationInFlightRef.current = true;
      setUpdatingTargetKey(targetKey);
      try {
        if (item.target.kind === "discussion") {
          return await discussionNavigation.togglePin(conversationId, pinned);
        }
        const currentState = await getConversationState(
          conversationId,
          false,
        ).unwrap();
        await updateConversationState({
          companyId,
          conversationId,
          request: { expectedRevision: currentState.revision, pinned },
        }).unwrap();
        if (item.target.kind !== "task") {
          dispatch(
            showConversationWorkspaceDirectMessage({ companyId, targetKey }),
          );
        } else {
          dispatch(
            rememberConversationWorkspaceTaskChats({
              companyId,
              taskIds: [item.target.taskId],
            }),
          );
        }
        return true;
      } catch {
        toast.error(
          pinned
            ? "Couldn’t pin this conversation. Please try again."
            : "Couldn’t unpin this conversation. Please try again.",
        );
        return false;
      } finally {
        stateMutationInFlightRef.current = false;
        setUpdatingTargetKey(undefined);
      }
    },
    [
      companyId,
      dispatch,
      getConversationState,
      updateConversationState,
      discussionNavigation,
    ],
  );
  const handleToggleConversationArchive = useCallback(
    async (item: ChatNavigatorItem) => {
      if (
        stateMutationInFlightRef.current ||
        !item.target.conversationId ||
        (item.target.kind === "discussion" &&
          item.target.discussionType !== "human_direct")
      )
        return false;
      stateMutationInFlightRef.current = true;
      setUpdatingTargetKey(chatConversationTargetKey(item.target));
      try {
        if (item.target.kind === "discussion") {
          const saved = await discussionNavigation.toggleArchive(
            item.target.conversationId,
            !item.archived,
          );
          if (!saved) return false;
          if (
            !item.archived &&
            activeDiscussionIdRef.current === item.target.conversationId
          )
            navigate("/app", { replace: true });
        } else {
          const currentState = await getConversationState(
            item.target.conversationId,
            false,
          ).unwrap();
          await updateConversationState({
            companyId,
            conversationId: item.target.conversationId,
            request: {
              expectedRevision: currentState.revision,
              archived: !item.archived,
            },
          }).unwrap();
        }
        toast.success(
          item.archived ? "Conversation restored" : "Conversation archived",
        );
        return true;
      } catch {
        toast.error(
          item.archived
            ? "Couldn’t restore this conversation. Please try again."
            : "Couldn’t archive this conversation. Please try again.",
        );
        return false;
      } finally {
        stateMutationInFlightRef.current = false;
        setUpdatingTargetKey(undefined);
      }
    },
    [
      companyId,
      getConversationState,
      updateConversationState,
      discussionNavigation,
      navigate,
    ],
  );

  const directMessageRecipients = useMemo<DirectMessageRecipient[]>(
    () => [
      ...(peopleQuery.currentData?.items ?? [])
        .filter(
          (person) =>
            !person.accountDisabled && person.actor.id !== session.userId,
        )
        .map<DirectMessageRecipient>((person) => ({
          id: person.actor.id,
          name: person.actor.name,
          kind: "human",
          description: person.email,
          avatarSrc: person.actor.avatarUrl ?? undefined,
        })),
      ...agents
        .filter(
          (agent) =>
            !agent.archivedAt &&
            (!hasAgentRoster || availableAgentIds?.has(agent.id)),
        )
        .map<DirectMessageRecipient>((agent) => ({
          id: agent.id,
          kind: "agent",
          name: agent.name,
          description: agent.title,
          avatarSrc: agent.avatarSrc ?? undefined,
        }))
        .sort((left, right) => left.name.localeCompare(right.name)),
    ],
    [
      agents,
      availableAgentIds,
      hasAgentRoster,
      peopleQuery.currentData,
      session.userId,
    ],
  );
  const handleOpenDirectMessage = useCallback(
    (agentId: string) => {
      const agent = agentById.get(agentId);
      if (
        !agent ||
        agent.archivedAt ||
        (hasAgentRoster && !availableAgentIds?.has(agentId))
      )
        return;
      setMobileNavigatorOpen(false);
      setNewDirectMessageOpen(false);
      const browse =
        workspaceState.navigatorBrowse ?? initialChatNavigatorBrowse;
      dispatch(
        setConversationWorkspaceNavigatorBrowse({
          companyId,
          browse: {
            view: browse.view,
            agentId: browse.view === "agents" ? agentId : null,
            query: "",
          },
        }),
      );
      const target = directTargetForAgent(
        agent,
        directConversationByAgentId.get(agent.id)?.id,
      );
      navigate(`${createChatWorkspaceHref(target)}&newSession=1`);
    },
    [
      agentById,
      availableAgentIds,
      companyId,
      directConversationByAgentId,
      dispatch,
      hasAgentRoster,
      navigate,
      workspaceState.navigatorBrowse,
    ],
  );
  const resolveMemberProfile = useCallback(
    (profile: ChatMemberProfile): ChatMemberProfile => {
      if (profile.kind === "agent") {
        const agent = agentById.get(profile.id);
        if (!agent) return profile;
        return {
          ...profile,
          name: agent.name,
          title: agent.title,
          avatarSrc: agent.avatarSrc || profile.avatarSrc,
          runtimeAvailable: agent.runtimeAvailable,
          archived: Boolean(agent.archivedAt),
        };
      }
      if (profile.id === session.userId) {
        return {
          ...profile,
          name: session.displayName ?? profile.name,
          title: `Company ${roleLabel(session.company?.role).toLowerCase()}`,
          avatarSrc: session.avatarUrl ?? null,
          email: session.email,
          isCurrentUser: true,
        };
      }
      const person = peopleQuery.currentData?.items.find(
        (member) => member.actor.id === profile.id,
      );
      if (person)
        return {
          ...profile,
          name: person.actor.name,
          avatarSrc: person.actor.avatarUrl,
          email: person.email,
          archived: person.accountDisabled,
        };
      return profile;
    },
    [
      agentById,
      session.userId,
      session.displayName,
      session.avatarUrl,
      session.email,
      session.company?.role,
      peopleQuery.currentData,
    ],
  );
  const canMessageProfile = useCallback(
    (profile: ChatMemberProfile) => {
      if (profile.kind === "human")
        return (
          profile.id !== session.userId &&
          !profile.archived &&
          Boolean(
            peopleQuery.currentData?.items.some(
              (person) =>
                person.actor.id === profile.id && !person.accountDisabled,
            ),
          )
        );
      const agent =
        profile.kind === "agent" ? agentById.get(profile.id) : undefined;
      return Boolean(
        agent &&
        !agent.archivedAt &&
        (!hasAgentRoster || availableAgentIds?.has(agent.id)),
      );
    },
    [
      agentById,
      availableAgentIds,
      hasAgentRoster,
      peopleQuery.currentData,
      session.userId,
    ],
  );
  const handleOpenProfile = useCallback(
    (profile: ChatMemberProfile, trigger: HTMLElement) => {
      profileReturnFocusRef.current = trigger;
      profileHandoffRef.current = true;
      setMobileNavigatorOpen(false);
      setProfileSelection({ companyId, profile });
    },
    [companyId],
  );
  const handleCloseProfile = useCallback(() => {
    setProfileSelection(null);
    profileHandoffRef.current = false;
  }, []);
  const handleMessageProfile = useCallback(
    (profile: ChatMemberProfile) => {
      if (!canMessageProfile(profile)) return;
      setProfileSelection(null);
      if (profile.kind === "human") void openHumanDiscussion(profile.id);
      else handleOpenDirectMessage(profile.id);
    },
    [canMessageProfile, handleOpenDirectMessage, openHumanDiscussion],
  );
  // Every member reads every Agent (ADR 0013); the profile page itself
  // renders read-only for viewers who may not configure it.
  const canViewAgentProfile = useCallback(
    (profile: ChatMemberProfile) =>
      profile.kind === "agent" && !profile.archived,
    [],
  );
  const handleViewAgentProfile = useCallback(
    (profile: ChatMemberProfile) => {
      if (!canViewAgentProfile(profile)) return;
      profileHandoffRef.current = true;
      navigate(`/agents/${encodeURIComponent(profile.id)}`);
    },
    [canViewAgentProfile, navigate],
  );
  const activeProfile =
    profileSelection?.companyId === companyId
      ? resolveMemberProfile(profileSelection.profile)
      : null;
  const profileContext = useMemo(
    () => ({
      canMessage: canMessageProfile,
      canViewProfile: canViewAgentProfile,
      onMessage: handleMessageProfile,
      onOpenProfile: handleOpenProfile,
      onViewProfile: handleViewAgentProfile,
      resolveProfile: resolveMemberProfile,
      renderPresence: (profile: ChatMemberProfile) =>
        profile.kind === "human" && !profile.archived ? (
          <DiscussionMemberPresence userId={profile.id} />
        ) : null,
    }),
    [
      canMessageProfile,
      canViewAgentProfile,
      handleMessageProfile,
      handleOpenProfile,
      handleViewAgentProfile,
      resolveMemberProfile,
    ],
  );
  const activeProfileConversationKey = activeTarget
    ? chatConversationTargetKey(activeTarget)
    : route.discussion
      ? chatConversationTargetKey(route.discussion)
      : "";
  useEffect(() => {
    setProfileSelection(null);
  }, [companyId, activeProfileConversationKey]);

  const handleRetryWorkspaceData = useCallback(() => {
    void peopleQuery.refetch();
    void activeTaskRunsQuery.refetch();
    void directConversationsQuery.refetch();
    void statesQuery.refetch();
    void tasksQuery.refetch();
    void questionsQuery.refetch();
    if (hasAgentRoster) {
      void agentsQuery.refetch();
    }
    void approvalsQuery.refetch();
    retryRespondRequests();
  }, [
    activeTaskRunsQuery,
    agentsQuery,
    approvalsQuery,
    retryRespondRequests,
    directConversationsQuery,
    statesQuery,
    hasAgentRoster,
    questionsQuery,
    tasksQuery,
    peopleQuery,
  ]);
  const discussionItems = [
    ...discussionNavigation.dms,
    ...discussionNavigation.channels,
  ].map(createDiscussionNavigatorItem);
  const navigatorPinned = [
    ...navigatorModel.pinned,
    ...discussionItems.filter((item) => item.pinned && !item.archived),
  ];
  const navigatorArchivedChats = [
    ...navigatorModel.archivedChats,
    ...discussionNavigation.archivedDms.map(createDiscussionNavigatorItem),
  ].sort(byRecentActivity);
  const navigatorDirectMessages = [
    ...navigatorModel.directMessages,
    ...discussionItems.filter(
      (item) =>
        item.target.kind === "discussion" &&
        item.target.discussionType === "human_direct" &&
        (!item.pinned || item.archived),
    ),
  ].sort(byRecentActivity);
  const navigatorGroups: ChatNavigatorItem[] = discussionNavigation.managing
    ? discussionNavigation.managed.map((item) => ({
        title: item.name,
        description: item.topic,
        badge: item.archivedAt ? "Archived" : undefined,
        iconName: item.isGeneral ? "hash" : "lock",
        canPin: false,
        target: {
          kind: "discussion",
          discussionType: "channel",
          conversationId: item.id,
          manage: true,
        },
      }))
    : discussionItems.filter(
        (item) =>
          item.target.kind === "discussion" &&
          item.target.discussionType === "channel" &&
          !item.pinned,
      );
  navigatorGroups.sort(
    (left, right) =>
      Number(right.iconName === "hash") - Number(left.iconName === "hash"),
  );
  const handleCreateGroup = () => {
    setMobileNavigatorOpen(false);
    setCreateGroupOpen(true);
  };
  const handleNavigatorBrowseChange = (browse: typeof navigatorBrowse) =>
    dispatch(setConversationWorkspaceNavigatorBrowse({ companyId, browse }));
  const navigator = (
    <div
      ref={isMobileViewport ? mobileNavigatorRef : desktopNavigatorRef}
      style={
        isMobileViewport
          ? undefined
          : {
              width: sidebarOnly
                ? defaultNavigatorWidth
                : navigatorResize.width,
            }
      }
      aria-label={isMobileViewport ? "Conversation navigator" : undefined}
      aria-modal={isMobileViewport || undefined}
      data-tour-id="conversation-navigator"
      className={cx(
        "workspace-chat-navigator-container relative h-full shrink-0",
        isMobileViewport &&
          "absolute inset-y-0 left-0 z-40 shadow-[24px_0_56px_rgba(0,0,0,0.34)]",
      )}
      role={isMobileViewport ? "dialog" : undefined}
    >
      {!sidebarOnly && !isMobileViewport && !navigatorCollapsed && (
        <div
          className="workspace-chat-navigator-resize"
          {...navigatorResize.separatorProps}
        />
      )}
      <ChatNavigator
        key={companyId}
        avatarMotionPaused={
          navigatorCollapsed && workspaceSidebar.previewId !== "chat"
        }
        activeTarget={
          route.discussion ?? (newSessionRequested ? undefined : activeTarget)
        }
        agents={navigatorAgents}
        browse={navigatorBrowse}
        preferences={navigatorPreferences}
        onPreferencesChange={(preferences) =>
          dispatch(
            setConversationWorkspaceNavigatorPreferences({
              companyId,
              preferences,
            }),
          )
        }
        directMessages={navigatorDirectMessages}
        taskChats={navigatorModel.taskChats}
        isLoading={
          (!cataloguesLoaded && !catalogueUnavailable) ||
          (navigatorBrowse.view === "archived" && awaitingConversationStates) ||
          discussionNavigation.people.isLoading ||
          discussionNavigation.groupQuery.isLoading
        }
        hasError={
          hasWorkspaceDataWarning ||
          statesQuery.isError ||
          (navigatorBrowse.view === "archived" &&
            discussionNavigation.people.isError)
        }
        pinned={navigatorPinned}
        archivedChats={navigatorArchivedChats}
        archivedFooter={
          <ChatNavigatorPageStatus
            label="direct messages"
            query={discussionNavigation.people}
          />
        }
        onToggleArchive={handleToggleConversationArchive}
        updatingTargetKey={updatingTargetKey}
        onTogglePin={handleToggleConversationPin}
        onRename={(item, trigger) => {
          if (item.target.kind !== "direct") return;
          const conversation = directConversations.find(
            (candidate) =>
              candidate.id === item.target.conversationId &&
              candidate.kind === "direct",
          );
          if (!conversation) return;
          setRenamingConversation({
            conversation,
            title: item.title,
            returnFocus: trigger,
            fallbackFocus:
              trigger
                .closest("aside")
                ?.querySelector<HTMLInputElement>('input[type="search"]') ??
              null,
          });
        }}
        groups={navigatorGroups}
        groupTitle={
          discussionNavigation.managing
            ? discussionNavigation.archived
              ? "Manage archived channels"
              : "Manage channels"
            : discussionNavigation.archived
              ? "Archived channels"
              : "Channels"
        }
        groupFooter={
          discussionNavigation.groupQuery.isError ||
          (!discussionNavigation.groupQuery.currentData &&
            discussionNavigation.groupQuery.isFetching) ||
          discussionNavigation.groupQuery.hasNextPage ? (
            <ChatNavigatorPageStatus
              label="channels"
              query={discussionNavigation.groupQuery}
            />
          ) : undefined
        }
        directMessageFooter={
          discussionNavigation.people.isError ||
          (!discussionNavigation.people.currentData &&
            discussionNavigation.people.isFetching) ||
          discussionNavigation.people.hasNextPage ? (
            <ChatNavigatorPageStatus
              label="direct messages"
              query={discussionNavigation.people}
            />
          ) : undefined
        }
        includeHidden={discussionNavigation.includeHidden}
        onCreateGroup={
          discussionNavigation.canManage ? handleCreateGroup : undefined
        }
        options={
          <ChatNavigatorOptions
            browse={navigatorBrowse}
            discussions={discussionNavigation}
            onBrowseChange={handleNavigatorBrowseChange}
            onCreateGroup={handleCreateGroup}
          />
        }
        onNewDirectMessage={() => {
          setMobileNavigatorOpen(false);
          setNewDirectMessageOpen(true);
        }}
        onNewAgentChat={handleOpenDirectMessage}
        onBrowseChange={handleNavigatorBrowseChange}
        onPeekTask={handlePeekTask}
        onSelect={(target) =>
          target.kind === "discussion"
            ? openDiscussion({
                conversationId: target.conversationId,
                type: target.discussionType,
                manage: target.manage,
              })
            : openTarget(target)
        }
      />
    </div>
  );

  if (!companyId) {
    return (
      <ChatWorkspaceUnavailable message="Company workspace is unavailable." />
    );
  }

  return (
    <ChatMemberProfileProvider value={profileContext}>
      <div className="workspace-chat-page">
        <main
          ref={chatLayoutRef}
          className="workspace-chat-layout relative flex h-full min-h-0 w-full overflow-hidden bg-[#061121] text-white"
        >
          {sidebarOnly ? (
            navigator
          ) : isMobileViewport ? null : (
            <WorkspacePageSidebar id="chat" className="workspace-chat-page">
              {navigator}
            </WorkspacePageSidebar>
          )}

          {!sidebarOnly && isMobileViewport && mobileNavigatorOpen ? (
            <WorkspaceModalPortal>
              <button
                aria-label="Close conversations"
                className="absolute inset-0 z-[35] bg-black/38"
                type="button"
                onClick={() => setMobileNavigatorOpen(false)}
              />
              {navigator}
            </WorkspaceModalPortal>
          ) : null}

          {!sidebarOnly && (
            <section
              className="workspace-chat-main relative min-w-0 flex-1 p-3 max-[899px]:p-0"
              aria-label="Active conversation"
              data-tour-id={
                activeTarget?.kind === "direct" &&
                activeTarget.agentId === ceoAgent?.id
                  ? "ceo-conversation"
                  : undefined
              }
            >
              {(hasWorkspaceDataWarning || statesQuery.isError) &&
              (!catalogueUnavailable || activeTarget) ? (
                <ChatWorkspaceDataNotice
                  message={
                    hasWorkspaceDataWarning
                      ? undefined
                      : "Chat archive and pin settings could not be loaded."
                  }
                  onRetry={handleRetryWorkspaceData}
                />
              ) : null}
              <button
                ref={mobileMenuButtonRef}
                aria-label="Open conversations"
                className="workspace-chat-menu-button absolute left-3 top-3 z-20 hidden size-9 place-items-center rounded-xl border border-white/10 bg-[#0F233F] text-white/68 shadow-lg max-[899px]:grid"
                type="button"
                onClick={() => setMobileNavigatorOpen(true)}
              >
                <Menu size={17} />
              </button>
              {discussionSelection ? (
                <DiscussionPane
                  selection={discussionSelection}
                  onSelect={openDiscussion}
                  onClose={() => navigate("/app", { replace: true })}
                />
              ) : newSessionRequested &&
                activeTarget?.kind === "direct" &&
                activeAgent ? (
                <NewChatSessionPreview
                  key={`${companyId}:${activeAgent.id}:${location.key}`}
                  agent={activeAgent}
                  companyId={companyId}
                  onSetUpAgent={setUpAgent}
                  initialDraft={readComposerDraftNavigationState(
                    location.state,
                  )}
                  onStarted={(conversationId) =>
                    openTarget({ ...activeTarget, conversationId })
                  }
                />
              ) : activeTarget && activeTeam ? (
                <ChatSessionMemoryScope
                  sessionKey={`${companyId}:${chatConversationTargetKey(activeTarget)}`}
                >
                  <ActiveConversationPane
                    key={`${companyId}:${chatConversationTargetKey(activeTarget)}`}
                    agent={activeAgent}
                    agents={agents}
                    allowStructuredActions={canApplyPlan}
                    availableAgentIds={availableAgentIds}
                    companyId={companyId}
                    archived={Boolean(
                      activeTarget.conversationId &&
                      (statesQuery.currentData?.items.some(
                        (state) =>
                          state.conversationId ===
                            activeTarget.conversationId && state.archivedAt,
                      ) ||
                        directConversations.find(
                          (item) => item.id === activeTarget.conversationId,
                        )?.archivedAt),
                    )}
                    conversationTitle={
                      activeTarget.kind === "direct"
                        ? directConversations.find(
                            (conversation) =>
                              conversation.id === activeTarget.conversationId,
                          )?.title
                        : undefined
                    }
                    focusedMessage={route.focusedMessage}
                    focusRequestKey={focusRequestKey}
                    isCeoConversation={
                      activeTarget.kind === "direct" &&
                      activeTarget.agentId === ceoAgent?.id
                    }
                    target={activeTarget}
                    task={activeTask}
                    taskPrefix={taskPrefix}
                    team={activeTeam}
                    teams={teamNodes}
                    onConversationResolved={handleConversationResolved}
                    onExitFocusedHistory={handleExitFocusedHistory}
                    onSetUpAgent={setUpAgent}
                    onNewChat={
                      activeTarget.kind === "direct"
                        ? () => handleOpenDirectMessage(activeTarget.agentId)
                        : undefined
                    }
                  />
                </ChatSessionMemoryScope>
              ) : catalogueUnavailable ? (
                <ChatWorkspaceUnavailable
                  message="Some workspace conversations could not be loaded."
                  onRetry={handleRetryWorkspaceData}
                />
              ) : directConversationsQuery.isFetching ||
                tasksQuery.isFetching ||
                awaitingConversationStates ||
                (hasAgentRoster && agentsQuery.isFetching) ? (
                <ChatWorkspaceUnavailable
                  loading
                  message={"Loading conversations…"}
                />
              ) : (
                <ChatWorkspaceUnavailable message="Choose a conversation or start a new chat with a teammate." />
              )}
            </section>
          )}
          <CreateGroupDialog
            open={createGroupOpen && can("channels.manage")}
            onOpenChange={setCreateGroupOpen}
            onCreated={(conversation) =>
              openDiscussion({
                conversationId: conversation.id,
                type: "channel",
              })
            }
          />
          {renamingConversation?.conversation.companyId === companyId ? (
            <RenameConversationDialog
              key={renamingConversation.conversation.id}
              {...renamingConversation}
              onClose={() => setRenamingConversation(null)}
            />
          ) : null}
          <NewDirectMessageDialog
            open={newDirectMessageOpen}
            onOpenChange={setNewDirectMessageOpen}
            recipients={directMessageRecipients}
            onSelect={async (recipients) => {
              if (recipients.length === 1 && recipients[0]?.kind === "agent") {
                handleOpenDirectMessage(recipients[0].id);
                return true;
              }
              return openHumanDiscussion(
                recipients
                  .filter((recipient) => recipient.kind === "human")
                  .map((recipient) => recipient.id),
                recipients
                  .filter((recipient) => recipient.kind === "agent")
                  .map((recipient) => recipient.id),
              );
            }}
            isLoading={
              (hasAgentRoster &&
                agentsQuery.isFetching &&
                !agentsQuery.currentData) ||
              directConversationsQuery.isLoading ||
              peopleQuery.isLoading
            }
            hasError={
              (hasAgentRoster && agentsQuery.isError) ||
              directConversationsQuery.isError ||
              peopleQuery.isError
            }
            onRetry={handleRetryWorkspaceData}
          />
          {bindDialog}
          {route.peekTaskId && !activeProfile ? (
            <TaskPeek
              presentation={
                activeTarget?.kind === "direct" &&
                activeTarget.agentId === ceoAgent?.id
                  ? "modal"
                  : "responsive"
              }
              companyId={companyId}
              fallbackReturnFocusRef={mobileMenuButtonRef}
              returnFocusRef={peekReturnFocusRef}
              suppressReturnFocusRef={profileHandoffRef}
              taskId={route.peekTaskId}
              onClose={handleClosePeek}
              onOpenTask={handleOpenStructuredTask}
              onOpenTaskChat={handleOpenTaskChatFromPeek}
              onPeekTask={handlePeekTask}
            />
          ) : null}
          {activeProfile ? (
            <MemberProfilePanel
              key={`${companyId}:${activeProfile.kind}:${activeProfile.id}`}
              profile={activeProfile}
              onClose={handleCloseProfile}
              onMessage={handleMessageProfile}
              onViewProfile={
                canViewAgentProfile(activeProfile)
                  ? () => handleViewAgentProfile(activeProfile)
                  : undefined
              }
              canMessage={canMessageProfile(activeProfile)}
              returnFocusRef={profileReturnFocusRef}
            />
          ) : null}
        </main>
      </div>
    </ChatMemberProfileProvider>
  );
}

function ChatWorkspaceUnavailable({
  loading = false,
  message,
  onRetry,
}: {
  loading?: boolean;
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="workspace-chat-empty-state grid h-full min-h-[280px] place-items-center rounded-3xl border border-white/8 bg-[#0B1A2B] px-8 text-center">
      <div>
        {loading ? (
          <span className="mx-auto mb-3 block size-5 animate-spin rounded-full border-2 border-white/18 border-t-[#FFD84D]" />
        ) : (
          <Bot className="mx-auto mb-3 text-white/28" size={24} />
        )}
        <p className="m-0 text-sm font-bold text-white/66">{message}</p>
        <p className="mt-1 text-xs text-white/34">
          <MessageSquareText className="mr-1 inline" size={13} />
          Select a conversation from the navigator.
        </p>
        {onRetry ? (
          <button
            className="mx-auto mt-4 flex h-9 items-center gap-2 rounded-xl border border-white/12 bg-white/5 px-3 text-xs font-bold text-white/72 transition hover:border-white/20 hover:bg-white/8 hover:text-white"
            type="button"
            onClick={onRetry}
          >
            <RefreshCw size={14} />
            Retry
          </button>
        ) : null}
      </div>
    </div>
  );
}

function ChatWorkspaceDataNotice({
  message = "Some workspace updates could not be loaded.",
  onRetry,
}: {
  message?: string;
  onRetry: () => void;
}) {
  return (
    <div
      className="workspace-chat-data-notice absolute left-1/2 top-5 z-30 flex max-w-[calc(100%-32px)] -translate-x-1/2 items-center gap-2 rounded-xl border border-amber-200/16 bg-[#0F233F] px-3 py-2 text-xs text-amber-50 shadow-xl"
      role="alert"
    >
      <CircleAlert className="shrink-0 text-amber-200" size={15} />
      <span className="truncate">{message}</span>
      <button
        className="shrink-0 rounded-lg border border-white/12 px-2 py-1 font-bold text-white/72 hover:bg-white/8 hover:text-white"
        type="button"
        onClick={onRetry}
      >
        Retry
      </button>
    </div>
  );
}

function createNavigatorModel({
  archivedConversationIds,
  pinnedTaskConversationIds,
  taskPrefix,
  agents,
  approvals,
  ceoAgent,
  directConversations: allDirectConversations,
  pendingDirectAgentId,
  questions,
  recentTargets,
  respondTaskIds,
  reviewTaskIds,
  surfacedTaskIds,
  visibleDirectMessageKeys,
  tasks,
  runningTaskIds,
}: {
  archivedConversationIds: ReadonlySet<string>;
  pinnedTaskConversationIds: ReadonlySet<string>;
  taskPrefix?: string;
  runningTaskIds: ReadonlySet<string>;
  agents: ChatWorkspaceAgent[];
  approvals: ApprovalDto[];
  ceoAgent?: ChatWorkspaceAgent;
  directConversations: DirectConversationListItem[];
  pendingDirectAgentId?: string;
  questions: QuestionDto[];
  recentTargets: ChatConversationTarget[];
  /** Tasks with a pending request this viewer may answer (decide view). */
  respondTaskIds: ReadonlySet<string>;
  /** Tasks whose review waits on this viewer (`useViewerReviewTaskIds`). */
  reviewTaskIds: ReadonlySet<string>;
  surfacedTaskIds: string[];
  visibleDirectMessageKeys: string[];
  tasks: TaskDto[];
}) {
  const agentById = new Map(agents.map((agent) => [agent.id, agent] as const));
  const directConversations = allDirectConversations.filter(
    (conversation) => !conversation.archivedAt,
  );
  const archivedChats = allDirectConversations
    .filter((conversation) => conversation.archivedAt && conversation.agent)
    .flatMap((conversation) =>
      conversation.agent
        ? [
            createDirectMessageNavigatorItem(
              agentById.get(conversation.agent.id) ?? conversation.agent,
              conversation,
            ),
          ]
        : [],
    );

  const directByAgentId = new Map<string, DirectConversationListItem>();
  directConversations.forEach((conversation) => {
    if (conversation.agent) {
      directByAgentId.set(conversation.agent.id, conversation);
    }
  });
  const visibleDirectMessages = new Set(visibleDirectMessageKeys);
  const pinned = directConversations
    .filter((conversation) => conversation.pinnedAt && conversation.agent)
    .sort((left, right) =>
      String(right.pinnedAt).localeCompare(String(left.pinnedAt)),
    )
    .flatMap((conversation) =>
      conversation.agent
        ? [
            createDirectMessageNavigatorItem(
              agentById.get(conversation.agent.id) ?? conversation.agent,
              conversation,
            ),
          ]
        : [],
    );
  const directMessages = directConversations
    .filter(
      (conversation) =>
        !conversation.pinnedAt &&
        conversation.agent &&
        (visibleDirectMessages.has(`direct-agent:${conversation.agent.id}`) ||
          visibleDirectMessages.has(`direct-conversation:${conversation.id}`) ||
          conversation.summary.lastMessage),
    )
    .sort((left, right) =>
      right.summary.updatedAt.localeCompare(left.summary.updatedAt),
    )
    .flatMap((conversation) =>
      conversation.agent
        ? [
            createDirectMessageNavigatorItem(
              agentById.get(conversation.agent.id) ?? conversation.agent,
              conversation,
            ),
          ]
        : [],
    );
  agents
    .filter(
      (agent) =>
        agent.id !== ceoAgent?.id &&
        agent.id === pendingDirectAgentId &&
        !agent.archivedAt &&
        !directByAgentId.has(agent.id) &&
        visibleDirectMessages.has(`direct-agent:${agent.id}`),
    )
    .forEach((agent) =>
      directMessages.unshift(createDirectMessageNavigatorItem(agent)),
    );
  const pendingQuestionTaskIds = new Set(
    questions
      .filter((question) => question.status === "pending")
      .map((question) => question.taskId),
  );
  const pendingApprovalTaskIds = new Set(
    approvals
      .filter((approval) => approval.status === "pending")
      .map((approval) => approval.taskId),
  );
  const recentTaskIds = new Set([
    ...surfacedTaskIds,
    ...recentTargets.flatMap((target) =>
      target.kind === "task" ? [target.taskId] : [],
    ),
  ]);
  const taskChats = tasks
    .map((task): ChatNavigatorItem => {
      const state = taskChatState(task, {
        canRespond: respondTaskIds.has(task.id),
        hasPendingApproval: pendingApprovalTaskIds.has(task.id),
        hasPendingQuestion: pendingQuestionTaskIds.has(task.id),
        running: runningTaskIds.has(task.id),
      });
      const needsAttention =
        taskNeedsAttention(task) || (state.live && respondTaskIds.has(task.id));
      const running =
        runningTaskIds.has(task.id) &&
        task.status !== "done" &&
        task.status !== "cancelled";
      const label =
        state.label === "Needs your input"
          ? "Needs input"
          : state.label === "Awaiting approval"
            ? "Needs approval"
            : state.label === "Ready for review"
              ? reviewTaskIds.has(task.id)
                ? "Needs review"
                : "In review"
              : task.status === "done"
                ? "Completed"
                : state.label === "Waiting for prerequisites"
                  ? "Waiting"
                  : state.label;
      return {
        ...createTaskNavigatorItem(task, agentById, taskPrefix),
        pinned: pinnedTaskConversationIds.has(task.conversationId),
        archived: archivedConversationIds.has(task.conversationId),
        badge: label,
        taskState: {
          active: state.live,
          running,
          needsAttention,
          recent: recentTaskIds.has(task.id),
          tone:
            task.status === "failed" || task.status === "error"
              ? "danger"
              : needsAttention
                ? "attention"
                : running
                  ? "running"
                  : "muted",
        },
      };
    })
    .sort((left, right) => {
      const priority = (item: ChatNavigatorItem) =>
        item.taskState?.needsAttention ? 0 : item.taskState?.running ? 1 : 2;
      const difference = priority(left) - priority(right);
      if (difference) return difference;
      // Progress updates must not keep moving active task rows while they are read.
      return priority(left) < 2
        ? (left.taskLabel ?? "").localeCompare(
            right.taskLabel ?? "",
            undefined,
            { numeric: true },
          )
        : byRecentActivity(left, right);
    });
  pinned.push(...taskChats.filter((item) => item.pinned && !item.archived));
  archivedChats.push(...taskChats.filter((item) => item.archived));
  archivedChats.sort(byRecentActivity);

  return {
    directMessages,
    pinned,
    taskChats: taskChats.filter((item) => !item.archived),
    archivedChats,
  };
}

function createDirectMessageNavigatorItem(
  agent: ChatWorkspaceAgent,
  conversation?: DirectConversationListItem,
): ChatNavigatorItem {
  return {
    updatedAt: conversation?.summary.updatedAt,
    avatarSrc: agent.avatarSrc ?? undefined,
    badge: conversation ? runBadge(conversation.summary.runs) : undefined,
    pinned: Boolean(conversation?.pinnedAt),
    archived: Boolean(conversation?.archivedAt),
    // Direct-list rows are private to their creator, who may rename them.
    canRename: conversation?.kind === "direct",
    description: agent.name,
    target: directTargetForAgent(agent, conversation?.id),
    title: directConversationTitle(conversation?.title, agent.name),
  };
}

function createTaskNavigatorItem(
  task: TaskDto,
  agentById: Map<string, ChatWorkspaceAgent>,
  taskPrefix?: string,
): ChatNavigatorItem {
  const agentId = taskAgentId(task);
  // A Task the assignee does themself shows the assignee's face; one an
  // Agent executes shows the Agent's.
  return {
    updatedAt: task.updatedAt,
    avatarName: agentId ? undefined : task.assignee.name,
    avatarSrc: agentId
      ? (agentById.get(agentId)?.avatarSrc ?? undefined)
      : (task.assignee.avatarUrl ?? undefined),
    taskLabel: formatTaskLabel(task, taskPrefix),
    description: task.assignee.name,
    target: targetForTask(task),
    title: task.title,
  };
}

function byRecentActivity(left: ChatNavigatorItem, right: ChatNavigatorItem) {
  return (
    (Date.parse(right.updatedAt ?? "") || 0) -
    (Date.parse(left.updatedAt ?? "") || 0)
  );
}

function createDiscussionNavigatorItem(
  conversation: DiscussionSummary,
): ChatNavigatorItem {
  const archived =
    conversation.type === "human_direct" &&
    Boolean(conversation.userState?.hiddenAt);
  return {
    title: conversation.peer?.displayName ?? conversation.title,
    avatarName: conversation.peer?.displayName,
    avatarSrc: conversation.peer?.avatarUrl ?? undefined,
    avatarUserId: conversation.peer?.userId,
    description:
      conversation.type === "human_direct" && !conversation.peer
        ? "Group conversation"
        : (conversation.topic ?? undefined),
    iconName:
      conversation.type === "channel"
        ? conversation.isGeneral
          ? "hash"
          : "lock"
        : !conversation.peer
          ? "group"
          : undefined,
    pinned: Boolean(conversation.userState?.pinnedAt),
    archived,
    unreadCount: conversation.userState?.unreadCount ?? 0,
    markedUnread: Boolean(conversation.userState?.manualUnreadSeq),
    updatedAt: conversation.updatedAt,
    badge:
      archived || conversation.archivedAt
        ? "Archived"
        : conversation.peer?.active === false
          ? "Inactive"
          : conversation.userState?.hiddenAt
            ? "Hidden"
            : undefined,
    target: {
      kind: "discussion",
      discussionType: conversation.type,
      conversationId: conversation.id,
    },
  };
}

function directTargetForAgent(
  agent: Pick<ChatWorkspaceAgent, "id">,
  conversationId?: string,
): Extract<ChatConversationTarget, { kind: "direct" }> {
  return {
    agentId: agent.id,
    ...(conversationId ? { conversationId } : {}),
    kind: "direct",
  };
}

function resolveTargetAgent(
  target: ChatConversationTarget,
  task: TaskDto | undefined,
  agentById: Map<string, ChatWorkspaceAgent>,
) {
  return target.kind === "task"
    ? agentById.get((task && taskAgentId(task)) ?? target.agentId ?? "")
    : agentById.get(target.agentId);
}

/** The Agent executing or assisting a Task. */
function taskAgentId(task: TaskDto) {
  return task.execution.agent?.id;
}

function agentTeamNode(
  agent: ChatWorkspaceAgent,
  isCeo: boolean,
): TeamNodeData {
  return {
    accentColor: agent.accentColor ?? undefined,
    agentId: agent.id,
    agentRole: agent.title,
    available: agent.runtimeAvailable ?? true,
    currentWork: "Conversation",
    href: "/app",
    iconSrc: agent.avatarSrc ?? "",
    label: agent.name,
    notificationCount: 0,
    status: agent.runtimeAvailable === false ? "idle" : "active",
    teamId: agentChatTeamId(agent.id, isCeo),
  };
}

function createChatWorkspaceAgents({
  directConversations,
  ownerAgents,
  tasks,
}: {
  directConversations: DirectConversationListItem[];
  ownerAgents: AgentDto[];
  tasks: TaskDto[];
}): ChatWorkspaceAgent[] {
  // `bound` is the viewer's own setup, which is what the composer acts on
  // (ADR 0014); the API never carries anybody else's.
  const agents = new Map<string, ChatWorkspaceAgent>(
    ownerAgents.map((agent) => [
      agent.id,
      {
        ...agent,
        // Studio runs on the viewer's own computer for this Agent.
        runtimeId: agent.binding?.runtime.id,
        accentColor: agentAccentColor(agent),
        bound: agent.binding !== null,
      },
    ]),
  );
  directConversations.forEach((conversation) => {
    const agent = conversation.agent;
    if (!agent || agents.has(agent.id)) return;
    agents.set(agent.id, {
      ...agent,
      bound: agent.runtimeAccess.execute || agent.runtimeAvailable,
    });
  });
  tasks.forEach((task) => {
    const agent = task.execution.agent;
    if (!agent || agents.has(agent.id)) {
      return;
    }
    agents.set(agent.id, {
      avatarSrc: agent.avatarUrl,
      id: agent.id,
      name: agent.name,
      title: "Agent",
    });
  });
  return [...agents.values()];
}

/** Without the owner's Agent list, the CEO is the Agent titled as one. */
function findCeoPresentation(agents: ChatWorkspaceAgent[]) {
  return agents.find((agent) => /\bceo\b|chief executive/i.test(agent.title));
}

function targetExists(
  target: ChatConversationTarget,
  taskById: Map<string, TaskDto>,
  directConversations: DirectConversationListItem[],
  agentById: Map<string, ChatWorkspaceAgent>,
) {
  if (target.kind === "task") return taskById.has(target.taskId);
  if (!target.conversationId) return agentById.has(target.agentId);
  const conversation = directConversations.find(
    (item) => item.id === target.conversationId,
  );
  return conversation?.agent?.id === target.agentId;
}

function targetForTask(
  task: TaskDto,
): Extract<ChatConversationTarget, { kind: "task" }> {
  const agentId = taskAgentId(task);
  return {
    ...(agentId ? { agentId } : {}),
    conversationId: task.conversationId,
    kind: "task",
    taskId: task.id,
  };
}

function runBadge(runs: { activeCount: number; queuedCount: number }) {
  if (runs.activeCount > 0) return "Running";
  if (runs.queuedCount > 0) return "Up next";
  return undefined;
}

export const Component = ChatWorkspacePage;

export default ChatWorkspacePage;
