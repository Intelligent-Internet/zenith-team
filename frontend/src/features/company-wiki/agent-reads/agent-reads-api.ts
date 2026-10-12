import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

type Schemas = components["schemas"];

export type RunWikiReadList = Schemas["RunWikiReadList"];
export type RunWikiRead = RunWikiReadList["items"][number];
export type WikiPageAgentReadList = Schemas["WikiPageAgentReadList"];
export type WikiPageAgentRead = WikiPageAgentReadList["items"][number];

/** Recent Runs shown under "Used by Agents" on a Wiki page. */
export const WIKI_PAGE_AGENT_READS_LIMIT = 10;

const companyPath = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}`;

export const agentReadsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    // Provides the company-wide Wiki tag so the existing Wiki realtime events
    // refresh `page.currentRevision` and `page.status`; the Run tag lets Run
    // lifecycle events refresh the trail when the Run settles.
    listRunWikiReads: builder.query<
      RunWikiReadList,
      { companyId: string; runId: string }
    >({
      queryFn: ({ companyId, runId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<RunWikiReadList>(
              `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/wiki-reads`,
            ),
          "Wiki sources could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, runId }) => [
        { type: "Wiki", id: companyId },
        { type: "Run", id: runId },
      ],
    }),
    listWikiPageAgentReads: builder.query<
      WikiPageAgentReadList,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<WikiPageAgentReadList>(
              `${companyPath(companyId)}/wiki/pages/${encodeURIComponent(pageId)}/agent-reads`,
              { params: { limit: WIKI_PAGE_AGENT_READS_LIMIT } },
            ),
          "Agent reads could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
      ],
    }),
  }),
});

export const { useListRunWikiReadsQuery, useListWikiPageAgentReadsQuery } =
  agentReadsApi;
