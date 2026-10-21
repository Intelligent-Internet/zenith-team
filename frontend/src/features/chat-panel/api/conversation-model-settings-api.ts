import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type {
  ConversationModelSettings,
  ConversationModelSettingsUpdate,
} from "@/shared/api/types";

export const conversationModelSettingsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getConversationModelSettings: builder.query<
      ConversationModelSettings,
      string
    >({
      queryFn: (conversationId) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<ConversationModelSettings>(
              `/conversations/${conversationId}/model-settings`,
            ),
          "Chat model settings could not be loaded.",
        ),
      providesTags: (_result, _error, id) => [{ type: "Conversation", id }],
    }),
    updateConversationModelSettings: builder.mutation<
      ConversationModelSettings,
      {
        conversationId: string;
        payload: ConversationModelSettingsUpdate;
      }
    >({
      queryFn: ({ conversationId, payload }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<ConversationModelSettings>(
              `/conversations/${conversationId}/model-settings`,
              payload,
            ),
          "Chat model settings could not be saved.",
        ),
      async onQueryStarted({ conversationId }, { dispatch, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            conversationModelSettingsApi.util.updateQueryData(
              "getConversationModelSettings",
              conversationId,
              () => data,
            ),
          );
        } catch {
          // Refetch on a conflict so the next edit uses the authoritative revision.
          dispatch(
            zenithTeamApi.util.invalidateTags([
              { type: "Conversation", id: conversationId },
            ]),
          );
        }
      },
    }),
  }),
});

export const {
  useGetConversationModelSettingsQuery,
  useUpdateConversationModelSettingsMutation,
} = conversationModelSettingsApi;
