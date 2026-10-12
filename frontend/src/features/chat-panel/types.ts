import type { PromptInputMessage } from "@/shared/ui/core/ai-elements/prompt-input";
import type { FileTransferHandler } from "@/shared/chat/attachment-preview";
import type { ReactNode } from "react";
import type { AgentRealtimeConnectionStatus } from "@/features/chat-panel/model/chat-connection-recovery";
import type { ChatPanelSurface } from "@/features/chat-panel/model/chat-panel-surface";
import type {
  AgentDto,
  TaskDto,
  ConversationRunQueueProjection,
} from "@/shared/api/types";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type {
  CeoDiscoveryMessagePayload,
  ChatMessage,
} from "@/shared/chat/types";
import type { TeamNodeData } from "@/features/home/types";
import type { ConversationDockTab } from "@/state";
import type { ConversationArtifactsContext } from "@/features/chat-panel/api/conversation-artifacts-api";

export type ChatConversationDecorator = {
  afterMessageId: number;
  className?: string;
  content: ReactNode;
  key: string;
};

export type ChatConversationItem =
  | {
      kind: "empty";
      key: string;
    }
  | {
      decorators: ChatConversationDecorator[];
      key: string;
      kind: "message";
      message: ChatMessage;
    }
  | {
      key: string;
      kind: "welcomeStart";
    }
  | {
      className?: string;
      content: ReactNode;
      key: string;
      kind: "slot";
    };

export type ChatCompanyModeSelection = "light" | "full";

export type DiscoveryPlanHandoff = {
  messageId: number;
  payload: CeoDiscoveryMessagePayload;
  selectedMode: ChatCompanyModeSelection;
};

export type ChatSendMessageOptions = {
  onFileTransfer?: FileTransferHandler;
  clientMessageId?: string;
  requestKey?: string;
  displayUserMessage?: boolean;
  hiddenTranscript?: boolean;
};

export type ChatFocusedMessageTarget = {
  conversationId: string;
  messageId: string;
  messageSeq: number;
};

export type ChatFocusedHistory = {
  targetSeq: number | null;
};

export type WorkspaceChatContext = {
  taskLabel?: string;
  agentName: string;
  provider?: AgentDto["provider"];
  task?: TaskDto;
};

export type ChatPanelProps = {
  artifactContext?: ConversationArtifactsContext;
  workspaceContext?: WorkspaceChatContext;
  activeTeam: TeamNodeData;
  allowAgentMailActions?: boolean;
  allowStructuredActions?: boolean;
  /**
   * Whether the viewer may answer the chat's questions, for a chat that
   * decides that apart from posting (a Task chat, ADR 0017). Unset, the
   * questions are locked with the composer (readOnly, allowStructuredActions).
   */
  canDecide?: boolean;
  composerAriaLabel?: string;
  composerAccessory?: ReactNode;
  composerPlaceholder?: string;
  connectionStatus?: AgentRealtimeConnectionStatus;
  conversationTab?: ConversationDockTab;
  conversationId?: string | null;
  /** Runtime selected by the trusted Team surface for Studio mode. */
  studioRuntimeId?: string | null;
  studioAgentId?: string | null;
  conversationDecorators?: ChatConversationDecorator[];
  conversationSlot?: ReactNode;
  conversationSlotClassName?: string;
  draftMessage: string;
  focusRequestKey: number;
  focusedHistory?: ChatFocusedHistory;
  hasOlderMessages?: boolean;
  headerActions?: ReactNode;
  historyLoadError?: string | null;
  initialRunQueue?: ConversationRunQueueProjection;
  isExpanded?: boolean;
  isLoadingOlderMessages?: boolean;
  layout?: "dock" | "workspace";
  messages: ChatMessage[];
  readOnly?: boolean;
  readOnlyPlaceholder?: string;
  showApprovals?: boolean;
  surface?: ChatPanelSurface;
  onApproveCompanyDashboard?: () => void;
  onClose: () => void;
  onDiscoveryPlanApproval?: (handoff: DiscoveryPlanHandoff) => void;
  onDiscoveryPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  onDraftMessageChange: (message: string) => void;
  onExpandedChange?: (isExpanded: boolean) => void;
  onLoadOlderMessages?: () => Promise<void>;
  onReviewTask?: () => void;
  onSelectTeam: (team: ChatTeamId) => void;
  onSendMessage: (
    message: PromptInputMessage,
    options?: ChatSendMessageOptions,
  ) => Promise<void>;
  selectedTeam: ChatTeamId;
  teams: TeamNodeData[];
};
