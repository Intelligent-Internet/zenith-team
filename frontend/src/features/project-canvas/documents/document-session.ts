import * as Y from "yjs";
import { Awareness } from "y-protocols/awareness";
import { apiClient, ApiClientError } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import {
  blockDocumentPath,
  documentScopeKey,
  type DocumentScope,
} from "./document-scope";
import { readSessionSnapshot } from "@/shared/session/session";
import { documentRecovery } from "./document-recovery";
import {
  canEditWithDocumentCapability,
  documentAuthoringCapability,
} from "./document-authoring-capability";
import {
  notifyDocumentMutation,
  watchDocumentChanges,
} from "../api/canvas-sync";
import { startDocumentPresence } from "./document-presence";
import { encodeUpdate, decodeUpdate } from "./document-update";
import { restoreDocumentIdentities } from "./document-identities";

type Document = components["schemas"]["CanvasDocument"];
type Session = components["schemas"]["CanvasSession"];
type DocumentLifecycle = "opening" | "active" | "closing" | "closed";
type DocumentPermission = "unknown" | "editable" | "readonly" | "denied";
type DocumentWriteOutcome = "idle" | "saving" | "invalid" | "retrying";
const remote = "zenith-server";
const finishingDocuments = new Map<string, Promise<void>>();
export type DocumentConnection = {
  doc: Y.Doc;
  awareness: Awareness;
  ids: string[];
  reserve: (count?: number, retry?: boolean) => void;
};
export type DocumentSessionStatus = {
  connection: DocumentConnection | null;
  ready: boolean;
  canEdit: boolean;
  intelligentUIAuthoringEnabled: boolean;
  reactiveReadOnly: boolean;
  saved: boolean;
  error: string;
  recoveryError: string;
  notice: string;
  people: Session[];
};
export const initialDocumentStatus: DocumentSessionStatus = {
  connection: null,
  ready: false,
  canEdit: false,
  intelligentUIAuthoringEnabled: false,
  reactiveReadOnly: false,
  saved: true,
  error: "",
  recoveryError: "",
  notice: "",
  people: [],
};

// A session owns CRDT state until its last write and recovery release finish.
// React observes it; metadata and immutable reads remain in RTK Query.
export function createDocumentSession(options: {
  scope: DocumentScope;
  versionId: string;
  isReadOnly: () => boolean;
  onUnsavedClose: () => void;
}) {
  const { scope, isReadOnly } = options;
  const versionId = "pageId" in scope ? scope.pageId : options.versionId;
  let pageId = "";
  let status = { ...initialDocumentStatus };
  const listeners = new Set<(status: DocumentSessionStatus) => void>();
  const publish = (patch: Partial<DocumentSessionStatus>) => {
    status = { ...status, ...patch };
    listeners.forEach((listener) => listener(status));
  };
  const setReady = (value: DocumentSessionStatus["ready"]) =>
    publish({ ready: value });
  const setCanEdit = (value: DocumentSessionStatus["canEdit"]) =>
    publish({
      canEdit: canEditWithDocumentCapability(value, status.reactiveReadOnly),
    });
  const setSaved = (value: DocumentSessionStatus["saved"]) =>
    publish({ saved: value });
  const setError = (value: DocumentSessionStatus["error"]) =>
    publish({ error: value });
  const setRecoveryError = (value: DocumentSessionStatus["recoveryError"]) =>
    publish({ recoveryError: value });
  const setNotice = (value: DocumentSessionStatus["notice"]) =>
    publish({ notice: value });
  const setPeople = (value: DocumentSessionStatus["people"]) =>
    publish({ people: value });
  const setConnection = (value: DocumentSessionStatus["connection"]) =>
    publish({ connection: value });
  const doc = new Y.Doc();
  const acceptedDoc = new Y.Doc();
  const awareness = new Awareness(doc);
  const ids: string[] = [];
  const controller = new AbortController();
  const url = blockDocumentPath(scope);
  const scopeKey = documentScopeKey(scope);
  const userId = readSessionSnapshot().userId;
  const recovery = documentRecovery({
    userId,
    scopeKey,
    versionId,
    signal: controller.signal,
    onError: setRecoveryError,
  });
  const cacheKey = recovery.cacheKey;
  const priorFinish = finishingDocuments.get(cacheKey);
  let snapshot: Document | undefined;
  let pendingUpdates: Uint8Array[] = [];
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  let refreshRequested = false;
  let authoringSuspended = false;
  let initializationState: "pending" | "loading" | "ready" = "pending";
  let initialization: Promise<void> | undefined;
  let predecessorReady = !priorFinish;
  let discarding = false;
  let lifecycle: DocumentLifecycle = "opening";
  let permission: DocumentPermission = "unknown";
  let writeOutcome: DocumentWriteOutcome = "idle";
  let loaded = false,
    busy = false,
    reserving = false;
  const isInitialized = () => initializationState === "ready";
  const isInitializing = () => initializationState === "loading";
  const isClosed = () => lifecycle === "closed";
  const isClosing = () => lifecycle === "closing";
  const isDenied = () => permission === "denied";
  const isInvalid = () => writeOutcome === "invalid";
  const isEditable = () => permission === "editable";
  let sequence = -1,
    generation = 0,
    acknowledged = 0;
  let reservationTarget = 0;
  let waitingForIDs = false;
  let flight: Promise<boolean> | null = null;
  const cacheSnapshot = recovery.cache;
  setReady(false);
  setCanEdit(false);
  setSaved(true);
  setError("");
  setRecoveryError("");
  setNotice("");
  setPeople([]);
  setConnection({
    doc,
    awareness,
    ids,
    reserve: (count, retry) => {
      void reserve(count, retry);
    },
  });
  const persist = (release = false) => {
    return recovery.save(snapshot, pendingUpdates, release).catch(() => {
      if (!isClosed() && !isClosing())
        setRecoveryError(
          "Local recovery could not be saved. Keep this document open until changes reach the server, or export your draft.",
        );
    });
  };
  const failure = (reason: unknown) => {
    if (isClosed()) return;
    if (
      reason instanceof ApiClientError &&
      [401, 403, 404, 409].includes(reason.status ?? 0)
    )
      permission = "denied";
    writeOutcome =
      reason instanceof ApiClientError && reason.status === 422
        ? "invalid"
        : "retrying";
    if (isClosing()) return;
    if (isDenied()) setCanEdit(false);
    const authoringDisabled =
      reason instanceof ApiClientError &&
      reason.code === "intelligent_ui_authoring_disabled";
    if (authoringDisabled) {
      authoringSuspended = true;
      const capability = documentAuthoringCapability(
        snapshot?.schemaVersion ?? 1,
        false,
      );
      publish({
        ...capability,
        ...(capability.reactiveReadOnly ? { canEdit: false } : {}),
      });
    }
    const rejectionDetail =
      reason instanceof ApiClientError && reason.code !== "canvas_invalid"
        ? reason.body?.detail
        : undefined;
    setError(
      authoringDisabled
        ? "Reactive editing is temporarily unavailable. Your unsaved changes are still here. Export your draft before reloading, or keep this document open until editing resumes."
        : reason instanceof ApiClientError && reason.status === 409
          ? "The document was moved or restructured while you were editing. Export your draft, then reload the saved version to reconcile the changes."
          : isDenied()
            ? "Access changed. Export your unsaved draft before closing."
            : isInvalid()
              ? `The server rejected this document edit.${rejectionDetail ? ` ${rejectionDetail}` : ""} Your draft is still open. Undo the last change or export your draft before reloading.`
              : "Connection interrupted. Edits remain in this open document. Reconnecting…",
    );
  };
  const discard = async () => {
    discarding = true;
    setCanEdit(false);
    try {
      if (flight) await flight;
      await recovery.save(snapshot, [], true);
      pendingUpdates = [];
      acknowledged = generation;
      setSaved(true);
      return true;
    } catch {
      discarding = false;
      setCanEdit(isEditable() && !isReadOnly() && !isDenied());
      setError("The local draft could not be discarded. Export it and retry.");
      return false;
    }
  };
  const reserve = async (count = 32, retry = false) => {
    if (
      isClosed() ||
      isClosing() ||
      discarding ||
      !isEditable() ||
      isReadOnly() ||
      status.reactiveReadOnly ||
      ids.length >= count ||
      isDenied()
    )
      return;
    if (count > 10000) {
      setNotice(
        "This insertion exceeds the document limit. Paste a smaller section.",
      );
      return;
    }
    reservationTarget = Math.max(reservationTarget, count);
    waitingForIDs ||= retry;
    if (retry) setNotice("Preparing this insertion. Please retry when ready.");
    if (reserving) return;
    reserving = true;
    try {
      while (
        ids.length < reservationTarget &&
        !isClosed() &&
        !isClosing() &&
        !discarding &&
        !isDenied() &&
        isEditable() &&
        !isReadOnly() &&
        !status.reactiveReadOnly
      ) {
        const result = await apiClient.post<
          components["schemas"]["CanvasBlockIDs"]
        >(
          `${url}/ids`,
          {
            count: Math.min(256, Math.max(32, reservationTarget - ids.length)),
          },
          { signal: controller.signal },
        );
        ids.push(...result.ids);
      }
      if (
        !isClosed() &&
        !isClosing() &&
        !discarding &&
        !isDenied() &&
        isEditable() &&
        !status.reactiveReadOnly
      ) {
        setCanEdit(!isReadOnly());
        if (waitingForIDs)
          setNotice("Ready to insert. Retry the insertion or paste.");
      }
    } catch (reason) {
      if (!isClosed() && !isClosing()) setNotice("");
      failure(reason);
    } finally {
      reserving = false;
      reservationTarget = 0;
      waitingForIDs = false;
    }
  };
  const applyAuthoringCapability = (result: Document) => {
    const capability = documentAuthoringCapability(
      result.schemaVersion,
      result.intelligentUIAuthoringEnabled === true,
    );
    publish({
      ...capability,
      ...(capability.reactiveReadOnly ? { canEdit: false } : {}),
    });
    if (capability.reactiveReadOnly) authoringSuspended = true;
    else if (result.intelligentUIAuthoringEnabled) authoringSuspended = false;
  };
  const apply = (result: Document) => {
    if (isClosed()) return;
    if (result.schemaVersion !== 1 && result.schemaVersion !== 2)
      throw new Error("Unsupported document schema");
    applyAuthoringCapability(result);
    pageId = result.pageId;
    sequence = result.sequence;
    permission = result.canEdit ? "editable" : "readonly";
    if (result.state) Y.applyUpdate(doc, decodeUpdate(result.state), remote);
    loaded = true;
    if (!isClosing()) {
      setCanEdit(isEditable() && !isReadOnly());
      setReady(true);
    }
  };
  const sync = async () => {
    if (
      isClosed() ||
      discarding ||
      isDenied() ||
      isInvalid() ||
      busy ||
      !isInitialized() ||
      readSessionSnapshot().userId !== userId
    )
      return false;
    busy = true;
    writeOutcome = "saving";
    try {
      let sent = generation;
      if (loaded && sent > acknowledged) {
        if (!isEditable() || authoringSuspended) {
          const current = await apiClient.get<Document>(url, {
            params: {
              after_seq: sequence,
              state_vector: encodeUpdate(Y.encodeStateVector(acceptedDoc)),
            },
            signal: controller.signal,
          });
          if (isClosed() || discarding) return false;
          // Refresh authority without merging remote structure into pending edits
          // or advancing their base sequence. The normal write fence still applies.
          applyAuthoringCapability(current);
          permission = current.canEdit ? "editable" : "readonly";
          if (!isClosing()) setCanEdit(isEditable() && !isReadOnly());
          if (authoringSuspended && !current.intelligentUIAuthoringEnabled)
            return false;
          authoringSuspended = false;
        }
        if (!isEditable() || isReadOnly()) return false;
        sent = generation;
        const priorSequence = sequence;
        const count = pendingUpdates.length;
        const update = Y.encodeStateAsUpdate(
          doc,
          Y.encodeStateVector(acceptedDoc),
        );
        const result = await apiClient.post<Document>(
          url,
          {
            update: encodeUpdate(update),
            schemaVersion: 2,
            baseSequence: sequence,
            stateVector: encodeUpdate(Y.encodeStateVector(doc)),
          },
          { signal: controller.signal },
        );
        if (isClosed() || discarding) return false;
        Y.applyUpdate(acceptedDoc, update);
        if (result.state)
          Y.applyUpdate(acceptedDoc, decodeUpdate(result.state));
        // New local work must not absorb unseen structural changes during a write.
        // Keep its original base until a later save passes the server fence.
        if (generation === sent || result.sequence <= priorSequence + 1) {
          apply(result);
          pendingUpdates.splice(0, count);
          snapshot = {
            ...result,
            state: encodeUpdate(Y.encodeStateAsUpdate(acceptedDoc)),
          };
        }
        acknowledged = sent;
        if (!isClosing()) setSaved(generation === acknowledged);
        await persist();
        if (snapshot) cacheSnapshot(snapshot);
        notifyDocumentMutation(scope, result.pageId, result.sequence);
      } else {
        const result = await apiClient.get<Document>(url, {
          params: {
            ...(loaded ? { after_seq: sequence } : {}),
            state_vector: encodeUpdate(Y.encodeStateVector(acceptedDoc)),
          },
          signal: controller.signal,
        });
        // A local edit made during this read gets merged by the next write.
        // Applying a structural remote change first could delete that edit.
        if (isClosed() || discarding) return false;
        if (generation === sent) {
          const priorSequence = sequence;
          const contentChanged = !!result.state;
          if (result.state)
            Y.applyUpdate(acceptedDoc, decodeUpdate(result.state));
          apply(result);
          if (contentChanged || !snapshot)
            snapshot = {
              ...result,
              state: encodeUpdate(Y.encodeStateAsUpdate(acceptedDoc)),
            };
          if (contentChanged && snapshot) cacheSnapshot(snapshot);
          if (result.sequence > priorSequence)
            notifyDocumentMutation(scope, result.pageId, result.sequence);
        } else refreshRequested = true;
      }
      if (!isClosed() && !isClosing() && !isDenied() && !isInvalid())
        setError("");
      return acknowledged === generation;
    } catch (reason) {
      failure(reason);
      return false;
    } finally {
      busy = false;
      if (writeOutcome === "saving") writeOutcome = "idle";
      if (refreshRequested && !isClosed() && !isDenied() && !isInvalid()) {
        refreshRequested = false;
        schedule();
      }
    }
  };
  const tick = () => {
    if (busy && flight) return flight;
    flight = sync();
    return flight;
  };
  const flush = async () => {
    if (flight) await flight;
    if (saveTimer !== undefined) {
      clearTimeout(saveTimer);
      saveTimer = undefined;
    }
    return generation === acknowledged || (await tick());
  };
  const schedule = () => {
    if (
      !predecessorReady ||
      isClosed() ||
      isClosing() ||
      discarding ||
      isDenied() ||
      isInvalid()
    )
      return;
    if (busy || isInitializing()) {
      refreshRequested = true;
      return;
    }
    if (saveTimer !== undefined) return;
    saveTimer = setTimeout(() => {
      saveTimer = undefined;
      if (isInitialized()) void tick();
      else void initialize();
    }, 300);
  };
  const changed = (update: Uint8Array, origin: unknown) => {
    if (origin === remote || !loaded) return;
    generation++;
    // An undo may remove the reactive addition that was refused. Let the server
    // evaluate the new draft once before suspending repeated writes again.
    authoringSuspended = false;
    pendingUpdates.push(update);
    writeOutcome = "idle";
    setNotice("");
    setSaved(false);
    void persist();
    schedule();
  };
  doc.on("update", changed);
  const initializeOnce = async () => {
    if (
      isClosed() ||
      discarding ||
      isDenied() ||
      isInvalid() ||
      isInitializing() ||
      isInitialized()
    )
      return;
    initializationState = "loading";
    try {
      let authoritativeBase: Document | undefined;
      if (!loaded) {
        const cached = await recovery.read();
        if (isClosed()) return;
        const base =
          cached ??
          (authoritativeBase = await apiClient.get<Document>(url, {
            signal: controller.signal,
          }));
        if (isClosed()) return;
        const recovered = await recovery.open(base);
        if (isClosed()) return;
        snapshot = recovered?.snapshot ?? base;
        if (snapshot.state) {
          Y.applyUpdate(doc, decodeUpdate(snapshot.state), remote);
          Y.applyUpdate(acceptedDoc, decodeUpdate(snapshot.state));
        }
        sequence = snapshot.sequence;
        pageId = snapshot.pageId;
        loaded = true;
        setReady(true);
        if (recovered?.update.length) {
          const update = recovered.update;
          Y.applyUpdate(doc, update, remote);
          pendingUpdates = [update];
          generation = 1;
          setSaved(false);
        }
      }
      // Cached permissions never authorize editing or publish restored work.
      const current =
        authoritativeBase && !generation
          ? authoritativeBase
          : await apiClient.get<Document>(url, {
              params: {
                after_seq: sequence,
                state_vector: encodeUpdate(Y.encodeStateVector(acceptedDoc)),
              },
              signal: controller.signal,
            });
      if (isClosed() || discarding) return;
      applyAuthoringCapability(current);
      permission = current.canEdit ? "editable" : "readonly";
      if (!generation) {
        if (current.state)
          Y.applyUpdate(acceptedDoc, decodeUpdate(current.state));
        apply(current);
        snapshot = {
          ...current,
          state: encodeUpdate(Y.encodeStateAsUpdate(acceptedDoc)),
        };
        cacheSnapshot(snapshot);
      } else {
        if (isEditable() && !isReadOnly())
          restoreDocumentIdentities(doc, acceptedDoc);
        setCanEdit(isEditable() && !isReadOnly());
      }
      initializationState = "ready";
      if (!isClosing()) lifecycle = "active";
      if (generation && status.reactiveReadOnly)
        setError(
          "Reactive editing is temporarily unavailable. This draft is not saved. Keep it open or export it before reloading.",
        );
      else if (generation && !isEditable())
        setError(
          "You can no longer edit this document. Export the recovered draft before closing.",
        );
      else setError("");
      if (isEditable() && !isReadOnly()) void reserve();
      if (generation) refreshRequested = true;
    } catch (reason) {
      failure(reason);
    } finally {
      if (initializationState === "loading") initializationState = "pending";
      if (refreshRequested) {
        refreshRequested = false;
        schedule();
      }
    }
  };
  const initialize = () => {
    if (isInitializing() && initialization) return initialization;
    initialization = initializeOnce();
    return initialization;
  };
  void (async () => {
    await priorFinish;
    predecessorReady = true;
    if (!isClosed() && !isClosing()) {
      await initialize();
    }
  })();
  const removeChanges = watchDocumentChanges(scope, {
    intervalMs: "wikiPageId" in scope ? 2000 : undefined,
    getPageId: () => pageId,
    getSequence: () => sequence,
    refresh: schedule,
  });
  const recoveryTimer = setInterval(() => {
    if (!isInitialized()) schedule();
    if (generation > acknowledged) {
      void persist();
      schedule();
    }
  }, 10000);
  const stopPresence = startDocumentPresence({
    scope,
    versionId,
    doc,
    awareness,
    enabled: () => loaded && !isClosed() && !isClosing() && !isDenied(),
    onPeople: setPeople,
  });
  const beforeUnload = (event: BeforeUnloadEvent) => {
    if (generation > acknowledged && readSessionSnapshot().userId === userId) {
      event.preventDefault();
      event.returnValue = "";
    }
  };
  window.addEventListener("beforeunload", beforeUnload);
  const close = () => {
    if (isClosing() || isClosed()) return;
    lifecycle = "closing";
    removeChanges();
    if (saveTimer !== undefined) clearTimeout(saveTimer);
    clearInterval(recoveryTimer);
    stopPresence();
    doc.off("update", changed);
    const finish = async () => {
      // Retain the document, recovery lock and unload protection until the last
      // write settles. This also covers sidebar navigation and browser Back.
      const timeout = setTimeout(() => controller.abort(), 15000);
      try {
        await priorFinish;
        if (initialization) await initialization;
        if (flight) await flight;
        if (generation > acknowledged && !controller.signal.aborted)
          await tick();
        await persist(true);
        if (
          generation > acknowledged &&
          readSessionSnapshot().userId === userId
        ) {
          options.onUnsavedClose();
        }
      } finally {
        lifecycle = "closed";
        clearTimeout(timeout);
        controller.abort();
        window.removeEventListener("beforeunload", beforeUnload);
        awareness.destroy();
        doc.destroy();
        acceptedDoc.destroy();
      }
    };
    const pending = finish();
    finishingDocuments.set(cacheKey, pending);
    void pending.finally(() => {
      if (finishingDocuments.get(cacheKey) === pending)
        finishingDocuments.delete(cacheKey);
    });
  };
  return {
    subscribe(listener: (value: DocumentSessionStatus) => void) {
      listeners.add(listener);
      listener(status);
      return () => {
        listeners.delete(listener);
      };
    },
    get sequence() {
      return sequence;
    },
    get pageId() {
      return pageId;
    },
    flush,
    discard,
    close,
  };
}
