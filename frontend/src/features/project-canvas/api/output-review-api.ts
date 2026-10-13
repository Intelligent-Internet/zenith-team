import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import {
  blockDocumentPath,
  type OutputDocumentScope,
} from "../documents/document-scope";
import type {
  components,
  operations,
} from "@/shared/api/generated/api.generated";

type OpenDocumentResult =
  operations["openOutputDocument"]["responses"][200]["content"]["application/json"];
type CheckpointResult =
  operations["checkpointOutputDocument"]["responses"][200]["content"]["application/json"];
type Scope = { companyId: string; outputId: string };
const path = ({ companyId, outputId }: Scope) =>
  `/companies/${encodeURIComponent(companyId)}/artifacts/${encodeURIComponent(outputId)}/review`;
const api = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    openOutputDocument: builder.mutation<
      OpenDocumentResult,
      OutputDocumentScope
    >({
      invalidatesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `review:${scope.outputId}` },
      ],
      queryFn: (scope) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<OpenDocumentResult>(
              `${blockDocumentPath(scope, "")}/open`,
              {},
            ),
          "The document could not be opened.",
        ),
    }),
    checkpointOutputDocument: builder.mutation<
      CheckpointResult,
      OutputDocumentScope
    >({
      invalidatesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `review:${scope.outputId}` },
      ],
      queryFn: (scope) =>
        executeBoardlyQuery(async () => {
          const path = blockDocumentPath(scope, "");
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
  useCheckpointOutputDocumentMutation,
  useOutputReviewQuery,
  useOutputReviewCommandMutation,
  useOutputDocumentVersionQuery,
} = api;
