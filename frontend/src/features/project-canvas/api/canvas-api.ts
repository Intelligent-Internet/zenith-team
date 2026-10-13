import { canvasPath, type CanvasScope } from "./canvas-scope";
import { apiClient } from "@/lib/api-client";
import type { QueryReturnValue } from "@reduxjs/toolkit/query";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { RootState } from "@/state/store";
import {
  notifyCanvasMutation,
  watchCanvasChanges,
  watchDocumentChanges,
} from "./canvas-sync";
import { readSessionSnapshot } from "@/shared/session/session";
import {
  readDocumentCache,
  writeDocumentCache,
  removeDocumentCache,
} from "../documents/document-cache";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

export { canvasPath, type CanvasScope } from "./canvas-scope";

type Schema = components["schemas"];
export type CanvasItem = Schema["CanvasItem"];
export type CanvasArtifact = Schema["CanvasArtifact"];
export type CanvasVersion = Schema["CanvasVersion"];
export type CanvasCommand = Schema["CanvasCommand"];
export type CanvasSnapshot = Schema["CanvasSnapshot"];

function reconcile<T extends { id: string }>(
  current: T[],
  next: T[],
  removed: Set<string>,
  full: boolean,
) {
  const incoming = new Map(next.map((value) => [value.id, value]));
  const rows = current.filter(
    (value) => !removed.has(value.id) && (!full || incoming.has(value.id)),
  );
  for (let i = 0; i < rows.length; i++) {
    const value = incoming.get(rows[i].id);
    if (value) {
      if (JSON.stringify(value) !== JSON.stringify(rows[i])) rows[i] = value;
      incoming.delete(value.id);
    }
  }
  return [...rows, ...incoming.values()];
}

// A pending access refresh is consumed by the next actual query, including when
// RTK deduplicates a refetch against an already-running request.
const fullSnapshots = new Set<string>();
const scopeKey = (scope: CanvasScope) =>
  `${scope.companyId}:${scope.projectId}`;

function reviewTags(scope: CanvasScope, next?: CanvasSnapshot) {
  const id = scopeKey(scope);
  if (!next || next.full) {
    return [
      { type: "CanvasThreads" as const, id },
      { type: "CanvasHistory" as const, id },
    ];
  }
  return [
    ...(next.artifacts.length
      ? [{ type: "CanvasHistory" as const, id: `${id}:timeline` }]
      : []),
    ...next.reviewVersionIds.map((versionId) => ({
      type: "CanvasThreads" as const,
      id: `${id}:${versionId}`,
    })),
    ...next.artifacts.map((artifact) => ({
      type: "CanvasHistory" as const,
      id: `${id}:${artifact.id}`,
    })),
  ];
}

function failedReviewTags(state: RootState, scope: CanvasScope) {
  // The board cursor may already include a change whose separate review fetch
  // failed. Recovery must retry that query even when the next delta is empty.
  return canvasApi.util
    .selectInvalidatedBy(state, reviewTags(scope))
    .flatMap(({ queryCacheKey, endpointName, originalArgs }) => {
      const query = state.boardlyApi.queries[queryCacheKey];
      const status = (query?.error as BoardlyApiError | undefined)?.status;
      if (
        query?.status !== "rejected" ||
        (status && status < 500 && status !== 408 && status !== 429)
      )
        return [];
      const args = originalArgs as CanvasScope & {
        versionId?: string;
        artifactId?: string;
      };
      const threads = endpointName === "canvasThreads";
      return [
        {
          type: threads
            ? ("CanvasThreads" as const)
            : ("CanvasHistory" as const),
          id: `${scopeKey(scope)}:${endpointName === "canvasTimeline" ? "timeline" : threads ? args.versionId : args.artifactId}`,
        },
      ];
    });
}

export const canvasApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getCanvas: builder.query<CanvasSnapshot, CanvasScope>({
      async queryFn(
        scope,
        { getState, signal },
      ): Promise<QueryReturnValue<CanvasSnapshot, BoardlyApiError, {}>> {
        const current = canvasApi.endpoints.getCanvas.select(scope)(
          getState() as RootState,
        ).data;
        const full = fullSnapshots.delete(scopeKey(scope));
        const result = await executeBoardlyQuery(
          () =>
            apiClient.get<CanvasSnapshot>(canvasPath(scope), {
              params: { after_seq: full ? 0 : (current?.sequence ?? 0) },
              signal,
            }),
          "The canvas could not be loaded.",
        );
        if (result.error && full && !signal.aborted)
          fullSnapshots.add(scopeKey(scope));
        return result;
      },
      providesTags: (_r, _e, scope) => [
        { type: "Canvas", id: scopeKey(scope) },
      ],
      keepUnusedDataFor: 0,
      merge: (current, next) => {
        if (next.sequence < current.sequence && !next.full) return;
        const removed = new Set(next.removed);
        const allowed = new Set(next.artifactIds);
        current.items = reconcile(
          current.items,
          next.items,
          removed,
          next.full,
        );
        current.artifacts = reconcile(
          current.artifacts.filter((value) => allowed.has(value.id)),
          next.artifacts,
          removed,
          next.full,
        );
        current.edges = reconcile(
          current.edges,
          next.edges,
          removed,
          next.full,
        );
        current.artifactIds = next.artifactIds;
        current.reviewVersionIds = next.reviewVersionIds;
        current.sequence = next.sequence;
        current.full = next.full;
        current.canvasId = next.canvasId;
        current.canEdit = next.canEdit;
        current.canManageFiles = next.canManageFiles;
        current.canEditDocuments = next.canEditDocuments;
      },
      async onQueryStarted(scope, { dispatch, queryFulfilled, getState }) {
        try {
          const { data } = await queryFulfilled;
          const tags = [
            ...reviewTags(scope, data),
            ...failedReviewTags(getState() as RootState, scope),
          ];
          if (tags.length) dispatch(canvasApi.util.invalidateTags(tags));
        } catch (failure) {
          const { error } = failure as { error: BoardlyApiError };
          if (error?.status && [401, 403, 404].includes(error.status)) {
            dispatch(
              canvasApi.util.updateQueryData("getCanvas", scope, (draft) => {
                draft.sequence = 0;
                draft.artifactIds = [];
                draft.reviewVersionIds = [];
                draft.items = [];
                draft.artifacts = [];
                draft.edges = [];
                draft.canEdit = false;
                draft.canManageFiles = false;
                draft.canEditDocuments = false;
              }),
            );
            dispatch(canvasApi.util.invalidateTags(reviewTags(scope)));
          }
        }
      },
      async onCacheEntryAdded(
        scope,
        { cacheDataLoaded, cacheEntryRemoved, getCacheEntry, dispatch },
      ) {
        let stopped = false;
        let abort: (() => void) | undefined;
        const sync = watchCanvasChanges(scope, {
          getSequence: () => getCacheEntry().data?.sequence ?? 0,
          refresh: async (full) => {
            if (full) fullSnapshots.add(scopeKey(scope));
            do {
              const request = dispatch(
                canvasApi.endpoints.getCanvas.initiate(scope, {
                  subscribe: false,
                  forceRefetch: true,
                }),
              );
              abort = request.abort;
              await request.unwrap();
              abort = undefined;
              // An access hint may have arrived while a different query was in
              // flight. Its full read must still run after that request finishes.
            } while (!stopped && fullSnapshots.has(scopeKey(scope)));
          },
        });
        void cacheEntryRemoved.then(() => {
          stopped = true;
          sync.dispose();
          abort?.();
          fullSnapshots.delete(scopeKey(scope));
        });
        try {
          await cacheDataLoaded;
          if (!stopped) sync.start();
        } catch {
          // The initial query owns its error; removal disposes subscriptions.
        }
      },
    }),
    canvasCommand: builder.mutation<
      Schema["CanvasResult"],
      CanvasScope & { command: CanvasCommand }
    >({
      queryFn: ({ command, ...scope }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schema["CanvasResult"]>(
              `${canvasPath(scope)}/operations`,
              command,
            ),
          "The change could not be saved.",
        ),
      async onQueryStarted(scope, { queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          notifyCanvasMutation(scope, data.sequence);
        } catch {
          // The mutation's caller owns save errors and conflict recovery.
        }
      },
      invalidatesTags: (_r, _e, scope) =>
        scope.command.type === "create_document" ? ["Wiki"] : [],
    }),
    canvasTimeline: builder.query<
      Schema["CanvasTimelinePage"],
      CanvasScope & { before?: string }
    >({
      queryFn: (scope, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasTimelinePage"]>(
              `${canvasPath(scope)}/timeline`,
              { params: { before: scope.before }, signal },
            ),
          "The artifact timeline could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => [
        { type: "CanvasHistory", id: `${scopeKey(scope)}:timeline` },
        { type: "CanvasHistory", id: scopeKey(scope) },
      ],
      keepUnusedDataFor: 0,
    }),
    canvasHistory: builder.query<
      Schema["CanvasHistory"],
      CanvasScope & { artifactId: string }
    >({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasHistory"]>(
              `${canvasPath(scope)}/artifacts/${scope.artifactId}/history`,
            ),
          "Version history could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => [
        {
          type: "CanvasHistory",
          id: `${scope.companyId}:${scope.projectId}:${scope.artifactId}`,
        },
        { type: "CanvasHistory", id: scopeKey(scope) },
      ],
      keepUnusedDataFor: 0,
    }),
    canvasThreads: builder.query<
      Schema["CanvasThreadList"],
      CanvasScope & { versionId: string }
    >({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasThreadList"]>(
              `${canvasPath(scope)}/versions/${scope.versionId}/threads`,
            ),
          "Comments could not be loaded.",
        ),
      async onQueryStarted(scope, { dispatch, queryFulfilled }) {
        try {
          await queryFulfilled;
        } catch {
          dispatch(
            canvasApi.util.updateQueryData("canvasThreads", scope, (draft) => {
              draft.items = [];
            }),
          );
        }
      },
      providesTags: (_r, _e, scope) => [
        {
          type: "CanvasThreads",
          id: `${scope.companyId}:${scope.projectId}:${scope.versionId}`,
        },
        { type: "CanvasThreads", id: `${scope.companyId}:${scope.projectId}` },
      ],
      keepUnusedDataFor: 30,
    }),
    canvasDocument: builder.query<
      Schema["CanvasDocument"],
      CanvasScope & { versionId: string; blockId?: string; preview?: boolean }
    >({
      async queryFn(
        args,
        { signal, getState },
      ): Promise<
        QueryReturnValue<Schema["CanvasDocument"], BoardlyApiError, {}>
      > {
        const { versionId, blockId, preview, ...scope } = args;
        const key = `${readSessionSnapshot().userId}:${scopeKey(scope)}:projection:${versionId}:${blockId ?? ""}:${!!preview}`;
        const current =
          canvasApi.endpoints.canvasDocument.select(args)(
            getState() as RootState,
          ).data ?? (await readDocumentCache<Schema["CanvasDocument"]>(key));
        const result = await executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasDocument"]>(
              `${canvasPath(scope)}/versions/${versionId}/document`,
              {
                params: {
                  projection: true,
                  block_id: blockId,
                  preview,
                  ...(current ? { after_seq: current.sequence } : {}),
                },
                signal,
              },
            ),
          "The live document could not be loaded.",
        );
        if (result.data) {
          const data =
            current &&
            result.data.sequence === current.sequence &&
            !result.data.content
              ? {
                  ...current,
                  ...result.data,
                  content: current.content,
                  markdown: current.markdown,
                }
              : result.data;
          if (data.sequence !== current?.sequence)
            void writeDocumentCache(key, data).catch(() => {});
          return { data };
        }
        if ([401, 403, 404].includes(result.error?.status ?? 0))
          await removeDocumentCache(key);
        return result;
      },
      async onCacheEntryAdded(
        args,
        { dispatch, getCacheEntry, cacheEntryRemoved },
      ) {
        const dispose = watchDocumentChanges(args, {
          getPageId: () => getCacheEntry().data?.pageId ?? "",
          getSequence: () => getCacheEntry().data?.sequence ?? -1,
          refresh: async () => {
            await dispatch(
              canvasApi.endpoints.canvasDocument.initiate(args, {
                subscribe: false,
                forceRefetch: true,
              }),
            ).unwrap();
          },
        });
        await cacheEntryRemoved;
        dispose();
      },
      providesTags: (_r, _e, scope) => [
        { type: "CanvasHistory", id: scopeKey(scope) },
      ],
      keepUnusedDataFor: 30,
    }),
    canvasLibrary: builder.query<Schema["CanvasLibrary"], CanvasScope>({
      queryFn: (scope, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasLibrary"]>(
              `${canvasPath(scope)}/library`,
              { signal },
            ),
          "The canvas library could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => [
        { type: "Canvas", id: scopeKey(scope) },
      ],
    }),
    canvasSearch: builder.query<
      Schema["CanvasSearch"],
      CanvasScope & { query: string }
    >({
      queryFn: ({ query, ...scope }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasSearch"]>(
              `${canvasPath(scope)}/search`,
              { params: { query }, signal },
            ),
          "Search is unavailable.",
        ),
      keepUnusedDataFor: 5,
    }),
    canvasBody: builder.query<
      Schema["CanvasBody"],
      CanvasScope & { versionId: string }
    >({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["CanvasBody"]>(
              `${canvasPath(scope)}/versions/${scope.versionId}/body`,
            ),
          "Document could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => [
        { type: "CanvasHistory", id: scopeKey(scope) },
      ],
      keepUnusedDataFor: 300,
    }),
  }),
});
export const {
  useGetCanvasQuery,
  useCanvasCommandMutation,
  useCanvasHistoryQuery,
  useCanvasTimelineQuery,
  useCanvasThreadsQuery,
  useCanvasBodyQuery,
  useCanvasDocumentQuery,
  useCanvasLibraryQuery,
  useCanvasSearchQuery,
} = canvasApi;
