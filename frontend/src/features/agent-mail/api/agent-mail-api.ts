import { agentMailService } from "@/features/agent-mail/api/agent-mail.service";
import type {
  AgentMailDraft,
  AgentMailDraftGroup,
  AgentMailDraftVersion,
  UpdateAgentMailDraftInput,
} from "@/features/agent-mail/model/agent-mail";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";

type ConversationIdentity = {
  companyId: string;
  conversationId: string;
};

type ConversationArgument = ConversationIdentity & {
  sourceRunIds: string[];
};

type DraftArgument = ConversationIdentity & {
  draftId: string;
};

type GroupArgument = ConversationIdentity & {
  groupId: string;
};

const conversationTag = (companyId: string, conversationId: string) =>
  `${companyId}:conversation:${conversationId}`;
const groupTag = (companyId: string, groupId: string) =>
  `${companyId}:group:${groupId}`;
const draftTag = (companyId: string, draftId: string) =>
  `${companyId}:draft:${draftId}`;

export function agentMailInvalidationTags(
  companyId: string,
  conversationId: string,
  groupId?: string,
  draftId?: string,
) {
  return [
    {
      id: conversationTag(companyId, conversationId),
      type: "AgentMail" as const,
    },
    ...(groupId
      ? [{ id: groupTag(companyId, groupId), type: "AgentMail" as const }]
      : []),
    ...(draftId
      ? [{ id: draftTag(companyId, draftId), type: "AgentMail" as const }]
      : []),
  ];
}

export const agentMailApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listAgentMailDraftGroups: builder.query<
      AgentMailDraftGroup[],
      ConversationArgument
    >({
      providesTags: (groups, _error, { companyId, conversationId }) => [
        ...agentMailInvalidationTags(companyId, conversationId),
        ...(groups?.flatMap((group) => [
          { id: groupTag(companyId, group.id), type: "AgentMail" as const },
          ...group.drafts.map((draft) => ({
            id: draftTag(companyId, draft.id),
            type: "AgentMail" as const,
          })),
        ]) ?? []),
      ],
      queryFn: ({ companyId, conversationId, sourceRunIds }) =>
        executeZenithTeamQuery(
          () =>
            agentMailService.listGroups(
              companyId,
              conversationId,
              sourceRunIds,
            ),
          "Mail drafts could not be loaded.",
          { retry: true },
        ),
    }),
    updateAgentMailDraft: builder.mutation<
      AgentMailDraft,
      DraftArgument & UpdateAgentMailDraftInput
    >({
      invalidatesTags: (_result, _error, argument) =>
        agentMailInvalidationTags(
          argument.companyId,
          argument.conversationId,
          undefined,
          argument.draftId,
        ),
      queryFn: ({ companyId, conversationId, draftId, ...input }) =>
        executeZenithTeamQuery(
          () =>
            agentMailService.updateDraft(
              companyId,
              conversationId,
              draftId,
              input,
            ),
          "Mail draft could not be saved.",
        ),
    }),
    deleteAgentMailDraft: builder.mutation<
      void,
      DraftArgument & { expectedRevision: number; groupId: string }
    >({
      invalidatesTags: (_result, _error, argument) =>
        agentMailInvalidationTags(
          argument.companyId,
          argument.conversationId,
          argument.groupId,
          argument.draftId,
        ),
      queryFn: ({ companyId, conversationId, draftId, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            agentMailService.deleteDraft(
              companyId,
              conversationId,
              draftId,
              expectedRevision,
            ),
          "Mail draft could not be deleted.",
        ),
    }),
    sendAgentMailDraft: builder.mutation<
      AgentMailDraft,
      DraftArgument & { expectedRevision: number }
    >({
      invalidatesTags: (_result, _error, argument) =>
        agentMailInvalidationTags(
          argument.companyId,
          argument.conversationId,
          undefined,
          argument.draftId,
        ),
      queryFn: ({ companyId, conversationId, draftId, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            agentMailService.sendDraft(
              companyId,
              conversationId,
              draftId,
              expectedRevision,
            ),
          "Mail draft could not be queued.",
        ),
    }),
    sendAgentMailDraftGroup: builder.mutation<
      AgentMailDraftGroup,
      GroupArgument & { drafts: AgentMailDraftVersion[] }
    >({
      invalidatesTags: (_result, _error, argument) =>
        agentMailInvalidationTags(
          argument.companyId,
          argument.conversationId,
          argument.groupId,
        ),
      queryFn: ({ companyId, conversationId, drafts, groupId }) =>
        executeZenithTeamQuery(
          () =>
            agentMailService.sendGroup(
              companyId,
              conversationId,
              groupId,
              drafts,
            ),
          "Mail drafts could not be queued.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const {
  useDeleteAgentMailDraftMutation,
  useListAgentMailDraftGroupsQuery,
  useSendAgentMailDraftGroupMutation,
  useSendAgentMailDraftMutation,
  useUpdateAgentMailDraftMutation,
} = agentMailApi;
