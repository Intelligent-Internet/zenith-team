import type { BaseQueryApi } from "@reduxjs/toolkit/query";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import { conversationService } from "@/shared/api/conversation.service";
import { companySearchService } from "@/shared/api/company-search.service";
import type {
  CompanySearchPage,
  CompanySearchRequest,
  ConversationStateDto,
  ConversationStateUpdateRequest,
  ConversationHistoryWindow,
  SendConversationMessageRequest,
  DirectConversationDto,
  RenameDirectConversationRequest,
} from "@/shared/api/types";

export const conversationApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceConversationStates: builder.query({
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => conversationService.listStates(companyId),
          "Chat filing states could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    listWorkspaceChatConversations: builder.query({
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => conversationService.listDirectConversations(companyId, true),
          "Chats could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    listWorkspaceConversationPins: builder.query({
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => conversationService.listPins(companyId),
          "Pinned chats could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    createWorkspaceChatSession: builder.mutation<
      DirectConversationDto,
      { companyId: string; agentId: string; requestKey: string }
    >({
      queryFn: ({ companyId, agentId, requestKey }) =>
        executeZenithTeamQuery(
          () =>
            conversationService.createDirectConversation(companyId, {
              agentId,
              requestKey,
            }),
          "The new chat could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    renameWorkspaceConversation: builder.mutation<
      DirectConversationDto,
      {
        companyId: string;
        conversationId: string;
        request: RenameDirectConversationRequest;
      }
    >({
      queryFn: ({ companyId, conversationId, request }) =>
        executeZenithTeamQuery(
          () =>
            conversationService.renameDirectConversation(
              companyId,
              conversationId,
              request,
            ),
          "The conversation could not be renamed.",
        ),
      invalidatesTags: (_result, _error, { companyId, conversationId }) => [
        { id: companyId, type: "Conversation" },
        { id: conversationId, type: "Conversation" },
      ],
      async onQueryStarted(
        { companyId, conversationId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          for (const endpoint of [
            "listWorkspaceChatConversations",
            "listWorkspaceDirectConversations",
          ] as const) {
            dispatch(
              conversationApi.util.updateQueryData(
                endpoint,
                companyId,
                (page) => {
                  const conversation = page?.items.find(
                    (item) => item.id === conversationId,
                  );
                  if (conversation && conversation.revision <= data.revision) {
                    conversation.title = data.title;
                    conversation.revision = data.revision;
                    conversation.updatedAt = data.updatedAt;
                  }
                },
              ),
            );
          }
        } catch {
          // Failed saves retain the draft; invalidation refreshes the saved title.
        }
      },
    }),
    startWorkspaceChatSession: builder.mutation<
      DirectConversationDto,
      {
        companyId: string;
        agentId: string;
        requestKey: string;
        messageKey: string;
        content: string;
      }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (
        { companyId, agentId, requestKey, messageKey, content },
        { dispatch },
      ) =>
        executeZenithTeamQuery(async () => {
          const conversation =
            await conversationService.createDirectConversation(companyId, {
              agentId,
              requestKey,
            });
          await conversationService.postMessage(conversation.id, {
            requestKey: messageKey,
            content,
          });
          await refreshWorkspaceChatConversations(dispatch, companyId);
          return conversation;
        }, "The new chat could not be started."),
    }),
    listWorkspaceDirectConversations: builder.query<
      Awaited<ReturnType<typeof conversationService.listDirectConversations>>,
      string
    >({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => conversationService.listDirectConversations(companyId),
          "Conversations could not be loaded.",
        ),
    }),
    getWorkspaceConversationState: builder.query<ConversationStateDto, string>({
      providesTags: (_result, _error, conversationId) => [
        { id: conversationId, type: "Conversation" },
      ],
      queryFn: (conversationId) =>
        executeZenithTeamQuery(
          () => conversationService.getConversationState(conversationId),
          "Chat state could not be loaded.",
        ),
    }),
    searchWorkspaceConversations: builder.query<
      CompanySearchPage,
      { companyId: string; request: CompanySearchRequest }
    >({
      queryFn: ({ companyId, request }) =>
        executeZenithTeamQuery(
          () => companySearchService.search(companyId, request),
          "Chats could not be searched.",
        ),
    }),
    getWorkspaceConversationHistory: builder.query<
      ConversationHistoryWindow,
      { companyId: string; conversationId: string }
    >({
      keepUnusedDataFor: 15 * 60,
      providesTags: (_result, _error, { conversationId }) => [
        { id: conversationId, type: "Message" },
      ],
      queryFn: ({ conversationId }) =>
        executeZenithTeamQuery(
          () => conversationService.loadLatestMessages(conversationId),
          "Messages could not be loaded.",
        ),
      // A message frame can arrive while this HTTP snapshot is in flight.
      // Keep those newer rows until a later read includes them.
      merge: (current, incoming) => ({
        ...incoming,
        items: [
          ...incoming.items,
          ...current.items.filter(
            (item) => item.sequence > incoming.throughSeq,
          ),
        ],
      }),
    }),
    listWorkspaceMessages: builder.query({
      providesTags: (_result, _error, conversationId: string) => [
        { id: conversationId, type: "Message" },
      ],
      queryFn: (conversationId: string) =>
        executeZenithTeamQuery(
          () => conversationService.listMessages(conversationId),
          "Messages could not be loaded.",
        ),
    }),
    sendWorkspaceMessage: builder.mutation({
      invalidatesTags: (
        _result,
        _error,
        argument: { conversationId: string },
      ) => [{ id: argument.conversationId, type: "Message" }],
      queryFn: (argument: {
        conversationId: string;
        request: SendConversationMessageRequest;
      }) =>
        executeZenithTeamQuery(
          () =>
            conversationService.postMessage(
              argument.conversationId,
              argument.request,
            ),
          "Message could not be sent.",
        ),
    }),
    updateWorkspaceConversationState: builder.mutation<
      ConversationStateDto,
      {
        companyId: string;
        conversationId: string;
        request: ConversationStateUpdateRequest;
      }
    >({
      invalidatesTags: (_result, _error, { companyId, conversationId }) => [
        { id: conversationId, type: "Conversation" },
        { id: companyId, type: "Conversation" },
      ],
      async onQueryStarted(
        { companyId, conversationId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            conversationApi.util.upsertQueryData(
              "getWorkspaceConversationState",
              conversationId,
              data,
            ),
          );
          dispatch(
            conversationApi.util.updateQueryData(
              "listWorkspaceConversationStates",
              companyId,
              (states) => {
                if (!states) return;
                const index = states.items.findIndex(
                  (item) => item.conversationId === conversationId,
                );
                if (index >= 0) states.items[index] = data;
                else states.items.push(data);
              },
            ),
          );
          dispatch(
            conversationApi.util.updateQueryData(
              "listWorkspaceChatConversations",
              companyId,
              (conversations) => {
                if (!conversations) return;
                const conversation = conversations.items.find(
                  (item) => item.id === conversationId,
                );
                if (conversation) {
                  conversation.pinnedAt = data.pinnedAt;
                  conversation.archivedAt = data.archivedAt;
                }
              },
            ),
          );
          dispatch(
            conversationApi.util.updateQueryData(
              "listWorkspaceDirectConversations",
              companyId,
              (conversations) => {
                const conversation = conversations.items.find(
                  (item) => item.id === conversationId,
                );
                if (conversation) conversation.pinnedAt = data.pinnedAt;
              },
            ),
          );
        } catch {
          // Invalidation refreshes the state and list after a conflict or failure.
        }
      },
      queryFn: (argument) =>
        executeZenithTeamQuery(
          () =>
            conversationService.updateConversationState(
              argument.conversationId,
              argument.request,
            ),
          "Chat state could not be updated.",
        ),
    }),
  }),
});

export async function refreshWorkspaceChatConversations(
  dispatch: BaseQueryApi["dispatch"],
  companyId: string,
) {
  // The route checks this catalogue before opening a conversation. A read
  // already in flight may predate creation, so let it finish before refetching.
  await dispatch(
    conversationApi.util.getRunningQueryThunk(
      "listWorkspaceChatConversations",
      companyId,
    ),
  );
  await dispatch(
    conversationApi.endpoints.listWorkspaceChatConversations.initiate(
      companyId,
      {
        forceRefetch: true,
        subscribe: false,
      },
    ),
  ).unwrap();
}

export const {
  useListWorkspaceConversationPinsQuery,
  useListWorkspaceConversationStatesQuery,
  useListWorkspaceChatConversationsQuery,
  useStartWorkspaceChatSessionMutation,
  useCreateWorkspaceChatSessionMutation,
  useRenameWorkspaceConversationMutation,
  useLazyGetWorkspaceConversationStateQuery,
  useLazySearchWorkspaceConversationsQuery,
  useListWorkspaceDirectConversationsQuery,
  useListWorkspaceMessagesQuery,
  useSendWorkspaceMessageMutation,
  useUpdateWorkspaceConversationStateMutation,
} = conversationApi;
