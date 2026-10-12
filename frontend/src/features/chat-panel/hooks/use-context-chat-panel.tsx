import type { WorkspaceChatContext } from "@/features/chat-panel/types";
import type { PromptInputMessage } from "@/shared/ui/core/ai-elements/prompt-input";
import { useCallback, useMemo, type ReactNode } from "react";

import { ChatPanel } from "../components/chat-panel";
import { readSessionSnapshot } from "@/app/session/session";
import { useLazyListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { useAgentRealtimeChat } from "@/features/chat-panel/hooks/use-agent-realtime-chat";
import { useChatPanelFlowActions } from "@/features/chat-panel/hooks/use-chat-panel-flow-actions";
import { useTaskConversationController } from "@/features/chat-panel/hooks/use-task-conversation-controller";
import type { AgentRealtimeConnectionStatus } from "@/features/chat-panel/model/chat-connection-recovery";
import type { ChatPanelSurface } from "@/features/chat-panel/model/chat-panel-surface";
import type { ChatConversationDecorator } from "@/features/chat-panel/types";
import {
  createUserChatMessage,
  withAgentGreetingMessage,
  type AgentGreetingSource,
} from "@/features/chat-panel/utils";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";
import type { TeamNodeData } from "@/features/home/types";
import type {
  ChatFocusedMessageTarget,
  ChatSendMessageOptions,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import { useFocusedMessageHistory } from "@/features/conversation-search/hooks/use-focused-message-history";
import { cockpitService } from "@/shared/api";
import { resolveDirectConversation } from "@/shared/api/direct-conversation";
import { CEO_CHAT_TEAM_ID } from "@/shared/chat/chat-team";
import { runService } from "@/shared/api/run.service";
import type {
  ActorRefDto,
  ConversationRunQueueProjection,
} from "@/shared/api/types";
import {
  appendChatMessage,
  selectChatDraftByTeam,
  selectChatMessagesByTeam,
  selectCompanyConversationDock,
  setTeamDraft,
  useAppDispatch,
  useAppSelector,
  type ConversationDockTab,
} from "@/state";

const noopSelectTeam = () => undefined;

export function useContextChatPanel({
  activeTeam,
  allowAgentMailActions,
  allowStructuredActions,
  appendUserMessageOnSend = false,
  canDecide,
  composerAriaLabel,
  composerAccessory,
  workspaceContext,
  composerPlaceholder,
  connectionStatus,
  conversationTab,
  conversationId,
  studioRuntimeId,
  studioAgentId,
  conversationDecorators,
  conversationSlot,
  conversationSlotClassName,
  agentGreetingSource,
  dockAware = true,
  draftMessage,
  enabled,
  focusRequestKey,
  focusedMessage,
  focusedHistoryRequestKey,
  hasOlderMessages,
  headerActions,
  historyLoadError,
  initialRunQueue,
  isExpanded,
  isLoadingOlderMessages,
  layout = "dock",
  messages,
  onAfterUserMessage,
  onAppendMessage,
  onApproveCompanyDashboard,
  onClose,
  onDiscoveryPlanApproval,
  onDiscoveryPlanHandoff,
  onDraftMessageChange,
  onExpandedChange,
  onExitFocusedHistory,
  onFocusedHistoryAvailable,
  onFocusedHistoryUnavailable,
  onLoadOlderMessages,
  onReviewTask,
  onSelectTeam = noopSelectTeam,
  readOnly,
  readOnlyPlaceholder,
  sendAgentMessage,
  selectedTeam,
  showApprovals,
  surface,
  teams,
}: {
  activeTeam: TeamNodeData | undefined;
  allowAgentMailActions?: boolean;
  allowStructuredActions?: boolean;
  appendUserMessageOnSend?: boolean;
  canDecide?: boolean;
  composerAriaLabel?: string;
  composerAccessory?: ReactNode;
  workspaceContext?: WorkspaceChatContext;
  composerPlaceholder?: string;
  connectionStatus?: AgentRealtimeConnectionStatus;
  conversationTab?: ConversationDockTab;
  conversationId?: string | null;
  studioRuntimeId?: string | null;
  studioAgentId?: string | null;
  conversationDecorators?: ChatConversationDecorator[];
  conversationSlot?: ReactNode;
  conversationSlotClassName?: string;
  agentGreetingSource?: AgentGreetingSource;
  dockAware?: boolean;
  draftMessage: string;
  enabled: boolean;
  focusRequestKey: number;
  focusedMessage?: ChatFocusedMessageTarget | null;
  focusedHistoryRequestKey?: number | string;
  hasOlderMessages?: boolean;
  headerActions?: ReactNode;
  historyLoadError?: string | null;
  initialRunQueue?: ConversationRunQueueProjection;
  isExpanded?: boolean;
  isLoadingOlderMessages?: boolean;
  layout?: "dock" | "workspace";
  messages: ChatMessage[];
  onAfterUserMessage?: (message: ChatMessage) => Promise<void> | void;
  onAppendMessage: (message: ChatMessage) => void;
  onApproveCompanyDashboard?: () => void;
  onClose: () => void;
  onDiscoveryPlanApproval?: (handoff: DiscoveryPlanHandoff) => void;
  onDiscoveryPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  onDraftMessageChange: (message: string) => void;
  onExpandedChange?: (expanded: boolean) => void;
  onExitFocusedHistory?: () => void;
  onFocusedHistoryAvailable?: () => void;
  onFocusedHistoryUnavailable?: () => void;
  onLoadOlderMessages?: () => Promise<void>;
  onReviewTask?: () => void;
  onSelectTeam?: (team: ChatTeamId) => void;
  readOnly?: boolean;
  readOnlyPlaceholder?: string;
  sendAgentMessage?: (
    message: ChatMessage,
    options?: ChatSendMessageOptions,
  ) => Promise<void>;
  selectedTeam: ChatTeamId;
  showApprovals?: boolean;
  surface?: ChatPanelSurface;
  teams: TeamNodeData[];
}) {
  const focusedHistory = useFocusedMessageHistory({
    focusedMessage,
    onAvailable: onFocusedHistoryAvailable,
    onUnavailable: onFocusedHistoryUnavailable ?? onExitFocusedHistory,
    requestKey: focusedHistoryRequestKey,
    team: selectedTeam,
  });
  const isFocusedHistoryActive = Boolean(
    focusedMessage && !focusedHistory.failed,
  );
  const chatPanelFlowActions = useChatPanelFlowActions({
    messages,
    sendAgentMessage,
  });
  const resolvedConversationId =
    focusedMessage?.conversationId ?? conversationId;
  const companyId = readSessionSnapshot().company?.id ?? "";
  const artifactContext =
    companyId && resolvedConversationId
      ? { companyId, conversationId: resolvedConversationId }
      : undefined;
  const displayMessages = useMemo(
    () =>
      isFocusedHistoryActive
        ? focusedHistory.messages
        : withAgentGreetingMessage(messages, agentGreetingSource),
    [
      agentGreetingSource,
      focusedHistory.messages,
      isFocusedHistoryActive,
      messages,
    ],
  );
  const handleSendMessage = useCallback(
    async (
      inputMessage: PromptInputMessage,
      options?: ChatSendMessageOptions,
    ) => {
      const chatMessage = createUserChatMessage({
        message: inputMessage,
        team: selectedTeam,
      });

      if (!chatMessage) {
        return;
      }

      onExitFocusedHistory?.();
      if (sendAgentMessage) {
        await sendAgentMessage(chatMessage, {
          ...options,
          clientMessageId:
            inputMessage.clientMessageId ?? options?.clientMessageId,
          requestKey: inputMessage.requestKey ?? options?.requestKey,
          onFileTransfer: inputMessage.onFileTransfer,
        });
      } else {
        await onAfterUserMessage?.(chatMessage);
      }
      if (appendUserMessageOnSend) {
        onAppendMessage(chatMessage);
      }
    },
    [
      appendUserMessageOnSend,
      onAfterUserMessage,
      onAppendMessage,
      onExitFocusedHistory,
      sendAgentMessage,
      selectedTeam,
    ],
  );

  if (!enabled || !activeTeam) {
    return null;
  }

  const composerTarget = getComposerTarget(activeTeam);

  const panel = (
    <ChatPanel
      activeTeam={activeTeam}
      artifactContext={artifactContext}
      allowAgentMailActions={allowAgentMailActions}
      allowStructuredActions={allowStructuredActions}
      canDecide={canDecide}
      composerAriaLabel={composerAriaLabel ?? `Message ${composerTarget}`}
      composerAccessory={composerAccessory}
      workspaceContext={workspaceContext}
      composerPlaceholder={composerPlaceholder ?? `Message ${composerTarget}`}
      connectionStatus={connectionStatus}
      conversationTab={conversationTab}
      conversationId={resolvedConversationId}
      studioRuntimeId={studioRuntimeId}
      studioAgentId={studioAgentId}
      conversationDecorators={conversationDecorators}
      conversationSlot={conversationSlot}
      conversationSlotClassName={conversationSlotClassName}
      draftMessage={draftMessage}
      focusRequestKey={focusRequestKey}
      focusedHistory={
        isFocusedHistoryActive
          ? {
              targetSeq: focusedHistory.targetSeq,
            }
          : undefined
      }
      hasOlderMessages={isFocusedHistoryActive ? false : hasOlderMessages}
      headerActions={headerActions}
      historyLoadError={isFocusedHistoryActive ? null : historyLoadError}
      initialRunQueue={initialRunQueue}
      isExpanded={isExpanded}
      layout={layout}
      isLoadingOlderMessages={
        isFocusedHistoryActive ? false : isLoadingOlderMessages
      }
      messages={displayMessages}
      readOnly={readOnly}
      readOnlyPlaceholder={readOnlyPlaceholder}
      selectedTeam={selectedTeam}
      showApprovals={showApprovals}
      surface={surface}
      teams={teams}
      onApproveCompanyDashboard={onApproveCompanyDashboard}
      onClose={onClose}
      onDiscoveryPlanApproval={
        onDiscoveryPlanApproval ?? chatPanelFlowActions.approveDiscoveryPlan
      }
      onDiscoveryPlanHandoff={onDiscoveryPlanHandoff}
      onDraftMessageChange={onDraftMessageChange}
      onExpandedChange={onExpandedChange}
      onLoadOlderMessages={
        isFocusedHistoryActive ? undefined : onLoadOlderMessages
      }
      onReviewTask={onReviewTask}
      onSelectTeam={onSelectTeam}
      onSendMessage={handleSendMessage}
    />
  );

  return dockAware && conversationTab ? (
    <ConversationDockSelection
      currentTab={conversationTab}
      fallback={panel}
      presentation={{
        isExpanded,
        layout,
        onClose,
        onExpandedChange,
      }}
      teams={teams}
    />
  ) : (
    panel
  );
}

type DockPanelPresentation = {
  isExpanded?: boolean;
  layout: "dock" | "workspace";
  onClose: () => void;
  onExpandedChange?: (expanded: boolean) => void;
};

function ConversationDockSelection({
  currentTab,
  fallback,
  presentation,
  teams,
}: {
  currentTab: ConversationDockTab;
  fallback: ReactNode;
  presentation: DockPanelPresentation;
  teams: TeamNodeData[];
}) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const dock = useAppSelector((state) =>
    selectCompanyConversationDock(state, companyId),
  );
  const currentTabIsRegistered = dock.tabs.some(
    (tab) => tab.id === currentTab.id,
  );
  const activeTab = dock.tabs.find((tab) => tab.id === dock.activeTabId);

  if (!currentTabIsRegistered || !activeTab || activeTab.id === currentTab.id) {
    return fallback;
  }

  return activeTab.kind === "task" ? (
    <DockTaskChatPanel
      presentation={presentation}
      tab={activeTab}
      teams={teams}
    />
  ) : (
    <DockDirectChatPanel
      presentation={presentation}
      tab={activeTab}
      teams={teams}
    />
  );
}

function DockTaskChatPanel({
  presentation,
  tab,
  teams,
}: {
  presentation: DockPanelPresentation;
  tab: ConversationDockTab;
  teams: TeamNodeData[];
}) {
  const activeTeam = resolveDockTeam(tab, teams);
  const taskConversation = useTaskConversationController({
    activeTeam,
    tab,
    teams,
  });

  return useContextChatPanel({
    ...taskConversation,
    appendUserMessageOnSend: false,
    dockAware: false,
    enabled: true,
    focusRequestKey: 0,
    isExpanded: presentation.isExpanded,
    layout: presentation.layout,
    onClose: presentation.onClose,
    onExpandedChange: presentation.onExpandedChange,
    readOnlyPlaceholder: "This task is closed. Its chat history is read-only.",
  });
}

function DockDirectChatPanel({
  presentation,
  tab,
  teams,
}: {
  presentation: DockPanelPresentation;
  tab: ConversationDockTab;
  teams: TeamNodeData[];
}) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const dispatch = useAppDispatch();
  const [loadAgents] = useLazyListCompanyAgentsQuery();
  const activeTeam = resolveDockTeam(tab, teams);
  const assistantActor = useMemo<ActorRefDto | undefined>(() => {
    const agentId = tab.agentId ?? activeTeam.agentId;
    if (!agentId) {
      return undefined;
    }
    return {
      avatarUrl: null,
      id: agentId,
      kind: "agent",
      name: activeTeam.label,
    };
  }, [activeTeam.agentId, activeTeam.label, tab.agentId]);
  const draftMessage = useAppSelector((state) =>
    selectChatDraftByTeam(state, tab.teamId),
  );
  const messages = useAppSelector((state) =>
    selectChatMessagesByTeam(state, tab.teamId),
  );
  const resolveConversation = useCallback(async () => {
    if (tab.conversationId) {
      return tab.conversationId;
    }
    if (tab.teamId === CEO_CHAT_TEAM_ID) {
      const conversation = await cockpitService.startBuilding(companyId);
      return conversation.conversation_id;
    }

    const agents = await loadAgents(companyId, true).unwrap();
    const agent = agents.items.find(
      (candidate) => candidate.id === (tab.agentId ?? tab.teamId),
    );
    if (!agent) {
      return null;
    }

    return resolveDirectConversation(agent.id, `${tab.title} conversation`);
  }, [companyId, loadAgents, tab]);
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
    loadActiveRunIds,
    loadInitialConversation: resolveConversation,
    resolveConversation,
    team: tab.teamId,
  });
  const conversationTab = useMemo(
    () => ({
      ...tab,
      ...(activeConversationId ? { conversationId: activeConversationId } : {}),
    }),
    [activeConversationId, tab],
  );
  const handleAppendMessage = useCallback(
    (message: ChatMessage) => {
      dispatch(appendChatMessage(message));
    },
    [dispatch],
  );
  const handleDraftMessageChange = useCallback(
    (message: string) => {
      dispatch(setTeamDraft({ message, team: tab.teamId }));
    },
    [dispatch, tab.teamId],
  );

  return useContextChatPanel({
    activeTeam,
    appendUserMessageOnSend: false,
    connectionStatus,
    conversationId: activeConversationId,
    conversationTab,
    dockAware: false,
    draftMessage,
    enabled: true,
    focusRequestKey: 0,
    hasOlderMessages,
    historyLoadError,
    isExpanded: presentation.isExpanded,
    isLoadingOlderMessages,
    layout: presentation.layout,
    messages,
    onAppendMessage: handleAppendMessage,
    onClose: presentation.onClose,
    onDraftMessageChange: handleDraftMessageChange,
    onExpandedChange: presentation.onExpandedChange,
    onLoadOlderMessages: loadOlderMessages,
    selectedTeam: tab.teamId,
    sendAgentMessage,
    teams,
  });
}

function resolveDockTeam(
  tab: ConversationDockTab,
  teams: TeamNodeData[],
): TeamNodeData {
  return (
    teams.find((team) => team.teamId === tab.teamId) ?? {
      agentId: tab.agentId,
      agentRole: tab.kind === "direct" ? tab.subtitle : "Agent",
      available: true,
      currentWork: tab.title,
      href: "#chat",
      teamId: tab.teamId,
      iconSrc: tab.avatarSrc,
      label:
        tab.kind === "direct"
          ? tab.title
          : tab.subtitle.replace(/^Task · /, ""),
      notificationCount: 0,
      status: "active",
    }
  );
}

function getComposerTarget(activeTeam: TeamNodeData): string {
  const label =
    activeTeam.agentRole.trim() || activeTeam.label.trim() || "agent";

  return label.toLowerCase().startsWith("the ") ? label : `the ${label}`;
}
