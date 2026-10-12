import { canonicalUuid } from "@/shared/lib/uuid";
import type {
  ConversationTranscriptEntryDto,
  ConversationTranscriptListOptions,
  ConversationTranscriptPageDto,
} from "@/shared/api/types";
import type { BoardlyRealtimeEvent } from "@/shared/realtime/realtime-client.service";

const CATCH_UP_PAGE_LIMIT = 200;
const MAX_CATCH_UP_CYCLES = 8;
const MAX_BUFFERED_LIVE_FRAMES = 256;
const MAX_BUFFERED_LIVE_BYTES = 4 << 20;
const utf8Encoder = new TextEncoder();
const TERMINAL_RUN_EVENTS = new Set([
  "run.cancelled",
  "run.completed",
  "run.failed",
]);

// The whole of a Run's committed head. There is no companion chain digest:
// an append holds the Conversation row lock and a committed item is never
// rewritten, so one `throughSeq` names one history — see
// docs/adr/0005-one-conversation-stream.md.
export type RunTranscriptHead = {
  throughSeq: number;
};

export type FrozenConversationTranscriptCatchUpResult = Map<
  string,
  RunTranscriptHead
>;

type FrozenConversationTranscriptCatchUpOptions = {
  acceptTranscriptHead: (runId: string, head: RunTranscriptHead) => boolean;
  deliver: (event: BoardlyRealtimeEvent) => boolean;
  deliverMessage: (message: ConversationTranscriptEntryDto) => boolean;
  fetchPage: (
    options: ConversationTranscriptListOptions,
  ) => Promise<ConversationTranscriptPageDto>;
  targetHeads: () => ReadonlyMap<string, RunTranscriptHead>;
  transcriptHead: (runId: string) => RunTranscriptHead | undefined;
  throughSeq: (runId: string) => number;
};

// Attach the shared socket listener first, then read one durable Conversation
// suffix. The Conversation cursor crosses Runs; each frozen Run head remains a
// separate integrity boundary, and lifecycle frames drain only after every
// target Run has reached its exact committed head.
export class FrozenConversationTranscriptCatchUp {
  private readonly buffered: BoardlyRealtimeEvent[] = [];
  private readonly bufferedTargetHeads = new Map<string, RunTranscriptHead>();
  private bufferedBytes = 0;
  private bufferFailure: Error | undefined;

  constructor(
    private readonly options: FrozenConversationTranscriptCatchUpOptions,
  ) {}

  buffer(event: BoardlyRealtimeEvent): boolean {
    if (this.bufferFailure) return false;
    const encodedBytes = encodedFrameBytes(event);
    if (
      encodedBytes < 1 ||
      this.buffered.length >= MAX_BUFFERED_LIVE_FRAMES ||
      encodedBytes > MAX_BUFFERED_LIVE_BYTES - this.bufferedBytes
    ) {
      return this.failBuffer();
    }
    this.buffered.push(event);
    this.bufferedBytes += encodedBytes;
    const runId = eventRunId(event);
    const head = terminalRunTranscriptHead(event);
    if (runId !== undefined && head) {
      const current = this.bufferedTargetHeads.get(runId)?.throughSeq ?? 0;
      if (head.throughSeq > current) this.bufferedTargetHeads.set(runId, head);
    }
    return true;
  }

  async run(): Promise<FrozenConversationTranscriptCatchUpResult> {
    for (let cycle = 0; cycle < MAX_CATCH_UP_CYCLES; cycle += 1) {
      this.throwIfBufferFailed();
      const targets = validatedTargets(this.options.targetHeads());
      for (const [runId, head] of this.bufferedTargetHeads) {
        if (!targets.has(runId)) continue;
        const current = targets.get(runId)?.throughSeq ?? 0;
        if (head.throughSeq > current) targets.set(runId, head);
      }
      await this.fetchSuffix(targets);
      this.throwIfBufferFailed();
      const heads = this.drainBuffered(targets);
      if (heads) return heads;
    }
    throw new Error("Conversation transcript catch-up could not converge");
  }

  private async fetchSuffix(
    targets: ReadonlyMap<string, RunTranscriptHead>,
  ): Promise<void> {
    const targetsReached = (): boolean => {
      for (const [runId, target] of targets) {
        if (this.options.throughSeq(runId) !== target.throughSeq) return false;
      }
      return true;
    };
    const pendingTargets = [...targets].filter(
      ([runId, target]) => this.options.throughSeq(runId) < target.throughSeq,
    );
    let afterSeq = pendingTargets.length
      ? Math.min(
          ...pendingTargets.map(([runId]) => this.options.throughSeq(runId)),
        )
      : 0;
    const highestTarget = pendingTargets.reduce(
      (highest, [, target]) => Math.max(highest, target.throughSeq),
      0,
    );
    if (pendingTargets.length === 0) {
      for (const [runId, target] of targets) {
        if (!this.options.acceptTranscriptHead(runId, target)) {
          throw new Error("Run transcript head was rejected");
        }
      }
      return;
    }
    while (true) {
      const page = await this.options.fetchPage({
        afterSeq,
        limit: CATCH_UP_PAGE_LIMIT,
      });
      // `throughSeq` is the transcript's durable head, not a page cursor: it
      // covers rows beyond this page, and `hasMore` is what says the page
      // stopped short of it.
      const advertisedHead = parseRunTranscriptHead(page.throughSeq);
      if (
        !advertisedHead ||
        page.items.length > CATCH_UP_PAGE_LIMIT ||
        (page.hasMore && page.items.length === 0)
      ) {
        throw new Error("Conversation transcript page is invalid");
      }
      // `seq` is the Conversation's, not a per-Run ordinal, so a Run's rows
      // ascend but are not dense — another item of the same conversation holds
      // the numbers in between. The window is a keyset read over
      // `seq > after_seq`, so ascending is the whole invariant, and the cursor
      // advances by the last row rather than by incrementing.
      let expectedSeq = afterSeq;
      for (const message of page.items) {
        if (!validMessage(message) || message.seq <= expectedSeq) {
          throw new Error("Conversation transcript suffix is not ascending");
        }
        expectedSeq = message.seq;
        const target = targets.get(message.runId);
        if (
          !target ||
          message.seq > target.throughSeq ||
          message.seq <= this.options.throughSeq(message.runId)
        ) {
          continue;
        }
        if (!this.options.deliverMessage(message)) {
          throw new Error("Conversation transcript merge was rejected");
        }
      }
      if (advertisedHead.throughSeq < expectedSeq) {
        throw new Error("Conversation transcript head does not cover its page");
      }
      if (targetsReached()) break;
      if (expectedSeq < highestTarget && page.hasMore) {
        afterSeq = expectedSeq;
        continue;
      }
      throw new Error("Conversation transcript did not reach every Run head");
    }
    for (const [runId, target] of targets) {
      if (!this.options.acceptTranscriptHead(runId, target)) {
        throw new Error("Run transcript head was rejected");
      }
    }
  }

  private drainBuffered(
    targets: ReadonlyMap<string, RunTranscriptHead>,
  ): FrozenConversationTranscriptCatchUpResult | undefined {
    this.throwIfBufferFailed();
    const snapshot = this.buffered.splice(0);
    this.bufferedBytes = 0;
    const messageFrames = snapshot
      .filter((event) => event.type === "run.message_created")
      .sort((left, right) => messageSequence(left) - messageSequence(right));
    if (
      messageFrames.some(
        (event) =>
          messageSequence(event) < 1 ||
          runMessageTranscriptHead(event) === undefined ||
          eventRunId(event) === undefined,
      )
    ) {
      throw new Error("Buffered Run message sequence is invalid");
    }
    const stableFrames = snapshot.filter(
      (event) => event.type !== "run.message_created",
    );
    // A frame past the held head is simply the next one: a Conversation-wide
    // seq cannot say whether the distance is a gap or another item's number.
    // The terminal head below is what still refuses an incomplete prefix and
    // sends this back through the HTTP suffix to repair it.
    for (const [index, event] of messageFrames.entries()) {
      const runId = eventRunId(event);
      if (runId === undefined) continue;
      if (!targets.has(runId)) {
        if (
          !this.prependBuffered([
            ...messageFrames.slice(index),
            ...stableFrames,
          ])
        ) {
          return undefined;
        }
        return undefined;
      }
      const seq = messageSequence(event);
      if (seq < this.options.throughSeq(runId)) continue;
      if (seq === this.options.throughSeq(runId)) {
        if (
          !sameRunTranscriptHead(
            this.options.transcriptHead(runId),
            runMessageTranscriptHead(event),
          )
        ) {
          throw new Error("Buffered Run message head conflicts");
        }
        continue;
      }
      if (
        !this.options.deliver(event) ||
        this.options.throughSeq(runId) !== seq ||
        !sameRunTranscriptHead(
          this.options.transcriptHead(runId),
          runMessageTranscriptHead(event),
        )
      ) {
        throw new Error("Buffered Run message merge was rejected");
      }
    }
    const transcriptHeads = new Map<string, RunTranscriptHead>();
    for (const runId of targets.keys()) {
      const head = this.options.transcriptHead(runId);
      if (!head || head.throughSeq !== this.options.throughSeq(runId)) {
        throw new Error("Run transcript head is unavailable after catch-up");
      }
      transcriptHeads.set(runId, head);
    }
    for (const [index, event] of stableFrames.entries()) {
      const terminalHead = terminalRunTranscriptHead(event);
      const runId = eventRunId(event);
      if (runId === undefined) continue;
      if (!targets.has(runId)) {
        if (!this.prependBuffered(stableFrames.slice(index))) return undefined;
        return undefined;
      }
      if (TERMINAL_RUN_EVENTS.has(event.type) && !terminalHead) {
        throw new Error("Run terminal transcript head is invalid");
      }
      if (terminalHead) {
        const transcriptHead = transcriptHeads.get(runId);
        if (!transcriptHead) {
          throw new Error("Run transcript head is unavailable after catch-up");
        }
        if (terminalHead.throughSeq < transcriptHead.throughSeq) {
          throw new Error("Run terminal transcript head is invalid");
        }
        if (!sameRunTranscriptHead(terminalHead, transcriptHead)) {
          if (!this.prependBuffered(stableFrames.slice(index))) {
            return undefined;
          }
          return undefined;
        }
      }
      if (!this.options.deliver(event)) {
        throw new Error("Buffered Run state was rejected");
      }
    }
    if (this.buffered.length > 0) return undefined;
    return transcriptHeads;
  }

  private prependBuffered(events: readonly BoardlyRealtimeEvent[]): boolean {
    const bytes = events.reduce(
      (sum, event) => sum + encodedFrameBytes(event),
      0,
    );
    if (
      events.length > MAX_BUFFERED_LIVE_FRAMES - this.buffered.length ||
      bytes > MAX_BUFFERED_LIVE_BYTES - this.bufferedBytes
    ) {
      return this.failBuffer();
    }
    this.buffered.unshift(...events);
    this.bufferedBytes += bytes;
    return true;
  }

  private failBuffer(): false {
    this.buffered.length = 0;
    this.bufferedBytes = 0;
    this.bufferFailure = new Error(
      "Run message catch-up live buffer exceeded its bound",
    );
    return false;
  }

  private throwIfBufferFailed(): void {
    if (this.bufferFailure) throw this.bufferFailure;
  }
}

function validMessage(message: ConversationTranscriptEntryDto): boolean {
  return (
    Boolean(message.id) &&
    Boolean(message.createdAt) &&
    canonicalUuid(message.runId) !== undefined &&
    validSequence(message.seq) &&
    message.seq > 0
  );
}

function validatedTargets(
  targets: ReadonlyMap<string, RunTranscriptHead>,
): Map<string, RunTranscriptHead> {
  if (targets.size === 0) {
    throw new Error("Conversation transcript has no target Runs");
  }
  const copy = new Map<string, RunTranscriptHead>();
  for (const [runId, head] of targets) {
    if (
      canonicalUuid(runId) === undefined ||
      !parseRunTranscriptHead(head.throughSeq)
    ) {
      throw new Error("Conversation transcript Run head is invalid");
    }
    copy.set(runId, { throughSeq: head.throughSeq });
  }
  return copy;
}

function eventRunId(event: BoardlyRealtimeEvent): string | undefined {
  return canonicalUuid(
    event.type === "run.message_created"
      ? event.payload.runId
      : event.payload.id,
  );
}

function messageSequence(event: BoardlyRealtimeEvent): number {
  return validSequence(event.payload?.seq) ? Number(event.payload?.seq) : -1;
}

export function terminalRunTranscriptHead(
  event: BoardlyRealtimeEvent,
): RunTranscriptHead | undefined {
  return TERMINAL_RUN_EVENTS.has(event.type)
    ? parseRunTranscriptHead(event.payload.transcriptThroughSeq)
    : undefined;
}

export function runMessageTranscriptHead(
  event: BoardlyRealtimeEvent,
): RunTranscriptHead | undefined {
  if (event.type !== "run.message_created") return undefined;
  const head = parseRunTranscriptHead(event.payload.throughSeq);
  return head?.throughSeq === messageSequence(event) ? head : undefined;
}

export function parseRunTranscriptHead(
  throughSeq: unknown,
): RunTranscriptHead | undefined {
  return validSequence(throughSeq)
    ? { throughSeq: Number(throughSeq) }
    : undefined;
}

export function sameRunTranscriptHead(
  left: RunTranscriptHead | undefined,
  right: RunTranscriptHead | undefined,
): boolean {
  return (
    left !== undefined &&
    right !== undefined &&
    left.throughSeq === right.throughSeq
  );
}

function encodedFrameBytes(event: BoardlyRealtimeEvent): number {
  try {
    return utf8Encoder.encode(JSON.stringify(event)).byteLength;
  } catch {
    return -1;
  }
}

function validSequence(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0;
}
