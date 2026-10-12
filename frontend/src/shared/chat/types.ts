import type { FileUIPart } from "ai";
import type {
  ActorRefDto,
  AgentProvider,
  RunDto,
  RunStatusDto,
} from "@/shared/api/types";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { components } from "@/shared/api/generated/api.generated";

export type DiscussionConversationTarget = {
  kind: "discussion";
  conversationId: string;
  discussionType: components["schemas"]["DiscussionSummary"]["type"];
  manage?: boolean;
  messageId?: string;
  threadRootId?: string;
};

export type ChatConversationTarget =
  | {
      agentId: string;
      conversationId?: string;
      kind: "direct";
    }
  | {
      agentId?: string;
      conversationId?: string;
      kind: "task";
      taskId: string;
    };

export type ChatMessage = {
  artifactIds?: string[];
  id: number;
  team: ChatTeamId;
  author: "user" | "team";
  actor?: ActorRefDto;
  text: string;
  afterUserInputRequestId?: string;
  clientMessageId?: string;
  conversationId?: string;
  createdAt?: string;
  files?: FileUIPart[];
  // A file on the message was left out for this viewer: a Project file of a
  // Project they do not read (ADR 0017). Shown as a neutral placeholder.
  attachmentsWithheld?: boolean;
  generatedFiles?: ChatGeneratedFile[];
  isThinking?: boolean;
  isStreaming?: boolean;
  itemId?: string;
  // The server's id for a durable message, when the row came from one.
  messageId?: string;
  pausedForUserInput?: boolean;
  activeSkills?: string[];
  activeTools?: string[];
  contentBlocks?: ChatMessageContentBlock[];
  runtimeActivities?: ChatRuntimeActivity[];
  richContent?: ChatRichContent;
  // The Run this item belongs to, on either author: the user's words belong to
  // the Run their send started, the agent's answer to the Run that wrote it.
  runId?: string;
  // Immutable Run creation clock, shared by live and restored elapsed labels.
  runCreatedAt?: RunDto["createdAt"];
  // Why the Run behind an assistant row failed, when it did. Server copy:
  // the provider-aware sentence, then the provider's own words.
  runFailure?: ChatRunFailure;
  // The server-owned lifecycle state for the Run behind this row. Historical
  // reads attach it from the Run list; live rows update it from lifecycle
  // frames so presentation never guesses completion from the socket alone.
  runStatus?: RunStatusDto;
  timestamp?: string;
  turnId?: number;
  turnKey?: string;
  userInputBoundaries?: ChatUserInputBoundary[];
};

export type ChatRunFailure = {
  attempt?: number;
  detail: string | null;
  maxAttempts?: number;
  message: string;
  provider: AgentProvider | null;
  // True only when a Run read named the runtime it ran on, which it does
  // for that runtime's registrant alone (ADR 0014): the Run provably ran on
  // the viewer's own computer. Lifecycle frames never name it, so a failure
  // known only from a frame is false until the chat reads that Run, which it
  // does as soon as the `failed` frame lands.
  ranOnViewerComputer: boolean;
  reason: string | null;
  // True once the server enqueued a retry child for this Run; retryActive
  // while that child has not ended.
  retried: boolean;
  retryActive?: boolean;
  runId: string;
};

export type ChatGeneratedFile = {
  eventSequence?: number;
  id: string;
  itemId?: string;
  operation: "create" | "modify" | "rename";
  path: string;
  truncated: boolean;
};

export type ChatUserInputBoundary = {
  blockIndex: number;
  eventSequence: number;
  requestId: string;
  textOffset: number;
};

export type ChatMessageContentBlock =
  | {
      id: string;
      itemId?: string;
      text: string;
      type: "text";
    }
  | {
      id: string;
      itemId: string;
      text: string;
      type: "reasoning";
    }
  | {
      activityId: string;
      id: string;
      type: "runtime_activity";
    };

export type ChatRuntimeActivity = {
  callId?: string;
  error?: string;
  exitCode?: number;
  input?: string;
  itemId?: string;
  id: string;
  label: string;
  output?: string;
  position: "before_message" | "after_message";
  status?: string;
  toolName: string;
  type: "tool";
};

export type ChatRichContent =
  CeoDiscoveryMessagePayload | CeoCompanyPlanMessagePayload;

export type CeoDiscoveryMessagePayload = {
  type: "ceo_discovery_message";
  message: string;
  flow_step:
    | "intro"
    | "idea_capture"
    | "audience_question"
    | "launch_question"
    | "stage_question"
    | "assets_question"
    | "mode_recommendation"
    | "ready_to_generate_plan";
  blocks: CeoDiscoveryBlock[];
  clarity_score?: number | null;
  completeness_score?: number | null;
  business_context_patch?: Record<string, unknown>;
  product_vision_snapshot?: Record<string, unknown> | null;
  runtime_trace?: CeoDiscoveryRuntimeTrace | null;
};

export type CeoRuntimeTrace = {
  active_skills: string[];
  used_tools: string[];
  tool_activities?: Array<{
    call_id?: string | null;
    input?: string | null;
    item_id?: string | null;
    output?: string | null;
    status?: string | null;
    tool_name?: string | null;
  }>;
};

export type CeoDiscoveryRuntimeTrace = CeoRuntimeTrace;

export type CeoDiscoveryBlock =
  | CeoDiscoveryParagraphBlock
  | CeoDiscoveryQuestionBlock
  | CeoDiscoveryCardBlock
  | CeoDiscoveryOptionGroupBlock
  | CeoDiscoveryModeSelectorBlock;

export type CeoDiscoveryParagraphBlock = {
  kind: "paragraph";
  text: string;
};

export type CeoDiscoveryQuestionBlock = {
  kind: "question";
  text: string;
};

export type CeoDiscoveryCardBlock = {
  kind: "summary_cards" | "ranked_cards";
  cards: string[];
};

export type CeoDiscoveryOptionGroupBlock = {
  kind: "option_group";
  id: string;
  label: string;
  selection_mode: "single" | "multi";
  send_behavior: "manual";
  initial_selected_option_ids: string[];
  options: Array<{ id: string; label: string; recommended?: boolean }>;
};

export type CeoDiscoveryModeSelectorBlock = {
  kind: "mode_selector";
  recommended_mode: "light" | "full";
};

export type CompanyPlanMode = "light" | "full";

export type CeoCompanyPlanTask = {
  /**
   * The Agent the Task is executed by (ADR 0014); empty when the assignee
   * does the work themself. The applier is the assignee of every Task the
   * plan creates. A Task that keeps its Agent may name an archived one.
   */
  agentId: string;
  id: string;
  title: string;
  outcome: string;
};

export type CeoCompanyPlanRevision = {
  revision_of_item_id?: string | null;
  user_feedback: string;
  change_summary: string[];
};

export type CeoCompanyPlanMessagePayload = {
  type: "ceo_company_plan_message";
  message: string;
  revision?: CeoCompanyPlanRevision | null;
  selected_mode: CompanyPlanMode;
  product_vision_snapshot: Record<string, unknown>;
  tasks: CeoCompanyPlanTask[];
  runtime_trace?: CeoRuntimeTrace | null;
};
