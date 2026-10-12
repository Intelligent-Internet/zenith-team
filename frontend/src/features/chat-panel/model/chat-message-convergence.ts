import type { ChatMessage } from "@/shared/chat/types";

export type StableChatMessage = ChatMessage & {
  messageId?: string;
  messageSequence?: number;
};

type StableChatMessageChanges = Partial<Omit<StableChatMessage, "id" | "team">>;

export class StableChatMessageIndex {
  private readonly byClientMessageId = new Map<string, StableChatMessage>();
  private readonly byLocalId = new Map<string, StableChatMessage>();
  private readonly byMessageId = new Map<string, StableChatMessage>();
  // A run id is on both authors now — the user's words and the agent's answer
  // share one. Only the agent's row is indexed by it, so this stays what every
  // caller asks it for: the answer belonging to that run.
  private readonly byAgentRunId = new Map<string, StableChatMessage>();

  remember(message: StableChatMessage): void {
    const key = localMessageKey(message.team, message.id);
    const previous = this.byLocalId.get(key);
    if (
      previous?.clientMessageId &&
      (previous.clientMessageId !== message.clientMessageId ||
        previous.author !== message.author)
    ) {
      this.byClientMessageId.delete(
        clientMessageKey(previous.clientMessageId, previous.author),
      );
    }
    if (previous?.messageId && previous.messageId !== message.messageId) {
      this.byMessageId.delete(previous.messageId);
    }
    if (
      previous?.author === "team" &&
      previous.runId !== undefined &&
      previous.runId !== message.runId
    ) {
      this.byAgentRunId.delete(previous.runId);
    }
    this.byLocalId.set(key, message);
    if (message.clientMessageId) {
      this.byClientMessageId.set(
        clientMessageKey(message.clientMessageId, message.author),
        message,
      );
    }
    if (message.messageId) {
      this.byMessageId.set(message.messageId, message);
    }
    if (message.author === "team" && message.runId !== undefined) {
      this.byAgentRunId.set(message.runId, message);
    }
  }

  replaceTeam(
    team: StableChatMessage["team"],
    messages: readonly StableChatMessage[],
  ): void {
    for (const [key, message] of this.byClientMessageId) {
      if (message.team === team) this.byClientMessageId.delete(key);
    }
    for (const [key, message] of this.byLocalId) {
      if (message.team === team) this.byLocalId.delete(key);
    }
    for (const [messageId, message] of this.byMessageId) {
      if (message.team === team) this.byMessageId.delete(messageId);
    }
    for (const [runId, message] of this.byAgentRunId) {
      if (message.team === team) this.byAgentRunId.delete(runId);
    }
    for (const message of messages) this.remember(message);
  }

  findByMessageId(messageId: string): StableChatMessage | undefined {
    return this.byMessageId.get(messageId);
  }

  findByClientMessageId(
    clientMessageId: string,
    author: StableChatMessage["author"],
  ): StableChatMessage | undefined {
    return this.byClientMessageId.get(
      clientMessageKey(clientMessageId, author),
    );
  }

  findByAgentRunId(runId: string): StableChatMessage | undefined {
    return this.byAgentRunId.get(runId);
  }

  listTeam(team: StableChatMessage["team"]): StableChatMessage[] {
    return [...this.byLocalId.values()].filter(
      (message) => message.team === team,
    );
  }

  update(
    team: StableChatMessage["team"],
    id: number,
    changes: StableChatMessageChanges,
  ): StableChatMessage {
    const currentByMessageId = changes.messageId
      ? this.byMessageId.get(changes.messageId)
      : undefined;
    const indexedCurrent =
      currentByMessageId ?? this.byLocalId.get(localMessageKey(team, id));
    const current = indexedCurrent?.team === team ? indexedCurrent : undefined;
    const updated: StableChatMessage = {
      ...(current ?? {
        author: "team",
        id,
        team,
        text: "",
      }),
      ...changes,
      id,
      team,
    };
    if (
      current?.clientMessageId &&
      (current.clientMessageId !== updated.clientMessageId ||
        current.author !== updated.author)
    ) {
      this.byClientMessageId.delete(
        clientMessageKey(current.clientMessageId, current.author),
      );
    }
    if (current?.messageId && current.messageId !== updated.messageId) {
      this.byMessageId.delete(current.messageId);
    }
    if (
      current?.author === "team" &&
      current.runId !== undefined &&
      current.runId !== updated.runId
    ) {
      this.byAgentRunId.delete(current.runId);
    }
    this.remember(updated);
    return updated;
  }

  clear(): void {
    this.byClientMessageId.clear();
    this.byLocalId.clear();
    this.byMessageId.clear();
    this.byAgentRunId.clear();
  }
}

export type StableRunEnvelopeTarget =
  | { kind: "append" }
  | { kind: "conflict" }
  | { kind: "patch"; message: StableChatMessage };

// The identity a canonical envelope resolves by: who wrote it, where, and
// which message, sequence or Run it belongs to. Nothing else is read.
export type StableRunEnvelopeIdentity = Pick<
  StableChatMessage,
  | "author"
  | "conversationId"
  | "messageId"
  | "messageSequence"
  | "runId"
  | "team"
>;

export function resolveStableRunEnvelopeTarget(
  index: StableChatMessageIndex,
  canonical: StableRunEnvelopeIdentity,
  expectedClientMessageId?: string,
): StableRunEnvelopeTarget {
  const byMessageId = canonical.messageId
    ? index.findByMessageId(canonical.messageId)
    : undefined;
  const byRunId =
    canonical.author === "team" && canonical.runId !== undefined
      ? index.findByAgentRunId(canonical.runId)
      : undefined;
  const byClientMessageId = expectedClientMessageId
    ? index.findByClientMessageId(expectedClientMessageId, "team")
    : undefined;
  if (expectedClientMessageId && !byClientMessageId) {
    return { kind: "conflict" };
  }
  const candidates = [byClientMessageId, byMessageId, byRunId].filter(
    (message): message is StableChatMessage => Boolean(message),
  );
  const target = candidates[0];
  if (!target) return { kind: "append" };
  if (
    candidates.some(
      (candidate) =>
        candidate.id !== target.id || candidate.team !== target.team,
    ) ||
    target.author !== "team" ||
    target.team !== canonical.team ||
    (target.conversationId &&
      canonical.conversationId &&
      target.conversationId !== canonical.conversationId) ||
    (expectedClientMessageId &&
      target.clientMessageId !== expectedClientMessageId) ||
    (target.messageId &&
      canonical.messageId &&
      target.messageId !== canonical.messageId) ||
    (target.messageSequence !== undefined &&
      canonical.messageSequence !== undefined &&
      target.messageSequence !== canonical.messageSequence) ||
    (target.runId !== undefined &&
      canonical.runId !== undefined &&
      target.runId !== canonical.runId)
  ) {
    return { kind: "conflict" };
  }
  return { kind: "patch", message: target };
}

export function patchStableMessageEnvelope(
  pending: StableChatMessage,
  canonical: StableChatMessage,
): StableChatMessage {
  if (
    pending.messageId &&
    canonical.messageId &&
    pending.messageId !== canonical.messageId
  ) {
    return pending;
  }
  return {
    ...pending,
    ...canonical,
    id: pending.id,
    messageSequence:
      canonical.messageSequence ?? pending.messageSequence ?? canonical.id,
  };
}

export function hasSameStableMessageId(
  left: StableChatMessage,
  right: StableChatMessage,
): boolean {
  return Boolean(
    left.messageId && right.messageId && left.messageId === right.messageId,
  );
}

function localMessageKey(team: StableChatMessage["team"], id: number): string {
  return `${team}:${id}`;
}

function clientMessageKey(
  clientMessageId: string,
  author: StableChatMessage["author"],
): string {
  return `${author}:${clientMessageId}`;
}
