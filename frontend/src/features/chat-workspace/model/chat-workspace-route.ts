import { chatConversationTargetKey } from "@/shared/chat/conversation-target";
import type {
  ChatConversationTarget,
  DiscussionConversationTarget,
} from "@/shared/chat/types";

export type ChatWorkspaceFocusedMessage = {
  conversationId: string;
  messageId: string;
  messageSeq: number;
};

export type ChatWorkspaceArtifactTarget = {
  artifactId: string;
  runId: string;
  fileId?: string;
};

export type ChatWorkspaceRoute = {
  artifact?: ChatWorkspaceArtifactTarget;
  runId?: string;
  reviewArtifacts?: boolean;
  discussion?: DiscussionConversationTarget;
  focusedMessage?: ChatWorkspaceFocusedMessage;
  peekTaskId?: string;
  target?: ChatConversationTarget;
};

const taskPeekOpenedInAppStateKey = "boardlyTaskPeekOpenedInApp";

export function createTaskPeekNavigationState() {
  return { [taskPeekOpenedInAppStateKey]: true } as const;
}

export function taskPeekWasOpenedInApp(state: unknown) {
  return Boolean(
    state &&
    typeof state === "object" &&
    taskPeekOpenedInAppStateKey in state &&
    (state as Record<string, unknown>)[taskPeekOpenedInAppStateKey] === true,
  );
}

const composerDraftStateKey = "boardlyComposerDraft";

// Route state for a `newSession=1` link that opens the new chat with its
// composer already filled in. The text is only a draft: the person reads,
// edits and sends it themselves.
export function createComposerDraftNavigationState(draft: string) {
  return { [composerDraftStateKey]: draft } as const;
}

export function readComposerDraftNavigationState(
  state: unknown,
): string | undefined {
  if (!state || typeof state !== "object") return undefined;
  const draft = (state as Record<string, unknown>)[composerDraftStateKey];
  return typeof draft === "string" && draft.trim() ? draft : undefined;
}

export function parseChatWorkspaceRoute(search: string): ChatWorkspaceRoute {
  const params = new URLSearchParams(search);
  const conversationKind = params.get("conversationKind");
  const conversationId = filledValue(params.get("conversationId"));
  const agentId = filledValue(params.get("agentId"));
  const taskId = filledValue(params.get("taskId"));
  const discussionType = params.get("discussionType");
  const discussion: DiscussionConversationTarget | undefined =
    conversationKind === "discussion" &&
    conversationId &&
    (discussionType === "human_direct" || discussionType === "channel")
      ? {
          kind: "discussion" as const,
          conversationId,
          discussionType,
          ...(filledValue(params.get("messageId"))
            ? { messageId: filledValue(params.get("messageId")) }
            : {}),
          ...(filledValue(params.get("threadRootId"))
            ? { threadRootId: filledValue(params.get("threadRootId")) }
            : {}),
          manage:
            discussionType === "channel" && params.get("view") === "manage",
        }
      : undefined;
  // Older links addressed an Agent as a "department" room; that is a direct
  // chat with the same Agent now.
  const target =
    (conversationKind === "direct" || conversationKind === "department") &&
    agentId
      ? ({
          agentId,
          ...(conversationId ? { conversationId } : {}),
          kind: "direct",
        } satisfies ChatConversationTarget)
      : conversationKind === "task" && taskId
        ? ({
            ...(agentId ? { agentId } : {}),
            ...(conversationId ? { conversationId } : {}),
            kind: "task",
            taskId,
          } satisfies ChatConversationTarget)
        : undefined;
  const messageId = filledValue(params.get("messageId"));
  const messageSeq = Number(params.get("messageSeq"));
  const focusedMessage =
    conversationId &&
    messageId &&
    Number.isSafeInteger(messageSeq) &&
    messageSeq > 0
      ? { conversationId, messageId, messageSeq }
      : undefined;
  const artifactId = filledValue(params.get("artifactId"));
  const runId = filledValue(params.get("runId"));
  const fileId = filledValue(params.get("fileId"));
  const artifact =
    target && conversationId && artifactId && runId
      ? { artifactId, runId, ...(fileId ? { fileId } : {}) }
      : undefined;

  return {
    ...(artifact ? { artifact } : {}),
    ...(target && conversationId && runId ? { runId } : {}),
    ...(target && params.get("artifacts") === "review"
      ? { reviewArtifacts: true }
      : {}),
    ...(discussion ? { discussion } : {}),
    ...(focusedMessage ? { focusedMessage } : {}),
    ...(filledValue(params.get("peekTaskId"))
      ? { peekTaskId: filledValue(params.get("peekTaskId")) }
      : {}),
    ...(target ? { target } : {}),
  };
}

export function createChatWorkspaceHref(
  target: ChatConversationTarget | DiscussionConversationTarget,
  options: {
    artifact?: ChatWorkspaceArtifactTarget;
    runId?: string;
    reviewArtifacts?: boolean;
    focusedMessage?: ChatWorkspaceFocusedMessage;
    peekTaskId?: string;
  } = {},
) {
  const params = new URLSearchParams();
  params.set("conversationKind", target.kind);
  if ("agentId" in target && target.agentId)
    params.set("agentId", target.agentId);
  if (target.kind === "discussion") {
    params.set("discussionType", target.discussionType);
    if (target.manage) params.set("view", "manage");
    if (target.messageId) params.set("messageId", target.messageId);
    if (target.threadRootId) params.set("threadRootId", target.threadRootId);
  }
  if (target.conversationId) {
    params.set("conversationId", target.conversationId);
  }
  if (target.kind === "task") params.set("taskId", target.taskId);
  if (options.focusedMessage) {
    params.set("conversationId", options.focusedMessage.conversationId);
    params.set("messageId", options.focusedMessage.messageId);
    params.set("messageSeq", String(options.focusedMessage.messageSeq));
  }
  if (options.peekTaskId) params.set("peekTaskId", options.peekTaskId);
  if (options.runId) params.set("runId", options.runId);
  if (options.artifact) {
    params.set("artifactId", options.artifact.artifactId);
    params.set("runId", options.artifact.runId);
    if (options.artifact.fileId) params.set("fileId", options.artifact.fileId);
  }
  if (options.reviewArtifacts) params.set("artifacts", "review");

  return `/app?${params.toString()}`;
}

export function sameChatConversationTarget(
  left: ChatConversationTarget | undefined,
  right: ChatConversationTarget | undefined,
) {
  return Boolean(
    left &&
    right &&
    chatConversationTargetKey(left) === chatConversationTargetKey(right),
  );
}

function filledValue(value: string | null) {
  const normalized = value?.trim();
  return normalized || undefined;
}
