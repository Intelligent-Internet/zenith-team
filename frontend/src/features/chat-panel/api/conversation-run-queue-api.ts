import { conversationService } from "@/shared/api/conversation.service";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { ConversationRunQueueProjection } from "@/shared/api/types";

export type ConversationRunQueueArgument = {
  companyId: string;
  conversationId: string;
};

export const conversationRunQueueApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getConversationRunQueue: builder.query<
      ConversationRunQueueProjection,
      ConversationRunQueueArgument
    >({
      providesTags: (_result, _error, { companyId, conversationId }) => [
        { id: companyId, type: "Conversation" },
        { id: conversationId, type: "Conversation" },
      ],
      queryFn: ({ conversationId }) =>
        executeBoardlyQuery(
          () => conversationService.getRunQueue(conversationId),
          "The conversation queue could not be loaded.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useLazyGetConversationRunQueueQuery } = conversationRunQueueApi;
