import { conversationApi } from "@/shared/api/conversation-api";
import type { FileTransferHandler } from "@/shared/chat/attachment-preview";
import { useCallback, useEffect, useRef, useState } from "react";
import { useStore } from "react-redux";
import {
  createTeamChatMessage,
  formatChatTimestamp,
} from "@/features/chat-panel/utils";
import type { AgentRealtimeConnectionStatus } from "@/features/chat-panel/model/chat-connection-recovery";
import { mergeRuntimeActivities } from "@/features/chat-panel/model/chat-runtime-trace";
import {
  assistantTextFromBlocks,
  completeAssistantTextBlock,
  completeReasoningTextBlock,
} from "@/features/chat-panel/model/canonical-content-blocks";
import {
  StableChatMessageIndex,
  type StableChatMessage,
} from "@/features/chat-panel/model/chat-message-convergence";
import {
  adoptableStoredEnvelope,
  clientStreamingKey,
  committedAgentSequencesForRun,
  conversationRunStreamingKey,
  conversationStreamingAliasKey,
  rebindStreamingStateToDurableRow,
  retireCompletedStreamingAliases,
  sameStreamingAssistant,
  type StreamingAssistantState,
} from "@/features/chat-panel/model/chat-streaming-identity";
import {
  appendNewerHistoryWindow,
  liveRunTimelineSegment,
  mergeDurableRunTimeline,
  mergeLatestHistoryWindow,
  mergeTraceList,
  prependOlderHistoryWindow,
  runTimelineFromRunMessages,
  type DurableConversationHistory,
  type DurableRunTimeline,
} from "@/features/chat-panel/model/chat-timeline";
import {
  publishQueuedReceipt,
  requestConversationRunQueueRefresh,
} from "@/features/chat-panel/hooks/use-conversation-run-queue";
import { CEO_CHAT_TEAM_ID, type ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";
import { zenithTeamApi, conversationService, runService } from "@/shared/api";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type { TaskWorkspaceResponse } from "@/shared/api/task.service";
import type {
  ActorRefDto,
  ConversationDisplayItemSummary,
  RunDto,
} from "@/shared/api/types";
import {
  createConversationSocketSession,
  ConversationMessageRejectedError,
  describeConversationSendFailure,
  type ConversationRunStreamEvent,
} from "@/shared/realtime/conversation-socket.service";
import {
  appendChatMessage,
  setTeamChatMessages,
  updateChatMessage,
  useAppDispatch,
  type RootState,
} from "@/state";
import type { RunFailureSource } from "@/features/chat-panel/model/agent-chat-transcript";
import { mergeConversationRuns } from "@/features/chat-panel/model/agent-chat-transcript";
import type { RuntimeTraceSnapshot } from "@/features/chat-panel/model/agent-chat-pending-history";
import type { ChatMessageIdentity } from "@/features/chat-panel/model/agent-chat-transcript";
import { agentIsThinkingAfterRunEvent } from "@/features/chat-panel/model/agent-chat-transcript";
import { runFailureSourceFromLifecycle } from "@/features/chat-panel/model/agent-chat-transcript";
import { TERMINAL_RUN_STATUSES } from "@/features/chat-panel/model/agent-chat-transcript";
import { stoppedMessage } from "@/features/chat-panel/model/agent-chat-transcript";
import { appendAssistantTerminalTextBlock } from "@/features/chat-panel/model/agent-chat-transcript";
import { runFailureFromRun } from "@/features/chat-panel/model/agent-chat-transcript";
import { rememberPendingHistoryTrace } from "@/features/chat-panel/model/agent-chat-pending-history";
import { runIdFromDisplayItem } from "@/features/chat-panel/model/agent-chat-transcript";
import { loadDurableConversationTranscript } from "@/features/chat-panel/api/agent-chat-history";
import { attachRunFailures } from "@/features/chat-panel/model/agent-chat-transcript";
import { displayItemsToChatMessages } from "@/features/chat-panel/model/agent-chat-transcript";
import { mergePendingHistoryReplay } from "@/features/chat-panel/model/agent-chat-pending-history";
import { mergeActiveStreamingMessages } from "@/features/chat-panel/model/agent-chat-pending-history";
import { activeStreamingStatesForConversation } from "@/features/chat-panel/model/agent-chat-pending-history";
import { loadConversationRuns } from "@/features/chat-panel/api/agent-chat-history";
import { removePendingUserMessage } from "@/features/chat-panel/model/agent-chat-pending-history";
import { HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX } from "@/features/chat-panel/model/agent-chat-transcript";
import { rememberPendingUserMessage } from "@/features/chat-panel/model/agent-chat-pending-history";
import { uploadMessageResources } from "@/features/chat-panel/api/agent-chat-uploads";
import { stampPendingUserMessage } from "@/features/chat-panel/model/agent-chat-pending-history";

const HISTORY_LOAD_ERROR = "Earlier messages could not be loaded.";

type ConversationSocketSession = ReturnType<
  typeof createConversationSocketSession
>;

type DurableHistoryReplay = (
  messageTeam: ChatTeamId,
  conversationId: string,
) => Promise<string>;

type ChatHistoryPaginationState = {
  conversationId: string | null;
  error: string | null;
  hasOlder: boolean;
  isLoading: boolean;
};

export type SendAgentMessageOptions = {
  onFileTransfer?: FileTransferHandler;
  clientMessageId?: string;
  requestKey?: string;
  displayUserMessage?: boolean;
  hiddenTranscript?: boolean;
};

type ActiveConversationSocket = {
  conversationId: string;
  socket: ConversationSocketSession;
  unsubscribe: () => void;
  unsubscribeRecovery: () => void;
  unsubscribeStatus: () => void;
};

type ResolveConversation = (team: ChatTeamId) => Promise<string | null>;

type ChatMessageChanges = Partial<Omit<StableChatMessage, "id" | "team">>;

type ChatMessageStore = {
  appendMessage?: (message: ChatMessage) => void;
  setMessages?: (team: ChatTeamId, messages: ChatMessage[]) => void;
  updateMessage?: (
    team: ChatTeamId,
    id: number,
    changes: ChatMessageChanges,
  ) => void;
};

type UseAgentRealtimeChatOptions = {
  assistantActor?: ActorRefDto;
  conversationKey?: string | null;
  messageStore?: ChatMessageStore;
  loadInitialConversation?: () => Promise<string | null>;
  loadInitialTaskWorkspace?: () => Promise<TaskWorkspaceResponse | null>;
  loadActiveRunIds?: (conversationId: string) => Promise<string[]>;
  resolveConversation: ResolveConversation;
  taskId?: string;
  team: ChatTeamId;
  unavailableMessage?: string;
};

export function useAgentRealtimeChat({
  assistantActor,
  conversationKey,
  loadInitialConversation,
  loadInitialTaskWorkspace,
  loadActiveRunIds,
  messageStore,
  resolveConversation,
  taskId,
  team,
  unavailableMessage = "The agent is not connected right now.",
}: UseAgentRealtimeChatOptions) {
  const dispatch = useAppDispatch();
  const reduxStore = useStore<RootState>();
  const [loadHistoryQuery] =
    conversationApi.useLazyGetWorkspaceConversationHistoryQuery();
  const [connectionStatus, setConnectionStatus] =
    useState<AgentRealtimeConnectionStatus>("idle");
  const [activeConversationId, setActiveConversationId] = useState<
    string | null
  >(null);
  const activeConversationIdRef = useRef<string | null>(null);
  const activeTeamRef = useRef<ChatTeamId>(team);
  const conversationByTeamRef = useRef<Record<string, string>>({});
  const teamByConversationRef = useRef<Record<string, ChatTeamId>>({});
  const conversationSocketRef = useRef<ActiveConversationSocket | null>(null);
  const durableHistoryReplayRef = useRef<DurableHistoryReplay | null>(null);
  // The conversation's Runs as last listed, plus the ones lifecycle frames
  // reported since, joined into durable history so failures survive a reload
  // and a scroll-back.
  const runsByConversationRef = useRef<Record<string, RunFailureSource[]>>({});
  const rememberConversationRun = useCallback(
    (conversationId: string, run: RunFailureSource) => {
      runsByConversationRef.current[conversationId] = mergeConversationRuns(
        runsByConversationRef.current[conversationId] ?? [],
        [run],
      );
    },
    [],
  );
  const durableHistoryByConversationRef = useRef<
    Record<string, DurableConversationHistory>
  >({});
  const durableRunTimelineByRunRef = useRef<Record<string, DurableRunTimeline>>(
    {},
  );
  const durableConversationTranscriptLoadRef = useRef<
    Map<string, Promise<void>>
  >(new Map());
  const durableConversationTranscriptThroughSeqRef = useRef<
    Record<string, number>
  >({});
  const taskWorkspaceLoadByKeyRef = useRef<
    Map<string, Promise<TaskWorkspaceResponse | null>>
  >(new Map());
  const appliedTaskWorkspaceByKeyRef = useRef<Record<string, string>>({});
  const assistantActorByConversationRef = useRef<Record<string, ActorRefDto>>(
    {},
  );
  const historyLoadInFlightRef = useRef<Set<string>>(new Set());
  const pendingHistoryTraceByConversationRef = useRef<
    Record<string, RuntimeTraceSnapshot>
  >({});
  const pendingUserMessagesByConversationRef = useRef<
    Record<string, StableChatMessage[]>
  >({});
  const streamingKeyByConversationRef = useRef<Record<string, string>>({});
  const streamingByKeyRef = useRef<Record<string, StreamingAssistantState>>({});
  const completedByKeyRef = useRef<Record<string, StreamingAssistantState>>({});
  const storedMessageIndexRef = useRef(new StableChatMessageIndex());
  const messageCounterRef = useRef(0);
  const [historyPagination, setHistoryPagination] =
    useState<ChatHistoryPaginationState>({
      conversationId: null,
      error: null,
      hasOlder: false,
      isLoading: false,
    });

  const appendStoredMessage = useCallback(
    (message: ChatMessage) => {
      const stableMessage = message as StableChatMessage;
      storedMessageIndexRef.current.remember(stableMessage);
      if (messageStore?.appendMessage) {
        messageStore.appendMessage(message);
        return;
      }
      dispatch(appendChatMessage(message));
    },
    [dispatch, messageStore],
  );

  const setStoredMessages = useCallback(
    (messageTeam: ChatTeamId, messages: ChatMessage[]) => {
      storedMessageIndexRef.current.replaceTeam(
        messageTeam,
        messages as StableChatMessage[],
      );
      if (messageStore?.setMessages) {
        messageStore.setMessages(messageTeam, messages);
        return;
      }
      dispatch(setTeamChatMessages({ messages, team: messageTeam }));
    },
    [dispatch, messageStore],
  );

  const updateStoredMessage = useCallback(
    (messageTeam: ChatTeamId, id: number, changes: ChatMessageChanges) => {
      const assistantChanges = {
        author: "team" as const,
        ...changes,
      };
      storedMessageIndexRef.current.update(messageTeam, id, assistantChanges);
      if (messageStore?.updateMessage) {
        messageStore.updateMessage(messageTeam, id, assistantChanges);
        return;
      }
      dispatch(
        updateChatMessage({ changes: assistantChanges, id, team: messageTeam }),
      );
    },
    [dispatch, messageStore],
  );

  const nextAssistantMessageId = useCallback(() => {
    messageCounterRef.current += 1;
    return Date.now() + messageCounterRef.current;
  }, []);

  const resumeRun = useCallback(
    (
      socket: ConversationSocketSession,
      runId: string,
      conversationId: string,
    ) => {
      const message = storedMessageIndexRef.current.findByAgentRunId(runId);
      return socket.resumeRun(runId, {
        ...(message?.conversationId === conversationId &&
        message.clientMessageId
          ? { clientRequestId: message.clientMessageId }
          : {}),
        conversationId,
      });
    },
    [],
  );

  const ensureStreamingMessage = useCallback(
    (
      messageTeam: ChatTeamId,
      key: string,
      {
        actor,
        identity,
        isThinking = true,
      }: {
        actor?: ActorRefDto;
        identity?: ChatMessageIdentity;
        isThinking?: boolean;
      } = {},
    ): StreamingAssistantState => {
      const existing = streamingByKeyRef.current[key];

      if (existing) {
        const shouldAttachActor = Boolean(actor && !existing.actor);
        if (identity || shouldAttachActor) {
          streamingByKeyRef.current[key] = {
            ...existing,
            ...(shouldAttachActor ? { actor } : {}),
            ...identity,
          };
          if (shouldAttachActor && actor) {
            updateStoredMessage(existing.team, existing.id, { actor });
          }
          return streamingByKeyRef.current[key];
        }
        return existing;
      }

      const nextIdentity = identity;

      // A row already committed for this Run — by its message id, or by the
      // Run itself when a chat opens or reconnects while the Run is live —
      // is the bubble to keep streaming into. Appending beside it would
      // show the agent's words twice (the committed item supersedes
      // scratch). A row bound to another live key stays that key's.
      const storedEnvelope = adoptableStoredEnvelope(
        storedMessageIndexRef.current,
        streamingByKeyRef.current,
        messageTeam,
        nextIdentity,
      );
      if (storedEnvelope) {
        const restored: StreamingAssistantState = {
          ...storedEnvelope,
          ...nextIdentity,
          actor: storedEnvelope.actor ?? actor,
          isThinking: storedEnvelope.isThinking ?? isThinking,
          team: messageTeam,
          text: storedEnvelope.text,
        };
        streamingByKeyRef.current[key] = restored;
        updateStoredMessage(messageTeam, storedEnvelope.id, {
          ...nextIdentity,
          ...(restored.actor ? { actor: restored.actor } : {}),
          isStreaming: true,
          isThinking: restored.isThinking,
        });
        return restored;
      }

      const message: StableChatMessage = {
        ...createTeamChatMessage({
          ...nextIdentity,
          id: nextAssistantMessageId(),
          isThinking,
          isStreaming: true,
          team: messageTeam,
          text: "",
        }),
        ...(actor ? { actor } : {}),
        ...(nextIdentity?.messageId
          ? { messageId: nextIdentity.messageId }
          : {}),
        ...(nextIdentity?.messageSequence === undefined
          ? {}
          : { messageSequence: nextIdentity.messageSequence }),
      };
      const next = {
        ...nextIdentity,
        ...(actor ? { actor } : {}),
        id: message.id,
        isThinking,
        team: messageTeam,
        text: "",
      };

      streamingByKeyRef.current[key] = next;
      appendStoredMessage(message);
      return next;
    },
    [appendStoredMessage, nextAssistantMessageId, updateStoredMessage],
  );

  const renderRunEvent = useCallback(
    (
      event: ConversationRunStreamEvent,
      messageTeam: ChatTeamId,
    ): { key: string; state: StreamingAssistantState } | undefined => {
      const key = conversationRunStreamingKey(
        messageTeam,
        event,
        streamingByKeyRef.current,
      );
      if (completedByKeyRef.current[key] && !streamingByKeyRef.current[key]) {
        return undefined;
      }

      if (event.kind === "message") {
        durableRunTimelineByRunRef.current[event.runId] =
          mergeDurableRunTimeline(
            durableRunTimelineByRunRef.current[event.runId],
            event.message,
          );
      }
      const timeline = durableRunTimelineByRunRef.current[event.runId];
      if (
        event.kind === "scratch_reset" &&
        !timeline &&
        !streamingByKeyRef.current[key]
      ) {
        return undefined;
      }

      const identity: ChatMessageIdentity = {
        ...(event.clientRequestId
          ? { clientMessageId: event.clientRequestId }
          : {}),
        conversationId: event.conversationId,
        runId: event.runId,
      };
      const actor =
        assistantActorByConversationRef.current[event.conversationId] ??
        assistantActor;
      const current = ensureStreamingMessage(messageTeam, key, {
        actor,
        identity,
      });
      // A bubble that adopted a committed row shows that row's segment of
      // the Run — everything after the previous committed agent message —
      // so the earlier rows keep their own words and nothing repeats.
      const display = runTimelineFromRunMessages(
        current.messageSequence === undefined
          ? (timeline?.items ?? [])
          : liveRunTimelineSegment(
              timeline?.items ?? [],
              current.messageSequence,
              committedAgentSequencesForRun(
                storedMessageIndexRef.current,
                messageTeam,
                event.runId,
              ),
            ),
        current.activeSkills,
      );
      let contentBlocks = display.contentBlocks;
      if (event.scratch.thinking) {
        contentBlocks = completeReasoningTextBlock(
          contentBlocks,
          event.scratch.thinking,
          `scratch:thinking:${event.runId}`,
        );
      }
      if (event.scratch.text) {
        contentBlocks = completeAssistantTextBlock(
          contentBlocks,
          event.scratch.text,
          `scratch:text:${event.runId}`,
        );
      }
      const text = assistantTextFromBlocks(contentBlocks);
      const activeTools = mergeTraceList(
        current.activeTools,
        display.activeTools,
      );
      const runtimeActivities = mergeRuntimeActivities(
        current.runtimeActivities,
        display.runtimeActivities,
      );
      const isThinking = agentIsThinkingAfterRunEvent(event);
      const runCreatedAt =
        (event.kind === "lifecycle" ? event.createdAt : undefined) ??
        storedMessageIndexRef.current.findByAgentRunId(event.runId)
          ?.runCreatedAt ??
        runsByConversationRef.current[event.conversationId]?.find(
          (run) => run.id === event.runId,
        )?.createdAt;
      const next: StreamingAssistantState = {
        ...current,
        ...identity,
        ...(activeTools?.length ? { activeTools } : {}),
        ...(contentBlocks?.length ? { contentBlocks } : {}),
        ...(display.generatedFiles?.length
          ? { generatedFiles: display.generatedFiles }
          : {}),
        isThinking,
        ...(runtimeActivities?.length ? { runtimeActivities } : {}),
        text,
      };
      streamingByKeyRef.current[key] = next;
      streamingKeyByConversationRef.current[
        conversationStreamingAliasKey(messageTeam, event.conversationId)
      ] = key;
      updateStoredMessage(current.team, current.id, {
        ...identity,
        activeTools,
        contentBlocks,
        generatedFiles: display.generatedFiles,
        isThinking,
        isStreaming: true,
        ...(event.kind === "lifecycle" ? { runStatus: event.status } : {}),
        ...(runCreatedAt ? { runCreatedAt } : {}),
        runtimeActivities,
        text,
        timestamp: formatChatTimestamp(),
      });
      return { key, state: next };
    },
    [assistantActor, ensureStreamingMessage, updateStoredMessage],
  );

  // A retry child changes what its parent's failure row should say: "retrying"
  // while the child is live, "retried" once it has ended. The failed Run's
  // row is the team row indexed by its Run id — a synthetic failure row
  // carries the Run id too.
  const markParentRetry = useCallback(
    (parentRunId: string, active: boolean) => {
      const row = storedMessageIndexRef.current.findByAgentRunId(parentRunId);
      const failure = row?.runFailure;
      if (
        !row ||
        !failure ||
        (failure.retried && failure.retryActive === active)
      ) {
        return;
      }
      updateStoredMessage(row.team, row.id, {
        runFailure: { ...failure, retried: true, retryActive: active },
      });
    },
    [updateStoredMessage],
  );

  // A lifecycle frame never names the runtime, so a failure only frames
  // reported cannot tell whether the Run ran on this viewer's computer, and
  // its card reads the neutral sentence. Read that Run — a run read names
  // `runtimeId` to the runtime's registrant alone (ADR 0014) — and word the
  // card from it, so the person who ran it gets the fix steps. A failed read
  // keeps the neutral sentence.
  const proveFailedRunComputer = useCallback(
    async (conversationId: string, runId: string) => {
      let run: RunDto;
      try {
        run = await runService.getRun(requireActiveCompanyId(), runId);
      } catch (error) {
        console.warn("The failed run could not be read", error);
        return;
      }
      rememberConversationRun(conversationId, run);
      const row = storedMessageIndexRef.current.findByAgentRunId(runId);
      const failure = row?.runFailure;
      const ranOnViewerComputer = Boolean(run.runtimeId);
      if (
        !row ||
        !failure ||
        failure.runId !== runId ||
        failure.ranOnViewerComputer === ranOnViewerComputer
      ) {
        return;
      }
      updateStoredMessage(row.team, row.id, {
        runFailure: { ...failure, ranOnViewerComputer },
      });
    },
    [rememberConversationRun, updateStoredMessage],
  );

  const handleRunEvent = useCallback(
    (event: ConversationRunStreamEvent) => {
      const messageTeam =
        teamByConversationRef.current[event.conversationId] ??
        activeTeamRef.current;
      const rendered = renderRunEvent(event, messageTeam);
      if (
        event.kind === "lifecycle" &&
        event.status === "queued" &&
        event.parentRunId
      ) {
        // A retry child was enqueued: the parent's failure row should say
        // the server is trying again rather than asking the person to.
        rememberConversationRun(
          event.conversationId,
          runFailureSourceFromLifecycle(event),
        );
        markParentRetry(event.parentRunId, true);
      }
      if (
        event.kind === "lifecycle" &&
        event.parentRunId &&
        TERMINAL_RUN_STATUSES.has(event.status)
      ) {
        markParentRetry(event.parentRunId, false);
      }
      if (
        !rendered ||
        event.kind !== "lifecycle" ||
        (event.status !== "completed" &&
          event.status !== "failed" &&
          event.status !== "cancelled")
      ) {
        return;
      }

      const hasDurableText = rendered.state.text.trim().length > 0;
      const terminalText =
        event.status === "cancelled" && !hasDurableText ? stoppedMessage() : "";
      const contentBlocks = terminalText
        ? appendAssistantTerminalTextBlock(
            rendered.state.contentBlocks,
            terminalText,
          )
        : rendered.state.contentBlocks;
      const completed: StreamingAssistantState = {
        ...rendered.state,
        contentBlocks,
        isThinking: false,
        text: assistantTextFromBlocks(contentBlocks) || rendered.state.text,
      };
      rememberConversationRun(
        event.conversationId,
        runFailureSourceFromLifecycle(event),
      );
      // The merged record, which keeps a runtime an earlier Run list named.
      const conversationRuns =
        runsByConversationRef.current[event.conversationId] ?? [];
      const terminalRun = conversationRuns.find(
        (run) => run.id === event.runId,
      );
      const runFailure =
        event.status === "failed" && terminalRun
          ? runFailureFromRun(
              terminalRun,
              conversationRuns.filter((run) => run.parentRunId === event.runId),
            )
          : undefined;
      updateStoredMessage(completed.team, completed.id, {
        contentBlocks,
        isThinking: false,
        isStreaming: false,
        runStatus: event.status,
        ...(runFailure ? { runFailure } : {}),
        text: completed.text,
        timestamp: formatChatTimestamp(),
      });
      // No Run list named this Run's runtime yet — the usual case for a Run
      // this chat just started — so read it before the card can say whose
      // computer the fix is on.
      if (runFailure && terminalRun?.runtimeId === undefined) {
        void proveFailedRunComputer(event.conversationId, event.runId);
      }
      retireCompletedStreamingAliases(
        streamingByKeyRef.current,
        completedByKeyRef.current,
        streamingKeyByConversationRef.current,
        rendered.key,
        completed,
      );
      if (messageTeam === CEO_CHAT_TEAM_ID) {
        rememberPendingHistoryTrace(
          pendingHistoryTraceByConversationRef.current,
          event.conversationId,
          completed,
        );
      }
    },
    [proveFailedRunComputer, renderRunEvent, updateStoredMessage],
  );

  const disconnectConversationSocket = useCallback(
    (conversationId?: string) => {
      const activeSocket = conversationSocketRef.current;

      if (!activeSocket) {
        return;
      }

      if (conversationId && activeSocket.conversationId !== conversationId) {
        return;
      }

      conversationSocketRef.current = null;
      activeSocket.unsubscribe();
      activeSocket.unsubscribeRecovery();
      activeSocket.unsubscribeStatus();
      activeSocket.socket.disconnect();
    },
    [],
  );

  const ensureConversationSocket = useCallback(
    (conversationId: string): ConversationSocketSession => {
      const currentSocket = conversationSocketRef.current;

      if (currentSocket && currentSocket.conversationId !== conversationId) {
        disconnectConversationSocket(currentSocket.conversationId);
      }

      let activeSocket = conversationSocketRef.current;
      if (!activeSocket) {
        const socket = createConversationSocketSession(taskId, conversationId);
        const recoverConversation = () => {
          void (async () => {
            const messageTeam =
              teamByConversationRef.current[conversationId] ??
              activeTeamRef.current;
            try {
              await durableHistoryReplayRef.current?.(
                messageTeam,
                conversationId,
              );
              if (!loadActiveRunIds) {
                return;
              }
              const runIds = await loadActiveRunIds(conversationId);
              await Promise.all(
                runIds.map((runId) => resumeRun(socket, runId, conversationId)),
              );
            } catch {
              // The shared socket owns reconnect. Recovery is bounded to one
              // durable replay and one active-run discovery per signal.
            }
          })();
        };
        activeSocket = {
          conversationId,
          socket,
          unsubscribe: socket.onRunEvent(handleRunEvent),
          unsubscribeRecovery: socket.onRecovery(recoverConversation),
          unsubscribeStatus: socket.onStatus(setConnectionStatus),
        };
        conversationSocketRef.current = activeSocket;
      }

      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);
      return activeSocket.socket;
    },
    [
      disconnectConversationSocket,
      handleRunEvent,
      loadActiveRunIds,
      resumeRun,
      taskId,
    ],
  );

  const publishHistoryPagination = useCallback(
    (
      conversationId: string,
      error: string | null = null,
      isLoading = false,
    ) => {
      if (activeConversationIdRef.current !== conversationId) {
        return;
      }
      const history = durableHistoryByConversationRef.current[conversationId];
      setHistoryPagination({
        conversationId,
        error,
        hasOlder: history?.hasOlder ?? false,
        isLoading,
      });
    },
    [],
  );

  const hydrateHistoryRunTimelines = useCallback(
    async (
      conversationId: string,
      items: readonly ConversationDisplayItemSummary[],
    ) => {
      const runIds = [
        ...new Set(
          items.flatMap((item) => {
            const runId = runIdFromDisplayItem(item);
            return runId === undefined ? [] : [runId];
          }),
        ),
      ];
      if (runIds.length === 0) return;
      const pending =
        durableConversationTranscriptLoadRef.current.get(conversationId);
      if (pending) {
        await pending;
        return;
      }
      const reloadFromStart = runIds.some(
        (runId) => durableRunTimelineByRunRef.current[runId]?.hasOlder === true,
      );
      const load = (async () => {
        const transcript = await loadDurableConversationTranscript(
          requireActiveCompanyId(),
          conversationId,
          durableRunTimelineByRunRef.current,
          runIds,
          durableConversationTranscriptThroughSeqRef.current[conversationId] ??
            0,
          reloadFromStart,
        );
        for (const [runIdText, timeline] of Object.entries(
          transcript.timelines,
        )) {
          const runId = runIdText;
          let merged = durableRunTimelineByRunRef.current[runId];
          for (const item of timeline.items) {
            merged = mergeDurableRunTimeline(merged, item);
          }
          durableRunTimelineByRunRef.current[runId] = {
            ...merged,
            hasOlder: false,
            items: merged?.items ?? [],
            throughSeq: merged?.throughSeq ?? 0,
          };
        }
        for (const runId of runIds) {
          durableRunTimelineByRunRef.current[runId] ??= {
            hasOlder: false,
            items: [],
            throughSeq: 0,
          };
        }
        durableConversationTranscriptThroughSeqRef.current[conversationId] =
          transcript.throughSeq;
      })();
      durableConversationTranscriptLoadRef.current.set(conversationId, load);
      try {
        await load;
      } finally {
        if (
          durableConversationTranscriptLoadRef.current.get(conversationId) ===
          load
        ) {
          durableConversationTranscriptLoadRef.current.delete(conversationId);
        }
      }
    },
    [],
  );

  const applyDurableHistory = useCallback(
    (
      messageTeam: ChatTeamId,
      conversationId: string,
      history: DurableConversationHistory,
    ) => {
      const mappedMessages = attachRunFailures(
        displayItemsToChatMessages(
          history.items,
          messageTeam,
          conversationId,
          Object.fromEntries(
            Object.entries(durableRunTimelineByRunRef.current).map(
              ([runId, timeline]) => [runId, timeline.items],
            ),
          ),
        ),
        runsByConversationRef.current[conversationId] ?? [],
        messageTeam,
        conversationId,
        { hasOlder: history.hasOlder },
      );
      const pendingTrace =
        pendingHistoryTraceByConversationRef.current[conversationId];
      const pendingUserMessages =
        pendingUserMessagesByConversationRef.current[conversationId];
      const mergeResult = mergePendingHistoryReplay(
        mappedMessages,
        pendingUserMessages,
        pendingTrace,
      );
      for (const durableMessage of mergeResult.messages) {
        const stableMessage = durableMessage as StableChatMessage;
        if (stableMessage.author !== "team" || !stableMessage.messageId) {
          continue;
        }
        const completed: StreamingAssistantState = {
          activeSkills: stableMessage.activeSkills,
          activeTools: stableMessage.activeTools,
          afterUserInputRequestId: stableMessage.afterUserInputRequestId,
          actor: stableMessage.actor,
          clientMessageId: stableMessage.clientMessageId,
          contentBlocks: stableMessage.contentBlocks,
          conversationId: stableMessage.conversationId,
          generatedFiles: stableMessage.generatedFiles,
          id: stableMessage.id,
          isThinking: false,
          itemId: stableMessage.itemId,
          messageId: stableMessage.messageId,
          messageSequence: stableMessage.messageSequence,
          runId: stableMessage.runId,
          runtimeActivities: stableMessage.runtimeActivities,
          team: stableMessage.team,
          text: stableMessage.text,
          turnId: stableMessage.turnId,
          turnKey: stableMessage.turnKey,
        };
        const matchingEntry = Object.entries(streamingByKeyRef.current).find(
          ([, streaming]) => {
            if (!sameStreamingAssistant(streaming, completed)) return false;
            return Boolean(
              (streaming.messageId &&
                streaming.messageId === completed.messageId) ||
              (streaming.runId !== undefined &&
                streaming.runId === completed.runId),
            );
          },
        );
        if (!matchingEntry) {
          continue;
        }
        // Only a Run known to be over retires its bubble; a committed row of
        // a Run still streaming becomes that bubble instead, so the events
        // that follow patch the row rather than being dropped as late.
        const runStatus =
          completed.runId === undefined
            ? undefined
            : runsByConversationRef.current[conversationId]?.find(
                (run) => run.id === completed.runId,
              )?.status;
        if (runStatus !== undefined && TERMINAL_RUN_STATUSES.has(runStatus)) {
          retireCompletedStreamingAliases(
            streamingByKeyRef.current,
            completedByKeyRef.current,
            streamingKeyByConversationRef.current,
            matchingEntry[0],
            completed,
          );
        } else {
          rebindStreamingStateToDurableRow(
            streamingByKeyRef.current,
            matchingEntry[0],
            completed,
          );
        }
      }
      const messages = mergeActiveStreamingMessages(
        mergeResult.messages,
        activeStreamingStatesForConversation(
          streamingByKeyRef.current,
          messageTeam,
          conversationId,
        ),
        pendingUserMessages,
      );

      setStoredMessages(messageTeam, messages);
      if (mergeResult.traceResolved) {
        delete pendingHistoryTraceByConversationRef.current[conversationId];
      }
      if (mergeResult.userMessagesResolved && mergeResult.traceResolved) {
        delete pendingUserMessagesByConversationRef.current[conversationId];
      }
    },
    [setStoredMessages],
  );

  const loadTaskWorkspace = useCallback(
    async (messageTeam: ChatTeamId) => {
      if (!loadInitialTaskWorkspace) {
        return null;
      }
      const cacheKey = conversationKey ?? messageTeam;
      const existing = taskWorkspaceLoadByKeyRef.current.get(cacheKey);
      if (existing) {
        return existing;
      }
      const pending = loadInitialTaskWorkspace();
      taskWorkspaceLoadByKeyRef.current.set(cacheKey, pending);
      try {
        return await pending;
      } catch (error) {
        if (taskWorkspaceLoadByKeyRef.current.get(cacheKey) === pending) {
          taskWorkspaceLoadByKeyRef.current.delete(cacheKey);
        }
        throw error;
      }
    },
    [conversationKey, loadInitialTaskWorkspace],
  );

  const applyTaskWorkspace = useCallback(
    (
      messageTeam: ChatTeamId,
      workspace: TaskWorkspaceResponse,
      shouldApply: () => boolean = () => true,
    ) => {
      if (!shouldApply()) {
        return null;
      }
      const conversationId = workspace.conversation.id;
      // The Agent doing the work streams the replies, never the member it
      // runs for (ADR 0014).
      if (workspace.task.execution.agent) {
        assistantActorByConversationRef.current[conversationId] =
          workspace.task.execution.agent;
      } else {
        delete assistantActorByConversationRef.current[conversationId];
      }
      const cacheKey = conversationKey ?? messageTeam;
      conversationByTeamRef.current[cacheKey] = conversationId;
      teamByConversationRef.current[conversationId] = messageTeam;
      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);

      if (appliedTaskWorkspaceByKeyRef.current[cacheKey] === conversationId) {
        publishHistoryPagination(conversationId);
        return conversationId;
      }

      for (const timeline of workspace.runTimelines) {
        durableRunTimelineByRunRef.current[timeline.runId] = {
          hasOlder: timeline.hasMore,
          items: timeline.items,
          throughSeq: timeline.throughSeq,
        };
      }
      const items = conversationService.displayItemsFromMessageViews(
        workspace.messages,
      );
      const earliestLoadedSeq = items[0]?.sequence ?? null;
      const history: DurableConversationHistory = {
        earliestAvailableSeq: workspace.earliestAvailableSeq,
        earliestLoadedSeq,
        hasOlder:
          earliestLoadedSeq !== null &&
          earliestLoadedSeq > workspace.earliestAvailableSeq,
        items,
        throughSeq: workspace.throughSeq,
      };
      durableHistoryByConversationRef.current[conversationId] = history;
      appliedTaskWorkspaceByKeyRef.current[cacheKey] = conversationId;
      applyDurableHistory(messageTeam, conversationId, history);
      publishHistoryPagination(conversationId);
      // The workspace read carries active Runs only; failed ones are joined
      // in when the conversation converges and lists its Runs.
      return conversationId;
    },
    [applyDurableHistory, conversationKey, publishHistoryPagination],
  );

  useEffect(() => {
    taskWorkspaceLoadByKeyRef.current.clear();
    appliedTaskWorkspaceByKeyRef.current = {};
  }, [loadInitialTaskWorkspace]);

  const loadConversationHistory = useCallback(
    async (
      messageTeam: ChatTeamId,
      conversationId: string,
      {
        shouldApply = () => true,
        revalidateMessages = false,
      }: { shouldApply?: () => boolean; revalidateMessages?: boolean } = {},
    ) => {
      const historyArgument = {
        companyId: requireActiveCompanyId(),
        conversationId,
      };
      if (!taskId && !durableHistoryByConversationRef.current[conversationId]) {
        const cached =
          conversationApi.endpoints.getWorkspaceConversationHistory.select(
            historyArgument,
          )(reduxStore.getState()).data;
        if (cached && shouldApply()) {
          durableHistoryByConversationRef.current[conversationId] = cached;
          applyDurableHistory(messageTeam, conversationId, cached);
          publishHistoryPagination(conversationId);
        }
      }
      // The Run list rides beside the transcript rather than gating it: the
      // history renders as soon as it lands and failed Runs are joined in
      // once the list arrives.
      const runsRequest = loadConversationRuns(conversationId).then((runs) => {
        if (!runs) return;
        runsByConversationRef.current[conversationId] = mergeConversationRuns(
          runsByConversationRef.current[conversationId] ?? [],
          runs,
        );
        const current = durableHistoryByConversationRef.current[conversationId];
        if (
          current &&
          shouldApply() &&
          activeConversationIdRef.current === conversationId
        ) {
          applyDurableHistory(messageTeam, conversationId, current);
        }
      });
      void runsRequest;
      const existing = durableHistoryByConversationRef.current[conversationId];
      // Attachment changes do not advance the message sequence. Recovery must
      // reread every loaded message, including older pages, before catching up.
      const afterSeq =
        revalidateMessages && existing?.earliestLoadedSeq != null
          ? existing.earliestLoadedSeq - 1
          : existing?.throughSeq;
      const catchup =
        taskId && afterSeq !== undefined
          ? await conversationService.loadTaskMessagesAfter(taskId, afterSeq)
          : null;
      if (!shouldApply()) return conversationId;
      const latest =
        catchup ??
        (taskId
          ? await conversationService.loadLatestTaskMessages(
              taskId,
              conversationId,
            )
          : await loadHistoryQuery(historyArgument).unwrap());
      if (!shouldApply()) {
        return conversationId;
      }
      const current = durableHistoryByConversationRef.current[conversationId];
      const history =
        catchup && current
          ? appendNewerHistoryWindow(current, catchup)
          : taskId && existing
            ? latest
            : mergeLatestHistoryWindow(current, latest);
      durableHistoryByConversationRef.current[conversationId] = history;

      const cacheKey = conversationKey ?? messageTeam;

      conversationByTeamRef.current[cacheKey] = conversationId;
      teamByConversationRef.current[conversationId] = messageTeam;
      activeConversationIdRef.current = conversationId;
      setActiveConversationId(conversationId);
      applyDurableHistory(messageTeam, conversationId, history);
      publishHistoryPagination(conversationId);
      // The reply is already durable. Tool and reasoning detail enrich it
      // afterwards instead of keeping the old Thinking bubble on screen.
      void hydrateHistoryRunTimelines(conversationId, history.items)
        .then(() => {
          const latestHistory =
            durableHistoryByConversationRef.current[conversationId];
          if (
            latestHistory &&
            shouldApply() &&
            activeConversationIdRef.current === conversationId
          ) {
            applyDurableHistory(messageTeam, conversationId, latestHistory);
          }
        })
        .catch((error) => {
          console.warn("Conversation run details could not be loaded", error);
        });
      return conversationId;
    },
    [
      applyDurableHistory,
      conversationKey,
      hydrateHistoryRunTimelines,
      loadHistoryQuery,
      publishHistoryPagination,
      reduxStore,
      taskId,
    ],
  );
  durableHistoryReplayRef.current = (messageTeam, conversationId) =>
    loadConversationHistory(messageTeam, conversationId, {
      revalidateMessages: true,
    });

  const connectAndConvergeConversation = useCallback(
    async (
      messageTeam: ChatTeamId,
      conversationId: string,
      socket: ConversationSocketSession,
      shouldContinue: () => boolean = () => true,
    ) => {
      setConnectionStatus("connecting");
      try {
        await Promise.all([
          socket.connect(),
          loadConversationHistory(messageTeam, conversationId, {
            shouldApply: shouldContinue,
          }),
        ]);
        if (!shouldContinue()) return;

        if (loadActiveRunIds) {
          const runIds = await loadActiveRunIds(conversationId);
          if (!shouldContinue()) return;
          await Promise.allSettled(
            runIds.map((runId) => resumeRun(socket, runId, conversationId)),
          );
        }
        if (shouldContinue()) {
          setConnectionStatus("connected");
        }
      } catch {
        if (shouldContinue()) {
          setConnectionStatus("error");
        }
      }
    },
    [loadActiveRunIds, loadConversationHistory, resumeRun],
  );

  const loadOlderMessages = useCallback(async () => {
    const conversationId = activeConversationIdRef.current;
    if (!conversationId || historyLoadInFlightRef.current.has(conversationId)) {
      return;
    }
    const history = durableHistoryByConversationRef.current[conversationId];
    if (!history?.hasOlder || history.earliestLoadedSeq === null) {
      publishHistoryPagination(conversationId);
      return;
    }

    const requestedBeforeSeq = history.earliestLoadedSeq;
    const messageTeam =
      teamByConversationRef.current[conversationId] ?? activeTeamRef.current;
    historyLoadInFlightRef.current.add(conversationId);
    publishHistoryPagination(conversationId, null, true);
    let loadError: string | null = null;

    try {
      const older = taskId
        ? await conversationService.loadTaskMessagesBefore(
            taskId,
            requestedBeforeSeq,
            history.earliestAvailableSeq,
          )
        : await conversationService.loadMessagesBefore(
            conversationId,
            requestedBeforeSeq,
            history.earliestAvailableSeq,
          );
      await hydrateHistoryRunTimelines(conversationId, older.items);
      const current = durableHistoryByConversationRef.current[conversationId];
      if (current?.earliestLoadedSeq === requestedBeforeSeq) {
        const nextHistory = prependOlderHistoryWindow(current, older);
        durableHistoryByConversationRef.current[conversationId] = nextHistory;
        applyDurableHistory(messageTeam, conversationId, nextHistory);
      }
    } catch {
      loadError = HISTORY_LOAD_ERROR;
    } finally {
      historyLoadInFlightRef.current.delete(conversationId);
      publishHistoryPagination(conversationId, loadError);
    }
  }, [
    applyDurableHistory,
    hydrateHistoryRunTimelines,
    publishHistoryPagination,
    taskId,
  ]);

  const ensureConversation = useCallback(
    async (
      messageTeam: ChatTeamId = team,
      beforeHistoryLoad?: (conversationId: string) => void,
    ) => {
      activeTeamRef.current = messageTeam;
      const cacheKey = conversationKey ?? messageTeam;
      const existingConversationId = conversationByTeamRef.current[cacheKey];
      if (existingConversationId) {
        activeConversationIdRef.current = existingConversationId;
        setActiveConversationId(existingConversationId);
        ensureConversationSocket(existingConversationId);
        publishHistoryPagination(existingConversationId);
        // Opening the chat and socket recovery already reconcile history.
        // Sending to an established chat must not wait for that read chain.
        return existingConversationId;
      }

      if (loadInitialTaskWorkspace) {
        const workspace = await loadTaskWorkspace(messageTeam);
        if (workspace) {
          const workspaceConversationId = workspace.conversation.id;
          beforeHistoryLoad?.(workspaceConversationId);
          const conversationId = applyTaskWorkspace(messageTeam, workspace);
          if (conversationId) {
            const socket = ensureConversationSocket(conversationId);
            await connectAndConvergeConversation(
              messageTeam,
              conversationId,
              socket,
            );
            return conversationId;
          }
        }
      }

      const conversationId = await resolveConversation(messageTeam);
      if (!conversationId) {
        setConnectionStatus("error");
        throw new Error("Agent conversation is unavailable.");
      }

      beforeHistoryLoad?.(conversationId);
      const socket = ensureConversationSocket(conversationId);
      await connectAndConvergeConversation(messageTeam, conversationId, socket);
      return conversationId;
    },
    [
      applyTaskWorkspace,
      connectAndConvergeConversation,
      conversationKey,
      ensureConversationSocket,
      loadInitialTaskWorkspace,
      loadTaskWorkspace,
      publishHistoryPagination,
      resolveConversation,
      team,
    ],
  );

  useEffect(() => {
    activeTeamRef.current = team;
  }, [team]);

  useEffect(() => {
    if (!loadInitialConversation && !loadInitialTaskWorkspace) {
      return undefined;
    }

    let cancelled = false;

    async function load() {
      try {
        if (loadInitialTaskWorkspace) {
          const workspace = await loadTaskWorkspace(team);
          if (!workspace || cancelled) {
            return;
          }
          const conversationId = applyTaskWorkspace(
            team,
            workspace,
            () => !cancelled,
          );
          if (!conversationId || cancelled) {
            return;
          }

          const socket = ensureConversationSocket(conversationId);
          await connectAndConvergeConversation(
            team,
            conversationId,
            socket,
            () => !cancelled,
          );
          return;
        }

        const conversationId = await loadInitialConversation?.();
        if (!conversationId || cancelled) {
          return;
        }
        const socket = ensureConversationSocket(conversationId);
        await connectAndConvergeConversation(
          team,
          conversationId,
          socket,
          () => !cancelled,
        );
      } catch {
        if (!cancelled) {
          setConnectionStatus("error");
        }
      }
    }

    void load();
    return () => {
      cancelled = true;
    };
  }, [
    applyTaskWorkspace,
    connectAndConvergeConversation,
    ensureConversationSocket,
    loadInitialConversation,
    loadInitialTaskWorkspace,
    loadTaskWorkspace,
    team,
  ]);

  useEffect(
    () => () => {
      historyLoadInFlightRef.current.clear();
      durableRunTimelineByRunRef.current = {};
      durableConversationTranscriptLoadRef.current.clear();
      durableConversationTranscriptThroughSeqRef.current = {};
      taskWorkspaceLoadByKeyRef.current.clear();
      appliedTaskWorkspaceByKeyRef.current = {};
      assistantActorByConversationRef.current = {};
      storedMessageIndexRef.current.clear();
      disconnectConversationSocket();
    },
    [disconnectConversationSocket],
  );

  const removeOptimisticSend = useCallback(
    (
      messageTeam: ChatTeamId,
      clientMessageId: string,
      assistantKey: string,
      conversationId?: string,
    ) => {
      delete streamingByKeyRef.current[assistantKey];
      delete completedByKeyRef.current[assistantKey];
      for (const [alias, key] of Object.entries(
        streamingKeyByConversationRef.current,
      )) {
        if (key === assistantKey) {
          delete streamingKeyByConversationRef.current[alias];
        }
      }
      if (conversationId) {
        removePendingUserMessage(
          pendingUserMessagesByConversationRef.current,
          conversationId,
          clientMessageId,
        );
      }
      setStoredMessages(
        messageTeam,
        storedMessageIndexRef.current
          .listTeam(messageTeam)
          .filter((item) => item.clientMessageId !== clientMessageId),
      );
    },
    [setStoredMessages],
  );

  const sendAgentMessage = useCallback(
    async (message: ChatMessage, options: SendAgentMessageOptions = {}) => {
      const content =
        (message.text.trim() ? message.text : "") ||
        (message.artifactIds?.length
          ? "Attached artifacts."
          : message.files?.length
            ? "Attached files."
            : "");

      if (!content) {
        return;
      }

      let resolvedConversationId: string | undefined;
      const shouldDisplayUserMessage =
        options.displayUserMessage !== false && !options.hiddenTranscript;
      const clientMessageId =
        options.clientMessageId ??
        `${options.hiddenTranscript ? HIDDEN_TRANSCRIPT_CLIENT_MESSAGE_ID_PREFIX : "ui-"}${message.id}`;
      const assistantKey = clientStreamingKey(message.team, clientMessageId);
      const visibleUserMessage = shouldDisplayUserMessage
        ? {
            ...message,
            clientMessageId,
          }
        : null;

      if (visibleUserMessage) {
        appendStoredMessage(visibleUserMessage);
      }
      const optimisticAssistant = ensureStreamingMessage(
        message.team,
        assistantKey,
        {
          actor: assistantActor,
          identity: {
            clientMessageId,
          },
          isThinking: true,
        },
      );

      try {
        const attachOptimisticMessagesToConversation = (
          conversationId: string,
        ) => {
          if (visibleUserMessage) {
            rememberPendingUserMessage(
              pendingUserMessagesByConversationRef.current,
              conversationId,
              {
                ...visibleUserMessage,
                conversationId,
              },
            );
          }
          const currentAssistant =
            streamingByKeyRef.current[assistantKey] ?? optimisticAssistant;
          const actor =
            assistantActorByConversationRef.current[conversationId] ??
            currentAssistant.actor ??
            assistantActor;
          if (actor) {
            assistantActorByConversationRef.current[conversationId] = actor;
          }
          streamingByKeyRef.current[assistantKey] = {
            ...currentAssistant,
            ...(actor ? { actor } : {}),
            conversationId,
          };
          updateStoredMessage(message.team, currentAssistant.id, {
            ...(actor ? { actor } : {}),
            clientMessageId,
            conversationId,
            isStreaming: true,
            isThinking: currentAssistant.isThinking,
          });
          streamingKeyByConversationRef.current[
            conversationStreamingAliasKey(message.team, conversationId)
          ] = assistantKey;
        };
        const conversationId = await ensureConversation(
          message.team,
          attachOptimisticMessagesToConversation,
        );
        resolvedConversationId = conversationId;
        const socket = ensureConversationSocket(conversationId);
        void socket.connect().catch(() => undefined);
        const resourceAttachments = await uploadMessageResources({
          clientMessageId,
          conversationId,
          files: message.files,
          onFileTransfer: options.onFileTransfer,
        });
        attachOptimisticMessagesToConversation(conversationId);

        const receipt = await socket.sendMessage({
          attachmentIds: resourceAttachments.map((attachment) => attachment.id),
          artifactIds: message.artifactIds,
          skillReferences: message.skillReferences,
          connectorReferences: message.connectorReferences,
          clientRequestId: clientMessageId,
          content,
          conversationId,
          requestKey: options.requestKey,
        });

        if (!receipt.ok) {
          throw new ConversationMessageRejectedError(receipt);
        }
        if (
          receipt.disposition === "queued" ||
          receipt.disposition === "removed"
        ) {
          if (receipt.disposition === "queued" && receipt.queueItem) {
            publishQueuedReceipt({ conversationId, item: receipt.queueItem });
          }
          removeOptimisticSend(
            message.team,
            clientMessageId,
            assistantKey,
            conversationId,
          );
          // The receipt already updated the queue. Release the composer on
          // acceptance while history catches up for this same chat session.
          void loadConversationHistory(message.team, conversationId, {
            shouldApply: () => conversationSocketRef.current?.socket === socket,
          }).catch(() => undefined);
          setConnectionStatus(
            receipt.realtimeConnected ? "connected" : "connecting",
          );
          return;
        }
        const assistant = streamingByKeyRef.current[assistantKey];
        if (assistant && receipt.message) {
          streamingByKeyRef.current[assistantKey] = {
            ...assistant,
            runId: receipt.message.runId,
          };
          updateStoredMessage(message.team, assistant.id, {
            runId: receipt.message.runId,
          });
        }
        // The send receipt names the item the user's words became. Stamping it
        // onto the optimistic bubble is what lets the durable replay match the
        // two rows by key instead of by comparing their text.
        if (visibleUserMessage && receipt.message) {
          const replayedMessage = storedMessageIndexRef.current.findByMessageId(
            receipt.message.id,
          );
          const identity = {
            messageId: receipt.message.id,
            messageSequence: receipt.message.seq,
            runId: receipt.message.runId,
          };
          stampPendingUserMessage(
            pendingUserMessagesByConversationRef.current,
            conversationId,
            clientMessageId,
            identity,
          );
          if (replayedMessage && replayedMessage.id !== visibleUserMessage.id) {
            // Recovery may already have shown the committed message after a
            // lost response. The retry confirms that row, not a second bubble.
            removePendingUserMessage(
              pendingUserMessagesByConversationRef.current,
              conversationId,
              clientMessageId,
            );
            setStoredMessages(
              visibleUserMessage.team,
              storedMessageIndexRef.current
                .listTeam(visibleUserMessage.team)
                .filter((item) => item.id !== visibleUserMessage.id),
            );
          } else {
            storedMessageIndexRef.current.update(
              visibleUserMessage.team,
              visibleUserMessage.id,
              { ...identity, author: "user" },
            );
            if (messageStore?.updateMessage) {
              messageStore.updateMessage(
                visibleUserMessage.team,
                visibleUserMessage.id,
                identity,
              );
            } else {
              dispatch(
                updateChatMessage({
                  changes: identity,
                  id: visibleUserMessage.id,
                  team: visibleUserMessage.team,
                }),
              );
            }
          }
        }
        setConnectionStatus(
          receipt.realtimeConnected ? "connected" : "connecting",
        );
      } catch (error) {
        const failure = describeConversationSendFailure(
          error,
          unavailableMessage,
        );
        removeOptimisticSend(
          message.team,
          clientMessageId,
          assistantKey,
          resolvedConversationId,
        );
        if (resolvedConversationId) {
          if (failure.status === 409) {
            requestConversationRunQueueRefresh(resolvedConversationId);
          }
          await loadConversationHistory(
            message.team,
            resolvedConversationId,
          ).catch(() => undefined);
        }
        if (failure.status === 404 || failure.status === 409) {
          dispatch(
            zenithTeamApi.util.invalidateTags([
              "Conversation",
              "Message",
              "Run",
              "Task",
            ]),
          );
        }
        if (failure.status === null) {
          setConnectionStatus("error");
        }
        throw error;
      }
    },
    [
      appendStoredMessage,
      assistantActor,
      dispatch,
      ensureConversation,
      ensureStreamingMessage,
      ensureConversationSocket,
      loadConversationHistory,
      messageStore,
      removeOptimisticSend,
      setStoredMessages,
      unavailableMessage,
    ],
  );

  const openConversation = useCallback(
    async (conversationId: string, messageTeam: ChatTeamId = team) => {
      activeTeamRef.current = messageTeam;
      const socket = ensureConversationSocket(conversationId);
      await connectAndConvergeConversation(messageTeam, conversationId, socket);
      return conversationId;
    },
    [connectAndConvergeConversation, ensureConversationSocket, team],
  );

  return {
    activeConversationId,
    connectionStatus,
    ensureConversation,
    hasOlderMessages: historyPagination.hasOlder,
    historyLoadError: historyPagination.error,
    isLoadingOlderMessages: historyPagination.isLoading,
    loadOlderMessages,
    openConversation,
    sendAgentMessage,
  };
}
