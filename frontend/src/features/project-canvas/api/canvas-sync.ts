import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import type { CanvasScope } from "./canvas-scope";
import {
  documentScopeKey,
  type DocumentScope,
} from "../documents/document-scope";

// Mutation acknowledgments and remote hints enter the same per-query scheduler.
// This holds listeners only; RTK Query owns the state and applied cursor.
const mutationListeners = new Set<
  (scope: CanvasScope, sequence: number) => void
>();

export function notifyCanvasMutation(scope: CanvasScope, sequence: number) {
  mutationListeners.forEach((listener) => listener(scope, sequence));
}

const documentListeners = new Set<
  (scope: DocumentScope, pageId: string, sequence: number) => void
>();
export function notifyDocumentMutation(
  scope: DocumentScope,
  pageId: string,
  sequence: number,
) {
  documentListeners.forEach((listener) => listener(scope, pageId, sequence));
}

export function watchDocumentChanges(
  scope: DocumentScope,
  options: {
    getPageId: () => string;
    intervalMs?: number;
    getSequence: () => number;
    refresh: () => void | Promise<void>;
  },
) {
  let stopped = false;
  let fetching = false;
  let requested = false;
  async function refresh() {
    if (stopped || fetching || document.hidden) return;
    fetching = true;
    requested = false;
    try {
      await options.refresh();
    } catch {
      // RTK exposes the read error; recovery signals and the slow audit retry it.
    } finally {
      fetching = false;
      // Hints received during a fetch must survive RTK's request deduplication.
      if (requested) void refresh();
    }
  }
  const request = () => {
    requested = true;
    void refresh();
  };
  const changed = (
    changedScope: DocumentScope,
    pageId: string,
    sequence: number,
  ) => {
    if (
      documentScopeKey(changedScope) === documentScopeKey(scope) &&
      (!options.getPageId() || pageId === options.getPageId()) &&
      sequence > options.getSequence()
    )
      request();
  };
  documentListeners.add(changed);
  const removeEvents = boardlyRealtimeClient.onEvent((event) => {
    if (event.companyId !== scope.companyId) return;
    if (
      event.type === "canvas.document_changed" &&
      ((options.getPageId() && event.payload.pageId === options.getPageId()) ||
        ("projectId" in scope && event.payload.projectId === scope.projectId) ||
        ("outputId" in scope && event.payload.outputId === scope.outputId)) &&
      typeof event.payload.pageId === "string" &&
      typeof event.payload.sequence === "number"
    ) {
      changed(scope, event.payload.pageId, event.payload.sequence);
    } else if (
      event.type === "auth.company_access_changed" ||
      event.type === "auth.company_access_revoked" ||
      event.type === "artifact.access_changed" ||
      event.type === "wiki.page_updated" ||
      ("projectId" in scope &&
        event.type === "project.archived" &&
        event.payload.id === scope.projectId)
    )
      request();
  });
  const removeSignals = subscribeToRefetchSignals(request);
  const removeStatus = boardlyRealtimeClient.onStatus((status) => {
    if (status.activeCompanyId === scope.companyId && status.status === "ready")
      request();
  });
  const timer = setInterval(() => {
    if (!document.hidden) request();
  }, options.intervalMs ?? 30000);
  return () => {
    stopped = true;
    clearInterval(timer);
    removeEvents();
    removeSignals();
    removeStatus();
    documentListeners.delete(changed);
  };
}

export function watchCanvasChanges(
  scope: CanvasScope,
  options: {
    getSequence?: () => number;
    refresh: (full: boolean) => Promise<void>;
  },
) {
  let started = false;
  let stopped = false;
  let fetching = false;
  let requested = false;
  let full = false;
  let announcedSequence = 0;
  let retryDelay = 1000;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const needsRefresh = () =>
    requested || full || announcedSequence > (options.getSequence?.() ?? 0);

  function schedule(delay = 50) {
    if (
      stopped ||
      !started ||
      fetching ||
      document.hidden ||
      timer !== undefined ||
      !needsRefresh()
    )
      return;
    timer = setTimeout(() => {
      timer = undefined;
      void refresh();
    }, delay);
  }

  function request(fullSnapshot = false, sequence?: number) {
    full ||= fullSnapshot;
    if (sequence === undefined || !options.getSequence) requested = true;
    else announcedSequence = Math.max(announcedSequence, sequence);
    schedule();
  }

  async function refresh() {
    if (stopped || document.hidden || !needsRefresh()) return;
    fetching = true;
    const requestedFull = full;
    const beforeSequence = options.getSequence?.() ?? 0;
    full = false;
    requested = false;
    let delay = 50;
    let retry = true;
    try {
      await options.refresh(requestedFull);
      if (
        announcedSequence > (options.getSequence?.() ?? 0) &&
        (options.getSequence?.() ?? 0) <= beforeSequence
      ) {
        // Bound retries if a stale hint points beyond the server's current head.
        delay = retryDelay;
        retryDelay = Math.min(retryDelay * 2, 30000);
      } else {
        retryDelay = 1000;
      }
    } catch (error) {
      requested = true;
      full ||= requestedFull;
      delay = retryDelay;
      retryDelay = Math.min(retryDelay * 2, 30000);
      // Access failures wait for a recovery signal/check rather than hot retrying.
      const status = (error as { status?: number | null })?.status;
      retry = !status || ![401, 403, 404].includes(status);
    } finally {
      fetching = false;
      // A hint arriving during the fetch stays pending until its sequence has
      // actually been applied. The notification itself never advances the cursor.
      if (retry) schedule(delay);
    }
  }

  const onMutation = (changedScope: CanvasScope, sequence: number) => {
    if (
      changedScope.companyId === scope.companyId &&
      changedScope.projectId === scope.projectId
    )
      request(false, sequence);
  };
  mutationListeners.add(onMutation);
  const removeEvents = boardlyRealtimeClient.onEvent((event) => {
    if (event.companyId !== scope.companyId) return;
    if (
      event.type === "canvas.changed" &&
      event.payload.projectId === scope.projectId
    ) {
      const sequence = event.payload.sequence;
      request(
        false,
        typeof sequence === "number" && Number.isSafeInteger(sequence)
          ? sequence
          : undefined,
      );
    }
    if (
      event.type === "deliverable.created" ||
      event.type === "deliverable.reviewed" ||
      event.type === "deliverable.superseded" ||
      event.type === "auth.company_access_changed" ||
      event.type === "artifact.access_changed" ||
      event.type === "conversation.discussion_access_revoked" ||
      event.type === "conversation.discussion_access_granted" ||
      ((event.type === "project.updated" ||
        event.type === "project.archived") &&
        event.payload.id === scope.projectId)
    )
      request(true);
  });
  const removeStatus = boardlyRealtimeClient.onStatus((snapshot) => {
    if (
      snapshot.activeCompanyId === scope.companyId &&
      snapshot.status === "ready"
    )
      request();
  });
  const removeSignals = subscribeToRefetchSignals((reason) => {
    // Resuming a view also reconciles grants that do not advance the canvas head.
    request(reason === "focus" || reason === "visible");
  });
  // Repairs missed hints (including a commit/publish crash); never a fast poll.
  const recoveryTimer = setInterval(() => request(), 30000);
  // Source grants can change independently of the canvas cursor. This slower
  // audit also repairs a missed access notification while the view stays open.
  const accessTimer = setInterval(() => request(true), 300000);

  return {
    start() {
      started = true;
      schedule();
    },
    dispose() {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
      clearInterval(recoveryTimer);
      clearInterval(accessTimer);
      mutationListeners.delete(onMutation);
      removeEvents();
      removeStatus();
      removeSignals();
    },
  };
}
