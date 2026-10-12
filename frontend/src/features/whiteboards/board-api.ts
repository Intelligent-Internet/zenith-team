import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
import type { QueryReturnValue } from "@reduxjs/toolkit/query";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { RootState } from "@/state/store";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";

type Schema = components["schemas"];
export type Board = Schema["Board"];
export type BoardItem = Schema["BoardItem"];
export type BoardEdge = Schema["BoardEdge"];
export type BoardCommand = Schema["BoardCommand"];
export type BoardBatch = Schema["BoardBatch"];
export type BoardSnapshot = Schema["BoardSnapshot"];
export type BoardHydratedItem = Schema["BoardHydratedItem"];
export type BoardFile = Schema["BoardFile"];
export type BoardScope = { companyId: string; boardId: string };
export const boardsPath = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}/whiteboards`;
export const boardPath = ({ companyId, boardId }: BoardScope) =>
  `${boardsPath(companyId)}/${encodeURIComponent(boardId)}`;
export const publicBoardPath = (token: string) =>
  `/public/whiteboards/${encodeURIComponent(token)}`;
export const boardFilePath = (
  base: string,
  attachmentId: string,
  variant: "original" | "preview",
) =>
  `${base}/files/${encodeURIComponent(attachmentId)}${variant === "preview" ? "?variant=preview" : ""}`;
export const boardScopeKey = (scope: BoardScope) =>
  `${scope.companyId}:${scope.boardId}`;
const listeners = new Set<(scope: BoardScope, sequence: number) => void>();
export function notifyBoardChange(scope: BoardScope, sequence: number) {
  listeners.forEach((listener) => listener(scope, sequence));
}
function reconcile<T extends { id: string }>(
  current: T[],
  incoming: T[],
  removed: Set<string>,
  full: boolean,
) {
  const next = new Map(incoming.map((item) => [item.id, item]));
  return [
    ...current.filter(
      (item) => !removed.has(item.id) && !next.has(item.id) && !full,
    ),
    ...incoming.filter((item) => !removed.has(item.id)),
  ];
}
export const whiteboardApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    attachWhiteboardFile: builder.mutation<
      Schema["BoardFile"],
      BoardScope & { input: Schema["BoardFileInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schema["BoardFile"]>(
              `${boardPath(scope)}/files`,
              input,
            ),
          "This file could not be added to the Whiteboard.",
        ),
    }),
    whiteboardAccess: builder.query<Schema["BoardAccess"], BoardScope>({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["BoardAccess"]>(`${boardPath(scope)}/access`),
          "Sharing could not be loaded.",
        ),
      providesTags: (_result, _error, scope) => [
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:access` },
      ],
      keepUnusedDataFor: 0,
    }),
    whiteboardChannel: builder.query<Schema["BoardChannel"], BoardScope>({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["BoardChannel"]>(
              `${boardPath(scope)}/channel`,
            ),
          "The Board's chat could not be loaded.",
        ),
      providesTags: (_result, _error, scope) => [
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:channel` },
      ],
      keepUnusedDataFor: 0,
    }),
    joinWhiteboardChannel: builder.mutation<Schema["BoardChannel"], BoardScope>(
      {
        queryFn: (scope) =>
          executeBoardlyQuery(
            () =>
              apiClient.post<Schema["BoardChannel"]>(
                `${boardPath(scope)}/channel`,
              ),
            "The Board's chat could not be opened.",
          ),
        invalidatesTags: (_result, _error, scope) => [
          { type: "Whiteboard", id: `${boardScopeKey(scope)}:channel` },
        ],
      },
    ),
    setWhiteboardAccess: builder.mutation<
      void,
      BoardScope & { input: Schema["BoardAccessInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.put<void>(`${boardPath(scope)}/access`, input),
          "Access could not be updated.",
        ),
      invalidatesTags: (_result, _error, scope) => [
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:access` },
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:channel` },
        { type: "Whiteboard", id: boardScopeKey(scope) },
        { type: "Whiteboard", id: scope.companyId },
      ],
    }),
    setWhiteboardVisibility: builder.mutation<
      void,
      BoardScope & { input: Schema["BoardVisibilityInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.put<void>(`${boardPath(scope)}/visibility`, input),
          "General access could not be updated.",
        ),
      invalidatesTags: (_result, _error, scope) => [
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:access` },
        { type: "Whiteboard", id: `${boardScopeKey(scope)}:channel` },
        { type: "Whiteboard", id: boardScopeKey(scope) },
        { type: "Whiteboard", id: scope.companyId },
      ],
    }),
    publicWhiteboard: builder.query<BoardSnapshot, string>({
      queryFn: (token) =>
        executeBoardlyQuery(
          () => apiClient.get<BoardSnapshot>(publicBoardPath(token)),
          "This Whiteboard link is unavailable.",
        ),
      keepUnusedDataFor: 0,
    }),
    listWhiteboards: builder.query<
      Schema["BoardList"],
      {
        companyId: string;
        search?: string;
        archived?: boolean;
        favorites?: boolean;
        /** Boards no active member manages; Company Whiteboard managers only. */
        unowned?: boolean;
        offset?: number;
      }
    >({
      queryFn: ({ companyId, ...params }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["BoardList"]>(boardsPath(companyId), {
              params: {
                ...params,
                search: params.search || undefined,
                limit: 100,
              },
            }),
          "Whiteboards could not be loaded.",
        ),
      providesTags: (_result, _error, args) => [
        { type: "Whiteboard", id: args.companyId },
      ],
    }),
    createWhiteboard: builder.mutation<
      Board,
      { companyId: string; input: Schema["CreateBoardInput"] }
    >({
      queryFn: ({ companyId, input }) =>
        executeBoardlyQuery(
          () => apiClient.post<Board>(boardsPath(companyId), input),
          "The Whiteboard could not be created.",
        ),
      invalidatesTags: (_result, _error, args) => [
        { type: "Whiteboard", id: args.companyId },
      ],
    }),
    updateWhiteboard: builder.mutation<
      Board,
      BoardScope & { input: Schema["UpdateBoardInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.patch<Board>(boardPath(scope), input),
          "The Whiteboard could not be updated.",
        ),
      invalidatesTags: (_result, _error, args) => [
        { type: "Whiteboard", id: args.companyId },
        { type: "Whiteboard", id: boardScopeKey(args) },
      ],
    }),
    claimWhiteboard: builder.mutation<
      Board,
      BoardScope & { input: Schema["BoardClaimInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.post<Board>(`${boardPath(scope)}/claim`, input),
          "The Whiteboard could not be claimed.",
        ),
      invalidatesTags: (_result, _error, args) => [
        { type: "Whiteboard", id: args.companyId },
        { type: "Whiteboard", id: boardScopeKey(args) },
        { type: "Whiteboard", id: `${boardScopeKey(args)}:access` },
        { type: "Whiteboard", id: `${boardScopeKey(args)}:channel` },
      ],
    }),
    favoriteWhiteboard: builder.mutation<
      unknown,
      BoardScope & { favorite: boolean }
    >({
      queryFn: ({ favorite, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.put(`${boardPath(scope)}/favorite`, { favorite }),
          "The favorite could not be saved.",
        ),
      invalidatesTags: (_result, _error, args) => [
        { type: "Whiteboard", id: args.companyId },
      ],
    }),
    getWhiteboard: builder.query<BoardSnapshot, BoardScope>({
      queryFn: (
        scope,
        { getState, signal },
      ): Promise<QueryReturnValue<BoardSnapshot, BoardlyApiError, {}>> => {
        const previous = whiteboardApi.endpoints.getWhiteboard.select(scope)(
          getState() as RootState,
        ).data;
        return executeBoardlyQuery(
          () =>
            apiClient.get<BoardSnapshot>(boardPath(scope), {
              params: { after_seq: previous?.sequence ?? 0 },
              signal,
            }),
          "The Whiteboard could not be loaded.",
        );
      },
      merge(current, next) {
        if (next.sequence < current.sequence) return;
        const removed = new Set(next.removed);
        current.items = reconcile(
          current.items,
          next.items,
          removed,
          next.full,
        );
        current.edges = reconcile(
          current.edges,
          next.edges,
          removed,
          next.full,
        );
        current.board = next.board;
        current.sequence = next.sequence;
        current.removed = next.removed;
        current.full = next.full;
      },
      providesTags: (_result, _error, scope) => [
        { type: "Whiteboard", id: boardScopeKey(scope) },
      ],
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(
        scope,
        { dispatch, getCacheEntry, cacheDataLoaded, cacheEntryRemoved },
      ) {
        let stopped = false,
          requested = true,
          fetching = false,
          ready = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        let targetSequence = 0;
        let retryDelay = 300;
        const refresh = async () => {
          timer = undefined;
          if (stopped || !ready || fetching || document.hidden) return;
          fetching = true;
          requested = false;
          let retry = true;
          try {
            await dispatch(
              whiteboardApi.endpoints.getWhiteboard.initiate(scope, {
                subscribe: false,
                forceRefetch: true,
              }),
            ).unwrap();
            retryDelay = 300;
          } catch (error) {
            retry = ![401, 403, 404].includes(
              (error as BoardlyApiError).status ?? 0,
            );
            requested = retry;
            retryDelay = Math.min(30000, Math.max(1000, retryDelay * 2));
          } finally {
            fetching = false;
            if (
              retry &&
              (requested ||
                targetSequence > (getCacheEntry().data?.sequence ?? 0))
            )
              schedule(retryDelay);
          }
        };
        const schedule = (delay = 30) => {
          if (!stopped && !timer)
            timer = setTimeout(() => void refresh(), delay);
        };
        const request = (sequence = 0) => {
          targetSequence = Math.max(sequence, targetSequence);
          requested = true;
          schedule();
        };
        const local = (changed: BoardScope, sequence: number) => {
          if (boardScopeKey(changed) === boardScopeKey(scope))
            request(sequence);
        };
        listeners.add(local);
        const events = boardlyRealtimeClient.onEvent((event) => {
          if (event.companyId !== scope.companyId) return;
          if (
            event.type.startsWith("whiteboard.") &&
            event.type !== "whiteboard.presence" &&
            "boardId" in event.payload &&
            event.payload.boardId === scope.boardId
          )
            request(
              "sequence" in event.payload &&
                typeof event.payload.sequence === "number"
                ? event.payload.sequence
                : 0,
            );
          if (event.type.startsWith("auth.company_access")) request();
        });
        const status = boardlyRealtimeClient.onStatus((event) => {
          if (
            event.activeCompanyId === scope.companyId &&
            event.status === "ready"
          )
            request();
        });
        const signals = subscribeToRefetchSignals(() => request());
        const audit = setInterval(() => request(), 30000);
        try {
          await cacheDataLoaded;
          ready = true;
          if (requested) schedule();
          await cacheEntryRemoved;
        } catch {
          /* The cache may be removed before its first response. */
        } finally {
          stopped = true;
          clearTimeout(timer);
          clearInterval(audit);
          events();
          status();
          signals();
          listeners.delete(local);
        }
      },
    }),
    whiteboardCommands: builder.mutation<
      Schema["BoardMutation"],
      BoardScope & { input: BoardBatch }
    >({
      queryFn: ({ input, ...scope }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schema["BoardMutation"]>(
              `${boardPath(scope)}/operations`,
              input,
            ),
          "The change could not be saved.",
        ),
      async onQueryStarted(scope, { queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          notifyBoardChange(scope, data.sequence);
        } catch {
          /* Caller retains retryable operations. */
        }
      },
    }),
  }),
});
export const {
  useAttachWhiteboardFileMutation,
  useListWhiteboardsQuery,
  useCreateWhiteboardMutation,
  useUpdateWhiteboardMutation,
  useClaimWhiteboardMutation,
  useFavoriteWhiteboardMutation,
  useGetWhiteboardQuery,
  useWhiteboardCommandsMutation,
} = whiteboardApi;
