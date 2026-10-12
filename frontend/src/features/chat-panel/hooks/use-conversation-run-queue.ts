import { useCallback, useEffect, useRef, useState } from "react";
import { useStore } from "react-redux";

import { readSessionSnapshot } from "@/app/session/session";
import {
  conversationRunQueueApi,
  useLazyGetConversationRunQueueQuery,
} from "@/features/chat-panel/api/conversation-run-queue-api";
import { conversationService, runService } from "@/shared/api";
import type {
  ConversationCurrentRun,
  ConversationRunQueueItem,
  ConversationRunQueueProjection,
} from "@/shared/api/types";
import {
  getApiErrorCode,
  getApiErrorMessage,
  isApiClientError,
} from "@/lib/api-client";
import { useAppDispatch } from "@/state";
import type { RootState } from "@/state/store";

export type ConversationCurrentRunView = ConversationCurrentRun & {
  cancellationPending?: boolean;
};

type ConversationRunQueueView = Omit<
  ConversationRunQueueProjection,
  "currentRun"
> & {
  currentRun: ConversationCurrentRunView | null;
};

export type ConversationRunQueueAvailability =
  "checking" | "ready" | "unavailable";

type ConversationRunQueueState = {
  availability: ConversationRunQueueAvailability;
  conversationId: string | null;
  error: string | null;
  projection: ConversationRunQueueView;
};

const emptyRunQueue: ConversationRunQueueView = {
  currentRun: null,
  items: [],
  limit: 20,
};

type QueuedReceiptDetail = {
  conversationId: string;
  item: ConversationRunQueueItem;
};

const queuedReceiptListeners = new Set<(detail: QueuedReceiptDetail) => void>();
const queueRefreshListeners = new Set<(conversationId: string) => void>();

export function publishQueuedReceipt(detail: QueuedReceiptDetail) {
  queuedReceiptListeners.forEach((listener) => listener(detail));
}

export function requestConversationRunQueueRefresh(conversationId: string) {
  queueRefreshListeners.forEach((listener) => listener(conversationId));
}

export function conversationRunQueueMutationErrorMessage(
  error: unknown,
  action: "remove" | "reorder",
) {
  const code = getApiErrorCode(error);
  if (code === "run_queue_conflict" || statusFromError(error) === 409) {
    return "The queue changed elsewhere. The latest queue has been restored.";
  }
  if (statusFromError(error) === 403) {
    return "You no longer have permission to change the queue.";
  }
  if (statusFromError(error) === 404) {
    return "This conversation or queued prompt no longer exists.";
  }
  if (statusFromError(error) === null) {
    return "Zenith Team could not be reached. Check your connection, then try again.";
  }
  return getApiErrorMessage(
    error,
    action === "remove"
      ? "The queued prompt could not be removed."
      : "The queue order could not be saved.",
  );
}

export function useConversationRunQueue(
  conversationId?: string | null,
  initialProjection?: ConversationRunQueueProjection,
) {
  const dispatch = useAppDispatch();
  const reduxStore = useStore();
  const companyId = readSessionSnapshot().company?.id;
  const normalizedConversationId = conversationId ?? null;
  const [loadRunQueue, runQueueQuery] = useLazyGetConversationRunQueueQuery();
  const [queueState, setQueueState] = useState<ConversationRunQueueState>(() =>
    initialQueueState(normalizedConversationId, initialProjection),
  );
  const [isLoading, setIsLoading] = useState(false);
  const [isReordering, setIsReordering] = useState(false);
  const [isStopping, setIsStopping] = useState(false);
  const [deletingItemIds, setDeletingItemIds] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const requestGenerationRef = useRef(0);

  const readQueue = useCallback(
    async (preferCacheValue: boolean, markChecking: boolean) => {
      if (!conversationId || !companyId) {
        setQueueState(initialQueueState(null));
        return null;
      }
      const generation = ++requestGenerationRef.current;
      setIsLoading(markChecking);
      if (markChecking) {
        setQueueState((current) => ({
          availability: "checking",
          conversationId,
          error: null,
          projection:
            current.conversationId === conversationId
              ? current.projection
              : emptyRunQueue,
        }));
      }
      try {
        const next = await loadRunQueue(
          { companyId, conversationId },
          preferCacheValue,
        ).unwrap();
        if (generation === requestGenerationRef.current) {
          setQueueState({
            availability: "ready",
            conversationId,
            error: null,
            projection: next,
          });
        }
        return next;
      } catch (error) {
        if (generation === requestGenerationRef.current) {
          setQueueState((current) => ({
            availability: "unavailable",
            conversationId,
            error: conversationRunQueueLoadErrorMessage(error),
            projection:
              current.conversationId === conversationId
                ? current.projection
                : emptyRunQueue,
          }));
        }
        throw error;
      } finally {
        if (generation === requestGenerationRef.current) {
          setIsLoading(false);
        }
      }
    },
    [companyId, conversationId, loadRunQueue],
  );

  const refresh = useCallback(() => readQueue(false, true), [readQueue]);

  useEffect(() => {
    const next = runQueueQuery.currentData;
    const argument = runQueueQuery.originalArgs;
    if (
      !next ||
      runQueueQuery.isFetching ||
      !argument ||
      argument.companyId !== companyId ||
      argument.conversationId !== conversationId
    ) {
      return;
    }
    setQueueState({
      availability: "ready",
      conversationId,
      error: null,
      projection: next,
    });
  }, [
    companyId,
    conversationId,
    runQueueQuery.currentData,
    runQueueQuery.isFetching,
    runQueueQuery.originalArgs,
  ]);

  useEffect(() => {
    requestGenerationRef.current += 1;
    setQueueState(initialQueueState(conversationId ?? null, initialProjection));
    setIsLoading(false);
    if (!companyId || !conversationId) return;

    void (async () => {
      const argument = { companyId, conversationId };
      const cachedProjection =
        conversationRunQueueApi.endpoints.getConversationRunQueue.select(
          argument,
        )(reduxStore.getState() as RootState).data;
      if (initialProjection && !cachedProjection) {
        await dispatch(
          conversationRunQueueApi.util.upsertQueryData(
            "getConversationRunQueue",
            argument,
            initialProjection,
          ),
        );
      }
      const projectionsDisagree = Boolean(
        initialProjection &&
        cachedProjection &&
        runQueueVersion(initialProjection) !==
          runQueueVersion(cachedProjection),
      );
      await readQueue(
        !projectionsDisagree,
        !initialProjection && !cachedProjection,
      );
    })().catch(() => undefined);
    const handleQueuedReceipt = (detail: QueuedReceiptDetail) => {
      if (detail?.conversationId !== conversationId) return;
      setQueueState((current) => {
        if (current.conversationId !== conversationId) {
          return current;
        }
        if (
          current.projection.items.some((item) => item.id === detail.item.id)
        ) {
          return current;
        }
        return {
          ...current,
          projection: {
            ...current.projection,
            items: [...current.projection.items, detail.item],
          },
        };
      });
      // The HTTP receipt may arrive after a terminal transaction already
      // promoted this item. Re-read the authority so that a late optimistic
      // receipt cannot resurrect a materialized queue row.
      void refresh().catch(() => undefined);
    };
    const handleRefreshRequest = (requestedConversationId: string) => {
      if (requestedConversationId === conversationId) {
        void refresh().catch(() => undefined);
      }
    };
    queuedReceiptListeners.add(handleQueuedReceipt);
    queueRefreshListeners.add(handleRefreshRequest);
    return () => {
      queuedReceiptListeners.delete(handleQueuedReceipt);
      queueRefreshListeners.delete(handleRefreshRequest);
      requestGenerationRef.current += 1;
    };
  }, [
    companyId,
    conversationId,
    dispatch,
    initialProjection,
    readQueue,
    reduxStore,
    refresh,
  ]);

  const currentState =
    queueState.conversationId === normalizedConversationId
      ? queueState
      : initialQueueState(normalizedConversationId, initialProjection);
  const projection = currentState.projection;

  const stop = useCallback(async () => {
    const run = projection.currentRun;
    if (!companyId || !run || isStopping || run.cancellationPending) return;
    setIsStopping(true);
    setQueueState((current) => ({
      ...current,
      projection: {
        ...current.projection,
        currentRun:
          current.projection.currentRun?.id === run.id
            ? { ...current.projection.currentRun, cancellationPending: true }
            : current.projection.currentRun,
      },
    }));
    try {
      await runService.cancelRun(companyId, run.id);
      await refresh();
    } catch (error) {
      await refresh().catch(() => undefined);
      throw error;
    } finally {
      setIsStopping(false);
    }
  }, [companyId, isStopping, projection.currentRun, refresh]);

  const remove = useCallback(
    async (item: ConversationRunQueueItem) => {
      if (!companyId || !conversationId || deletingItemIds.has(item.id)) return;
      const previous = projection;
      setDeletingItemIds((current) => new Set(current).add(item.id));
      setQueueState((current) => ({
        ...current,
        projection: {
          ...current.projection,
          items: current.projection.items.filter(
            (candidate) => candidate.id !== item.id,
          ),
        },
      }));
      try {
        const next = await conversationService.deleteRunQueueItem(
          conversationId,
          item.id,
          item.revision,
        );
        setQueueState({
          availability: "ready",
          conversationId,
          error: null,
          projection: next,
        });
        dispatch(
          conversationRunQueueApi.util.upsertQueryData(
            "getConversationRunQueue",
            { companyId, conversationId },
            next,
          ),
        );
      } catch (error) {
        setQueueState({
          availability: "checking",
          conversationId,
          error: null,
          projection: previous,
        });
        await refresh().catch(() => undefined);
        throw error;
      } finally {
        setDeletingItemIds((current) => {
          const next = new Set(current);
          next.delete(item.id);
          return next;
        });
      }
    },
    [companyId, conversationId, deletingItemIds, dispatch, projection, refresh],
  );

  const reorder = useCallback(
    async (items: ConversationRunQueueItem[]) => {
      if (!companyId || !conversationId || isReordering) return;
      const previous = projection;
      setQueueState((current) => ({
        ...current,
        projection: { ...current.projection, items },
      }));
      setIsReordering(true);
      try {
        const next = await conversationService.reorderRunQueue(conversationId, {
          items: items.map((item) => ({
            expectedRevision: item.revision,
            id: item.id,
          })),
        });
        setQueueState({
          availability: "ready",
          conversationId,
          error: null,
          projection: next,
        });
        dispatch(
          conversationRunQueueApi.util.upsertQueryData(
            "getConversationRunQueue",
            { companyId, conversationId },
            next,
          ),
        );
      } catch (error) {
        setQueueState({
          availability: "checking",
          conversationId,
          error: null,
          projection: previous,
        });
        await refresh().catch(() => undefined);
        throw error;
      } finally {
        setIsReordering(false);
      }
    },
    [companyId, conversationId, dispatch, isReordering, projection, refresh],
  );

  return {
    ...projection,
    availability: currentState.availability,
    deletingItemIds,
    error: currentState.error,
    isLoading,
    isReordering,
    isStopping,
    refresh,
    remove,
    reorder,
    stop,
  };
}

function initialQueueState(
  conversationId: string | null,
  projection?: ConversationRunQueueProjection,
): ConversationRunQueueState {
  return {
    availability: projection ? "ready" : "checking",
    conversationId,
    error: null,
    projection: projection ?? emptyRunQueue,
  };
}

function conversationRunQueueLoadErrorMessage(error: unknown) {
  const status = statusFromError(error);
  if (status === 403) {
    return "You no longer have permission to read the queue.";
  }
  if (status === 404) {
    return "This conversation no longer exists.";
  }
  if (status === null) {
    return "Zenith Team could not check the queue. Check your connection.";
  }
  return getApiErrorMessage(
    error,
    "The queue could not be checked. Sending is paused until it is available.",
  );
}

function statusFromError(error: unknown) {
  if (isApiClientError(error)) return error.status;
  if (error && typeof error === "object" && "status" in error) {
    return typeof error.status === "number" ? error.status : null;
  }
  return null;
}

function runQueueVersion(projection: ConversationRunQueueProjection) {
  return JSON.stringify({
    currentRun: projection.currentRun
      ? [
          projection.currentRun.id,
          projection.currentRun.status,
          projection.currentRun.queuedAt,
        ]
      : null,
    items: projection.items.map((item) => [item.id, item.revision]),
    limit: projection.limit,
  });
}
