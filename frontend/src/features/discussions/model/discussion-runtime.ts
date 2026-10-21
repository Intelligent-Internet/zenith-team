import type { LibraryArtifact } from "@/features/chat-panel/api/artifact-library-api";
import { remapDiscussionReferences } from "./discussion-mentions";
import {
  discussionsApi,
  discussionHistoryEndpoint,
  type DiscussionMessageAction,
  type DiscussionMutation,
  type DiscussionThreadState,
  type DiscussionScope,
  type DiscussionSession,
  type DiscussionState,
  type DiscussionReceipt,
  type DiscussionSummary,
  type SendDiscussionRequest,
} from "@/features/discussions/api/discussions-api";
import type { ZenithTeamApiError } from "@/shared/api/zenith-team-api";
import store from "@/state/store";
import {
  zenithTeamRealtimeClient,
  realtimeEventConversationId,
} from "@/shared/realtime/realtime-client.service";
import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";

type HistoryHandle = ReturnType<
  ReturnType<typeof discussionsApi.endpoints.getDiscussionHistory.initiate>
>;
type StateHandle = ReturnType<
  ReturnType<typeof discussionsApi.endpoints.getDiscussionStates.initiate>
>;
type SendHandle = ReturnType<
  ReturnType<typeof discussionsApi.endpoints.sendDiscussionMessage.initiate>
>;
export type DiscussionAnchor = {
  id: string;
  offset: number;
  cursor: string;
  atBottom: boolean;
};
export type DiscussionDraftAttachment = {
  id: string;
  name: string;
  size: number;
  contentType: string;
  previewUrl?: string;
};
export type DiscussionDraft = {
  text: string;
  start: number;
  end: number;
  attachmentIds: string[];
  attachments: DiscussionDraftAttachment[];
  artifacts: LibraryArtifact[];
  references: SendDiscussionRequest["body"]["references"];
};
type OwnedDraft = DiscussionDraft & {
  authority?: Pick<
    DiscussionState,
    "grantGeneration" | "companyMembershipRevision"
  >;
};
type PendingSend = {
  body: SendDiscussionRequest;
  submitted: string;
  submittedAttachmentIds: string[];
  status: "sending" | "failed";
  error?: string;
  handle?: SendHandle;
};
type HistoryLease = {
  scope: DiscussionScope;
  visible: boolean;
  touched: number;
  phase: "checking" | "ready" | "error" | "revoked";
  error?: string;
  guard: number;
  lease?: HistoryHandle;
  request?: HistoryHandle;
  busy: boolean;
  artifactRevision: number;
};

let generation = 0;
export const emptyDiscussionDraft: DiscussionDraft = {
  text: "",
  start: 0,
  end: 0,
  attachmentIds: [],
  attachments: [],
  artifacts: [],
  references: [],
};

/** Owns subscription leases and ephemeral UI state; every server response stays in RTK. */
export class DiscussionRuntime {
  readonly session: DiscussionSession;
  private pausedReads = new Set<string>();
  private reading = new Set<string>();
  private leases = new Map<string, HistoryLease>();
  private threads = new Map<string, HistoryLease>();
  private writes = new Map<string, { abort: () => void }>();
  private drafts = new Map<string, OwnedDraft>();
  private anchors = new Map<string, DiscussionAnchor>();
  private sends = new Map<string, PendingSend>();
  private listeners = new Set<() => void>();
  private version = 0;
  private stopped = false;
  private epoch = 0;
  private artifactRevision = 0;
  private visibleIds: string[] = [];
  private hintedIds = new Set<string>();
  private sidebarOffset = 0;
  private batch?: StateHandle;
  private pendingBatch?: StateHandle;
  private batchArgs?: DiscussionSession & { conversationIds: string[] };
  private checking = false;
  private dirty = false;
  private listsDirty = false;
  private lastCheckAt = 0;
  private lastListRefreshAt = Date.now();
  private scheduleTimer?: ReturnType<typeof setTimeout>;
  private expiryTimer?: ReturnType<typeof setTimeout>;
  private cleanupSignals?: () => void;
  private cleanupEvents?: () => void;
  private cleanupStatus?: () => void;

  constructor(companyId: string, userId: string) {
    this.session = { companyId, userId, generation: ++generation };
  }
  get active() {
    return !this.stopped;
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.version;
  private emit() {
    this.version += 1;
    this.listeners.forEach((listener) => listener());
  }
  scope(conversationId: string, rootId?: string): DiscussionScope {
    return {
      ...this.session,
      conversationId,
      view: rootId ? "thread" : "root",
      ...(rootId ? { rootId } : {}),
    };
  }

  start() {
    this.epoch += 1;
    this.stopped = false;
    this.cleanupSignals = subscribeToRefetchSignals((reason) => {
      const connected = zenithTeamRealtimeClient.getSnapshot().status === "ready";
      // DevTools/renderer focus is not a recovery event. Visibility and
      // reconnect still revalidate access, without clearing an authorized view.
      if (reason === "focus" && connected) return;
      if (
        !this.dirty &&
        Date.now() - this.lastCheckAt < 5_000 &&
        reason !== "recovery"
      )
        return;
      this.artifactRevision += 1;
      if (reason === "recovery")
        this.visibleIds.forEach((id) => this.hintedIds.add(id));
      this.schedule(reason === "recovery" || this.listsNeedRefresh());
    });
    this.cleanupEvents = zenithTeamRealtimeClient.onEvent((event) => {
      if (event.companyId !== this.session.companyId) return;
      const id = realtimeEventConversationId(event);
      if (event.type === "conversation.discussion_access_revoked" && id) {
        this.revoke(id);
        this.schedule(true);
      } else if (
        event.type === "artifact.access_changed" ||
        event.type === "artifact.changed" ||
        // A Whiteboard card shows its new title.
        event.type === "whiteboard.renamed" ||
        event.type === "conversation.discussion_huddle_changed" ||
        event.type === "project.access_changed" ||
        (event.type === "message.updated" && id && this.state(id))
      ) {
        // message.updated: a file on a message joined or left a Project,
        // which changes who sees it. project.access_changed: who reads a
        // Project changed, so a Project file in any open chat may now show
        // or be withheld; the server sends no per-message frame for that.
        // Reread the open histories like an artifact access change.
        this.artifactRevision += 1;
        this.schedule();
      } else if (event.type.startsWith("conversation.discussion_")) {
        if (event.type === "conversation.discussion_resync") {
          this.visibleIds.forEach((id) => this.hintedIds.add(id));
          this.artifactRevision += 1;
        }
        if (id) this.hintedIds.add(id);
        if (id && !this.state(id))
          store.dispatch(
            discussionsApi.util.invalidateTags([
              { type: "DiscussionDetail", id },
            ]),
          );
        // Known message hints need a state/history check, not every metadata
        // endpoint. The batch detects configuration and personal-state changes.
        this.schedule(!id || !this.state(id));
      } else if (
        event.type.startsWith("member.") ||
        event.type === "company.updated"
      ) {
        for (const entry of this.leases.values()) entry.phase = "checking";
        store.dispatch(
          discussionsApi.util.invalidateTags([{ type: "DiscussionDetail" }]),
        );
        this.emit();
        this.schedule(true);
      }
    });
    let wasReady = false;
    this.cleanupStatus = zenithTeamRealtimeClient.onStatus((snapshot) => {
      if (snapshot.status === "ready" && !wasReady) {
        this.visibleIds.forEach((id) => this.hintedIds.add(id));
        this.artifactRevision += 1;
        this.schedule(true);
      }
      wasReady = snapshot.status === "ready";
    });
    this.schedule(true);
    return () => this.stop();
  }

  private listsNeedRefresh() {
    // Returning to the app refreshes list membership after a long absence.
    return Date.now() - this.lastListRefreshAt >= 300_000;
  }

  private stop() {
    this.epoch += 1;
    this.stopped = true;
    this.checking = false;
    this.dirty = false;
    this.cleanupEvents?.();
    this.cleanupSignals?.();
    this.cleanupStatus?.();
    clearTimeout(this.scheduleTimer);
    clearTimeout(this.expiryTimer);
    this.scheduleTimer = undefined;
    this.pendingBatch?.abort();
    this.pendingBatch?.unsubscribe();
    this.pendingBatch = undefined;
    this.batch?.abort();
    this.batch?.unsubscribe();
    this.batch = undefined;
    this.batchArgs = undefined;
    for (const entry of this.leases.values()) this.release(entry);
    for (const entry of this.threads.values()) this.release(entry);
    for (const write of this.writes.values()) write.abort();
    this.threads.clear();
    this.writes.clear();
    for (const pending of this.sends.values()) {
      pending.handle?.abort();
      pending.handle?.reset();
    }
    this.leases.clear();
    for (const draft of this.drafts.values())
      for (const file of draft.attachments)
        if (file.previewUrl) URL.revokeObjectURL(file.previewUrl);
    this.drafts.clear();
    this.anchors.clear();
    this.sends.clear();
    this.pausedReads.clear();
    this.reading.clear();
    this.emit();
  }

  setVisibleIds(ids: string[]) {
    const next = [...new Set(ids)].sort();
    if (next.join() === this.visibleIds.join()) return;
    this.visibleIds = next;
    this.sidebarOffset = 0;
    this.schedule();
  }

  open(id: string) {
    this.pausedReads.delete(id);
    let entry = this.leases.get(id);
    if (!entry) {
      entry = {
        scope: this.scope(id),
        visible: true,
        touched: Date.now(),
        phase: "checking",
        guard: 0,
        busy: false,
        artifactRevision: -1,
      };
      this.leases.set(id, entry);
    }
    entry.visible = true;
    entry.touched = Date.now();
    const state = this.state(id);
    const pages = discussionsApi.endpoints.getDiscussionHistory.select(
      entry.scope,
    )(store.getState()).data?.pages;
    // A warm switch keeps the authorized window visible while the state batch
    // checks for changes. Recovery and changed grants still gate the view.
    if (
      entry.phase !== "ready" ||
      !entry.lease ||
      zenithTeamRealtimeClient.getSnapshot().status !== "ready" ||
      entry.artifactRevision !== this.artifactRevision ||
      !state?.capabilities.readHistory ||
      !pages?.length ||
      pages.some(
        (page) =>
          page.grantGeneration !== state.grantGeneration ||
          page.companyMembershipRevision !== state.companyMembershipRevision,
      )
    )
      entry.phase = "checking";
    this.expire();
    this.emit();
    this.schedule();
    return () => {
      if (this.leases.get(id) !== entry) return;
      entry.visible = false;
      const thread = this.threads.get(id);
      if (thread) thread.visible = false;
      entry.touched = Date.now();
      this.expire();
    };
  }

  private release(entry: HistoryLease) {
    entry.guard += 1;
    entry.request?.abort();
    entry.lease?.abort();
    entry.lease?.unsubscribe();
    entry.lease = undefined;
    entry.request = undefined;
    entry.busy = false;
  }

  private expire() {
    clearTimeout(this.expiryTimer);
    const inactive = [...this.leases.entries()]
      .filter(([, entry]) => !entry.visible)
      .sort((a, b) => a[1].touched - b[1].touched);
    for (const [id, entry] of inactive) {
      if (Date.now() - entry.touched < 300_000 && this.leases.size <= 10)
        continue;
      this.release(entry);
      this.leases.delete(id);
      const thread = this.threads.get(id);
      if (thread) this.release(thread);
      this.threads.delete(id);
    }
    const oldest = [...this.leases.values()]
      .filter((entry) => !entry.visible)
      .sort((a, b) => a.touched - b.touched)[0];
    if (oldest && !this.stopped)
      this.expiryTimer = setTimeout(
        () => this.expire(),
        Math.max(1, oldest.touched + 300_000 - Date.now()),
      );
  }

  revoke(id: string) {
    const entry = this.leases.get(id);
    if (entry) {
      this.release(entry);
      entry.phase = "revoked";
    }
    const thread = this.threads.get(id);
    if (thread) this.release(thread);
    this.threads.delete(id);
    this.writes.get(id)?.abort();
    this.writes.delete(id);
    for (const key of new Set([
      ...this.drafts.keys(),
      ...this.anchors.keys(),
      ...this.sends.keys(),
    ])) {
      if (key !== id && !key.startsWith(`${id}:`)) continue;
      this.drafts.delete(key);
      this.anchors.delete(key);
      const pending = this.sends.get(key);
      pending?.handle?.abort();
      pending?.handle?.reset();
      this.sends.delete(key);
    }
    this.emit();
  }

  readState(id: string, rootId?: string) {
    if (!rootId) return this.state(id)?.userState;
    const pages = discussionsApi.endpoints.getDiscussionThread.select(
      this.scope(id, rootId),
    )(store.getState()).data?.pages;
    return pages?.reduce((latest, page) =>
      (page.state?.revision ?? -1) >= (latest.state?.revision ?? -1)
        ? page
        : latest,
    ).state;
  }
  readsPaused(id: string, rootId?: string) {
    return this.pausedReads.has(this.streamKey(id, rootId));
  }
  async markUnread(id: string, seq: number, rootId?: string) {
    const key = this.streamKey(id, rootId);
    this.pausedReads.add(key);
    this.emit();
    try {
      await this.writeReadState(id, { manualUnreadSeq: seq }, rootId);
    } catch (error) {
      this.pausedReads.delete(key);
      this.emit();
      throw error;
    }
  }
  async readThrough(id: string, seq: number, rootId?: string) {
    const key = this.streamKey(id, rootId);
    const state = this.readState(id, rootId);
    if (
      this.pausedReads.has(key) ||
      this.reading.has(key) ||
      !state ||
      (seq <= state.readThroughSeq && !state.manualUnreadSeq)
    )
      return;
    this.reading.add(key);
    try {
      await this.writeReadState(id, { readThroughSeq: seq }, rootId);
    } catch {
      /* A failed read never moves the local cursor; the next read-state or recovery signal retries. */
    } finally {
      this.reading.delete(key);
    }
  }
  async resumeReading(id: string, rootId?: string) {
    await this.writeReadState(id, { manualUnreadSeq: 0 }, rootId);
    this.pausedReads.delete(this.streamKey(id, rootId));
    this.emit();
  }
  private async writeReadState(
    id: string,
    change: { readThroughSeq?: number; manualUnreadSeq?: number },
    rootId?: string,
  ) {
    const access = this.state(id);
    const state = this.readState(id, rootId);
    if (
      !this.active ||
      !access?.capabilities.readState ||
      !access.grantGeneration ||
      !state
    )
      throw new Error("Conversation is unavailable.");
    const body = {
      ...change,
      grantGeneration: access.grantGeneration,
      expectedRevision: state.revision,
    };
    const args = { ...this.session, conversationId: id, body };
    const request = rootId
      ? store.dispatch(
          discussionsApi.endpoints.updateDiscussionThreadState.initiate(
            { ...args, rootId, body },
            { track: false },
          ),
        )
      : store.dispatch(
          discussionsApi.endpoints.updateDiscussionReadState.initiate(args, {
            track: false,
          }),
        );
    try {
      await request.unwrap();
      if (this.active) this.schedule(true);
    } finally {
      request.reset();
    }
  }
  private streamKey(id: string, rootId?: string) {
    return rootId ? `${id}:${rootId}` : id;
  }
  status(id: string, rootId?: string) {
    if (!rootId) return this.leases.get(id);
    const entry = this.threads.get(id);
    return entry?.scope.rootId === rootId ? entry : undefined;
  }
  state(id: string) {
    if (!this.batchArgs) return undefined;
    return discussionsApi.endpoints.getDiscussionStates
      .select(this.batchArgs)(store.getState())
      .data?.items.find((state) => state.conversationId === id);
  }
  draft(id: string, rootId?: string) {
    return this.drafts.get(this.streamKey(id, rootId)) ?? emptyDiscussionDraft;
  }
  setDraft(
    id: string,
    draft: DiscussionDraft,
    rootId?: string,
    options?: { references: "replace" },
  ) {
    if (this.stopped || this.leases.get(id)?.phase === "revoked") return;
    const key = this.streamKey(id, rootId);
    const previous = this.draft(id, rootId);
    if (previous.text !== draft.text && options?.references !== "replace") {
      draft = {
        ...draft,
        references: remapDiscussionReferences(
          previous.text,
          draft.text,
          previous.references,
        ),
      };
    }

    const state = this.state(id);
    const authority =
      this.drafts.get(key)?.authority ??
      (state
        ? {
            grantGeneration: state.grantGeneration,
            companyMembershipRevision: state.companyMembershipRevision,
          }
        : undefined);
    this.drafts.set(key, { ...draft, authority });
    this.emit();
  }
  pending(id: string, rootId?: string) {
    return this.sends.get(this.streamKey(id, rootId));
  }
  dismissPending(id: string, rootId?: string) {
    const key = this.streamKey(id, rootId);
    if (this.sends.get(key)?.status === "failed") {
      this.sends.delete(key);
      this.emit();
    }
  }
  anchor(id: string, rootId?: string) {
    return this.anchors.get(this.streamKey(id, rootId));
  }
  clearAnchor(id: string, rootId?: string) {
    this.anchors.delete(this.streamKey(id, rootId));
  }
  setAnchor(id: string, anchor: DiscussionAnchor, rootId?: string) {
    if (this.stopped || this.leases.get(id)?.phase === "revoked") return;
    this.anchors.set(this.streamKey(id, rootId), anchor);
  }
  private ensureThread(id: string, rootId: string) {
    let entry = this.threads.get(id);
    if (entry?.scope.rootId !== rootId) {
      if (entry) this.release(entry);
      entry = {
        scope: this.scope(id, rootId),
        visible: true,
        touched: Date.now(),
        phase: "checking",
        guard: 0,
        busy: false,
        artifactRevision: -1,
      };
      this.threads.set(id, entry);
    }
    return entry!;
  }
  openThread(id: string, rootId: string) {
    this.pausedReads.delete(this.streamKey(id, rootId));
    const entry = this.ensureThread(id, rootId);
    entry.visible = true;
    this.emit();
    this.schedule();
    return () => {
      if (this.threads.get(id) === entry) entry.visible = false;
    };
  }

  schedule(lists = false) {
    if (this.stopped) return;
    this.dirty = true;
    this.listsDirty ||= lists;
    // Fixed delay from the first hint also bounds a continuously busy socket.
    if (
      this.scheduleTimer ||
      this.checking ||
      document.visibilityState !== "visible"
    )
      return;
    this.scheduleTimer = setTimeout(() => {
      this.scheduleTimer = undefined;
      void this.check();
    }, 150);
  }

  private async check() {
    if (this.stopped || this.checking) return;
    const epoch = this.epoch;
    this.checking = true;
    this.lastCheckAt = Date.now();
    this.dirty = false;
    const active = [...this.leases.entries()]
      .filter(([, entry]) => entry.visible)
      .map(([id]) => id);
    const sidebar = this.visibleIds.filter((id) => !active.includes(id));
    const rotated = [
      ...sidebar.slice(this.sidebarOffset),
      ...sidebar.slice(0, this.sidebarOffset),
    ];
    const ids = [...new Set([...active, ...this.hintedIds, ...rotated])].slice(
      0,
      50,
    );
    ids.forEach((id) => this.hintedIds.delete(id));
    if (this.hintedIds.size) this.dirty = true;
    this.sidebarOffset = sidebar.length
      ? (this.sidebarOffset + 50 - active.length) % sidebar.length
      : 0;
    let refreshLists = this.listsDirty;
    const changedDetails = new Set<string>();
    this.listsDirty = false;
    try {
      if (ids.length) {
        const args = { ...this.session, conversationIds: ids };
        const previous = this.batch;
        const previousStates = new Map(ids.map((id) => [id, this.state(id)]));
        const handle = store.dispatch(
          discussionsApi.endpoints.getDiscussionStates.initiate(args, {
            forceRefetch: true,
          }),
        );
        this.pendingBatch = handle;
        const response = await handle.unwrap();
        if (
          this.stopped ||
          this.epoch !== epoch ||
          this.pendingBatch !== handle
        )
          return;
        this.batch = handle;
        this.pendingBatch = undefined;
        // Keep the previous authorized RTK result readable while a differently
        // keyed batch is pending (sidebar reorder/additions change its IDs).
        this.batchArgs = args;
        previous?.unsubscribe();
        const states = new Map(
          response.items.map((state) => [state.conversationId, state]),
        );
        for (const id of ids) {
          const state = states.get(id);
          if (!state) {
            this.revoke(id);
            refreshLists = true;
            changedDetails.add(id);
            continue;
          }
          const before = previousStates.get(id);
          if (
            before &&
            (before.conversationRevision !== state.conversationRevision ||
              before.configurationRevision !== state.configurationRevision ||
              before.grantRevision !== state.grantRevision ||
              before.companyMembershipRevision !==
                state.companyMembershipRevision ||
              before.userState?.pinnedAt !== state.userState?.pinnedAt ||
              before.userState?.hiddenAt !== state.userState?.hiddenAt ||
              before.userState?.revision !== state.userState?.revision ||
              before.userState?.unreadCount !== state.userState?.unreadCount)
          ) {
            refreshLists = true;
            changedDetails.add(id);
          }
          const entry = this.leases.get(id);
          if (entry?.phase === "revoked") entry.phase = "checking";
          if (!entry?.visible) continue;
          const pages = discussionsApi.endpoints[
            discussionHistoryEndpoint(entry.scope)
          ].select(entry.scope)(store.getState()).data?.pages;
          const draftAuthority = this.drafts.get(id)?.authority;
          const draftGrantChanged =
            draftAuthority &&
            (draftAuthority.grantGeneration !== state.grantGeneration ||
              draftAuthority.companyMembershipRevision !==
                state.companyMembershipRevision);
          const grantChanged =
            draftGrantChanged ||
            pages?.some(
              (page) =>
                page.companyMembershipRevision !==
                  state.companyMembershipRevision ||
                page.grantGeneration !== state.grantGeneration,
            );
          if (grantChanged) {
            this.revoke(id);
            await this.replaceWindow(id);
          } else if (!pages?.length) {
            const anchor = this.anchors.get(id);
            await this.load(
              entry,
              anchor?.atBottom === false ? anchor.cursor : null,
            );
          } else if (
            this.anchors.get(id)?.atBottom === false &&
            !pages.some((page) =>
              page.items.some((item) => item.id === this.anchors.get(id)?.id),
            )
          ) {
            await this.replaceWindow(id, this.anchors.get(id)!.cursor);
          } else if (
            entry.artifactRevision !== this.artifactRevision ||
            pages.some(
              (page) =>
                page.conversationRevision !== state.conversationRevision,
            )
          ) {
            await this.refresh(entry);
          } else {
            entry.phase = "ready";
          }
          const thread = this.threads.get(id);
          if (thread?.visible && entry.phase === "ready") {
            const threadPages =
              discussionsApi.endpoints.getDiscussionThread.select(thread.scope)(
                store.getState(),
              ).data?.pages;
            if (!threadPages?.length)
              await this.load(
                thread,
                this.anchor(id, thread.scope.rootId)?.atBottom === false
                  ? this.anchor(id, thread.scope.rootId)!.cursor
                  : null,
              );
            else if (
              thread.artifactRevision !== this.artifactRevision ||
              threadPages.some(
                (page) =>
                  page.conversationRevision !== state.conversationRevision,
              ) ||
              before?.userState?.revision !== state.userState?.revision
            )
              await this.refresh(thread);
            else thread.phase = "ready";
          }
        }
        this.emit();
      }
      if (refreshLists && !this.stopped && this.epoch === epoch) {
        this.lastListRefreshAt = Date.now();
        store.dispatch(
          discussionsApi.util.invalidateTags([
            { type: "DiscussionList", id: this.session.companyId },
          ]),
        );
        // Inactive details now survive navigation too. Evict changed entries
        // without subscribers so reopening cannot reuse old channel metadata.
        for (const id of new Set([...active, ...changedDetails]))
          store.dispatch(
            discussionsApi.util.invalidateTags([
              { type: "DiscussionDetail", id },
            ]),
          );
      }
    } catch (error) {
      if (!this.stopped && this.epoch === epoch) {
        ids.forEach((id) => this.hintedIds.add(id));
        this.listsDirty ||= refreshLists;
        // A failed read still needs repair when no later event arrives.
        this.scheduleTimer = setTimeout(() => {
          this.scheduleTimer = undefined;
          this.schedule();
        }, 5_000);
        for (const id of active) {
          const entry = this.leases.get(id);
          if (entry && entry.phase !== "revoked") {
            this.readFailed(entry, error);
          }
        }
        this.emit();
      }
    } finally {
      if (this.epoch === epoch) {
        this.pendingBatch?.unsubscribe();
        this.pendingBatch = undefined;
        this.checking = false;
        if (this.dirty && !this.stopped) this.schedule();
      }
    }
  }

  private async load(entry: HistoryLease, cursor: string | null = null) {
    if (this.writes.has(entry.scope.conversationId)) return;
    if (entry.busy || this.stopped) {
      this.dirty = true;
      return;
    }
    const guard = entry.guard;
    const artifactRevision = this.artifactRevision;
    entry.busy = true;
    entry.phase = "checking";
    // A failed initial load still owns a subscription. Replace it on retry
    // so release/revoke can evict the cache through the single current lease.
    entry.lease?.unsubscribe();
    const handle = store.dispatch(
      discussionsApi.endpoints[discussionHistoryEndpoint(entry.scope)].initiate(
        entry.scope,
        {
          initialPageParam: cursor,
        },
      ),
    );
    entry.lease = handle;
    entry.request = handle;
    try {
      const history = await handle.unwrap();
      if (!this.stopped && guard === entry.guard) {
        const page = history.pages.at(-1);
        const message = page?.items.at(-1);
        if (page && message && message.seq === page.headSeq)
          this.updateActivity(entry.scope.conversationId, {
            message,
            headSeq: page.headSeq,
            conversationRevision: page.conversationRevision,
          });
        entry.artifactRevision = artifactRevision;
        entry.phase = "ready";
        entry.error = undefined;
      }
    } catch (error) {
      if (!this.stopped && guard === entry.guard) {
        if (
          cursor &&
          (error as ZenithTeamApiError)?.code === "discussion_cursor_expired"
        ) {
          this.revoke(entry.scope.conversationId);
          this.schedule(true);
        } else this.readFailed(entry, error);
      }
    } finally {
      if (guard === entry.guard) {
        entry.busy = false;
        entry.request = undefined;
        this.emit();
        if (
          entry.phase === "ready" &&
          entry.artifactRevision !== this.artifactRevision
        )
          this.schedule();
      }
    }
  }

  private async refresh(entry: HistoryLease) {
    if (this.writes.has(entry.scope.conversationId)) return;
    if (entry.busy || !entry.lease) {
      this.dirty = true;
      return;
    }
    const artifactRevision = this.artifactRevision;
    store.dispatch(
      discussionsApi.util.updateQueryData(
        discussionHistoryEndpoint(entry.scope),
        entry.scope,
        (data) => {
          data.pageParams = data.pages.map((page) => page.refreshCursor);
        },
      ),
    );
    await this.read(entry, () => entry.lease!.refetch());
    if (entry.phase === "ready") entry.artifactRevision = artifactRevision;
    if (
      entry.phase === "ready" &&
      entry.artifactRevision !== this.artifactRevision
    )
      this.schedule();
    const pages = discussionsApi.endpoints[
      discussionHistoryEndpoint(entry.scope)
    ].select(entry.scope)(store.getState()).data?.pages;
    const last = pages?.at(-1);
    if (
      entry.phase === "ready" &&
      this.anchor(entry.scope.conversationId, entry.scope.rootId)?.atBottom !==
        false &&
      last &&
      last.streamHeadSeq > (last.items.at(-1)?.seq ?? 0)
    ) {
      await this.page(
        entry.scope.conversationId,
        "forward",
        entry.scope.rootId,
      );
    }
  }

  private async read(entry: HistoryLease, request: () => HistoryHandle) {
    if (this.writes.has(entry.scope.conversationId)) return;
    if (entry.busy || this.stopped) {
      this.dirty = true;
      return;
    }
    const guard = entry.guard;
    entry.busy = true;
    const handle = request();
    entry.request = handle;
    this.emit();
    try {
      const history = await handle.unwrap();
      if (!this.stopped && guard === entry.guard) {
        const page = history.pages.at(-1);
        const message = page?.items.at(-1);
        if (page && message && message.seq === page.headSeq)
          this.updateActivity(entry.scope.conversationId, {
            message,
            headSeq: page.headSeq,
            conversationRevision: page.conversationRevision,
          });
        entry.phase = "ready";
        entry.error = undefined;
      }
    } catch (error) {
      if (!this.stopped && guard === entry.guard) {
        if ((error as ZenithTeamApiError)?.code === "discussion_cursor_expired") {
          this.revoke(entry.scope.conversationId);
          this.schedule(true);
        } else this.readFailed(entry, error);
      }
    } finally {
      if (guard === entry.guard) {
        entry.busy = false;
        entry.request = undefined;
        this.emit();
      }
    }
  }

  private readFailed(entry: HistoryLease, error: unknown) {
    if (
      (error as ZenithTeamApiError)?.status === 404 ||
      (error as ZenithTeamApiError)?.status === 403
    ) {
      if (!entry.scope.rootId) {
        this.revoke(entry.scope.conversationId);
        return;
      }
      // A missing thread does not prove that conversation access was revoked.
      this.schedule();
      entry.phase = "error";
      entry.error = discussionError(error);
    } else {
      entry.phase = "error";
      entry.error = discussionError(error);
    }
  }

  async page(id: string, direction: "forward" | "backward", rootId?: string) {
    const entry = this.status(id, rootId);
    if (!entry || entry.phase !== "ready" || entry.busy) return;
    await this.read(entry, () =>
      store.dispatch(
        discussionsApi.endpoints[
          discussionHistoryEndpoint(entry.scope)
        ].initiate(entry.scope, {
          subscribe: false,
          direction,
        }),
      ),
    );
  }

  async replaceWindow(
    id: string,
    cursor: string | null = null,
    rootId?: string,
  ) {
    const entry = this.status(id, rootId);
    if (!entry || this.stopped) return;
    this.release(entry);
    const guard = entry.guard;
    entry.phase = "checking";
    this.emit();
    // keepUnusedDataFor: 0 evicts through RTK's public subscription lifecycle.
    await new Promise((resolve) => setTimeout(resolve, 0));
    if (
      this.stopped ||
      entry.guard !== guard ||
      this.status(id, rootId) !== entry
    )
      return;
    if (cursor === null) this.clearAnchor(id, rootId);
    await this.load(entry, cursor);
  }

  async jumpLatest(id: string, rootId?: string) {
    const entry = this.status(id, rootId);
    if (!entry || this.stopped || entry.phase !== "ready") return;
    const guard = entry.guard;
    if (entry.request) await entry.request;
    if (this.stopped || entry.guard !== guard || entry.phase !== "ready")
      return;
    this.clearAnchor(id, rootId);
    // Refetch one latest page in the existing subscription. Keep the old
    // window mounted until the authorized response replaces it atomically.
    store.dispatch(
      discussionsApi.util.updateQueryData(
        discussionHistoryEndpoint(entry.scope),
        entry.scope,
        (data) => {
          data.pageParams[0] = null;
        },
      ),
    );
    await this.read(entry, () =>
      store.dispatch(
        discussionsApi.endpoints[
          discussionHistoryEndpoint(entry.scope)
        ].initiate(entry.scope, {
          forceRefetch: true,
          subscribe: false,
          refetchCachedPages: false,
        }),
      ),
    );
  }

  private updateActivity(
    id: string,
    receipt: Pick<
      DiscussionReceipt,
      "message" | "headSeq" | "conversationRevision"
    >,
  ) {
    const update = (item: DiscussionSummary) => {
      if (
        item.id !== id ||
        item.conversationRevision > receipt.conversationRevision
      )
        return;
      item.conversationRevision = receipt.conversationRevision;
      item.headSeq = Math.max(item.headSeq, receipt.headSeq);
      if (item.updatedAt < receipt.message.createdAt)
        item.updatedAt = receipt.message.createdAt;
    };
    for (const endpoint of [
      "listHumanDiscussions",
      "listChatChannels",
    ] as const) {
      for (const args of discussionsApi.util.selectCachedArgsForQuery(
        store.getState(),
        endpoint,
      )) {
        if (
          args.companyId !== this.session.companyId ||
          args.generation !== this.session.generation
        )
          continue;
        store.dispatch(
          discussionsApi.util.updateQueryData(endpoint, args, (data) => {
            for (const page of data.pages)
              for (const item of page.items) update(item);
          }),
        );
      }
    }
  }

  isMutating(id: string) {
    return this.writes.has(id);
  }

  private async performWrite<T>(
    id: string,
    start: () => {
      unwrap: () => Promise<T>;
      abort: () => void;
      reset: () => void;
    },
  ): Promise<T | undefined> {
    const entry = this.leases.get(id);
    if (this.stopped || entry?.phase !== "ready")
      throw new Error("Conversation access must be checked first.");
    if (this.writes.has(id))
      throw new Error("Another change is being saved. Please try again.");
    const guard = entry.guard;
    const token = { abort: () => {} };
    this.writes.set(id, token);
    this.emit();
    let handle: ReturnType<typeof start> | undefined;
    try {
      const related = store
        .dispatch(discussionsApi.util.getRunningQueriesThunk())
        .filter((request) => {
          const args = request.arg as Partial<DiscussionScope> | undefined;
          return (
            args?.companyId === this.session.companyId &&
            args.conversationId === id
          );
        });
      await Promise.allSettled([
        entry.request,
        this.threads.get(id)?.request,
        ...related,
      ]);
      if (
        this.stopped ||
        entry.guard !== guard ||
        this.writes.get(id) !== token
      )
        return;
      handle = start();
      token.abort = () => handle?.abort();
      const result = await handle.unwrap();
      if (
        this.stopped ||
        entry.guard !== guard ||
        this.writes.get(id) !== token
      )
        return;
      return result;
    } finally {
      handle?.reset();
      if (this.writes.get(id) === token) this.writes.delete(id);
      this.emit();
      this.schedule();
    }
  }

  private patchMessage(id: string, receipt: DiscussionMutation) {
    for (const endpoint of [
      "getDiscussionHistory",
      "getDiscussionThread",
    ] as const) {
      for (const scope of discussionsApi.util.selectCachedArgsForQuery(
        store.getState(),
        endpoint,
      )) {
        if (
          scope.companyId !== this.session.companyId ||
          scope.generation !== this.session.generation ||
          scope.conversationId !== id
        )
          continue;
        store.dispatch(
          discussionsApi.util.updateQueryData(endpoint, scope, (data) => {
            for (const page of data.pages) {
              for (let index = 0; index < page.items.length; index++) {
                const item = page.items[index];
                if (
                  item.id === receipt.message.id &&
                  item.revision <= receipt.message.revision
                )
                  page.items[index] = receipt.message;
              }
              if (
                page.root?.id === receipt.message.id &&
                page.root.revision <= receipt.message.revision
              )
                page.root = receipt.message;
            }
          }),
        );
      }
    }
    for (const args of discussionsApi.util.selectCachedArgsForQuery(
      store.getState(),
      "getDiscussionPins",
    )) {
      if (
        args.companyId !== this.session.companyId ||
        args.generation !== this.session.generation ||
        args.conversationId !== id
      )
        continue;
      store.dispatch(
        discussionsApi.util.updateQueryData(
          "getDiscussionPins",
          args,
          (data) => {
            for (const page of data.pages) {
              page.items = page.items.filter(
                (pin) =>
                  pin.message.id !== receipt.message.id ||
                  receipt.message.pinned,
              );
              for (const pin of page.items) {
                if (pin.message.id === receipt.message.id)
                  pin.message = receipt.message;
                if (pin.root?.id === receipt.message.id)
                  pin.root = receipt.message;
              }
            }
          },
        ),
      );
    }
    if (receipt.message.deletedAt) {
      for (const args of discussionsApi.util.selectCachedArgsForQuery(
        store.getState(),
        "getDiscussionReactions",
      )) {
        if (
          args.companyId !== this.session.companyId ||
          args.generation !== this.session.generation ||
          args.messageId !== receipt.message.id
        )
          continue;
        store.dispatch(
          discussionsApi.util.updateQueryData(
            "getDiscussionReactions",
            args,
            (data) => {
              for (const page of data.pages) {
                page.items = [];
                page.nextCursor = null;
              }
            },
          ),
        );
      }
    }
  }

  async mutateMessage(
    id: string,
    messageId: string,
    action: DiscussionMessageAction,
  ) {
    const receipt = await this.performWrite(id, () =>
      store.dispatch(
        discussionsApi.endpoints.mutateDiscussionMessage.initiate({
          ...this.session,
          conversationId: id,
          messageId,
          action,
        }),
      ),
    );
    if (receipt) this.patchMessage(id, receipt);
    return receipt;
  }

  async followThread(
    id: string,
    rootId: string,
    state: DiscussionThreadState,
    follow: "following" | "unfollowed",
  ) {
    const result = await this.performWrite(id, () =>
      store.dispatch(
        discussionsApi.endpoints.updateDiscussionThreadState.initiate({
          ...this.session,
          conversationId: id,
          rootId,
          body: { expectedRevision: state.revision, follow },
        }),
      ),
    );
    if (result)
      store.dispatch(
        discussionsApi.util.updateQueryData(
          "getDiscussionThread",
          this.scope(id, rootId),
          (data) => {
            for (const page of data.pages) page.state = result;
          },
        ),
      );
  }

  async focusMessage(id: string, messageId: string) {
    const entry = this.leases.get(id);
    if (this.stopped || entry?.phase !== "ready") return;
    const guard = entry.guard;
    const handle = store.dispatch(
      discussionsApi.endpoints.getDiscussionContext.initiate(
        { ...this.session, conversationId: id, messageId },
        { forceRefetch: true },
      ),
    );
    try {
      const context = await handle.unwrap();
      if (this.stopped || entry.guard !== guard) return;
      const rootId = context.root?.id;
      if (rootId) this.ensureThread(id, rootId);
      this.setAnchor(
        id,
        {
          id: messageId,
          offset: -48,
          cursor: context.refreshCursor,
          atBottom: false,
        },
        rootId,
      );
      await this.replaceWindow(id, context.refreshCursor, rootId);
      return { rootId };
    } catch (error) {
      if ([403, 404].includes(Number((error as ZenithTeamApiError)?.status)))
        this.schedule();
      throw error;
    } finally {
      handle.unsubscribe();
    }
  }

  async send(id: string, retry = false, rootId?: string) {
    const key = this.streamKey(id, rootId);
    if (this.stopped || this.sends.get(key)?.status === "sending") return;
    const draft = this.draft(id, rootId);
    let pending = this.sends.get(key);
    if (!retry) {
      if (
        pending ||
        (!draft.text.trim() &&
          draft.attachmentIds.length === 0 &&
          draft.artifacts.length === 0) ||
        !this.state(id)?.capabilities.send
      )
        return;
      const body: SendDiscussionRequest = {
        requestKey: crypto.randomUUID(),
        threadRootId: rootId ?? null,
        body: {
          version: 1,
          markdown: draft.text.replace(/\r\n?/g, "\n"),
          references: draft.references,
        },
        attachmentIds: draft.attachmentIds,
        artifactIds: draft.artifacts.map((a) => a.id),
      };
      pending = {
        body,
        submitted: draft.text,
        submittedAttachmentIds: [...draft.attachmentIds],
        status: "sending",
      };
      this.sends.set(key, pending);
    }
    if (!pending) return;
    pending.status = "sending";
    pending.error = undefined;
    this.emit();
    let handle: SendHandle | undefined;
    try {
      const receipt = await this.performWrite(id, () => {
        handle = store.dispatch(
          discussionsApi.endpoints.sendDiscussionMessage.initiate({
            ...this.scope(id, rootId),
            body: pending!.body,
          }),
        );
        pending!.handle = handle;
        return handle;
      });
      if (!receipt || this.stopped || this.sends.get(key) !== pending) return;
      this.patchMessage(id, receipt);
      this.updateActivity(id, receipt);
      const currentDraft = this.draft(id, rootId);
      const sent = new Set(pending.submittedAttachmentIds);
      const remaining = currentDraft.attachments.filter(
        (file) => !sent.has(file.id),
      );
      const remainingIds = currentDraft.attachmentIds.filter(
        (fileId) => !sent.has(fileId),
      );
      currentDraft.attachments
        .filter((file) => sent.has(file.id) && file.previewUrl)
        .forEach((file) => URL.revokeObjectURL(file.previewUrl!));
      const remainingArtifacts = currentDraft.artifacts.filter(
        (a) => !pending.body.artifactIds?.includes(a.id),
      );
      if (
        currentDraft.text === pending.submitted &&
        remaining.length === 0 &&
        remainingArtifacts.length === 0
      )
        this.drafts.delete(key);
      else
        this.drafts.set(key, {
          ...currentDraft,
          ...(currentDraft.text === pending.submitted
            ? { text: "", start: 0, end: 0, references: [] }
            : {}),
          attachmentIds: remainingIds,
          attachments: remaining,
          artifacts: remainingArtifacts,
        });
      this.sends.delete(key);
      this.clearAnchor(id, rootId);
      await this.jumpLatest(id, rootId);
      this.schedule();
    } catch (error) {
      if (!this.stopped && this.sends.get(key) === pending) {
        pending.status = "failed";
        pending.error = discussionError(error);
      }
    } finally {
      handle?.reset();
      this.emit();
    }
  }

  /**
   * Makes a Whiteboard in this chat: the server creates the Whiteboard
   * Artifact and shares it here with a message, which lands like any other.
   * A retry with the same `requestKey` returns the same message.
   */
  async sendWhiteboard(id: string, title: string, requestKey: string) {
    if (this.stopped || !this.state(id)?.capabilities.send)
      throw new Error("You can't send messages in this conversation.");
    const receipt = await this.performWrite(id, () =>
      store.dispatch(
        discussionsApi.endpoints.sendDiscussionMessage.initiate({
          ...this.scope(id),
          body: {
            requestKey,
            threadRootId: null,
            body: { version: 1, markdown: "", references: [] },
            whiteboard: { title },
          },
        }),
      ),
    );
    if (!receipt || this.stopped) return undefined;
    this.patchMessage(id, receipt);
    this.updateActivity(id, receipt);
    this.clearAnchor(id);
    void this.jumpLatest(id).then(() => this.schedule());
    return receipt;
  }
}

export function discussionError(error: unknown) {
  const value = error as Partial<ZenithTeamApiError> | undefined;
  if (value?.code === "discussion_revision_conflict")
    return "This message or conversation changed. Review the latest version, then try again.";
  if (value?.code === "discussion_unavailable")
    return "Chat is temporarily unavailable. Please try again. Your draft is kept.";
  if (value?.code === "discussion_read_only")
    return "This conversation is read-only. Your message has not been sent.";
  if (value?.status === 404 || value?.status === 403)
    return "This message or conversation is unavailable, or this action is not allowed.";
  return value?.message || "We couldn’t connect. Please try again.";
}
