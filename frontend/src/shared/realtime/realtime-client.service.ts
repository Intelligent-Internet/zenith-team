import {
  ZENITH_TEAM_API_BASE_URL,
  isAuthenticationFailure,
  refreshAuthSession,
} from "@/lib/api-client";
import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

const CONNECT_TIMEOUT_MS = 10_000;
const RECONNECT_STABILITY_MS = 30_000;
const MAX_RECENT_EVENT_IDS = 1_024;
const RECONNECT_DELAYS_MS = [500, 1_000, 2_500, 5_000, 10_000, 30_000] as const;
// After the fast ladder is exhausted the client keeps retrying forever at this
// slow cadence. A laptop sleep or a long outage must never leave the session
// permanently without realtime; terminal is reserved for deliberate stop() and
// for auth refusals that a token refresh could not fix.
const SLOW_RECONNECT_DELAY_MS = 60_000;
// A lone invalid frame is dropped and counted; only an unbroken run of them
// (a wrong endpoint or protocol mismatch) recycles the connection.
const MAX_CONSECUTIVE_INVALID_FRAMES = 10;
// Application close codes the server uses to refuse an authenticated session
// outright. Retrying cannot fix these once a token refresh has failed, so they
// are the only close codes allowed to end in the terminal state.
const AUTH_REFUSAL_CLOSE_CODES = new Set([
  4001, // unauthorized
  4003, // forbidden
]);

export type ZenithTeamRealtimeStatus =
  "idle" | "connecting" | "ready" | "reconnecting" | "terminal";

export type ZenithTeamRealtimeSnapshot = {
  activeCompanyId?: string;
  status: ZenithTeamRealtimeStatus;
};

export type ZenithTeamRealtimeActorType =
  ApiComponents["schemas"]["RealtimeEnvelope"]["actorType"];

export type ZenithTeamRealtimeEvent =
  ApiComponents["schemas"]["RealtimeEnvelope"];

export type ZenithTeamRunMessagePayload =
  ApiComponents["schemas"]["RunMessagePayload"];

export type ZenithTeamTerminalRunLifecyclePayload =
  ApiComponents["schemas"]["TerminalRunLifecyclePayload"];

export type ZenithTeamRunMessageRealtimeEvent =
  ApiComponents["schemas"]["RunMessageRealtimeEvent"];

export type ZenithTeamTerminalRunRealtimeEvent =
  ApiComponents["schemas"]["TerminalRunRealtimeEvent"];

export function realtimeEventConversationId(
  event: ZenithTeamRealtimeEvent,
): string | undefined {
  const ref = recordValue(event.payload.ref);
  const conversation = recordValue(event.payload.conversation);
  const referencedConversationId = stringValue(ref?.conversationId);
  return (
    referencedConversationId ??
    stringValue(event.payload.conversationId) ??
    stringValue(conversation?.id)
  );
}

type RealtimeEventHandler = (event: ZenithTeamRealtimeEvent) => void;
type RealtimeAuthenticationFailureHandler = () => void;
type RealtimeStatusHandler = (snapshot: ZenithTeamRealtimeSnapshot) => void;
type RealtimeRecoveryHandler = (reason: string) => void;

type ReadyWaiter = {
  reject: (error: Error) => void;
  resolve: () => void;
  timer: ReturnType<typeof setTimeout>;
};

export function resolveZenithTeamRealtimeUrl(
  companyId: string,
  apiBaseUrl = ZENITH_TEAM_API_BASE_URL,
): string {
  const normalizedCompanyId = companyId.trim();
  if (!normalizedCompanyId) {
    throw new Error("Realtime company id is required");
  }

  const url = new URL(apiBaseUrl, window.location.origin);
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username ||
    url.password
  ) {
    throw new Error("Realtime API URL is invalid");
  }

  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = "/api/v1/ws";
  url.search = "";
  url.searchParams.append("company_id", normalizedCompanyId);
  url.hash = "";
  return url.toString();
}

export function parseZenithTeamRealtimeEvent(
  source: string,
): ZenithTeamRealtimeEvent | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
  } catch {
    return undefined;
  }

  // Envelopes are validated by their REQUIRED keys and types only; unknown
  // extra keys are tolerated on purpose. An installable desktop client must
  // survive additive server evolution, so a new server field is stripped from
  // the returned event rather than rejecting the frame.
  const frame = recordValue(parsed);
  if (!frame) {
    return undefined;
  }

  const eventId = stringValue(frame.eventId);
  const type = stringValue(frame.type);
  const createdAt = stringValue(frame.createdAt);
  const actorId = nullableStringValue(frame.actorId);
  const companyId = nullableStringValue(frame.companyId);
  const actorType = actorTypeValue(frame.actorType);
  const payload = recordValue(frame.payload);
  if (
    !eventId ||
    !type ||
    !createdAt ||
    Number.isNaN(Date.parse(createdAt)) ||
    actorId === undefined ||
    companyId === undefined ||
    !actorType ||
    !payload
  ) {
    return undefined;
  }

  return { actorId, actorType, companyId, createdAt, eventId, payload, type };
}

class ZenithTeamRealtimeClient {
  private activeCompanyId: string | undefined;
  private authRefreshAttempted = false;
  private authRefreshPending = false;
  private connectTimer: ReturnType<typeof setTimeout> | undefined;
  private generation = 0;
  private hasConnected = false;
  private invalidFrameStreak = 0;
  private reconnectAttempt = 0;
  private reconnectStabilityTimer: ReturnType<typeof setTimeout> | undefined;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private running = false;
  private socket: WebSocket | null = null;
  private status: ZenithTeamRealtimeStatus = "idle";
  private readonly eventHandlers = new Set<RealtimeEventHandler>();
  private readonly authenticationFailureHandlers =
    new Set<RealtimeAuthenticationFailureHandler>();
  private readonly readyWaiters = new Set<ReadyWaiter>();
  private readonly recentEventIds = new Set<string>();
  private readonly recentEventOrder: string[] = [];
  private readonly recoveryHandlers = new Set<RealtimeRecoveryHandler>();
  private readonly statusHandlers = new Set<RealtimeStatusHandler>();

  getSnapshot(): ZenithTeamRealtimeSnapshot {
    return {
      status: this.status,
      ...(this.activeCompanyId
        ? { activeCompanyId: this.activeCompanyId }
        : {}),
    };
  }

  start(companyId: string): void {
    const normalizedCompanyId = companyId.trim();
    if (!normalizedCompanyId) {
      this.stop();
      return;
    }
    if (this.activeCompanyId === normalizedCompanyId) {
      if (this.status === "terminal") return;
      this.running = true;
      if (!this.socket && !this.reconnectTimer && !this.authRefreshPending) {
        this.deferConnect();
      }
      return;
    }

    this.stop();
    this.running = true;
    this.activeCompanyId = normalizedCompanyId;
    this.deferConnect();
  }

  stop(): void {
    this.running = false;
    this.generation += 1;
    this.clearConnectionTimers();
    this.rejectReadyWaiters(new Error("Realtime connection stopped"));
    const socket = this.socket;
    this.socket = null;
    closeSocket(socket, 1000, "client stopped");
    this.activeCompanyId = undefined;
    this.authRefreshAttempted = false;
    this.authRefreshPending = false;
    this.hasConnected = false;
    this.invalidFrameStreak = 0;
    this.reconnectAttempt = 0;
    this.recentEventIds.clear();
    this.recentEventOrder.length = 0;
    this.setStatus("idle");
  }

  // Nudges a waiting reconnect to run immediately, restarting the fast delay
  // ladder. Window focus and visibility changes call this so a slow-tier
  // retry does not sit out its full minute.
  retryNow(): void {
    if (!this.running || this.socket || this.authRefreshPending) return;
    this.reconnectAttempt = 0;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
    this.connect();
  }

  // A network change can leave WebSocket marked OPEN even though its old
  // connection no longer receives frames. Replace it so readiness also runs
  // the normal REST recovery for events missed during the outage.
  reconnectNow(): void {
    if (!this.running || this.authRefreshPending) return;
    const socket = this.socket;
    this.socket = null;
    this.generation += 1;
    this.clearConnectionTimers();
    closeSocket(socket, 1000, "network changed");
    this.reconnectAttempt = 0;
    this.connect();
  }

  async ensureReady(companyId: string): Promise<void> {
    const normalizedCompanyId = companyId.trim();
    if (!normalizedCompanyId) {
      throw new Error("Realtime company id is required");
    }
    if (
      this.status === "terminal" &&
      this.activeCompanyId === normalizedCompanyId
    ) {
      throw new Error("Realtime connection is unavailable");
    }
    this.start(normalizedCompanyId);
    if (this.status === "ready") return;
    return new Promise<void>((resolve, reject) => {
      const waiter: ReadyWaiter = {
        reject,
        resolve,
        timer: setTimeout(() => {
          this.readyWaiters.delete(waiter);
          reject(new Error("Realtime connection timed out"));
        }, CONNECT_TIMEOUT_MS),
      };
      this.readyWaiters.add(waiter);
    });
  }

  onEvent(handler: RealtimeEventHandler): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  onAuthenticationFailure(
    handler: RealtimeAuthenticationFailureHandler,
  ): () => void {
    this.authenticationFailureHandlers.add(handler);
    return () => this.authenticationFailureHandlers.delete(handler);
  }

  onRecovery(handler: RealtimeRecoveryHandler): () => void {
    this.recoveryHandlers.add(handler);
    return () => this.recoveryHandlers.delete(handler);
  }

  onStatus(handler: RealtimeStatusHandler): () => void {
    this.statusHandlers.add(handler);
    handler(this.getSnapshot());
    return () => this.statusHandlers.delete(handler);
  }

  private deferConnect(): void {
    const generation = this.generation;
    const isReconnect = this.hasConnected || this.reconnectAttempt > 0;
    this.setStatus(isReconnect ? "reconnecting" : "connecting");

    // React StrictMode replays effect setup and cleanup in the same turn.
    // Defer only socket construction so stop() can invalidate that replay;
    // deliberate stops still close any socket that was already constructed.
    queueMicrotask(() => {
      if (this.generation !== generation) return;
      this.connect();
    });
  }

  private connect(): void {
    const companyId = this.activeCompanyId;
    if (!this.running || !companyId || this.socket || this.authRefreshPending) {
      return;
    }

    const isReconnect = this.hasConnected || this.reconnectAttempt > 0;
    this.setStatus(isReconnect ? "reconnecting" : "connecting");
    const generation = ++this.generation;
    let opened = false;
    let socket: WebSocket;
    try {
      socket = new WebSocket(resolveZenithTeamRealtimeUrl(companyId));
    } catch {
      this.handleRetryableFailure(false);
      return;
    }

    this.socket = socket;
    this.connectTimer = setTimeout(() => {
      if (!this.isCurrent(socket, generation)) return;
      this.socket = null;
      this.generation += 1;
      this.clearConnectTimer();
      closeSocket(socket);
      this.handleRetryableFailure(false);
    }, CONNECT_TIMEOUT_MS);

    socket.onopen = () => {
      if (!this.isCurrent(socket, generation)) return;
      opened = true;
      this.clearConnectTimer();
      const recovered = this.hasConnected;
      this.hasConnected = true;
      this.authRefreshAttempted = false;
      this.invalidFrameStreak = 0;
      this.reconnectStabilityTimer = setTimeout(() => {
        if (!this.isCurrent(socket, generation)) return;
        this.reconnectStabilityTimer = undefined;
        this.reconnectAttempt = 0;
      }, RECONNECT_STABILITY_MS);
      this.setStatus("ready");
      this.resolveReadyWaiters();
      if (recovered) {
        this.recoveryHandlers.forEach((handler) => handler("reconnected"));
      }
    };

    socket.onmessage = (message) => {
      if (!this.isCurrent(socket, generation)) return;
      const event =
        typeof message.data === "string"
          ? parseZenithTeamRealtimeEvent(message.data)
          : undefined;
      if (!event) {
        // A single invalid frame is dropped, not fatal: an installed client
        // must tolerate a server speaking a slightly newer dialect. Only an
        // unbroken run of invalid frames — every frame, which means a wrong
        // endpoint or protocol mismatch — recycles the connection.
        this.invalidFrameStreak += 1;
        if (this.invalidFrameStreak < MAX_CONSECUTIVE_INVALID_FRAMES) return;
        this.invalidFrameStreak = 0;
        this.socket = null;
        this.generation += 1;
        this.clearReconnectStabilityTimer();
        closeSocket(socket, 1007, "invalid realtime frames");
        this.handleRetryableFailure(true);
        return;
      }
      this.invalidFrameStreak = 0;
      this.handleEvent(event);
    };

    socket.onerror = () => {
      // Browsers disclose neither an HTTP upgrade status nor a useful socket
      // error. The close event owns terminal/retry classification.
    };

    socket.onclose = (event) => {
      if (!this.isCurrent(socket, generation)) return;
      this.socket = null;
      this.clearReconnectStabilityTimer();
      this.clearConnectTimer();
      if (!this.running) return;
      if (AUTH_REFUSAL_CLOSE_CODES.has(event.code)) {
        this.handleAuthRefusal(
          event.reason || "Realtime authorization refused",
        );
        return;
      }
      this.handleRetryableFailure(opened);
    };
  }

  private handleEvent(event: ZenithTeamRealtimeEvent): void {
    if (this.hasSeenEvent(event.eventId)) return;
    this.eventHandlers.forEach((handler) => handler(event));
  }

  private handleRetryableFailure(opened: boolean): void {
    if (!this.running) return;
    if (!opened && !this.authRefreshAttempted) {
      // A failed HTTP upgrade may be an expired token; a network outage fails
      // the refresh too. A rejected or missing saved session cannot recover by
      // retrying the socket, while an unreachable API still can.
      this.attemptAuthRefresh((error) => {
        if (isAuthenticationFailure(error)) {
          this.setAuthenticationTerminal(error);
          return;
        }
        this.scheduleReconnect();
      });
      return;
    }
    this.scheduleReconnect();
  }

  private handleAuthRefusal(reason: string): void {
    if (!this.running) return;
    if (this.authRefreshAttempted) {
      this.setTerminal(new Error(reason));
      return;
    }
    // A refusal after a successful open resets authRefreshAttempted, so a
    // server that keeps refusing while refreshes succeed would otherwise loop
    // at full speed. Reconnecting through the delay ladder keeps that cheap.
    this.attemptAuthRefresh(() => this.setTerminal(new Error(reason)), false);
  }

  private attemptAuthRefresh(
    onRefreshFailure: (error: unknown) => void,
    reconnectImmediately = true,
  ): void {
    this.authRefreshAttempted = true;
    this.authRefreshPending = true;
    const generation = this.generation;
    void refreshAuthSession().then(
      () => {
        if (this.generation !== generation || !this.authRefreshPending) return;
        this.authRefreshPending = false;
        this.scheduleReconnect(reconnectImmediately);
      },
      (error) => {
        if (this.generation !== generation || !this.authRefreshPending) return;
        this.authRefreshPending = false;
        onRefreshFailure(error);
      },
    );
  }

  private setAuthenticationTerminal(error: unknown): void {
    this.setTerminal(
      error instanceof Error
        ? error
        : new Error("Realtime authentication is unavailable"),
    );
    this.authenticationFailureHandlers.forEach((handler) => handler());
  }

  private scheduleReconnect(immediate = false): void {
    if (!this.running || this.reconnectTimer || this.socket) return;

    this.setStatus("reconnecting");
    // Past the fast ladder the client drops to an indefinite slow tier rather
    // than the terminal state: realtime must survive laptop sleeps and long
    // outages, and retryNow() short-circuits the wait when connectivity
    // observably returns.
    const baseDelay = immediate
      ? 0
      : (RECONNECT_DELAYS_MS[this.reconnectAttempt] ?? SLOW_RECONNECT_DELAY_MS);
    const delay = Math.round(baseDelay * (0.8 + Math.random() * 0.4));
    if (this.reconnectAttempt < RECONNECT_DELAYS_MS.length) {
      this.reconnectAttempt += 1;
    }
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      this.connect();
    }, delay);
  }

  private setTerminal(error: Error): void {
    this.running = false;
    this.authRefreshPending = false;
    this.generation += 1;
    this.clearConnectionTimers();
    this.rejectReadyWaiters(error);
    this.setStatus("terminal");
  }

  private setStatus(status: ZenithTeamRealtimeStatus): void {
    if (this.status === status) return;
    this.status = status;
    const snapshot = this.getSnapshot();
    this.statusHandlers.forEach((handler) => handler(snapshot));
  }

  private isCurrent(socket: WebSocket, generation: number): boolean {
    return this.socket === socket && this.generation === generation;
  }

  private clearConnectTimer(): void {
    if (this.connectTimer) clearTimeout(this.connectTimer);
    this.connectTimer = undefined;
  }

  private clearConnectionTimers(): void {
    this.clearConnectTimer();
    this.clearReconnectStabilityTimer();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
  }

  private clearReconnectStabilityTimer(): void {
    if (this.reconnectStabilityTimer) {
      clearTimeout(this.reconnectStabilityTimer);
    }
    this.reconnectStabilityTimer = undefined;
  }

  private resolveReadyWaiters(): void {
    for (const waiter of this.readyWaiters) {
      clearTimeout(waiter.timer);
      waiter.resolve();
      this.readyWaiters.delete(waiter);
    }
  }

  private rejectReadyWaiters(error: Error): void {
    for (const waiter of this.readyWaiters) {
      clearTimeout(waiter.timer);
      waiter.reject(error);
      this.readyWaiters.delete(waiter);
    }
  }

  private hasSeenEvent(eventId: string): boolean {
    if (this.recentEventIds.has(eventId)) return true;
    this.recentEventIds.add(eventId);
    this.recentEventOrder.push(eventId);
    while (this.recentEventOrder.length > MAX_RECENT_EVENT_IDS) {
      const removed = this.recentEventOrder.shift();
      if (removed) this.recentEventIds.delete(removed);
    }
    return false;
  }
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function nullableStringValue(value: unknown): string | null | undefined {
  if (value === null) return null;
  return stringValue(value);
}

function actorTypeValue(
  value: unknown,
): ZenithTeamRealtimeActorType | undefined {
  return value === "user" || value === "agent" || value === "system"
    ? value
    : undefined;
}

function closeSocket(
  socket: WebSocket | null | undefined,
  code?: number,
  reason?: string,
): void {
  if (!socket) return;
  try {
    if (code === undefined) {
      socket.close();
    } else {
      socket.close(code, reason);
    }
  } catch {
    // A browser can reject close(code, reason) while an HTTP upgrade is still
    // pending. The client already detached that socket, so no state remains.
  }
}

export const zenithTeamRealtimeClient = new ZenithTeamRealtimeClient();
