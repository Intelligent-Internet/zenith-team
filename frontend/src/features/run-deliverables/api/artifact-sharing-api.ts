import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
type Schema = components["schemas"];
type Artifact = { companyId: string; artifactId: string };
const base = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}`;
const artifactPath = (a: Artifact) =>
  `${base(a.companyId)}/artifacts/${encodeURIComponent(a.artifactId)}`;
const tags = [{ type: "Artifact" as const }, { type: "Deliverable" as const }];
export const artifactSharingApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    setArtifactVisibility: builder.mutation<
      Schema["ArtifactAccess"],
      Artifact & { body: Schema["ArtifactVisibilityInput"] }
    >({
      invalidatesTags: tags,
      queryFn: (a) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<Schema["ArtifactAccess"]>(
              `${artifactPath(a)}/visibility`,
              a.body,
            ),
          "Access changed. Refresh and try again.",
        ),
    }),
    revokeArtifactConversation: builder.mutation<
      Schema["ArtifactAccess"],
      Artifact & { conversationId: string; expectedRevision: number }
    >({
      invalidatesTags: tags,
      queryFn: (a) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<Schema["ArtifactAccess"]>(
              `${artifactPath(a)}/conversations/${encodeURIComponent(a.conversationId)}?expectedRevision=${a.expectedRevision}`,
            ),
          "Group access could not be revoked.",
        ),
    }),
    listArtifactShareConversations: builder.query<
      Schema["DiscussionSummary"][],
      string
    >({
      queryFn: (companyId) =>
        executeBoardlyQuery(async () => {
          const result: Schema["DiscussionSummary"][] = [];
          for (const path of ["channels", "human-conversations"]) {
            let cursor: string | undefined;
            do {
              const page = await apiClient.get<Schema["DiscussionList"]>(
                `${base(companyId)}/${path}`,
                { params: { limit: 100, cursor } },
              );
              result.push(
                ...page.items.filter((item) => item.capabilities.send),
              );
              cursor = page.nextCursor ?? undefined;
            } while (cursor);
          }
          return result;
        }, "Conversations could not be loaded."),
    }),
    shareArtifactConversation: builder.mutation<
      Schema["DiscussionReceipt"],
      Artifact & { conversationId: string; requestKey: string }
    >({
      invalidatesTags: tags,
      queryFn: (a) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schema["DiscussionReceipt"]>(
              `${base(a.companyId)}/conversations/${encodeURIComponent(a.conversationId)}/discussion-messages`,
              {
                requestKey: a.requestKey,
                body: { version: 1, markdown: "", references: [] },
                artifactIds: [a.artifactId],
              },
            ),
          "This artifact could not be shared with the conversation.",
        ),
    }),
    getPublicArtifact: builder.query<Schema["PublicArtifact"], string>({
      queryFn: (token) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schema["PublicArtifact"]>(
              `/public/artifacts/${encodeURIComponent(token)}`,
            ),
          "This link is unavailable or has been revoked.",
        ),
    }),
  }),
});
export const {
  useSetArtifactVisibilityMutation,
  useRevokeArtifactConversationMutation,
  useListArtifactShareConversationsQuery,
  useShareArtifactConversationMutation,
  useGetPublicArtifactQuery,
} = artifactSharingApi;

export function downloadPublicArtifactFile(
  token: string,
  ordinal: number,
  signal?: AbortSignal,
): Promise<Blob> {
  return apiClient.get<Blob>(
    `/public/artifacts/${encodeURIComponent(token)}/items/${ordinal}/download`,
    { responseType: "blob", timeout: 0, signal },
  );
}
