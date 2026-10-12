import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

type Status = components["schemas"]["HuddleAIStatus"];
type Budget = components["schemas"]["HuddleBudget"];
type Scope = { companyId: string; huddleId: string };
const path = ({ companyId, huddleId }: Scope) =>
  `/companies/${encodeURIComponent(companyId)}/huddles/${encodeURIComponent(huddleId)}/ai`;
const api = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getHuddleHistory: builder.infiniteQuery<
      components["schemas"]["DiscussionPage"],
      { companyId: string; conversationId: string; userId: string },
      string | null
    >({
      keepUnusedDataFor: 0,
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.olderCursor ?? undefined,
      },
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<components["schemas"]["DiscussionPage"]>(
              `/companies/${queryArg.companyId}/conversations/${queryArg.conversationId}/discussion-messages`,
              { params: { limit: 50, cursor: pageParam ?? undefined }, signal },
            ),
          "Huddle history is unavailable.",
        ),
    }),
    getHuddleChat: builder.query<
      components["schemas"]["DiscussionPage"],
      { companyId: string; conversationId: string; userId: string }
    >({
      keepUnusedDataFor: 0,
      queryFn: ({ companyId, conversationId }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<components["schemas"]["DiscussionPage"]>(
              `/companies/${companyId}/conversations/${conversationId}/discussion-messages`,
              { params: { limit: 50 }, signal },
            ),
          "Call chat is unavailable.",
        ),
    }),
    sendHuddleChat: builder.mutation<
      components["schemas"]["DiscussionReceipt"],
      {
        companyId: string;
        conversationId: string;
        body: components["schemas"]["SendDiscussionRequest"];
      }
    >({
      queryFn: ({ companyId, conversationId, body }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<components["schemas"]["DiscussionReceipt"]>(
              `/companies/${companyId}/conversations/${conversationId}/discussion-messages`,
              body,
              { signal },
            ),
          "Message could not be sent. Your text has been kept.",
        ),
    }),
    getHuddleAI: builder.query<Status, Scope>({
      queryFn: (scope) =>
        executeBoardlyQuery(
          () => apiClient.get<Status>(path(scope)),
          "Voice Agent status is unavailable.",
        ),
    }),
    setHuddleAI: builder.mutation<
      Status,
      Scope & { body: components["schemas"]["SetHuddleAI"] }
    >({
      queryFn: ({ body, ...scope }) =>
        executeBoardlyQuery(
          () => apiClient.put<Status>(path(scope), body),
          "Could not change the voice Agent for this call.",
        ),
    }),
    updateHuddleProposal: builder.mutation<
      components["schemas"]["HuddleProposal"],
      Scope & {
        proposalId: string;
        body: components["schemas"]["UpdateHuddleProposalRequest"];
      }
    >({
      queryFn: ({ proposalId, body, ...scope }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<components["schemas"]["HuddleProposal"]>(
              `${path(scope)}/proposals/${encodeURIComponent(proposalId)}`,
              body,
              { signal },
            ),
          "Could not update the work proposal. Your edits have been kept.",
        ),
    }),
    submitHuddleProposal: builder.mutation<
      components["schemas"]["HuddleProposal"],
      Scope & {
        proposalId: string;
        body: components["schemas"]["SubmitHuddleProposalRequest"];
      }
    >({
      queryFn: ({ proposalId, body, ...scope }, { signal }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<components["schemas"]["HuddleProposal"]>(
              `${path(scope)}/proposals/${encodeURIComponent(proposalId)}/submit`,
              body,
              { signal },
            ),
          "Could not submit the work proposal.",
        ),
      invalidatesTags: (result, _error, { companyId }) =>
        result
          ? [
              { type: "Task", id: companyId },
              { type: "Conversation", id: companyId },
            ]
          : [],
    }),
    getHuddleBudget: builder.query<Budget, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => apiClient.get<Budget>(`/companies/${companyId}/huddle-budget`),
          "Could not read the huddle budget.",
        ),
    }),
    setHuddleBudget: builder.mutation<
      Budget,
      {
        companyId: string;
        monthlyLimitUsdTicks: string;
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, ...body }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<Budget>(
              `/companies/${companyId}/huddle-budget`,
              body,
            ),
          "Could not update the huddle budget.",
        ),
    }),
  }),
});
export const {
  useGetHuddleHistoryInfiniteQuery,
  useGetHuddleChatQuery,
  useSendHuddleChatMutation,
  useGetHuddleAIQuery,
  useSetHuddleAIMutation,
  useUpdateHuddleProposalMutation,
  useSubmitHuddleProposalMutation,
  useGetHuddleBudgetQuery,
  useSetHuddleBudgetMutation,
} = api;
