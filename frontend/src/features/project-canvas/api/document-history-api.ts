import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import {
  blockDocumentPath,
  type DocumentScope,
} from "../documents/document-scope";

type Schema = components["schemas"];
export type HistoryScope = Exclude<DocumentScope, { wikiPageId: string }>;
type Target = { scope: HistoryScope; versionId: string };
export type HistoryEntry = Schema["DocumentHistoryEntry"];
const path = (target: Target) => `${blockDocumentPath(target.scope)}/history`;
const tag = (target: Target) => ({
  type: "CanvasHistory" as const,
  id: path(target),
});
const api = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    documentHistory: builder.query<
      Schema["DocumentHistory"],
      Target & {
        actorId?: string;
        after?: string;
        before?: string;
        beforeSequence?: number;
      }
    >({
      providesTags: (_result, _error, target) => [tag(target)],
      queryFn: (target, { signal }) =>
        executeZenithTeamQuery(() => {
          const params = new URLSearchParams();
          if (target.actorId) params.set("actor_id", target.actorId);
          if (target.after) params.set("after", target.after);
          if (target.before) params.set("before", target.before);
          if (target.beforeSequence)
            params.set("before_sequence", String(target.beforeSequence));
          return apiClient.get<Schema["DocumentHistory"]>(
            `${path(target)}?${params}`,
            { signal },
          );
        }, "History could not be loaded."),
    }),
    documentHistoryState: builder.query<
      Schema["DocumentHistoryState"],
      Target & { sequence: number }
    >({
      providesTags: (_result, _error, target) => [tag(target)],
      queryFn: (target, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DocumentHistoryState"]>(
              `${path(target)}/${target.sequence}`,
              { signal },
            ),
          "This saved state could not be loaded.",
        ),
    }),
    restoreDocumentHistory: builder.mutation<
      Schema["DocumentHistoryState"],
      Target & { sequence: number; expectedSequence: number }
    >({
      invalidatesTags: (_result, _error, target) => [tag(target)],
      queryFn: (target) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["DocumentHistoryState"]>(
              `${path(target)}/${target.sequence}/restore`,
              { expectedSequence: target.expectedSequence, schemaVersion: 2 },
            ),
          "The document changed. Refresh history before restoring.",
        ),
    }),
    nameDocumentHistory: builder.mutation<
      void,
      Target & { sequence: number; label: string }
    >({
      invalidatesTags: (_result, _error, target) => [tag(target)],
      queryFn: (target) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<void>(`${path(target)}/${target.sequence}/name`, {
              label: target.label,
            }),
          "The checkpoint name could not be saved.",
        ),
    }),
  }),
});
export const {
  useDocumentHistoryQuery,
  useDocumentHistoryStateQuery,
  useRestoreDocumentHistoryMutation,
  useNameDocumentHistoryMutation,
} = api;
