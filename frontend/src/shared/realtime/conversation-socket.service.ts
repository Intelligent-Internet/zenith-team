import { canonicalUuid } from "@/shared/lib/uuid";
import { readSessionSnapshot } from "@/app/session/session";
import {
  getApiErrorBody,
  getApiErrorCode,
  getApiErrorMessage,
  isApiClientError,
} from "@/lib/api-client";
import { conversationService } from "@/shared/api/conversation.service";
import { runService } from "@/shared/api/run.service";
import type {
  AgentProvider,
  ConversationRunQueueItem,
  ConversationTranscriptEntryDto,
  RunDto,
  SendConversationMessageResponse,
} from "@/shared/api/types";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import {
  boardlyRealtimeClient,
  realtimeEventConversationId,
  type BoardlyRealtimeEvent,
  type BoardlyRealtimeSnapshot,
} from "@/shared/realtime/realtime-client.service";
import {
  FrozenConversationTranscriptCatchUp,
  runMessageTranscriptHead,
  sameRunTranscriptHead,
  terminalRunTranscriptHead,
  type RunTranscriptHead,
} from "@/shared/realtime/run-event-catch-up.service";
import {
  subscribeToLocalRunPreview,
  type LocalRunPreviewFrame,
} from "@/shared/realtime/local-run-preview.service";

// Not a second source of status — the payload is. This is the accept-list of
// run status event types, and the cross-check that rejects a frame whose type
// and payload disagree.
const RUN_STATUS_BY_EVENT = {
  "run.cancelled": "cancelled",
  "run.completed": "completed",
  "run.dispatched": "dispatched",
  "run.auth_wait_changed": "running",
  "run.failed": "failed",
  "run.queued": "queued",
  "run.running": "running",
  "run.waiting_local_directory": "waiting_local_directory",
} as const satisfies Record<string, RunDto["status"]>;
const TERMINAL_RUN_EVENT_TYPES = new Set([
  "run.completed",
  "run.failed",
  "run.cancelled",
]);
const LOCAL_PREVIEW_MAX_CHARS_PER_ITEM = 64 << 10;
const LOCAL_PREVIEW_MAX_CHARS_PER_RUN = 256 << 10;
const LOCAL_PREVIEW_MAX_ITEMS_PER_RUN = 128;

export type ConversationRealtimeStatus = "connected" | "connecting" | "error";

export type ConversationRunContext = {
  clientRequestId?: string;
  conversationId: string;
};

export type ConversationRunScratch = {
  text: string;
  thinking: string;
};

type ConversationRunEventBase = ConversationRunContext & {
  runId: string;
  scratch: ConversationRunScratch;
};

// The failure copy a `run.failed` frame carries, so the chat can say what
// went wrong from the frame alone (see docs/api/runs.md, "Show
// failureMessage, then failureDetail").
export type ConversationRunFailure = {
  detail: string | null;
  message: string | null;
  provider: AgentProvider | null;
  reason: string | null;
};

export type ConversationRunStreamEvent =
  | (ConversationRunEventBase & {
      channel: "text" | "thinking";
      content: string;
      itemKey?: string;
      kind: "delta";
    })
  | (ConversationRunEventBase & {
      kind: "message";
      message: ConversationTranscriptEntryDto;
    })
  | (ConversationRunEventBase & {
      attempt?: number;
      createdAt?: RunDto["createdAt"];
      failure?: ConversationRunFailure;
      kind: "lifecycle";
      maxAttempts?: number;
      parentRunId?: string | null;
      status: RunDto["status"];
      // The server's clock for this state, when the frame carries it.
      updatedAt?: string;
    })
  | (ConversationRunEventBase & {
      kind: "scratch_reset";
      reason: "catch_up" | "reconnect" | "terminal";
    });

export type ConversationMessageCommand = {
  artifactIds?: string[];
  attachmentIds: string[];
  clientRequestId: string;
  content: string;
  conversationId: string;
  requestKey?: string;
};

export type ConversationMessageReceipt =
  | {
      accepted: false;
      code: string | null;
      detail: string | null;
      error: string;
      networkError: boolean;
      ok: false;
      status: number | null;
    }
  | {
      accepted: true;
      disposition: "message" | "queued" | "removed";
      // The appended item's own identity, present only on the `message`
      // disposition. It is what lets the caller bind its optimistic bubble to
      // the durable row by key instead of by comparing rendered text.
      message?: { id: string; runId: string; seq: number };
      ok: true;
      queueItem?: ConversationRunQueueItem;
      realtimeConnected: boolean;
    };

export class ConversationMessageRejectedError extends Error {
  readonly code: string | null;
  readonly detail: string | null;
  readonly networkError: boolean;
  readonly status: number | null;

  constructor(receipt: Extract<ConversationMessageReceipt, { ok: false }>) {
    super(receipt.detail ?? receipt.error);
    this.name = "ConversationMessageRejectedError";
    this.code = receipt.code;
    this.detail = receipt.detail;
    this.networkError = receipt.networkError;
    this.status = receipt.status;
  }
}

export type ConversationSendFailure = {
  code: string | null;
  detail: string | null;
  message: string;
  status: number | null;
};

export function describeConversationSendFailure(
  error: unknown,
  fallback = "The message could not be sent.",
): ConversationSendFailure {
  const rejected =
    error instanceof ConversationMessageRejectedError ? error : null;
  const status =
    rejected?.status ?? (isApiClientError(error) ? error.status : null);
  const code = rejected?.code ?? getApiErrorCode(error);
  const detail = rejected?.detail ?? getApiErrorBody(error)?.detail ?? null;

  if (code === "run_queue_full") {
    return {
      code,
      detail,
      message:
        "The queue is full. Remove a queued prompt or wait for one to start, then send again.",
      status,
    };
  }
  if (code === "run_queue_conflict") {
    return {
      code,
      detail,
      message:
        "The queue changed while you were sending. Review the restored queue before sending again.",
      status,
    };
  }
  if (code === "conversation_initialization_pending") {
    return {
      code,
      detail,
      message:
        "Your CEO has not started researching your company yet. Send your draft in a moment, or use another chat meanwhile.",
      status,
    };
  }
  if (code === "conversation_conflict") {
    return {
      code,
      detail,
      message:
        "This conversation changed and cannot accept the message. Review its current Agent or Task state before sending again.",
      status,
    };
  }
  if (status === 403) {
    return {
      code,
      detail,
      message: "You no longer have permission to send in this conversation.",
      status,
    };
  }
  if (status === 404) {
    return {
      code,
      detail,
      message:
        "This conversation no longer exists. Open another conversation to continue.",
      status,
    };
  }
  if (
    rejected?.networkError ||
    (isApiClientError(error) && error.status === null)
  ) {
    return {
      code,
      detail,
      message:
        "Zenith Team could not be reached. Check your connection before sending again.",
      status,
    };
  }

  return {
    code,
    detail,
    message:
      detail?.trim() ||
      (error instanceof Error && error.message.trim()) ||
      fallback,
    status,
  };
}

export class ConversationSocketSession {
  private disposed = false;
  private recovering = false;
  private readonly eventHandlers = new Set<
    (event: ConversationRunStreamEvent) => void
  >();
  private readonly recoveryHandlers = new Set<() => void>();
  private readonly pendingSends = new Map<
    Promise<ConversationMessageReceipt>,
    string
  >();
  private readonly deferredQueuedRuns = new Map<string, BoardlyRealtimeEvent>();
  private recoveryPendingAfterSend = false;
  private conversationCatchUp:
    | {
        catchUp: FrozenConversationTranscriptCatchUp;
        promise: Promise<void>;
        rerun: boolean;
        targetsFrozen: boolean;
        targetHeads: Map<string, RunTranscriptHead>;
      }
    | undefined;
  private readonly runContext = new Map<string, ConversationRunContext>();
  // Which Runs have an authoritative committed head. `runMessageThroughSeq`
  // always holds a number; this says whether that number is the Run's head or
  // just the last row applied by an in-flight HTTP page.
  private readonly runsWithKnownTranscriptHead = new Set<string>();
  private readonly runMessageThroughSeq = new Map<string, number>();
  private readonly runPreviewItems = new Map<
    string,
    Map<string, Partial<ConversationRunScratch>>
  >();
  private readonly runScratch = new Map<string, ConversationRunScratch>();
  private readonly committedRunItemKeys = new Map<string, Set<string>>();
  private readonly statusHandlers = new Set<
    (status: ConversationRealtimeStatus) => void
  >();
  private readonly terminalRunIds = new Set<string>();
  private readonly trackedRunIds = new Set<string>();
  private readonly removeEventListener: () => void;
  private readonly removeLocalPreviewListener: () => void;
  private readonly removeRecoveryListener: () => void;
  private readonly removeStatusListener: () => void;

  constructor(
    private readonly activeCompanyId: () => string | undefined = () =>
      readSessionSnapshot().company?.id,
    private readonly taskId?: string,
    private readonly conversationId?: string,
  ) {
    this.removeEventListener = boardlyRealtimeClient.onEvent((event) => {
      this.handleEvent(event);
    });
    this.removeLocalPreviewListener = subscribeToLocalRunPreview((frame) => {
      this.handleLocalPreview(frame);
    });
    this.removeRecoveryListener = boardlyRealtimeClient.onRecovery(() => {
      this.handleRecovery();
    });
    this.removeStatusListener = boardlyRealtimeClient.onStatus((snapshot) => {
      const status = conversationStatus(snapshot);
      this.statusHandlers.forEach((handler) => handler(status));
    });
  }

  onRunEvent(handler: (event: ConversationRunStreamEvent) => void): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  onRecovery(handler: () => void): () => void {
    this.recoveryHandlers.add(handler);
    return () => this.recoveryHandlers.delete(handler);
  }

  onStatus(handler: (status: ConversationRealtimeStatus) => void): () => void {
    this.statusHandlers.add(handler);
    handler(conversationStatus(boardlyRealtimeClient.getSnapshot()));
    return () => this.statusHandlers.delete(handler);
  }

  async connect(): Promise<void> {
    const companyId = this.activeCompanyId();
    if (!companyId) throw new Error("Active Company is unavailable");
    await boardlyRealtimeClient.ensureReady(companyId);
    if (!this.disposed) {
      this.statusHandlers.forEach((handler) => handler("connected"));
    }
  }

  async sendMessage(
    command: ConversationMessageCommand,
  ): Promise<ConversationMessageReceipt> {
    const pending = this.postMessage(command);
    this.pendingSends.set(pending, command.conversationId);
    try {
      return await pending;
    } finally {
      this.pendingSends.delete(pending);
      if (!this.disposed) {
        for (const [runId, event] of this.deferredQueuedRuns) {
          const conversationId = realtimeEventConversationId(event);
          if (
            conversationId &&
            ![...this.pendingSends.values()].includes(conversationId)
          ) {
            this.deferredQueuedRuns.delete(runId);
            this.handleEvent(event);
          }
        }
        if (this.pendingSends.size === 0 && this.recoveryPendingAfterSend) {
          this.recoveryPendingAfterSend = false;
          this.notifyRecovery();
        }
      }
    }
  }

  private async postMessage(
    command: ConversationMessageCommand,
  ): Promise<ConversationMessageReceipt> {
    const companyId = this.activeCompanyId();
    if (!companyId) {
      return rejectedReceipt(new Error("Active Company is unavailable"));
    }
    if (!this.acceptsConversation(command.conversationId)) {
      return rejectedReceipt(new Error("Conversation identity is invalid"));
    }

    const requestKey = command.requestKey ?? generateUuidV7();

    let response: SendConversationMessageResponse;
    try {
      const request = {
        attachmentIds: command.attachmentIds,
        artifactIds: command.artifactIds,
        content: command.content,
        requestKey,
      };
      response = this.taskId
        ? await conversationService.postTaskMessage(this.taskId, request)
        : await conversationService.postMessage(
            command.conversationId,
            request,
          );
    } catch (error) {
      return rejectedReceipt(error, "Message send failed");
    }

    const result = response;
    let appended: { id: string; runId: string; seq: number } | undefined;
    if (result.disposition === "message") {
      // Every item belongs to a run, so an accepted immediate send always
      // names one, on the item and on the receipt, and the two must agree.
      if (
        !result.message ||
        result.message.conversationId !== command.conversationId ||
        result.runId === undefined ||
        result.runId !== result.message.runId
      ) {
        return rejectedReceipt(
          new Error("Conversation response identity is invalid"),
        );
      }
      if (
        !this.trackRun(result.message.runId, {
          clientRequestId: command.clientRequestId,
          conversationId: command.conversationId,
        })
      ) {
        return rejectedReceipt(
          new Error("Conversation response identity is invalid"),
        );
      }
      appended = {
        id: result.message.id,
        runId: result.message.runId,
        seq: result.message.seq,
      };
      void this.recoverRun(result.message.runId, "catch_up").catch(() => {
        this.notifyRecovery();
      });
    } else if (result.disposition === "queued") {
      // A prompt still waiting behind a live run has no run identity yet —
      // `runs.id` is taken by the promotion transaction. Follow the queue
      // item until its own `run.queued` frame arrives.
      if (!result.queueItem) {
        return rejectedReceipt(
          new Error("Conversation queue response identity is invalid"),
        );
      }
    }

    return {
      accepted: true,
      disposition: result.disposition,
      ...(appended ? { message: appended } : {}),
      ok: true,
      ...(result.queueItem ? { queueItem: result.queueItem } : {}),
      realtimeConnected: boardlyRealtimeClient.getSnapshot().status === "ready",
    };
  }

  async resumeRun(
    runId: string,
    context: ConversationRunContext,
  ): Promise<void> {
    // Recovery can discover the Run before the send response identifies it.
    // Let that receipt attach its client request before any stream resumes.
    await Promise.allSettled(
      [...this.pendingSends]
        .filter(
          ([, conversationId]) => conversationId === context.conversationId,
        )
        .map(([pending]) => pending),
    );
    if (this.disposed || this.terminalRunIds.has(runId)) return;
    if (!this.acceptsConversation(context.conversationId)) {
      throw new Error("Run resume Conversation is invalid");
    }
    if (!this.trackRun(runId, context)) {
      throw new Error("Run resume identity conflicts");
    }
    await this.recoverRun(runId, "catch_up");
  }

  disconnect(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.deferredQueuedRuns.clear();
    this.conversationCatchUp = undefined;
    this.runContext.clear();
    this.runsWithKnownTranscriptHead.clear();
    this.runMessageThroughSeq.clear();
    this.runPreviewItems.clear();
    this.runScratch.clear();
    this.committedRunItemKeys.clear();
    this.terminalRunIds.clear();
    this.trackedRunIds.clear();
    this.eventHandlers.clear();
    this.recoveryHandlers.clear();
    this.statusHandlers.clear();
    this.removeEventListener();
    this.removeLocalPreviewListener();
    this.removeRecoveryListener();
    this.removeStatusListener();
  }

  private acceptsConversation(conversationId: string): boolean {
    return (
      Boolean(conversationId) &&
      (!this.conversationId || conversationId === this.conversationId)
    );
  }

  private trackRun(runId: string, context: ConversationRunContext): boolean {
    if (
      canonicalUuid(runId) === undefined ||
      !this.acceptsConversation(context.conversationId)
    ) {
      return false;
    }
    const current = this.runContext.get(runId);
    if (
      current &&
      (current.conversationId !== context.conversationId ||
        (current.clientRequestId &&
          context.clientRequestId &&
          current.clientRequestId !== context.clientRequestId))
    ) {
      return false;
    }
    this.mergeRunContext(runId, context);
    this.trackedRunIds.add(runId);
    return true;
  }

  private mergeRunContext(
    runId: string,
    context: ConversationRunContext,
  ): void {
    this.runContext.set(runId, {
      ...this.runContext.get(runId),
      ...context,
    });
  }

  private handleEvent(event: BoardlyRealtimeEvent): void {
    // Who reads a Project changed (ADR 0017): a Project file on a message in
    // this history may now show or be withheld, and no message frame says
    // so. Reload the history; the read decides what this viewer sees.
    if (event.type === "project.access_changed") {
      this.notifyRecovery();
      return;
    }
    const eventConversationId = realtimeEventConversationId(event);
    // message.updated is an opaque hint that a message's files changed who may
    // see them (ADR 0017); the history reload refetches the filtered list.
    if (
      (event.type === "message.created" ||
        event.type === "message.updated" ||
        event.type === "conversation.updated") &&
      eventConversationId &&
      this.acceptsConversation(eventConversationId)
    ) {
      this.notifyRecovery();
      return;
    }

    const identity = runIdentity(event);
    if (!identity || !this.acceptsConversation(identity.conversationId)) return;

    let adoptedQueuedRun = false;
    if (event.type === "run.queued") {
      // A queued frame can beat the POST response. Its Run ID alone cannot
      // identify which optimistic prompt it belongs to; the receipt can.
      if (
        !this.trackedRunIds.has(identity.runId) &&
        [...this.pendingSends.values()].includes(identity.conversationId)
      ) {
        this.deferredQueuedRuns.set(identity.runId, event);
        return;
      }
      adoptedQueuedRun = !this.trackedRunIds.has(identity.runId);
      const pendingContext = this.runContext.get(identity.runId);
      if (
        !this.trackRun(identity.runId, {
          ...pendingContext,
          conversationId: identity.conversationId,
        })
      ) {
        this.notifyRecovery();
        return;
      }
    }
    if (
      !this.trackedRunIds.has(identity.runId) ||
      this.terminalRunIds.has(identity.runId)
    ) {
      return;
    }

    if (adoptedQueuedRun) {
      if (!this.applyEvent(event)) {
        this.notifyRecovery();
        return;
      }
      void this.recoverRun(identity.runId, "catch_up").catch(() => {
        this.notifyRecovery();
      });
      return;
    }

    const catchUp = this.conversationCatchUp;
    if (catchUp) {
      if (!catchUp.catchUp.buffer(event)) this.notifyRecovery();
      return;
    }

    if (isTerminalRunEvent(event)) {
      this.clearScratch(identity.runId, "terminal");
      const terminalHead = terminalRunTranscriptHead(event);
      if (
        sameRunTranscriptHead(terminalHead, this.transcriptHead(identity.runId))
      ) {
        if (!this.applyEvent(event)) this.notifyRecovery();
        return;
      }
      void this.recoverConversation(event).catch(() => {
        this.notifyRecovery();
      });
      return;
    }

    if (!this.applyEvent(event)) this.notifyRecovery();
  }

  private handleRecovery(): void {
    if (this.disposed || this.recovering) return;
    this.recovering = true;
    const runs = [...this.trackedRunIds].filter(
      (runId) => !this.terminalRunIds.has(runId),
    );
    runs.forEach((runId) => this.clearScratch(runId, "reconnect"));
    void Promise.allSettled(
      runs.map((runId) => this.recoverRun(runId, "reconnect", false)),
    ).then(() => {
      this.recovering = false;
      if (!this.disposed) this.notifyRecovery();
    });
  }

  private handleLocalPreview(frame: LocalRunPreviewFrame): void {
    if (frame.type === "run.preview.reset") {
      for (const runId of [...this.runPreviewItems.keys()]) {
        this.clearScratch(runId, "reconnect");
      }
      return;
    }
    // Desktop preview and durable API events share the same UUID identity.
    const runId = canonicalUuid(frame.runId);
    if (
      runId === undefined ||
      this.disposed ||
      frame.companyId !== this.activeCompanyId() ||
      !this.acceptsConversation(frame.conversationId) ||
      !this.trackedRunIds.has(runId) ||
      this.terminalRunIds.has(runId) ||
      // The preview plane is drained in polled batches, so a delta for an item
      // the durable stream already committed can still be in flight. Rejecting
      // it is what keeps that text from being rendered twice.
      this.committedRunItemKeys.get(runId)?.has(frame.itemKey)
    ) {
      return;
    }
    const context = this.runContext.get(runId);
    if (!context || context.conversationId !== frame.conversationId) return;

    let items = this.runPreviewItems.get(runId);
    if (!items) {
      items = new Map();
      this.runPreviewItems.set(runId, items);
    }
    const item = items.get(frame.itemKey) ?? {};
    const scratchChannel = frame.channel === "reasoning" ? "thinking" : "text";
    item[scratchChannel] = (
      frame.operation === "replace"
        ? frame.content
        : `${item[scratchChannel] ?? ""}${frame.content}`
    ).slice(0, LOCAL_PREVIEW_MAX_CHARS_PER_ITEM);
    items.set(frame.itemKey, item);
    this.boundPreviewItems(items);
    const scratch = this.refreshScratchFromPreviewItems(runId);
    this.emit({
      ...context,
      channel: scratchChannel,
      content: frame.content,
      itemKey: frame.itemKey,
      kind: "delta",
      runId,
      scratch,
    });
  }

  private boundPreviewItems(
    items: Map<string, Partial<ConversationRunScratch>>,
  ): void {
    while (
      items.size > LOCAL_PREVIEW_MAX_ITEMS_PER_RUN ||
      previewCharacterCount(items) > LOCAL_PREVIEW_MAX_CHARS_PER_RUN
    ) {
      const oldestKey = items.keys().next().value;
      if (oldestKey === undefined) return;
      items.delete(oldestKey);
    }
  }

  private async recoverRun(
    runId: string,
    reason: "catch_up" | "reconnect",
    clearScratch = true,
  ): Promise<void> {
    if (
      this.disposed ||
      this.terminalRunIds.has(runId) ||
      !this.trackedRunIds.has(runId)
    ) {
      return;
    }
    if (clearScratch) this.clearScratch(runId, reason);
    await this.recoverConversation();
  }

  private recoverConversation(
    bufferedEvent?: BoardlyRealtimeEvent,
  ): Promise<void> {
    const existing = this.conversationCatchUp;
    if (existing) {
      if (bufferedEvent && !existing.catchUp.buffer(bufferedEvent)) {
        return Promise.reject(
          new Error("Conversation catch-up buffer rejected recovery state"),
        );
      }
      for (const runId of this.activeTrackedRunIds()) {
        if (!existing.targetHeads.has(runId)) {
          existing.targetHeads.set(runId, {
            throughSeq: this.runMessageThroughSeq.get(runId) ?? 0,
          });
        }
      }
      if (existing.targetsFrozen) existing.rerun = true;
      return existing.promise;
    }
    const companyId = this.activeCompanyId();
    const conversationId = this.recoveryConversationId();
    if (!companyId || !conversationId) {
      return Promise.reject(
        new Error("Conversation catch-up identity is unavailable"),
      );
    }
    const targetHeads = new Map<string, RunTranscriptHead>();
    for (const runId of this.activeTrackedRunIds()) {
      targetHeads.set(runId, {
        throughSeq: this.runMessageThroughSeq.get(runId) ?? 0,
      });
    }
    const catchUp = new FrozenConversationTranscriptCatchUp({
      acceptTranscriptHead: (runId, head) =>
        this.acceptTranscriptHead(runId, head),
      deliver: (event) => this.applyEvent(event),
      deliverMessage: (message) =>
        this.applyTranscriptMessage(message.runId, message),
      fetchPage: (options) =>
        conversationService.getTranscript(companyId, conversationId, options),
      targetHeads: () => targetHeads,
      transcriptHead: (runId) => this.transcriptHead(runId),
      throughSeq: (runId) => this.runMessageThroughSeq.get(runId) ?? 0,
    });
    if (bufferedEvent && !catchUp.buffer(bufferedEvent)) {
      return Promise.reject(
        new Error("Conversation catch-up buffer rejected recovery state"),
      );
    }
    const state = {
      catchUp,
      promise: Promise.resolve(),
      rerun: false,
      targetsFrozen: false,
      targetHeads,
    };
    this.conversationCatchUp = state;
    state.promise = (async () => {
      try {
        do {
          state.rerun = false;
          state.targetsFrozen = false;
          const projections = await runService.listAllRuns(companyId, {
            conversationId,
          });
          const projectionsByID = new Map(
            projections.map((projection) => [projection.id, projection]),
          );
          const activeRunIds = this.activeTrackedRunIds();
          for (const runId of [...targetHeads.keys()]) {
            if (!activeRunIds.includes(runId)) targetHeads.delete(runId);
          }
          for (const runId of activeRunIds) {
            const projection = projectionsByID.get(runId);
            const context = this.runContext.get(runId);
            if (
              !projection ||
              !context ||
              projection.companyId !== companyId ||
              projection.conversationId !== conversationId
            ) {
              throw new Error("Run projection identity conflicts");
            }
            targetHeads.set(runId, {
              throughSeq: Math.max(
                projection.transcriptThroughSeq,
                this.runMessageThroughSeq.get(runId) ?? 0,
              ),
            });
          }
          if (targetHeads.size === 0) return;
          state.targetsFrozen = true;
          const result = await catchUp.run();
          for (const [runId, head] of result) {
            if (
              !this.terminalRunIds.has(runId) &&
              !sameRunTranscriptHead(this.transcriptHead(runId), head)
            ) {
              throw new Error("Conversation catch-up did not converge");
            }
          }
          for (const projection of projections) {
            if (!targetHeads.has(projection.id)) continue;
            const terminal = terminalEventFromRunProjection(projection);
            if (!terminal) continue;
            const terminalHead = terminalRunTranscriptHead(terminal);
            if (
              !sameRunTranscriptHead(
                terminalHead,
                this.transcriptHead(projection.id),
              )
            ) {
              throw new Error("Run terminal transcript did not converge");
            }
            this.clearScratch(projection.id, "terminal");
            if (!this.applyEvent(terminal)) {
              throw new Error("Run terminal projection was rejected");
            }
          }
        } while (state.rerun && !this.disposed);
      } finally {
        if (this.conversationCatchUp === state) {
          this.conversationCatchUp = undefined;
        }
      }
    })();
    return state.promise;
  }

  private applyEvent(event: BoardlyRealtimeEvent): boolean {
    const identity = runIdentity(event);
    if (!identity || !this.acceptsConversation(identity.conversationId)) {
      return false;
    }
    const { runId } = identity;
    if (this.terminalRunIds.has(runId)) return true;
    if (!this.trackedRunIds.has(runId)) return false;
    const context = this.runContext.get(runId);
    if (!context || context.conversationId !== identity.conversationId) {
      return false;
    }

    if (event.type === "run.message_created") {
      const message = runMessageFromEvent(event);
      const messageHead = runMessageTranscriptHead(event);
      if (!message || !messageHead || messageHead.throughSeq !== message.seq) {
        return false;
      }
      // `seq` is the conversation's, not a per-run ordinal, so the next item
      // of a run is only guaranteed to be *greater* than the held head, never
      // exactly one past it. Advancement is by the last row's seq; the terminal
      // head is what still proves nothing was lost.
      const throughSeq = this.runMessageThroughSeq.get(runId) ?? 0;
      if (message.seq < throughSeq) return true;
      if (message.seq === throughSeq) {
        if (sameRunTranscriptHead(messageHead, this.transcriptHead(runId))) {
          return true;
        }
        this.clearScratch(runId, "catch_up");
        void this.recoverConversation(event).catch(() => {
          this.notifyRecovery();
        });
        return true;
      }
      return this.applyTranscriptMessage(runId, message, true);
    }

    const status = runStatusFromEvent(event);
    if (!status) return false;
    if (
      isTerminalRunEvent(event) &&
      !sameRunTranscriptHead(
        terminalRunTranscriptHead(event),
        this.transcriptHead(runId),
      )
    ) {
      return false;
    }
    this.emit({
      ...context,
      ...runLifecycleFacts(event.payload, status),
      kind: "lifecycle",
      runId,
      scratch: this.scratch(runId),
      status,
    });
    if (isTerminalRunEvent(event)) this.markRunTerminal(runId);
    return true;
  }

  // `headIsAuthoritative` separates a live run.message — which carries the
  // Run's head with it — from a row handed over by an in-flight HTTP page,
  // which only advances the applied sequence until the page's own head lands.
  private applyTranscriptMessage(
    runId: string,
    message: ConversationTranscriptEntryDto,
    headIsAuthoritative = false,
  ): boolean {
    const context = this.runContext.get(runId);
    const throughSeq = this.runMessageThroughSeq.get(runId) ?? 0;
    if (!context || message.seq <= throughSeq) return false;

    this.runMessageThroughSeq.set(runId, message.seq);
    if (headIsAuthoritative) {
      this.runsWithKnownTranscriptHead.add(runId);
    } else {
      this.runsWithKnownTranscriptHead.delete(runId);
    }
    this.commitPreviewItem(runId, message);
    this.emit({
      ...context,
      kind: "message",
      message,
      runId,
      scratch: this.scratch(runId),
    });
    return true;
  }

  private acceptTranscriptHead(
    runId: string,
    head: RunTranscriptHead,
  ): boolean {
    if ((this.runMessageThroughSeq.get(runId) ?? 0) !== head.throughSeq) {
      return false;
    }
    this.runsWithKnownTranscriptHead.add(runId);
    return true;
  }

  private transcriptHead(runId: string): RunTranscriptHead | undefined {
    return this.runsWithKnownTranscriptHead.has(runId)
      ? { throughSeq: this.runMessageThroughSeq.get(runId) ?? 0 }
      : undefined;
  }

  // The committed item and its preview share one key, so convergence is a
  // delete on that key — the durable row simply takes the preview's place.
  private commitPreviewItem(
    runId: string,
    message: ConversationTranscriptEntryDto,
  ): void {
    let committed = this.committedRunItemKeys.get(runId);
    if (!committed) {
      committed = new Set();
      this.committedRunItemKeys.set(runId, committed);
    }
    committed.add(message.itemKey);
    const items = this.runPreviewItems.get(runId);
    items?.delete(message.itemKey);
    if (items?.size === 0) this.runPreviewItems.delete(runId);
    this.refreshScratchFromPreviewItems(runId);
  }

  private refreshScratchFromPreviewItems(
    runId: string,
  ): ConversationRunScratch {
    const scratch = { text: "", thinking: "" };
    for (const item of this.runPreviewItems.get(runId)?.values() ?? []) {
      scratch.text += item.text ?? "";
      scratch.thinking += item.thinking ?? "";
    }
    this.runScratch.set(runId, scratch);
    return { ...scratch };
  }

  private clearScratch(
    runId: string,
    reason: "catch_up" | "reconnect" | "terminal",
  ): void {
    const context = this.runContext.get(runId);
    if (!context) return;
    this.runPreviewItems.delete(runId);
    const scratch = { text: "", thinking: "" };
    this.runScratch.set(runId, scratch);
    this.emit({ ...context, kind: "scratch_reset", reason, runId, scratch });
  }

  private scratch(runId: string): ConversationRunScratch {
    return { ...(this.runScratch.get(runId) ?? { text: "", thinking: "" }) };
  }

  private emit(event: ConversationRunStreamEvent): void {
    this.eventHandlers.forEach((handler) => handler(event));
  }

  private markRunTerminal(runId: string): void {
    this.terminalRunIds.add(runId);
    this.trackedRunIds.delete(runId);
    this.conversationCatchUp?.targetHeads.delete(runId);
    this.runContext.delete(runId);
    this.runsWithKnownTranscriptHead.delete(runId);
    this.runMessageThroughSeq.delete(runId);
    this.runPreviewItems.delete(runId);
    this.runScratch.delete(runId);
    this.committedRunItemKeys.delete(runId);
  }

  private activeTrackedRunIds(): string[] {
    return [...this.trackedRunIds].filter(
      (runId) => !this.terminalRunIds.has(runId),
    );
  }

  private recoveryConversationId(): string | undefined {
    const conversationIds = new Set(
      this.activeTrackedRunIds()
        .map((runId) => this.runContext.get(runId)?.conversationId)
        .filter((value): value is string => Boolean(value)),
    );
    return conversationIds.size === 1
      ? conversationIds.values().next().value
      : undefined;
  }

  private notifyRecovery(): void {
    // History can contain this prompt too. Its receipt must identify the
    // optimistic row before a replay tries to merge the committed one.
    if (this.pendingSends.size > 0) {
      this.recoveryPendingAfterSend = true;
      return;
    }
    this.recoveryHandlers.forEach((handler) => handler());
  }
}

function runIdentity(
  event: BoardlyRealtimeEvent,
): { conversationId: string; runId: string } | undefined {
  if (!event.type.startsWith("run.")) return undefined;
  const runId = canonicalUuid(
    isRunStatusEventType(event.type) ? event.payload.id : event.payload.runId,
  );
  const conversationId = stringValue(event.payload.conversationId);
  return runId !== undefined && conversationId
    ? { conversationId, runId }
    : undefined;
}

function previewCharacterCount(
  items: Map<string, Partial<ConversationRunScratch>>,
): number {
  let count = 0;
  for (const item of items.values()) {
    count += (item.text?.length ?? 0) + (item.thinking?.length ?? 0);
  }
  return count;
}

export function runMessageFromEvent(
  event: BoardlyRealtimeEvent,
): ConversationTranscriptEntryDto | undefined {
  if (event.type !== "run.message_created") return undefined;
  const id = stringValue(event.payload.id);
  const createdAt = stringValue(event.payload.createdAt);
  const seq = positiveSafeInteger(event.payload.seq);
  const itemKey = stringValue(event.payload.itemKey);
  const role = runItemRole(event.payload.role);
  const parts = runItemParts(event.payload.parts);
  const itemDigest = sha256Digest(event.payload.itemDigest);
  const runId = canonicalUuid(event.payload.runId);
  if (
    !id ||
    !createdAt ||
    Number.isNaN(Date.parse(createdAt)) ||
    seq === undefined ||
    !itemKey ||
    !role ||
    !parts ||
    !itemDigest ||
    runId === undefined
  ) {
    return undefined;
  }
  return {
    createdAt,
    id,
    itemDigest,
    itemKey,
    parts,
    role,
    runId,
    seq,
  };
}

function runStatusFromEvent(
  event: BoardlyRealtimeEvent,
): RunDto["status"] | undefined {
  if (!isRunStatusEventType(event.type)) return undefined;
  const expectedStatus = RUN_STATUS_BY_EVENT[event.type];
  return event.payload.status === expectedStatus ? expectedStatus : undefined;
}

function isRunStatusEventType(
  value: string,
): value is keyof typeof RUN_STATUS_BY_EVENT {
  return Object.prototype.hasOwnProperty.call(RUN_STATUS_BY_EVENT, value);
}

export function isTerminalRunEvent(event: BoardlyRealtimeEvent): boolean {
  return (
    TERMINAL_RUN_EVENT_TYPES.has(event.type) &&
    runStatusFromEvent(event) !== undefined
  );
}

export function terminalEventFromRunProjection(
  projection: RunDto,
): BoardlyRealtimeEvent | undefined {
  if (
    projection.status !== "completed" &&
    projection.status !== "failed" &&
    projection.status !== "cancelled"
  ) {
    return undefined;
  }
  return {
    actorId: null,
    actorType: "system",
    // Synthesised locally from a REST read, so no server named a tenant for
    // it. Nothing downstream reads companyId off a frame it built itself.
    companyId: null,
    createdAt: projection.updatedAt ?? projection.createdAt,
    eventId: `${projection.id}:${projection.status}`,
    payload: {
      attempt: projection.attempt,
      conversationId: projection.conversationId,
      failureDetail: projection.failureDetail ?? null,
      failureMessage: projection.failureMessage ?? null,
      failureReason: projection.failureReason,
      id: projection.id,
      maxAttempts: projection.maxAttempts,
      parentRunId: projection.parentRunId,
      provider: projection.provider ?? null,
      status: projection.status,
      transcriptThroughSeq: projection.transcriptThroughSeq,
      updatedAt: projection.updatedAt,
    },
    type: `run.${projection.status}`,
  };
}

// The envelope payload is untyped on the wire; read the lifecycle facts the
// chat renders (creation time, attempt ceiling, retry linkage, failure copy) so a
// frame without them still yields a status-only event.
function runLifecycleFacts(
  payload: Record<string, unknown>,
  status: RunDto["status"],
): Pick<
  Extract<ConversationRunStreamEvent, { kind: "lifecycle" }>,
  | "attempt"
  | "createdAt"
  | "failure"
  | "maxAttempts"
  | "parentRunId"
  | "updatedAt"
> {
  const attempt = numberOrUndefined(payload.attempt);
  const createdAt = stringOrNull(payload.createdAt) ?? undefined;
  const maxAttempts = numberOrUndefined(payload.maxAttempts);
  const parentRunId = stringOrNull(payload.parentRunId);
  const updatedAt = stringOrNull(payload.updatedAt) ?? undefined;
  const failure: ConversationRunFailure | undefined =
    status === "failed"
      ? {
          detail: stringOrNull(payload.failureDetail) ?? null,
          message: stringOrNull(payload.failureMessage) ?? null,
          provider: providerOrNull(payload.provider),
          reason: stringOrNull(payload.failureReason) ?? null,
        }
      : undefined;
  return {
    ...(attempt === undefined ? {} : { attempt }),
    ...(createdAt === undefined ? {} : { createdAt }),
    ...(maxAttempts === undefined ? {} : { maxAttempts }),
    ...(parentRunId === undefined ? {} : { parentRunId }),
    ...(updatedAt === undefined ? {} : { updatedAt }),
    ...(failure ? { failure } : {}),
  };
}

// The wire's closed provider set; anything else reads as no provider, so a
// frame from a newer server cannot smuggle an unknown label into the copy.
function providerOrNull(value: unknown): AgentProvider | null {
  return value === "claude" || value === "codex" ? value : null;
}

function stringOrNull(value: unknown): string | null | undefined {
  if (value === null) return null;
  return typeof value === "string" ? value : undefined;
}

function numberOrUndefined(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function conversationStatus(
  snapshot: BoardlyRealtimeSnapshot,
): ConversationRealtimeStatus {
  if (snapshot.status === "ready") return "connected";
  if (snapshot.status === "connecting" || snapshot.status === "reconnecting") {
    return "connecting";
  }
  return "error";
}

function rejectedReceipt(
  error: unknown,
  fallback = "Message send failed",
): ConversationMessageReceipt {
  const body = getApiErrorBody(error);
  const apiError = isApiClientError(error);
  return {
    accepted: false,
    code: getApiErrorCode(error),
    detail: body?.detail.trim() || null,
    error: getApiErrorMessage(error, fallback),
    networkError: apiError && error.status === null,
    ok: false,
    status: apiError ? error.status : null,
  };
}

function runItemRole(
  value: unknown,
): ConversationTranscriptEntryDto["role"] | undefined {
  return value === "agent" || value === "tool" || value === "system"
    ? value
    : undefined;
}

function runItemParts(
  value: unknown,
): ConversationTranscriptEntryDto["parts"] | undefined {
  if (!Array.isArray(value) || value.length === 0) return undefined;
  const parts = value.map(runItemPart);
  return parts.every(
    (part): part is ConversationTranscriptEntryDto["parts"][number] =>
      Boolean(part),
  )
    ? parts
    : undefined;
}

function runItemPart(
  value: unknown,
): ConversationTranscriptEntryDto["parts"][number] | undefined {
  const part = objectValue(value);
  if (!part) return undefined;
  if (
    part.type === "text" ||
    part.type === "reasoning" ||
    part.type === "error"
  ) {
    const text = stringValue(part.text);
    return text ? { text, type: part.type } : undefined;
  }
  if (part.type === "toolCall") {
    const callId = stringValue(part.callId);
    const name = stringValue(part.name);
    const input = optionalObject(part.input);
    return callId && name && input !== undefined
      ? { callId, ...(input ? { input } : {}), name, type: "toolCall" }
      : undefined;
  }
  if (part.type === "toolResult") {
    const callId = stringValue(part.callId);
    const name = optionalString(part.name);
    const output = optionalString(part.output);
    const status = optionalToolResultStatus(part.status);
    const exitCode = optionalNonNegativeSafeInteger(part.exitCode);
    const durationMs = optionalNonNegativeSafeInteger(part.durationMs);
    return callId &&
      name !== undefined &&
      output !== undefined &&
      status !== undefined &&
      exitCode !== undefined &&
      durationMs !== undefined
      ? {
          callId,
          ...(durationMs === null ? {} : { durationMs }),
          ...(exitCode === null ? {} : { exitCode }),
          ...(name === null ? {} : { name }),
          ...(output === null ? {} : { output }),
          ...(status === null ? {} : { status }),
          type: "toolResult",
        }
      : undefined;
  }
  return undefined;
}

function optionalToolResultStatus(
  value: unknown,
):
  | "in_progress"
  | "completed"
  | "failed"
  | "declined"
  | "cancelled"
  | null
  | undefined {
  if (value === undefined) return null;
  return value === "in_progress" ||
    value === "completed" ||
    value === "failed" ||
    value === "declined" ||
    value === "cancelled"
    ? value
    : undefined;
}

function optionalObject(
  value: unknown,
): Record<string, unknown> | null | undefined {
  return value === undefined ? null : objectValue(value);
}

function optionalNonNegativeSafeInteger(
  value: unknown,
): number | null | undefined {
  return value === undefined
    ? null
    : Number.isSafeInteger(value) && Number(value) >= 0
      ? Number(value)
      : undefined;
}

function optionalString(value: unknown): string | null | undefined {
  return value === undefined
    ? null
    : typeof value === "string"
      ? value
      : undefined;
}

function sha256Digest(value: unknown): string | undefined {
  return typeof value === "string" && /^[0-9a-f]{64}$/u.test(value)
    ? value
    : undefined;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function positiveSafeInteger(value: unknown): number | undefined {
  return Number.isSafeInteger(value) && Number(value) > 0
    ? Number(value)
    : undefined;
}

export function createConversationSocketSession(
  taskId?: string,
  conversationId?: string,
) {
  return new ConversationSocketSession(undefined, taskId, conversationId);
}
