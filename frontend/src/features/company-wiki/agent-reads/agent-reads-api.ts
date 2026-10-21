import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import { wikiTags } from "@/shared/api/wiki-tags";

type Schemas = components["schemas"];

export type RunWikiReadList = Schemas["RunWikiReadList"];
export type RunWikiRead = RunWikiReadList["items"][number];
export type WikiPageAgentReadList = Schemas["WikiPageAgentReadList"];
export type WikiPageAgentRead = WikiPageAgentReadList["items"][number];

/** Recent Runs shown under "Used by Agents" on a Wiki page. */
export const WIKI_PAGE_AGENT_READS_LIMIT = 10;

const companyPath = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}`;

export const agentReadsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    // Provides the Run's own evidence tag for the evidence frame, and the page
    // lists tag so page frames refresh `page.currentRevision` and
    // `page.status`; the Run tag lets Run lifecycle events refresh the trail
    // when the Run settles.
    listRunWikiReads: builder.query<
      RunWikiReadList,
      { companyId: string; runId: string }
    >({
      queryFn: ({ companyId, runId }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<RunWikiReadList>(
              `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/wiki-reads`,
            ),
          "Wiki sources could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, runId }) => [
        wikiTags.runReads(runId),
        wikiTags.pages(companyId),
        { type: "Run", id: runId },
      ],
    }),
    listWikiPageAgentReads: builder.query<
      WikiPageAgentReadList,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiPageAgentReadList>(
              `${companyPath(companyId)}/wiki/pages/${encodeURIComponent(pageId)}/agent-reads`,
              { params: { limit: WIKI_PAGE_AGENT_READS_LIMIT } },
            ),
          "Agent reads could not be loaded.",
        ),
      // The page's own frame refreshes `Used by Agents` when its content moves;
      // the Run evidence frame refreshes every page's list when a Run reads.
      providesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.agentReads(companyId),
      ],
    }),
  }),
});

export const { useListRunWikiReadsQuery, useListWikiPageAgentReadsQuery } =
  agentReadsApi;
