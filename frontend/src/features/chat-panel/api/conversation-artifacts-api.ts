import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

export type ConversationArtifactsContext = {
  companyId: string;
  conversationId: string;
  runId?: string;
};
export type ConversationArtifactPage =
  components["schemas"]["ConversationArtifactPage"];
export type ConversationArtifact = ConversationArtifactPage["items"][number];

export const conversationArtifactsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getConversationArtifacts: builder.infiniteQuery<
      ConversationArtifactPage,
      ConversationArtifactsContext,
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
      },
      providesTags: (result, _error, { companyId, conversationId }) => [
        { type: "Artifact" },
        { type: "Artifact", id: `${companyId}:${conversationId}` },
        ...(result?.pages.flatMap((page) =>
          page.items.flatMap((item) => [
            { type: "Artifact" as const, id: item.artifact.id },
            { type: "Deliverable" as const, id: item.runId ?? undefined },
            { type: "Run" as const, id: item.runId ?? undefined },
          ]),
        ) ?? []),
      ],
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<ConversationArtifactPage>(
              `/companies/${encodeURIComponent(queryArg.companyId)}/conversations/${encodeURIComponent(queryArg.conversationId)}/artifacts`,
              {
                params: {
                  limit: 20,
                  cursor: pageParam ?? undefined,
                  runId: queryArg.runId,
                },
                signal,
              },
            ),
          "Artifacts could not be loaded.",
        ),
    }),
  }),
});

export const { useGetConversationArtifactsInfiniteQuery } =
  conversationArtifactsApi;
