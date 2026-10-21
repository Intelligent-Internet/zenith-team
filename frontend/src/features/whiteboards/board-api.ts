import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import type { QueryReturnValue } from "@reduxjs/toolkit/query";
import type { ZenithTeamApiError } from "@/shared/api/zenith-team-api";
import type { RootState } from "@/state/store";
import { zenithTeamRealtimeClient } from "@/shared/realtime/realtime-client.service";
import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";

type Schema = components["schemas"];
export type BoardItem = Schema["BoardItem"];
export type BoardEdge = Schema["BoardEdge"];
export type BoardCommand = Schema["BoardCommand"];
export type BoardBatch = Schema["BoardBatch"];
export type BoardSnapshot = Schema["BoardSnapshot"];
export type BoardHydratedItem = Schema["BoardHydratedItem"];
export type BoardFile = Schema["BoardFile"];
export type BoardScope = { companyId: string; boardId: string };
// A Whiteboard is an Artifact: its Board lives under the Artifact's id, and a
// public Artifact link reads it read-only.
export const boardPath = ({ companyId, boardId }: BoardScope) =>
  `/companies/${encodeURIComponent(companyId)}/artifacts/${encodeURIComponent(boardId)}/whiteboard`;
export const publicBoardPath = (token: string) =>
  `/public/artifacts/${encodeURIComponent(token)}/whiteboard`;
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
// Calls back when a Board changes: a commit from this window, or a
// collaborator's while this member has it open.
export function onBoardChange(scope: BoardScope, changed: () => void) {
  const local = (other: BoardScope) => {
    if (boardScopeKey(other) === boardScopeKey(scope)) changed();
  };
  listeners.add(local);
  const events = zenithTeamRealtimeClient.onEvent((event) => {
    if (
      event.companyId === scope.companyId &&
      event.type === "whiteboard.changed" &&
      "boardId" in event.payload &&
      event.payload.boardId === scope.boardId
    )
      changed();
  });
  return () => {
    listeners.delete(local);
    events();
  };
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
export const whiteboardApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    attachWhiteboardFile: builder.mutation<
      Schema["BoardFile"],
      BoardScope & { input: Schema["BoardFileInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["BoardFile"]>(
              `${boardPath(scope)}/files`,
              input,
            ),
          "This file could not be added to the Whiteboard.",
        ),
    }),
    publicWhiteboard: builder.query<BoardSnapshot, string>({
      queryFn: (token) =>
        executeZenithTeamQuery(
          () => apiClient.get<BoardSnapshot>(publicBoardPath(token)),
          "This Whiteboard link is unavailable.",
        ),
      keepUnusedDataFor: 0,
    }),
    getWhiteboard: builder.query<BoardSnapshot, BoardScope>({
      queryFn: (
        scope,
        { getState, signal },
      ): Promise<QueryReturnValue<BoardSnapshot, ZenithTeamApiError, {}>> => {
        const previous = whiteboardApi.endpoints.getWhiteboard.select(scope)(
          getState() as RootState,
        ).data;
        return executeZenithTeamQuery(
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
              (error as ZenithTeamApiError).status ?? 0,
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
        const events = zenithTeamRealtimeClient.onEvent((event) => {
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
        const status = zenithTeamRealtimeClient.onStatus((event) => {
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
    // Retitles the Whiteboard Artifact. The open Board shows the new title at
    // once; chats and Artifact lists refresh from whiteboard.renamed.
    renameWhiteboard: builder.mutation<
      Schema["Board"],
      BoardScope & { title: Schema["BoardTitleInput"]["title"] }
    >({
      invalidatesTags: [{ type: "Artifact" }],
      queryFn: ({ title, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<Schema["Board"]>(`${boardPath(scope)}/title`, {
              title,
            }),
          "The whiteboard could not be renamed.",
        ),
      async onQueryStarted({ title, ...scope }, { dispatch, queryFulfilled }) {
        const shown = dispatch(
          whiteboardApi.util.updateQueryData(
            "getWhiteboard",
            scope,
            (draft) => {
              draft.board.title = title.trim();
            },
          ),
        );
        try {
          await queryFulfilled;
        } catch {
          shown.undo();
        }
      },
    }),
    whiteboardCommands: builder.mutation<
      Schema["BoardMutation"],
      BoardScope & { input: BoardBatch }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
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
  usePublicWhiteboardQuery,
  useGetWhiteboardQuery,
  useRenameWhiteboardMutation,
  useWhiteboardCommandsMutation,
} = whiteboardApi;
