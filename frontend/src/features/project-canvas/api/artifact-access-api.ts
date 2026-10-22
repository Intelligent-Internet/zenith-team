import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";

type Schema = components["schemas"];
export type ArtifactScope = { companyId: string; artifactId: string };
const accessPath = (scope: ArtifactScope) =>
  `/companies/${encodeURIComponent(scope.companyId)}/artifacts/${encodeURIComponent(scope.artifactId)}/access`;
const api = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    retainedArtifactContent: builder.query<
      Schema["RetainedArtifactContent"],
      ArtifactScope & { snapshotId?: string }
    >({
      providesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `live:${scope.artifactId}` },
      ],
      queryFn: ({ snapshotId, ...scope }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["RetainedArtifactContent"]>(
              `/companies/${encodeURIComponent(scope.companyId)}/artifacts/${encodeURIComponent(scope.artifactId)}/content`,
              { signal, params: { snapshotId } },
            ),
          "This saved content is unavailable or you no longer have access.",
        ),
    }),
    liveArtifactAccess: builder.query<
      Schema["LiveArtifactAccess"],
      ArtifactScope
    >({
      providesTags: (_data, _error, scope) => [
        { type: "Artifact", id: `live:${scope.artifactId}` },
      ],
      queryFn: (scope, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["LiveArtifactAccess"]>(accessPath(scope), {
              signal,
            }),
          "This Page is unavailable or you no longer have access.",
        ),
    }),
    changeLiveArtifactAccess: builder.mutation<
      Schema["LiveArtifactAccess"],
      ArtifactScope & { change: Schema["LiveArtifactAccessChange"] }
    >({
      invalidatesTags: [{ type: "Artifact" }],
      queryFn: ({ change, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<Schema["LiveArtifactAccess"]>(
              accessPath(scope),
              change,
            ),
          "Sharing changed. Refresh and try again.",
        ),
    }),
    readablePages: builder.query<Schema["PageList"], string>({
      providesTags: [{ type: "Artifact" }],
      queryFn: (companyId, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["PageList"]>(
              `/companies/${encodeURIComponent(companyId)}/pages`,
              { signal },
            ),
          "Pages could not be loaded.",
        ),
    }),
  }),
});
export const {
  useLiveArtifactAccessQuery,
  useRetainedArtifactContentQuery,
  useChangeLiveArtifactAccessMutation,
  useReadablePagesQuery,
} = api;
