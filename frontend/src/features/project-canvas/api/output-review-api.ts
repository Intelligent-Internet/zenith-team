import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import {
  blockDocumentPath,
  type OutputDocumentScope,
  type PageDocumentScope,
} from "../documents/document-scope";
import type {
  components,
  operations,
} from "@/shared/api/generated/api.generated";

type OpenDocumentResult =
  operations["openOutputDocument"]["responses"][200]["content"]["application/json"];
type CheckpointResult =
  operations["checkpointPageDocument"]["responses"][200]["content"]["application/json"];
type Scope = { companyId: string; outputId: string };
const path = ({ companyId, outputId }: Scope) =>
  `/companies/${encodeURIComponent(companyId)}/artifacts/${encodeURIComponent(outputId)}/review`;
const api = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    openOutputDocument: builder.mutation<
      OpenDocumentResult,
      OutputDocumentScope
    >({
      invalidatesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `review:${scope.outputId}` },
      ],
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<OpenDocumentResult>(
              `${path(scope)}/items/${encodeURIComponent(scope.outputItemId)}/document/open`,
              {},
            ),
          "The document could not be opened.",
        ),
    }),
    checkpointPageDocument: builder.mutation<
      CheckpointResult,
      PageDocumentScope
    >({
      queryFn: (scope) =>
        executeZenithTeamQuery(async () => {
          const path = blockDocumentPath(scope);
          const draft = await apiClient.get<
            components["schemas"]["CanvasDocument"]
          >(`${path}?projection=true`);
          return apiClient.post<CheckpointResult>(`${path}/checkpoints`, {
            sequence: draft.sequence,
          });
        }, "The document version could not be saved."),
    }),
    outputDocumentVersion: builder.query<
      components["schemas"]["CanvasBody"],
      Scope & { versionId: string }
    >({
      queryFn: ({ versionId, ...scope }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["CanvasBody"]>(
              `${path(scope)}/versions/${encodeURIComponent(versionId)}/body`,
              { signal },
            ),
          "This version is unavailable.",
        ),
    }),
    outputReview: builder.query<components["schemas"]["OutputReview"], Scope>({
      providesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `review:${scope.outputId}` },
      ],
      queryFn: (scope, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["OutputReview"]>(path(scope), {
              signal,
            }),
          "Comments could not be loaded.",
        ),
    }),
    outputReviewCommand: builder.mutation<
      components["schemas"]["CanvasResult"],
      Scope & { command: components["schemas"]["CanvasCommand"] }
    >({
      invalidatesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `review:${scope.outputId}` },
      ],
      queryFn: ({ command, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<components["schemas"]["CanvasResult"]>(
              `${path(scope)}/operations`,
              command,
            ),
          "The comment could not be saved. Refresh and try again.",
        ),
    }),
  }),
});
export const {
  useOpenOutputDocumentMutation,
  useCheckpointPageDocumentMutation,
  useOutputReviewQuery,
  useOutputReviewCommandMutation,
  useOutputDocumentVersionQuery,
} = api;
