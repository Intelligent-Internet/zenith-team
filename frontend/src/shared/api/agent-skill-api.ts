import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "./zenith-team-api";
import type { components, operations } from "./generated/api.generated";

export type MessageSkillScope = { companyId: string } & NonNullable<
  operations["listMessageSkills"]["parameters"]["query"]
>;

type Schemas = components["schemas"];
export type AgentSkill = Schemas["AgentSkill"];
export type AgentSkillPreview = Schemas["AgentSkillPreview"];
type Scope = { companyId: string; agentId: string };
const base = ({ companyId, agentId }: Scope) =>
  `/companies/${encodeURIComponent(companyId)}/agents/${encodeURIComponent(agentId)}`;
const tags = ({ companyId }: Scope) => [
  { type: "AgentSkill" as const, id: companyId },
];
const mutationTags = (scope: Scope) => [
  ...tags(scope),
  { type: "Agent" as const, id: scope.companyId },
];

export const agentSkillApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    messageSkills: builder.query<
      Schemas["MessageSkillCollection"],
      MessageSkillScope
    >({
      queryFn: ({ companyId, ...params }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schemas["MessageSkillCollection"]>(
              `/companies/${encodeURIComponent(companyId)}/message-skills`,
              { params },
            ),
          "Skills could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, conversation_id }) => [
        { type: "AgentSkill", id: companyId },
        { type: "Agent", id: companyId },
        ...(conversation_id
          ? [
              { type: "DiscussionDetail" as const, id: conversation_id },
              { type: "ChannelAgent" as const, id: conversation_id },
            ]
          : []),
      ],
    }),
    agentSkills: builder.query<Schemas["AgentSkillCollection"], Scope>({
      merge: (current, incoming) => {
        current.items = incoming.items.map((skill) => {
          const cached = current.items.find((item) => item.id === skill.id);
          return cached && cached.revision > skill.revision ? cached : skill;
        });
      },
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schemas["AgentSkillCollection"]>(
              `${base(scope)}/skills`,
            ),
          "Custom skills could not be loaded.",
        ),
      providesTags: (_result, _error, scope) => tags(scope),
    }),
    previewSkillUpload: builder.mutation<
      AgentSkillPreview,
      Scope & { file: File }
    >({
      queryFn: ({ file, ...scope }) => {
        const body = new FormData();
        body.append("file", file);
        return executeZenithTeamQuery(
          () =>
            apiClient.post<AgentSkillPreview>(
              `${base(scope)}/skill-imports/upload`,
              body,
              {
                timeout: 120_000,
              },
            ),
          "The skill package could not be read.",
        );
      },
    }),
    previewSkillGitHub: builder.mutation<
      AgentSkillPreview,
      Scope & { input: Schemas["AgentSkillGitHubRequest"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<AgentSkillPreview>(
              `${base(scope)}/skill-imports/github`,
              input,
              {
                timeout: 120_000,
              },
            ),
          "The public GitHub source could not be read.",
        ),
    }),
    installAgentSkills: builder.mutation<
      Schemas["AgentSkillCollection"],
      Scope & { input: Schemas["AgentSkillInstallRequest"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schemas["AgentSkillCollection"]>(
              `${base(scope)}/skills`,
              input,
            ),
          "Skills could not be installed.",
        ),
      invalidatesTags: (_result, _error, scope) => mutationTags(scope),
    }),
    updateAgentSkill: builder.mutation<
      AgentSkill,
      Scope & { skillId: string; input: Schemas["AgentSkillUpdateRequest"] }
    >({
      async onQueryStarted(
        { companyId, agentId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            agentSkillApi.util.updateQueryData(
              "agentSkills",
              { companyId, agentId },
              (catalog) => {
                const index = catalog.items.findIndex(
                  (skill) => skill.id === data.id,
                );
                if (
                  index >= 0 &&
                  catalog.items[index].revision <= data.revision
                ) {
                  catalog.items[index] = data;
                }
              },
            ),
          );
        } catch {
          // Invalidation refreshes the catalog after a conflict or failure.
        }
      },
      queryFn: ({ skillId, input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<AgentSkill>(
              `${base(scope)}/skills/${encodeURIComponent(skillId)}`,
              input,
              input.changes?.length || input.deletePaths?.length
                ? { timeout: 120_000 }
                : undefined,
            ),
          "The skill could not be updated.",
        ),
      invalidatesTags: (_result, _error, scope) => mutationTags(scope),
    }),
    removeAgentSkill: builder.mutation<
      void,
      Scope & { skillId: string; expectedRevision: number }
    >({
      queryFn: ({ skillId, expectedRevision, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.delete<void>(
              `${base(scope)}/skills/${encodeURIComponent(skillId)}`,
              { data: { expectedRevision } },
            ),
          "The skill could not be removed.",
        ),
      invalidatesTags: (_result, _error, scope) => mutationTags(scope),
    }),
    agentSkillReference: builder.query<
      Schemas["AgentSkillReference"],
      Scope & { skillId: string; path: string; versionId?: string }
    >({
      queryFn: ({ skillId, path, versionId, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schemas["AgentSkillReference"]>(
              `${base(scope)}/skills/${encodeURIComponent(skillId)}/reference`,
              { params: { path, versionId } },
            ),
          "The skill file could not be loaded.",
        ),
      providesTags: (_result, _error, scope) => tags(scope),
    }),
  }),
});
export const {
  useMessageSkillsQuery,
  useAgentSkillsQuery,
  usePreviewSkillUploadMutation,
  usePreviewSkillGitHubMutation,
  useInstallAgentSkillsMutation,
  useUpdateAgentSkillMutation,
  useRemoveAgentSkillMutation,
  useAgentSkillReferenceQuery,
} = agentSkillApi;
