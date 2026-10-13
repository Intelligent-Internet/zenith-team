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
    // An Agent or Task chat makes a Whiteboard without a message: it joins
    // the chat's Artifacts, which the answer is shaped like.
    createChatWhiteboard: builder.mutation<
      ConversationArtifact,
      ConversationArtifactsContext & { title: string }
    >({
      invalidatesTags: (_result, _error, { companyId, conversationId }) => [
        { type: "Artifact", id: `${companyId}:${conversationId}` },
      ],
      queryFn: ({ conversationId, title }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<ConversationArtifact>(
              `/conversations/${encodeURIComponent(conversationId)}/whiteboards`,
              { title },
            ),
          "The whiteboard could not be created.",
        ),
      // The list holds it before the refetch lands, so the panel can open
      // it at once.
      async onQueryStarted(
        { companyId, conversationId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            conversationArtifactsApi.util.updateQueryData(
              "getConversationArtifacts",
              { companyId, conversationId },
              (draft) => {
                const first = draft.pages[0];
                if (
                  first &&
                  !first.items.some(
                    (item) => item.artifact.id === data.artifact.id,
                  )
                )
                  first.items.unshift(data);
              },
            ),
          );
        } catch {
          /* The caller reports the failure. */
        }
      },
    }),
  }),
});

export const {
  useCreateChatWhiteboardMutation,
  useGetConversationArtifactsInfiniteQuery,
} = conversationArtifactsApi;
