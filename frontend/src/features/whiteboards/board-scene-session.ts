import type { components } from "@/shared/api/generated/api.generated";
import type {
  BoardScope,
  BoardBatch,
  BoardCommand,
  BoardItem,
  BoardEdge,
} from "./board-api";
import { boardScopeKey } from "./board-api";
import { readBoardDrafts, writeBoardDraft } from "./board-drafts";

type Mutation = components["schemas"]["BoardMutation"];
type Points = NonNullable<BoardCommand["points"]>;
type SceneDraft = {
  input: BoardBatch;
  status: "saving" | "offline" | "conflict" | "stale";
  message?: string;
  inverse: BoardCommand[];
  historyMode?: "normal" | "undo" | "redo";
  /** When the change was made; old drafts are never resent automatically. */
  created?: number;
};
/** An entry whose objects cannot be guarded is kept only to explain why. */
type HistoryEntry = { commands: BoardCommand[]; blocked?: string };
type RevisionChange = {
  id: string;
  field: "geometry" | "content" | "edge";
  before: number;
  after: number;
};
export const geometryOf = (item: BoardItem) => ({
  x: item.x,
  y: item.y,
  width: item.width,
  height: item.height,
  rotation: item.rotation,
  stackOrder: item.stackOrder,
  parentFrameId: item.parentFrameId,
  readingOrder: item.readingOrder,
});
const revisionField = (command: BoardCommand): RevisionChange["field"] =>
  ["content", "delete_item", "restore_item"].includes(command.type)
    ? "content"
    : command.type === "geometry"
      ? "geometry"
      : "edge";
function inverseCommands(
  commands: BoardCommand[],
  items: BoardItem[],
  edges: BoardEdge[],
): BoardCommand[] {
  const itemsById = new Map(items.map((item) => [item.id, item]));
  const edgesById = new Map(edges.map((edge) => [edge.id, edge]));
  const deletedEdges = new Set(
    commands
      .filter((command) => command.type === "delete_edge")
      .map((command) => command.id),
  );
  // An object this batch changes itself gets its own inverse, which already
  // carries its parent; a deleted frame never reattaches it a second time.
  const touched = new Set(
    commands.flatMap((command) => (command.id ? [command.id] : [])),
  );
  let children: Map<string, BoardItem[]> | undefined;
  const childrenOf = (id: string) => {
    if (!children) {
      children = new Map();
      for (const item of items)
        if (item.parentFrameId)
          children.set(item.parentFrameId, [
            ...(children.get(item.parentFrameId) ?? []),
            item,
          ]);
    }
    return children.get(id) ?? [];
  };
  const inverse = [...commands].reverse().flatMap<BoardCommand>((command) => {
    const item = command.id ? itemsById.get(command.id) : undefined,
      edge = command.id ? edgesById.get(command.id) : undefined;
    if (command.type === "geometry" && item)
      return [{ type: "geometry", id: item.id, geometry: geometryOf(item) }];
    if (command.type === "content" && item)
      return [{ type: "content", id: item.id, properties: item.properties }];
    if (command.type === "delete_item" && item)
      return [
        {
          type: "restore_item",
          id: item.id,
          expectedRevision: item.contentRevision + 1,
          expectedGeometryRevision: item.geometryRevision + 1,
          expectedTextRevision: item.textRevision,
        },
        ...edges
          .filter(
            (edge) =>
              !deletedEdges.has(edge.id) &&
              (edge.sourceId === item.id || edge.targetId === item.id),
          )
          .map<BoardCommand>((edge) => ({
            type: "restore_edge",
            id: edge.id,
            expectedRevision: edge.revision + 1,
          })),
        ...childrenOf(item.id)
          .filter((child) => !touched.has(child.id))
          .map<BoardCommand>((child) => ({
            type: "geometry",
            id: child.id,
            expectedRevision: child.geometryRevision + 1,
            geometry: geometryOf(child),
          })),
      ];
    if (command.type === "restore_item")
      return [{ type: "delete_item", id: command.id }];
    if (command.type === "update_edge" && edge)
      return [
        {
          type: "update_edge",
          id: edge.id,
          label: edge.label,
          relation: edge.relation,
          sourceRef: edge.sourceId,
          targetRef: edge.targetId,
          line: edge.line,
          startMarker: edge.startMarker,
          endMarker: edge.endMarker,
          route: edge.route,
          ...(edge.route === "message" && edge.anchorY != null
            ? { anchorY: edge.anchorY }
            : {}),
          sourceLabel: edge.sourceLabel,
          targetLabel: edge.targetLabel,
        },
      ];
    if (command.type === "delete_edge" && edge)
      return [
        {
          type: "restore_edge",
          id: edge.id,
          expectedRevision: edge.revision + 1,
        },
      ];
    if (command.type === "restore_edge")
      return [{ type: "delete_edge", id: command.id }];
    if (command.type === "create_item")
      return [{ type: "delete_item", draftId: command.draftId }];
    if (command.type === "create_edge")
      return [{ type: "delete_edge", draftId: command.draftId }];
    return [];
  });
  // At most one inverse per object and field. Every one of them carries the
  // state before the batch, so a repeated command adds nothing but a guard
  // that the first inverse would already have advanced.
  const seen = new Set<string>();
  const unique = inverse.filter((command) => {
    if (
      !command.id ||
      !["geometry", "content", "update_edge"].includes(command.type)
    )
      return true;
    const key = `${revisionField(command)}:${command.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  // Restore deleted objects first, so reattached children find their frame,
  // and endpoints before their connectors. Deleting both endpoints only
  // removes a connector once; explicitly deleted connectors already have an
  // inverse.
  const restoredEdges = new Map(
    unique
      .filter((command) => command.type === "restore_edge")
      .map((command) => [command.id, command]),
  );
  return [
    ...unique.filter((command) => command.type === "restore_item"),
    ...unique.filter(
      (command) =>
        command.type !== "restore_item" && command.type !== "restore_edge",
    ),
    ...restoredEdges.values(),
  ];
}
function guarded(commands: BoardCommand[], result: Mutation): BoardCommand[] {
  const items = new Map(result.items.map((item) => [item.id, item]));
  const edges = new Map(result.edges.map((edge) => [edge.id, edge]));
  // Each accepted command advances its field once. A later command on the
  // same object and field expects the revision the earlier one leaves behind,
  // never the same observed revision.
  const next = new Map<string, number>();
  const expect = (key: string, observed: number | undefined) => {
    const value = next.get(key) ?? observed;
    if (value !== undefined) next.set(key, value + 1);
    return value;
  };
  return commands.map((command) => {
    const id =
      command.id ?? (command.draftId ? result.ids[command.draftId] : undefined);
    const item = id ? items.get(id) : undefined,
      edge = id ? edges.get(id) : undefined;
    const field = revisionField(command);
    const lifecycle =
      command.type === "delete_item" || command.type === "restore_item";
    const observed =
      field === "content"
        ? item?.contentRevision
        : field === "geometry"
          ? item?.geometryRevision
          : edge?.revision;
    return {
      ...command,
      id,
      draftId: undefined,
      expectedRevision: expect(
        `${field}:${id}`,
        observed ?? command.expectedRevision,
      ),
      expectedGeometryRevision: lifecycle
        ? expect(
            `geometry:${id}`,
            item?.geometryRevision ?? command.expectedGeometryRevision,
          )
        : undefined,
      expectedTextRevision: lifecycle
        ? (item?.textRevision ?? command.expectedTextRevision)
        : undefined,
    };
  });
}
// A replayed receipt omits objects that changed after the original commit.
// Undo cannot guard an object it does not see, so such an entry is not undone.
function replayComplete(commands: BoardCommand[], result: Mutation) {
  const items = new Set(result.items.map((item) => item.id));
  const edges = new Set(result.edges.map((edge) => edge.id));
  return commands.every((command) => {
    if (command.type === "delete_item" || command.type === "delete_edge")
      return true;
    const id =
      command.id ?? (command.draftId ? result.ids[command.draftId] : undefined);
    if (!id) return false;
    return command.type.endsWith("_edge") ? edges.has(id) : items.has(id);
  });
}
function acknowledgedRevisions(draft: SceneDraft, result: Mutation) {
  const changes: RevisionChange[] = [];
  const add = (
    id: string,
    field: RevisionChange["field"],
    before: number | undefined,
    after: number | undefined,
  ) => {
    if (before !== undefined && after !== undefined)
      changes.push({ id, field, before, after });
  };
  const items = new Map(result.items.map((item) => [item.id, item]));
  const edges = new Map(result.edges.map((edge) => [edge.id, edge]));
  // A replayed receipt proves only the objects it returns. Another change may
  // have followed for an absent one, so no revision is inferred for it.
  const inferred = (value: number) => (result.replayed ? undefined : value);
  for (const command of draft.input.commands) {
    if (!command.id || command.expectedRevision === undefined) continue;
    const item = items.get(command.id);
    const edge = edges.get(command.id);
    const before = command.expectedRevision;
    if (command.type === "geometry")
      add(command.id, "geometry", before, item?.geometryRevision);
    if (command.type === "content")
      add(command.id, "content", before, item?.contentRevision);
    if (command.type === "delete_item" || command.type === "restore_item") {
      // Deleted rows are omitted from receipts. Each accepted lifecycle command
      // increments both revisions once; only guarded fields prove the old value.
      add(
        command.id,
        "content",
        before,
        item?.contentRevision ?? inferred(before + 1),
      );
      const geometry = command.expectedGeometryRevision;
      add(
        command.id,
        "geometry",
        geometry,
        item?.geometryRevision ??
          (geometry === undefined ? undefined : inferred(geometry + 1)),
      );
    }
    if (["update_edge", "delete_edge", "restore_edge"].includes(command.type))
      add(command.id, "edge", before, edge?.revision ?? inferred(before + 1));
  }
  if (
    !result.replayed &&
    draft.input.commands.some((command) => command.type === "delete_item")
  ) {
    const removed = new Set(result.removed);
    for (const inverse of draft.inverse) {
      if (!inverse.id || inverse.expectedRevision === undefined) continue;
      // Deleting a frame detaches children and deleting an item removes edges.
      // Their inverse guards retain the observed revision through restoration.
      if (inverse.type === "restore_edge" && removed.has(inverse.id))
        add(
          inverse.id,
          "edge",
          inverse.expectedRevision - 1,
          inverse.expectedRevision,
        );
      if (
        inverse.type === "geometry" &&
        items.get(inverse.id)?.geometryRevision === inverse.expectedRevision
      )
        add(
          inverse.id,
          "geometry",
          inverse.expectedRevision - 1,
          inverse.expectedRevision,
        );
    }
  }
  return changes;
}
function advanceHistory(stack: HistoryEntry[], changes: RevisionChange[]) {
  if (!changes.length) return;
  const byGuard = new Map(
    changes.map((change) => [
      `${change.id}:${change.field}:${change.before}`,
      change.after,
    ]),
  );
  for (const entry of stack) {
    entry.commands = entry.commands.map((command) => {
      const revision = byGuard.get(
        `${command.id}:${revisionField(command)}:${command.expectedRevision}`,
      );
      const geometry = byGuard.get(
        `${command.id}:geometry:${command.expectedGeometryRevision}`,
      );
      return {
        ...command,
        expectedRevision: revision ?? command.expectedRevision,
        expectedGeometryRevision: geometry ?? command.expectedGeometryRevision,
      };
    });
  }
}
export const boardBatchLimit = 1000;
const boardMaxBatchBytes = 4 << 20;
// Request keys expire on the server after 30 days; a draft older than this
// may already be saved, so resending it could apply it twice.
const staleDraftAge = 29 * 24 * 60 * 60 * 1000;
const batchLimitLabel = boardBatchLimit.toLocaleString("en-US");

/** A change refused before anything was recorded or sent. */
export class BoardChangeTooLarge extends Error {}
function checkBatch(commands: BoardCommand[], inverse: BoardCommand[]) {
  // One atomic batch, and its undo, may touch at most this many objects.
  if (commands.length > boardBatchLimit || inverse.length > boardBatchLimit)
    throw new BoardChangeTooLarge(
      `This change touches more than ${batchLimitLabel} objects and connectors. Select fewer and try again.`,
    );
  const bytes = new TextEncoder().encode(
    JSON.stringify({ requestKey: crypto.randomUUID(), commands }),
  ).length;
  if (bytes > boardMaxBatchBytes)
    throw new BoardChangeTooLarge(
      "This change is too large to save at once. Select fewer objects and try again.",
    );
}

const retryableStatus = (status: number | null | undefined) =>
  !status || ![400, 401, 403, 404, 409, 410, 413, 422].includes(status);

// Explain why the server refused a draft instead of always blaming a
// collaborator; the draft stays recoverable either way.
function rejectionMessage(error: unknown, draft: SceneDraft) {
  const {
    code,
    status,
    file = false,
  } = error as {
    code?: string | null;
    status?: number | null;
    file?: boolean;
  };
  if (retryableStatus(status))
    return "Offline — changes are retained on this device.";
  const retained = "Your draft is retained; export it or discard it.";
  const history = draft.historyMode !== "normal" && draft.historyMode;
  if (file) return `A file in this change is no longer available. ${retained}`;
  if (code === "whiteboard_locked")
    return history
      ? `This ${history} is blocked because an object is locked. Unlock it, discard this draft and try again.`
      : `This object is locked. Unlock it before moving it. ${retained}`;
  if (code === "whiteboard_conflict")
    return `A collaborator changed this object. ${retained}`;
  if (code === "whiteboard_item_not_found")
    return `An object in this change no longer exists. ${retained}`;
  if (code === "whiteboard_request_key_reused")
    return `This save attempt no longer matches the change recorded for it. ${retained}`;
  if (code === "whiteboard_limit")
    return `This change exceeds a Whiteboard size limit, so it cannot be saved. ${retained}`;
  if (status === 413 || code === "request_too_large")
    return `This change is too large to save at once. ${retained}`;
  if (status === 401 || status === 403 || status === 404)
    return `Your access to this Whiteboard changed. ${retained}`;
  return `The Whiteboard rejected this change. ${retained}`;
}

export class BoardSceneSession {
  drafts = new Map<string, SceneDraft>();
  private receipts: Mutation[] = [];
  private strokePoints = new Map<string, Points>();
  private busy = new Map<string, Promise<Mutation | undefined>>();
  private listeners = new Set<() => void>();
  private version = 0;
  private undoStack: HistoryEntry[] = [];
  private redoStack: HistoryEntry[] = [];
  private closed = false;
  private timer?: ReturnType<typeof setTimeout>;
  private retryDelay = 1000;
  // Drafts committed in this tab whose first attempt has not started. Every
  // other attempt references its files again before sending.
  private fresh = new Set<string>();
  // Pending IDs and unsaved ink, derived once per change rather than per item.
  private derived?: { pending: Set<string>; local: Map<string, Points> };
  ready: Promise<void> = Promise.resolve();
  private started = false;
  acknowledgedSequence = 0;
  private prefix: string;
  constructor(
    scope: BoardScope,
    private readonly userId: string,
    private send: (input: BoardBatch) => Promise<Mutation>,
    private readonly options: {
      /** Reference an uploaded file on this Board again (idempotent). */
      attach?: (attachmentId: string) => Promise<unknown>;
      /** Saved ink the snapshot now holds; the caller may cache it. */
      retireStroke?: (id: string, points: Points) => void;
    } = {},
  ) {
    this.prefix = `${userId}:${boardScopeKey(scope)}:scene:v1:`;
  }
  start() {
    if (this.started) return;
    this.started = true;
    this.ready = readBoardDrafts<SceneDraft>(this.prefix)
      .then((rows) => {
        if (this.closed) return;
        const now = Date.now();
        rows.forEach(({ value: draft, touched }) => {
          draft.created ??= touched ?? now;
          if (
            draft.status !== "conflict" &&
            now - draft.created > staleDraftAge
          ) {
            draft.status = "stale";
            draft.message =
              "This change waited on this device for more than 29 days and may already be saved. Check the Board, then export it, discard it, or send it again.";
          }
          this.drafts.set(draft.input.requestKey, draft);
        });
        this.emit();
        this.retry();
      })
      .catch(() => {
        this.storageError =
          "Local recovery is unavailable. Keep unsaved work open.";
        this.emit();
      });
  }
  storageError = "";
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.version;
  private emit() {
    this.derived = undefined;
    this.version++;
    this.listeners.forEach((listener) => listener());
  }
  get dirty() {
    return this.drafts.size > 0;
  }
  get canUndo() {
    return this.undoStack.length > 0 && !this.dirty;
  }
  get canRedo() {
    return this.redoStack.length > 0 && !this.dirty;
  }
  private derive() {
    if (this.derived) return this.derived;
    const pending = new Set<string>();
    const local = new Map<string, Points>();
    for (const receipt of this.receipts)
      for (const item of receipt.items) pending.add(item.id);
    for (const draft of this.drafts.values())
      for (const command of draft.input.commands) {
        if (command.id) pending.add(command.id);
        if (command.points && command.draftId)
          local.set(
            `local:${draft.input.requestKey}:${command.draftId}`,
            command.points,
          );
      }
    this.derived = { pending, local };
    return this.derived;
  }
  /** Shared until the next change; callers must not modify it. */
  get pendingIds(): ReadonlySet<string> {
    return this.derive().pending;
  }
  acceptSnapshot(sequence: number) {
    if (!this.receipts.some((receipt) => receipt.sequence <= sequence)) return;
    const settled = this.receipts.filter(
      (receipt) => receipt.sequence <= sequence,
    );
    this.receipts = this.receipts.filter(
      (receipt) => receipt.sequence > sequence,
    );
    // The snapshot holds these objects now. Their ink leaves this session for
    // the caller's bounded cache instead of growing for the Board's lifetime.
    for (const receipt of settled)
      for (const item of receipt.items) {
        const points = this.strokePoints.get(item.id);
        if (!points) continue;
        this.strokePoints.delete(item.id);
        this.options.retireStroke?.(item.id, points);
      }
    this.derived = undefined;
  }
  private acknowledged<T extends { id: string }>(
    rows: T[],
    receiptRows: (receipt: Mutation) => T[],
  ): Map<string, T> {
    const result = new Map(rows.map((row) => [row.id, row]));
    for (const receipt of this.receipts) {
      for (const row of receiptRows(receipt)) result.set(row.id, row);
      for (const id of receipt.removed) result.delete(id);
    }
    return result;
  }
  preview(items: BoardItem[]): BoardItem[] {
    const result = this.acknowledged(items, (receipt) => receipt.items);
    for (const draft of this.drafts.values()) {
      if (draft.status === "conflict" || draft.status === "stale") continue;
      for (const command of draft.input.commands) {
        const item = command.id ? result.get(command.id) : undefined;
        if (command.type === "geometry" && item && command.geometry)
          result.set(item.id, { ...item, ...command.geometry });
        if (command.type === "content" && item && command.properties)
          result.set(item.id, { ...item, properties: command.properties });
        if (command.type === "delete_item" && item) result.delete(item.id);
        if (
          command.type !== "create_item" ||
          !command.geometry ||
          !command.itemType
        )
          continue;
        const id = `local:${draft.input.requestKey}:${command.draftId}`;
        result.set(id, {
          ...command.geometry,
          id,
          type: command.itemType as BoardItem["type"],
          properties: command.properties ?? { locked: false },
          geometryRevision: 0,
          contentRevision: 0,
          textRevision: 0,
          lifecycleRevision: 0,
          text: command.text ?? "",
          textTruncated: false,
          createdBy: {
            id: this.userId,
            kind: "user",
            name: "",
            avatarUrl: null,
          },
          updatedBy: {
            id: this.userId,
            kind: "user",
            name: "",
            avatarUrl: null,
          },
          createdByRunId: null,
          updatedByRunId: null,
          deleted: false,
        });
      }
    }
    return [...result.values()];
  }
  previewEdges(edges: BoardEdge[]): BoardEdge[] {
    return [...this.acknowledged(edges, (receipt) => receipt.edges).values()];
  }
  points(id: string) {
    return this.strokePoints.get(id) ?? this.derive().local.get(id);
  }
  async commit(
    commands: BoardCommand[],
    items: BoardItem[],
    edges: BoardEdge[],
  ) {
    const inverse = inverseCommands(commands, items, edges);
    checkBatch(commands, inverse);
    const input = { requestKey: crypto.randomUUID(), commands };
    this.drafts.set(input.requestKey, {
      input,
      inverse,
      status: "saving",
      historyMode: "normal",
      created: Date.now(),
    });
    this.fresh.add(input.requestKey);
    this.emit();
    await this.persist(input.requestKey);
    return this.process(input.requestKey);
  }
  private async persist(key: string) {
    try {
      await writeBoardDraft(this.prefix + key, this.drafts.get(key));
    } catch {
      this.storageError =
        "Local recovery is unavailable. Keep unsaved work open.";
      this.emit();
    }
  }
  private process(key: string): Promise<Mutation | undefined> {
    const existing = this.busy.get(key);
    if (existing) return existing;
    const draft = this.drafts.get(key);
    if (!draft || draft.status === "conflict" || draft.status === "stale")
      return Promise.resolve(undefined);
    const resent = !this.fresh.delete(key);
    const work = (async () => {
      draft.status = "saving";
      this.emit();
      try {
        // An uploaded file may have been released while this draft waited;
        // referencing it again first is idempotent.
        const files = resent
          ? [
              ...new Set(
                draft.input.commands.flatMap((command) =>
                  command.type === "create_item" &&
                  command.properties?.attachmentId
                    ? [command.properties.attachmentId]
                    : [],
                ),
              ),
            ]
          : [];
        if (files.length && this.options.attach) {
          const attach = this.options.attach;
          await Promise.all(files.map((file) => attach(file))).catch(
            (error: unknown) => {
              throw { ...(error as object), file: true };
            },
          );
        }
        const result = await this.send(draft.input);
        this.retryDelay = 1000;
        this.acknowledgedSequence = Math.max(
          this.acknowledgedSequence,
          result.sequence,
        );
        // A receipt proves the write before the metadata subscription catches up.
        // Keep its geometry and newly drawn ink visible throughout that handoff.
        this.receipts.push(result);
        this.receipts.sort((a, b) => a.sequence - b.sequence);
        this.derived = undefined;
        const created = new Set(result.items.map((item) => item.id));
        for (const command of draft.input.commands) {
          if (
            command.type !== "create_item" ||
            !command.draftId ||
            !command.points
          )
            continue;
          const id = result.ids[command.draftId];
          // Ink coordinates are immutable, including across delete/restore.
          if (created.has(id)) this.strokePoints.set(id, command.points);
        }
        const history: HistoryEntry =
          result.replayed && !replayComplete(draft.input.commands, result)
            ? {
                commands: [],
                blocked:
                  "This change was saved by an earlier attempt and has changed since, so it can't be undone.",
              }
            : { commands: guarded(draft.inverse, result) };
        // Advance only revisions proven by our accepted command, never unrelated
        // fields included in a receipt or revisions from a remote snapshot.
        const revisions = acknowledgedRevisions(draft, result);
        advanceHistory(this.undoStack, revisions);
        advanceHistory(this.redoStack, revisions);
        if (draft.historyMode === "normal") {
          this.undoStack.push(history);
          this.redoStack = [];
        }
        if (draft.historyMode === "undo") {
          this.undoStack.pop();
          this.redoStack.push(history);
        }
        if (draft.historyMode === "redo") {
          this.redoStack.pop();
          this.undoStack.push(history);
        }
        this.drafts.delete(key);
        await this.persist(key);
        this.emit();
        return result;
      } catch (error) {
        const status = (error as { status?: number | null }).status;
        draft.status = retryableStatus(status) ? "offline" : "conflict";
        draft.message = rejectionMessage(error, draft);
        await this.persist(key);
        this.emit();
        if (!this.closed && draft.status === "offline") {
          clearTimeout(this.timer);
          this.timer = setTimeout(() => this.retry(), this.retryDelay);
          this.retryDelay = Math.min(30000, this.retryDelay * 2);
        }
        throw error;
      } finally {
        this.busy.delete(key);
      }
    })();
    this.busy.set(key, work);
    return work;
  }
  retry() {
    for (const [key, draft] of this.drafts)
      if (draft.status !== "conflict" && draft.status !== "stale")
        void this.process(key).catch(() => {});
  }
  /** Send a draft held back for its age, once the member confirms it. */
  resend(key: string) {
    const draft = this.drafts.get(key);
    if (!draft || draft.status !== "stale") return;
    draft.status = "saving";
    draft.created = Date.now();
    draft.message = undefined;
    void this.process(key).catch(() => {});
  }
  async discard(key: string) {
    this.drafts.delete(key);
    this.fresh.delete(key);
    await this.persist(key);
    this.emit();
  }
  async flush() {
    await this.ready;
    while (this.dirty) {
      if (
        [...this.drafts.values()].some(
          (draft) => draft.status === "conflict" || draft.status === "stale",
        )
      )
        throw new Error("Resolve conflicting Board changes before saving.");
      await Promise.all(
        [...this.drafts.keys()].map((key) => this.process(key)),
      );
    }
  }
  async undo(items: BoardItem[], edges: BoardEdge[]) {
    await this.applyHistory(this.undoStack, items, edges);
  }
  async redo(items: BoardItem[], edges: BoardEdge[]) {
    await this.applyHistory(this.redoStack, items, edges);
  }
  private async applyHistory(
    from: HistoryEntry[],
    items: BoardItem[],
    edges: BoardEdge[],
  ) {
    if (this.dirty) return;
    const entry = from.at(-1);
    if (!entry) return;
    const action = from === this.undoStack ? "undo" : "redo";
    // An entry that cannot be applied is dropped with its reason, so older
    // history stays reachable; its guards still refuse stale changes.
    if (entry.blocked) {
      from.pop();
      this.emit();
      throw new Error(entry.blocked);
    }
    const inverse = inverseCommands(entry.commands, items, edges);
    try {
      checkBatch(entry.commands, inverse);
    } catch {
      from.pop();
      this.emit();
      throw new Error(
        `This ${action} would change more than ${batchLimitLabel} objects and connectors at once, so it can't be applied.`,
      );
    }
    // These revisions come from this member's acknowledgement, never the latest
    // remote snapshot. Undo therefore cannot overwrite a collaborator's change.
    const input = { requestKey: crypto.randomUUID(), commands: entry.commands };
    this.drafts.set(input.requestKey, {
      input,
      inverse,
      status: "saving",
      historyMode: action,
      created: Date.now(),
    });
    this.fresh.add(input.requestKey);
    this.emit();
    await this.persist(input.requestKey);
    await this.process(input.requestKey);
  }
  dispose() {
    this.closed = true;
    clearTimeout(this.timer);
    this.listeners.clear();
  }
}
