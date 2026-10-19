/**
 * Cache tags of the Company Wiki. Every query provides, and every mutation or
 * realtime frame invalidates, only the tags of the views it changes, so one
 * page edit does not refetch the collections, the graph or unrelated pages.
 */
export type WikiTag = { type: "Wiki"; id: string };

const tag = (id: string): WikiTag => ({ type: "Wiki", id });

export const wikiTags = {
  /** The collection list. */
  collections: (companyId: string) => tag(`${companyId}:collections`),
  /** Page lists, the graph, and anything showing another page's title, status or revision. */
  pages: (companyId: string) => tag(`${companyId}:pages`),
  /** One published page: its detail, links, revisions and "Used by Agents". */
  page: (companyId: string, pageId: string) =>
    tag(`${companyId}:page:${pageId}`),
  /** Every page detail, for changes that move pages without a page frame (a deleted collection unfiles them). */
  pageDetails: (companyId: string) => tag(`${companyId}:page-details`),
  /** One page's working draft. */
  draft: (companyId: string, pageId: string) =>
    tag(`${companyId}:page:${pageId}:draft`),
  /** Every page's "Used by Agents" list, which the Run evidence trail feeds. */
  agentReads: (companyId: string) => tag(`${companyId}:agent-reads`),
  /** One Run's Wiki evidence trail. */
  runReads: (runId: string) => tag(`run:${runId}`),
};
