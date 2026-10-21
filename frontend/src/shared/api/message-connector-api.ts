import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "./zenith-team-api";
import type { components, operations } from "./generated/api.generated";

export type MessageConnectorScope = { companyId: string } & NonNullable<
  operations["listMessageConnectors"]["parameters"]["query"]
>;
export const messageConnectorApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    messageConnectors: builder.query<
      components["schemas"]["MessageConnectorCollection"],
      MessageConnectorScope
    >({
      queryFn: ({ companyId, ...params }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["MessageConnectorCollection"]>(
              `/companies/${encodeURIComponent(companyId)}/message-connectors`,
              { params },
            ),
          "Connected plugins could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, conversation_id }) => [
        { type: "Connector", id: companyId },
        { type: "Plugin", id: companyId },
        { type: "Runtime", id: companyId },
        { type: "Agent", id: companyId },
        ...(conversation_id
          ? [
              { type: "DiscussionDetail" as const, id: conversation_id },
              { type: "ChannelAgent" as const, id: conversation_id },
            ]
          : []),
      ],
    }),
  }),
});
export const { useMessageConnectorsQuery } = messageConnectorApi;
