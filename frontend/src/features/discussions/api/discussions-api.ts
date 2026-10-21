import { companySearchService } from "@/shared/api/company-search.service";
import type {
  CompanySearchPage,
  CompanySearchRequest,
} from "@/shared/api/types";
import { hasRecentPresenceActivity } from "../model/presence-activity";
import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";

type Schema = components["schemas"];
export type DiscussionSummary = Schema["DiscussionSummary"];
export type DiscussionMessage = Schema["DiscussionMessage"];
// A shared history window can be the root stream or a thread response.
export type DiscussionPage = Schema["DiscussionPage"] &
  Partial<Pick<Schema["DiscussionThreadPage"], "root" | "state">>;
export type DiscussionState = Schema["DiscussionState"];
export type DiscussionMutation = Schema["DiscussionMutation"];
export type DiscussionThreadState = Schema["DiscussionThreadState"];
export type DiscussionReceipt = Schema["DiscussionReceipt"];
export type ChannelManagement = Schema["ChannelManagement"];
export type ChannelDetail = Schema["ChannelDetail"];
export type DiscussionDetail = ChannelDetail | Schema["HumanDirectDetail"];
export type CreateChannelRequest = Schema["CreateChannelRequest"];
export type SendDiscussionRequest = Schema["SendDiscussionRequest"];

// Generation belongs to this authenticated UI session, never to a token.
export type DiscussionSession = {
  companyId: string;
  userId: string;
  generation: number;
};
export type DiscussionScope = DiscussionSession & {
  conversationId: string;
  view: "root" | "thread";
  rootId?: string;
};
export type DiscussionAddress = DiscussionSession & {
  conversationId: string;
  type: DiscussionSummary["type"];
};
export type DiscussionPageParam = string | null;
export type ChannelListRequest = DiscussionSession & {
  query: string;
  archived: boolean;
  includeHidden: boolean;
};
export type DiscussionMessageAction =
  | { type: "edit"; body: Schema["EditDiscussionRequest"] }
  | { type: "delete"; body: Schema["ChannelRevisionRequest"] }
  | { type: "pin"; active: boolean }
  | { type: "reaction"; emoji: string; active: boolean };

export const discussionHistoryEndpoint = (scope: DiscussionScope) =>
  scope.view === "thread"
    ? ("getDiscussionThread" as const)
    : ("getDiscussionHistory" as const);

export type ChannelAction =
  | { action: "update"; name: string; topic: string }
  | { action: "addMember" | "removeMember"; memberUserId: string }
  | { action: "addAgent" | "removeAgent"; agentId: string }
  | { action: "leave" | "archive" | "restore" };
export type ChannelChange = DiscussionSession & {
  conversationId: string;
  expectedRevision: number;
} & ChannelAction;

const companyPath = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}`;
const channelPath = (arg: { companyId: string; conversationId: string }) =>
  `${companyPath(arg.companyId)}/channels/${encodeURIComponent(arg.conversationId)}`;
const conversationPath = (arg: { companyId: string; conversationId: string }) =>
  `${companyPath(arg.companyId)}/conversations/${encodeURIComponent(arg.conversationId)}`;

const channelTags = (companyId: string, conversationId?: string) => [
  { type: "DiscussionList" as const, id: companyId },
  ...(conversationId
    ? [{ type: "DiscussionDetail" as const, id: conversationId }]
    : []),
];

export const discussionsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getChannelInvocations: builder.query<
      Schema["ChannelInvocationPage"],
      DiscussionSession & { conversationId: string }
    >({
      keepUnusedDataFor: 0,
      providesTags: (_result, _error, { companyId, conversationId }) => [
        { type: "ChannelAgent", id: companyId },
        { type: "ChannelAgent", id: conversationId },
      ],
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["ChannelInvocationPage"]>(
              `${channelPath(arg)}/agent-invocations`,
              { signal },
            ),
          "Agent status could not be loaded.",
        ),
    }),
    getChannelAgentActivity: builder.query<
      Schema["ChannelAgentActivity"],
      DiscussionSession & { conversationId: string; agentId: string }
    >({
      keepUnusedDataFor: 0,
      providesTags: (_result, _error, { companyId, conversationId }) => [
        { type: "ChannelAgentActivity", id: companyId },
        { type: "ChannelAgentActivity", id: conversationId },
      ],
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["ChannelAgentActivity"]>(
              `${channelPath(arg)}/agents/${encodeURIComponent(arg.agentId)}/activity`,
              { signal },
            ),
          "Agent activity is unavailable.",
        ),
    }),
    retryChannelInvocation: builder.mutation<
      Schema["ChannelInvocationPage"],
      DiscussionSession & {
        conversationId: string;
        invocationId: string;
        requestKey: string;
      }
    >({
      invalidatesTags: (_result, _error, { conversationId }) => [
        { type: "ChannelAgent", id: conversationId },
        { type: "ChannelAgentActivity", id: conversationId },
      ],
      queryFn: (arg) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["ChannelInvocationPage"]>(
              `${channelPath(arg)}/agent-invocations/${encodeURIComponent(arg.invocationId)}/retry`,
              { requestKey: arg.requestKey },
            ),
          "This invocation could not be retried.",
        ),
    }),
    getMemberPresence: builder.query<
      Schema["MemberPresenceResponse"],
      { companyId: string; userIds: string[]; reportActivity?: boolean }
    >({
      keepUnusedDataFor: 0,
      providesTags: ["MemberPresence"],
      queryFn: ({ companyId, userIds, reportActivity }, { signal }) =>
        executeZenithTeamQuery(async () => {
          const items = new Map<
            string,
            Schema["MemberPresenceResponse"]["items"][number]
          >();
          const loadPresence = async (userIds: string[], active: boolean) => {
            const page = await apiClient.post<Schema["MemberPresenceResponse"]>(
              `${companyPath(companyId)}/member-presence`,
              { userIds, active },
              { signal },
            );
            for (const item of page.items) items.set(item.userId, item);
          };
          const active = !!reportActivity && hasRecentPresenceActivity();
          const ids = [...new Set(userIds)];
          for (let offset = 0; offset < Math.max(1, ids.length); offset += 50) {
            await loadPresence(
              ids.slice(offset, offset + 50),
              offset === 0 && active,
            );
          }
          // RTK Query joins an in-flight read when focus returns. If that
          // read started inactive, report the new activity before publishing
          // its result instead of waiting for the next polling interval.
          if (!active && reportActivity && hasRecentPresenceActivity()) {
            await loadPresence([], true);
          }
          return { items: [...items.values()] };
        }, "Presence could not be loaded."),
    }),
    setMemberAway: builder.mutation<
      Schema["MemberPresenceResponse"],
      { companyId: string; manualAway: boolean }
    >({
      invalidatesTags: ["MemberPresence"],
      queryFn: ({ companyId, manualAway }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["MemberPresenceResponse"]>(
              `${companyPath(companyId)}/member-presence`,
              { manualAway, active: !manualAway },
            ),
          "Status could not be updated.",
        ),
    }),
    getDiscussionHistory: builder.infiniteQuery<
      DiscussionPage,
      DiscussionScope,
      DiscussionPageParam
    >({
      keepUnusedDataFor: 0,
      infiniteQueryOptions: {
        initialPageParam: null,
        maxPages: 5,
        getNextPageParam: (page) => page.newerCursor,
        getPreviousPageParam: (page) => page.olderCursor ?? undefined,
      },
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<DiscussionPage>(
              `${conversationPath(queryArg)}/discussion-messages`,
              { params: { limit: 50, cursor: pageParam ?? undefined }, signal },
            ),
          "Messages could not be loaded.",
        ),
    }),
    getDiscussionThread: builder.infiniteQuery<
      DiscussionPage,
      DiscussionScope,
      DiscussionPageParam
    >({
      keepUnusedDataFor: 0,
      infiniteQueryOptions: {
        initialPageParam: null,
        maxPages: 3,
        getNextPageParam: (page) => page.newerCursor,
        getPreviousPageParam: (page) => page.olderCursor ?? undefined,
      },
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<DiscussionPage>(
              `${conversationPath(queryArg)}/threads/${encodeURIComponent(queryArg.rootId!)}/messages`,
              { params: { limit: 50, cursor: pageParam ?? undefined }, signal },
            ),
          "Messages could not be loaded.",
        ),
    }),
    getDiscussionContext: builder.query<
      Schema["DiscussionContext"],
      DiscussionSession & { conversationId: string; messageId: string }
    >({
      keepUnusedDataFor: 0,
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DiscussionContext"]>(
              `${conversationPath(arg)}/discussion-messages/${encodeURIComponent(arg.messageId)}/context`,
              { signal },
            ),
          "Message context could not be loaded.",
        ),
    }),
    getDiscussionPins: builder.infiniteQuery<
      Schema["DiscussionPins"],
      DiscussionSession & { conversationId: string },
      string | null
    >({
      keepUnusedDataFor: 0,
      infiniteQueryOptions: {
        initialPageParam: null,
        maxPages: 3,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
        getPreviousPageParam: () => undefined,
      },
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DiscussionPins"]>(
              `${conversationPath(queryArg)}/discussion-pins`,
              { params: { limit: 25, cursor: pageParam ?? undefined }, signal },
            ),
          "Pinned messages could not be loaded.",
        ),
    }),
    getDiscussionReactions: builder.infiniteQuery<
      Schema["DiscussionReactions"],
      DiscussionSession & { conversationId: string; messageId: string },
      string | null
    >({
      keepUnusedDataFor: 0,
      infiniteQueryOptions: {
        initialPageParam: null,
        maxPages: 3,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
        getPreviousPageParam: () => undefined,
      },
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DiscussionReactions"]>(
              `${conversationPath(queryArg)}/discussion-messages/${encodeURIComponent(queryArg.messageId)}/reactions`,
              { params: { limit: 50, cursor: pageParam ?? undefined }, signal },
            ),
          "Reactions could not be loaded.",
        ),
    }),
    mutateDiscussionMessage: builder.mutation<
      DiscussionMutation,
      DiscussionSession & {
        conversationId: string;
        messageId: string;
        action: DiscussionMessageAction;
      }
    >({
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(() => {
          const message = `${conversationPath(arg)}/discussion-messages/${encodeURIComponent(arg.messageId)}`;
          const action = arg.action;
          if (action.type === "edit")
            return apiClient.patch<DiscussionMutation>(message, action.body, {
              signal,
            });
          if (action.type === "delete")
            return apiClient.delete<DiscussionMutation>(message, {
              data: action.body,
              signal,
            });
          const path =
            action.type === "pin"
              ? `${conversationPath(arg)}/discussion-pins/${encodeURIComponent(arg.messageId)}`
              : `${message}/reactions/${encodeURIComponent(action.emoji)}`;
          return action.active
            ? apiClient.put<DiscussionMutation>(path, undefined, { signal })
            : apiClient.delete<DiscussionMutation>(path, { signal });
        }, "This message could not be updated."),
    }),
    searchDiscussion: builder.query<
      CompanySearchPage,
      DiscussionSession &
        CompanySearchRequest & {
          conversationId: string;
          conversationRevision: number;
          grantGeneration: number;
        }
    >({
      keepUnusedDataFor: 0,
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            companySearchService.search(
              arg.companyId,
              {
                query: arg.query,
                conversationId: arg.conversationId,
                kinds: ["message"],
                authorId: arg.authorId,
                after: arg.after,
                before: arg.before,
                hasFile: arg.hasFile,
                includeArchived: true,
                limit: 50,
              },
              signal,
            ),
          "Messages could not be searched.",
        ),
    }),
    updateDiscussionReadState: builder.mutation<
      Schema["DiscussionUserState"],
      DiscussionSession & {
        conversationId: string;
        body: Schema["DiscussionReadRequest"];
      }
    >({
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<Schema["DiscussionUserState"]>(
              `${conversationPath(arg)}/discussion-read-state`,
              arg.body,
              { signal },
            ),
          "Read state could not be saved.",
        ),
    }),
    updateDiscussionThreadState: builder.mutation<
      DiscussionThreadState,
      DiscussionSession & {
        conversationId: string;
        rootId: string;
        body: Schema["UpdateDiscussionThreadRequest"];
      }
    >({
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<DiscussionThreadState>(
              `${conversationPath(arg)}/threads/${encodeURIComponent(arg.rootId)}/state`,
              arg.body,
              { signal },
            ),
          "Thread preference could not be saved.",
        ),
    }),
    getDiscussionStates: builder.query<
      Schema["DiscussionStatesResponse"],
      DiscussionSession & { conversationIds: string[] }
    >({
      keepUnusedDataFor: 0,
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["DiscussionStatesResponse"]>(
              `${companyPath(arg.companyId)}/conversation-states/query`,
              { conversationIds: arg.conversationIds },
              { signal },
            ),
          "Conversation access could not be checked.",
        ),
    }),
    listHumanDiscussions: builder.infiniteQuery<
      Schema["DiscussionList"],
      DiscussionSession & { includeHidden: boolean },
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        // Sidebar pages accumulate from the head; evicting it would also
        // prevent refetch from discovering newly created conversations.
        getNextPageParam: (page) => page.nextCursor ?? undefined,
        getPreviousPageParam: () => undefined,
      },
      providesTags: (_result, _error, arg) => channelTags(arg.companyId),
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DiscussionList"]>(
              `${companyPath(queryArg.companyId)}/human-conversations`,
              {
                params: {
                  limit: 50,
                  cursor: pageParam ?? undefined,
                  include_hidden: queryArg.includeHidden,
                },
                signal,
              },
            ),
          "Direct messages could not be loaded.",
        ),
    }),
    listChatChannels: builder.infiniteQuery<
      Schema["DiscussionList"],
      ChannelListRequest,
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
        getPreviousPageParam: () => undefined,
      },
      providesTags: (_result, _error, arg) => channelTags(arg.companyId),
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["DiscussionList"]>(
              `${companyPath(queryArg.companyId)}/channels`,
              {
                params: {
                  limit: 50,
                  cursor: pageParam ?? undefined,
                  q: queryArg.query || undefined,
                  archived: queryArg.archived,
                  include_hidden: queryArg.includeHidden,
                  view: "chat",
                },
                signal,
              },
            ),
          "Channels could not be loaded.",
        ),
    }),
    listManagedChannels: builder.infiniteQuery<
      Schema["ChannelManagementList"],
      ChannelListRequest,
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
        getPreviousPageParam: () => undefined,
      },
      providesTags: (_result, _error, arg) => channelTags(arg.companyId),
      queryFn: ({ queryArg, pageParam }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["ChannelManagementList"]>(
              `${companyPath(queryArg.companyId)}/channels`,
              {
                params: {
                  limit: 50,
                  cursor: pageParam ?? undefined,
                  q: queryArg.query || undefined,
                  archived: queryArg.archived,
                  view: "manage",
                },
                signal,
              },
            ),
          "Channel settings could not be loaded.",
        ),
    }),
    getDiscussionDetail: builder.query<DiscussionDetail, DiscussionAddress>({
      providesTags: (_result, _error, arg) => [
        { type: "DiscussionDetail", id: arg.conversationId },
      ],
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<DiscussionDetail>(
              arg.type === "channel"
                ? channelPath(arg)
                : `${companyPath(arg.companyId)}/human-conversations/${encodeURIComponent(arg.conversationId)}`,
              { signal },
            ),
          "Conversation details could not be loaded.",
        ),
    }),
    getManagedChannel: builder.query<
      ChannelManagement,
      DiscussionSession & { conversationId: string }
    >({
      keepUnusedDataFor: 0,
      providesTags: (_result, _error, arg) => [
        { type: "DiscussionDetail", id: arg.conversationId },
      ],
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<ChannelManagement>(channelPath(arg), {
              params: { view: "manage" },
              signal,
            }),
          "Channel details could not be loaded.",
        ),
    }),
    createHumanDiscussion: builder.mutation<
      Schema["HumanDirectResult"],
      DiscussionSession & { body: Schema["CreateHumanConversationRequest"] }
    >({
      invalidatesTags: (_result, _error, arg) => channelTags(arg.companyId),
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["HumanDirectResult"]>(
              `${companyPath(arg.companyId)}/human-conversations`,
              arg.body,
              { signal },
            ),
          "The direct message could not be opened.",
        ),
    }),
    inviteHumanDiscussionMembers: builder.mutation<
      Schema["HumanDirectDetail"],
      DiscussionSession & {
        conversationId: string;
        body: Schema["InviteHumanConversationRequest"];
      }
    >({
      invalidatesTags: (_result, _error, arg) =>
        channelTags(arg.companyId, arg.conversationId),
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["HumanDirectDetail"]>(
              `${companyPath(arg.companyId)}/human-conversations/${encodeURIComponent(arg.conversationId)}/members`,
              arg.body,
              { signal },
            ),
          "Members could not be invited. Your selections have been kept.",
        ),
    }),
    createChatChannel: builder.mutation<
      Schema["ChannelReceipt"],
      DiscussionSession & { body: CreateChannelRequest }
    >({
      invalidatesTags: (_result, _error, arg) => channelTags(arg.companyId),
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["ChannelReceipt"]>(
              `${companyPath(arg.companyId)}/channels`,
              arg.body,
              { signal },
            ),
          "The channel could not be created. Your selections have been kept.",
        ),
    }),
    changeChatChannel: builder.mutation<
      ChannelManagement | void,
      ChannelChange
    >({
      invalidatesTags: (_result, _error, arg) =>
        channelTags(arg.companyId, arg.conversationId),
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(async () => {
          const url = channelPath(arg);
          const body: Schema["ChannelRevisionRequest"] = {
            expectedRevision: arg.expectedRevision,
          };
          if (arg.action === "update") {
            return apiClient.patch<ChannelManagement>(
              url,
              { ...body, name: arg.name, topic: arg.topic },
              { signal },
            );
          }
          if (arg.action === "addMember" || arg.action === "removeMember") {
            const path = `${url}/members/${encodeURIComponent(arg.memberUserId)}`;
            return arg.action === "addMember"
              ? apiClient.put<ChannelManagement>(path, body, { signal })
              : apiClient.delete<ChannelManagement>(path, {
                  data: body,
                  signal,
                });
          }
          if (arg.action === "addAgent" || arg.action === "removeAgent") {
            const path = `${url}/agents/${encodeURIComponent(arg.agentId)}`;
            return arg.action === "addAgent"
              ? apiClient.put<ChannelManagement>(path, body, { signal })
              : apiClient.delete<ChannelManagement>(path, {
                  data: body,
                  signal,
                });
          }
          if (arg.action === "leave")
            return apiClient.post<void>(`${url}/leave`, body, { signal });
          return apiClient.post<ChannelManagement>(
            `${url}/${arg.action}`,
            body,
            { signal },
          );
        }, "The channel could not be updated. Refresh its details and try again."),
    }),
    sendDiscussionMessage: builder.mutation<
      DiscussionReceipt,
      DiscussionScope & { body: SendDiscussionRequest }
    >({
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<DiscussionReceipt>(
              `${conversationPath(arg)}/discussion-messages`,
              arg.body,
              { signal },
            ),
          "The message could not be sent. Retry uses the same message identity.",
        ),
    }),
    updateDiscussionPersonalState: builder.mutation<
      Schema["DiscussionUserState"],
      DiscussionScope & { body: Schema["UpdateDiscussionStateRequest"] }
    >({
      invalidatesTags: (_result, _error, arg) =>
        channelTags(arg.companyId, arg.conversationId),
      queryFn: (arg, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<Schema["DiscussionUserState"]>(
              `${conversationPath(arg)}/discussion-state`,
              arg.body,
              { signal },
            ),
          "Your conversation preference could not be saved.",
        ),
    }),
  }),
});
