import * as Y from "yjs";
import { apiClient, ApiClientError } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import {
  boardPath,
  boardScopeKey,
  notifyBoardChange,
  type BoardScope,
  type BoardHydratedItem,
} from "./board-api";
import {
  decodeBoardUpdate,
  deleteBoardDraft,
  encodeBoardUpdate,
  readBoardDrafts,
  writeBoardDraft,
} from "./board-drafts";

type TextInput = components["schemas"]["BoardTextInput"];
type TextResult = components["schemas"]["BoardTextResult"];
/** `stamp` identifies the write, so a tab removes only a row it owns. */
export type NoteDraft = {
  update: string;
  text?: string;
  inflight?: TextInput;
  retries?: TextInput[];
  stamp?: string;
};
type StoredNoteDraft = { key: string; value: NoteDraft };
export type NoteStatus =
  "loading" | "saved" | "saving" | "offline" | "conflict" | "failed";
/** The server's plain-text limit for one note. */
export const boardMaxNoteText = 64 * 1024;
export function noteText(doc: Y.Doc): Y.XmlText {
  const paragraph = doc.getXmlFragment("content").get(0);
  if (
    !(paragraph instanceof Y.XmlElement) ||
    !(paragraph.get(0) instanceof Y.XmlText)
  )
    throw new Error("This note uses an unsupported text format.");
  return paragraph.get(0) as Y.XmlText;
}
function replaceText(text: Y.XmlText, next: string) {
  const previous = text.toString();
  let start = 0;
  while (
    start < previous.length &&
    start < next.length &&
    previous[start] === next[start]
  )
    start++;
  let end = 0;
  while (
    end < previous.length - start &&
    end < next.length - start &&
    previous[previous.length - 1 - end] === next[next.length - 1 - end]
  )
    end++;
  if (previous.length - start - end)
    text.delete(start, previous.length - start - end);
  if (next.length - start - end)
    text.insert(start, next.slice(start, next.length - end));
}

// Scene revisions never touch this document. Each note independently acknowledges
// updates; a slow or conflicting object cannot stall typing in another note.
export class BoardNoteSession {
  doc = new Y.Doc();
  private accepted = new Y.Doc();
  private undoManager?: Y.UndoManager;
  private pending: Uint8Array[] = [];
  private inflight?: TextInput;
  private inflightUpdate?: Uint8Array;
  private retries: TextInput[] = [];
  private sending?: Promise<void>;
  private timer?: ReturnType<typeof setTimeout>;
  private listeners = new Set<() => void>();
  private generation = 0;
  private disposed = false;
  private localOrigin = {};
  private controller = new AbortController();
  private writeChain = Promise.resolve();
  // Set once the saved note and any local draft are merged. Until then this
  // session neither writes nor removes the stored draft.
  private hydrated = false;
  // Keep source rows until their updates are saved in this session's row.
  private recovered: StoredNoteDraft[] = [];
  // The stamp of the stored row this session last wrote.
  private stamp?: string;
  // The note itself refuses this draft (deleted or restored, or blocked).
  private blocked = false;
  private retryDelay = 1000;
  status: NoteStatus = "loading";
  error = "";
  revision = 0;
  ready: Promise<void>;
  readonly key: string;
  private readonly draftKey: string;
  constructor(
    readonly scope: BoardScope,
    readonly itemId: string,
    readonly lifecycleRevision: number,
    userId: string,
    private hydrate: () => Promise<BoardHydratedItem>,
    private recoverDraft = true,
  ) {
    this.key = `${userId}:${boardScopeKey(scope)}:note:v1:${itemId}:${lifecycleRevision}`;
    // Concurrent tabs never replace each other's unacknowledged updates.
    this.draftKey = `${this.key}:${crypto.randomUUID()}`;
    this.ready = this.initialize();
  }
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.generation;
  private emit() {
    this.generation++;
    this.listeners.forEach((listener) => listener());
  }
  get loaded() {
    return this.hydrated;
  }
  get text() {
    return this.hydrated ? noteText(this.doc).toString() : "";
  }
  get dirty() {
    return (
      this.pending.length > 0 || !!this.inflight || this.retries.length > 0
    );
  }
  /** Unsaved text exists, merged or still waiting for the note to load. */
  get hasDraft() {
    return this.dirty || this.recovered.length > 0;
  }
  /** Writing, or waiting to write; a session in this state is never released. */
  get busy() {
    return (
      this.status === "loading" || !!this.sending || this.timer !== undefined
    );
  }
  get canEdit() {
    return this.recoverDraft;
  }
  /** The unsaved text to export: plain text, or the raw draft if unreadable. */
  draftExport(): { content: string; json: boolean } {
    if (this.hydrated) return { content: this.text, json: false };
    if (
      this.recovered.length === 1 &&
      this.recovered[0].value.text !== undefined
    )
      return { content: this.recovered[0].value.text, json: false };
    return {
      content: JSON.stringify(
        this.recovered.map((row) => row.value),
        null,
        2,
      ),
      json: true,
    };
  }
  /** The acknowledged state this note holds, for a delta-only refresh. */
  stateVector() {
    return encodeBoardUpdate(Y.encodeStateVector(this.accepted));
  }
  private async initialize() {
    try {
      const saved = this.hydrate();
      // A failed load must still know about its draft to keep it recoverable.
      saved.catch(() => {});
      const recovered = this.recoverDraft
        ? await readBoardDrafts<NoteDraft>(this.key).then(
            (rows) =>
              rows.filter(
                (row) =>
                  row.key === this.key || row.key.startsWith(`${this.key}:`),
              ),
            () => {
              this.error =
                "Local recovery is unavailable. Keep this note open until Saved.";
              return [];
            },
          )
        : [];
      if (this.disposed) return;
      if (recovered.length) {
        this.recovered = recovered;
        this.emit();
      }
      const item = await saved;
      if (this.disposed) return;
      if (item.lifecycleRevision !== this.lifecycleRevision)
        throw new Error(
          "This note was deleted or restored. Reopen it to use its current version.",
        );
      if (!item.state)
        throw new Error("The server did not return the note text state.");
      this.apply(item);
      this.undoManager?.destroy();
      this.undoManager = new Y.UndoManager(noteText(this.doc), {
        trackedOrigins: new Set([this.localOrigin]),
        captureTimeout: 500,
      });
      const retries = new Map<string, TextInput>();
      for (const { value } of this.recovered) {
        if (value.update) {
          const update = decodeBoardUpdate(value.update);
          Y.applyUpdate(this.doc, update, "recovery");
          this.pending.push(update);
        }
        for (const input of [value.inflight, ...(value.retries ?? [])]) {
          if (input) retries.set(input.requestKey, input);
        }
      }
      // Requests with unknown outcomes keep their original receipt keys.
      this.retries = [...retries.values()];
      this.hydrated = true;
      this.doc.on("update", this.onUpdate);
      this.status = this.dirty ? "saving" : "saved";
      this.emit();
      if (this.dirty) this.schedule();
    } catch (error) {
      if (this.disposed) return;
      // The stored draft is untouched; the note can be loaded again.
      this.status = "failed";
      this.error =
        error instanceof Error && error.message
          ? error.message
          : "The note could not be opened.";
      this.emit();
    }
  }
  /** Load a note again after its first load failed. */
  retry() {
    if (this.status !== "failed" || this.disposed) return;
    this.status = "loading";
    this.error = "";
    this.emit();
    this.ready = this.initialize();
  }
  private apply(saved: BoardHydratedItem) {
    if (!saved.state || saved.textRevision < this.revision) return;
    Y.applyUpdate(this.accepted, decodeBoardUpdate(saved.state), "server");
    Y.applyUpdate(this.doc, decodeBoardUpdate(saved.state), "server");
    this.revision = saved.textRevision;
    this.emit();
  }
  merge(saved: BoardHydratedItem) {
    if (!this.hydrated) return;
    if (saved.lifecycleRevision !== this.lifecycleRevision) {
      this.blocked = true;
      this.status = "conflict";
      this.error =
        "This note was deleted and restored. Your earlier draft remains local; export it before leaving.";
      this.emit();
      return;
    }
    this.apply(saved);
  }
  private onUpdate = (update: Uint8Array, origin: unknown) => {
    if (origin !== this.localOrigin && origin !== this.undoManager) {
      this.emit();
      return;
    }
    this.pending.push(update);
    this.status = "saving";
    this.error = "";
    void this.persist();
    this.emit();
    this.schedule();
  };
  edit(value: string) {
    if (!this.hydrated || this.status === "conflict") return;
    this.doc.transact(
      () => replaceText(noteText(this.doc), value),
      this.localOrigin,
    );
  }
  startComposition() {
    const doc = new Y.Doc();
    Y.applyUpdate(doc, Y.encodeStateAsUpdate(this.doc));
    return { doc, vector: Y.encodeStateVector(doc) };
  }
  /**
   * Merge an IME composition. Returns the caret as positions that survive
   * the remote edits made while composing, like the non-IME path.
   */
  finishComposition(
    composition: { doc: Y.Doc; vector: Uint8Array },
    value: string,
    selection?: { start: number; end: number },
  ) {
    const text = noteText(composition.doc);
    composition.doc.transact(() => replaceText(text, value));
    const caret = selection && {
      anchor: Y.createRelativePositionFromTypeIndex(text, selection.start),
      head: Y.createRelativePositionFromTypeIndex(text, selection.end),
    };
    Y.applyUpdate(
      this.doc,
      Y.encodeStateAsUpdate(composition.doc, composition.vector),
      this.localOrigin,
    );
    composition.doc.destroy();
    return caret;
  }
  undo() {
    this.undoManager?.undo();
  }
  redo() {
    this.undoManager?.redo();
  }
  block(message: string) {
    if (this.status === "conflict" && this.error === message) return;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.blocked = true;
    this.status = "conflict";
    this.error = message;
    this.emit();
  }
  async discard() {
    if (!this.hydrated) {
      // A draft that never loaded is removed only by this explicit choice.
      await this.clearRecoveredDrafts(this.recovered);
      this.emit();
      return;
    }
    clearTimeout(this.timer);
    this.timer = undefined;
    this.pending = [];
    this.inflight = undefined;
    this.inflightUpdate = undefined;
    this.retries = [];
    // Show the saved text again; the discarded edits leave this document.
    this.doc.off("update", this.onUpdate);
    this.undoManager?.destroy();
    this.doc.destroy();
    this.doc = new Y.Doc();
    Y.applyUpdate(this.doc, Y.encodeStateAsUpdate(this.accepted), "server");
    this.undoManager = new Y.UndoManager(noteText(this.doc), {
      trackedOrigins: new Set([this.localOrigin]),
      captureTimeout: 500,
    });
    this.doc.on("update", this.onUpdate);
    this.retryDelay = 1000;
    if (!this.blocked) {
      this.status = "saved";
      this.error = "";
    }
    await this.persist();
    this.emit();
  }
  private async clearRecoveredDrafts(rows: StoredNoteDraft[]) {
    await Promise.all(
      rows.map(({ key, value }) =>
        deleteBoardDraft<NoteDraft>(
          key,
          (current) => (current.stamp ?? "") === (value.stamp ?? ""),
        ),
      ),
    );
    if (this.recovered === rows) this.recovered = [];
  }
  private persist() {
    // A read-only session does not own or remove an earlier writable draft,
    // and a session that has not merged the stored draft must not replace it.
    if (!this.recoverDraft || !this.hydrated) return Promise.resolve();
    const recovered = this.recovered;
    let write: () => Promise<void>;
    if (this.dirty) {
      const value: NoteDraft = {
        text: noteText(this.doc).toString(),
        update: encodeBoardUpdate(
          Y.encodeStateAsUpdate(this.doc, Y.encodeStateVector(this.accepted)),
        ),
        inflight: this.inflight,
        retries: this.retries,
        stamp: crypto.randomUUID(),
      };
      this.stamp = value.stamp;
      write = async () => {
        // Durably copy all adopted updates before retiring their old rows.
        await writeBoardDraft(this.draftKey, value);
        await this.clearRecoveredDrafts(recovered);
      };
    } else {
      const owned = this.stamp;
      write = async () => {
        if (owned !== undefined)
          await deleteBoardDraft<NoteDraft>(
            this.draftKey,
            (row) => (row.stamp ?? "") === owned,
          );
        await this.clearRecoveredDrafts(recovered);
      };
    }
    this.writeChain = this.writeChain
      .catch(() => {})
      .then(write)
      .catch(() => {
        this.error =
          "Local recovery is unavailable. Keep this note open until Saved.";
        this.emit();
      });
    return this.writeChain;
  }
  private schedule(delay = 250) {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (!this.disposed)
      this.timer = setTimeout(() => {
        this.timer = undefined;
        void this.flush().catch(() => {});
      }, delay);
  }
  async flush(): Promise<void> {
    await this.ready;
    if (this.sending) {
      await this.sending;
      if (this.dirty) return this.flush();
      return;
    }
    if (this.status === "conflict" || this.status === "failed")
      throw new Error(this.error);
    if (!this.dirty) return;
    this.sending = this.send();
    try {
      await this.sending;
    } finally {
      this.sending = undefined;
    }
    if (this.dirty) return this.flush();
  }
  private async send() {
    if (!this.inflight && this.retries.length) {
      [this.inflight, ...this.retries] = this.retries;
      this.inflightUpdate = decodeBoardUpdate(this.inflight.update);
    }
    if (!this.inflight) {
      this.inflightUpdate = Y.mergeUpdates(this.pending);
      this.pending = [];
      this.inflight = {
        requestKey: crypto.randomUUID(),
        itemId: this.itemId,
        lifecycleRevision: this.lifecycleRevision,
        update: encodeBoardUpdate(this.inflightUpdate),
        stateVector: encodeBoardUpdate(Y.encodeStateVector(this.doc)),
      };
    }
    const inflight = this.inflight;
    const update = this.inflightUpdate!;
    await this.persist();
    try {
      const result = await apiClient.post<TextResult>(
        `${boardPath(this.scope)}/text`,
        inflight,
        { signal: this.controller.signal },
      );
      Y.applyUpdate(this.accepted, update, "ack");
      // Discarded while it was sending: the server kept it, so the note shows it.
      if (this.inflight !== inflight) Y.applyUpdate(this.doc, update, "server");
      if (result.update) {
        Y.applyUpdate(
          this.accepted,
          decodeBoardUpdate(result.update),
          "server",
        );
        Y.applyUpdate(this.doc, decodeBoardUpdate(result.update), "server");
      }
      this.revision = Math.max(this.revision, result.textRevision);
      if (this.inflight === inflight) {
        this.inflight = undefined;
        this.inflightUpdate = undefined;
      }
      this.retryDelay = 1000;
      this.status = this.dirty ? "saving" : "saved";
      this.error = "";
      await this.persist();
      notifyBoardChange(this.scope, result.sequence);
      this.emit();
    } catch (error) {
      const status = error instanceof ApiClientError ? error.status : null;
      const code = error instanceof ApiClientError ? error.code : null;
      // A refused or oversized draft never succeeds by resending it.
      const refused =
        status !== null &&
        [400, 401, 403, 404, 409, 410, 413, 422].includes(status);
      this.status = refused ? "conflict" : "offline";
      this.error = !refused
        ? "Offline — this note is saved on this device and will retry."
        : status === 413 ||
            code === "whiteboard_limit" ||
            code === "request_too_large"
          ? "This note is too large to save. Export your text, discard this draft, then shorten it."
          : code === "whiteboard_item_not_found"
            ? "This note no longer exists. Your unsaved text is preserved locally; export it before leaving."
            : "This note cannot accept this draft. Your unsaved text is preserved locally; copy it before leaving.";
      this.emit();
      if (!refused) {
        this.schedule(this.retryDelay);
        this.retryDelay = Math.min(30000, this.retryDelay * 2);
      }
      throw error;
    }
  }
  dispose() {
    this.disposed = true;
    clearTimeout(this.timer);
    this.timer = undefined;
    void this.persist();
    this.controller.abort();
    this.doc.off("update", this.onUpdate);
    this.undoManager?.destroy();
    this.doc.destroy();
    this.accepted.destroy();
    this.listeners.clear();
  }
}
