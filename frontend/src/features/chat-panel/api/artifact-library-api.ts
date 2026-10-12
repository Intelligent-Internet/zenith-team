import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
type Result = components["schemas"]["ArtifactRunResult"];
export type LibraryArtifact = Result["artifact"] &
  NonNullable<Result["library"]> &
  Pick<Result, "runId">;
type Page = { items: LibraryArtifact[]; nextCursor?: string | null };
function libraryPage(page: {
  items: Result[];
  nextCursor?: string | null;
}): Page {
  return {
    ...page,
    items: page.items.flatMap(({ artifact, library, runId }) =>
      library ? [{ ...artifact, ...library, runId }] : [],
    ),
  };
}
export type LibraryQuery = {
  companyId: string;
  q?: string;
  ownership?: "owned" | "shared";
  source?: "channel" | "direct" | "task" | "schedule";
};
export const artifactLibraryApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getArtifactSelection: builder.query<
      Page,
      { companyId: string; ids: string[] }
    >({
      providesTags: [{ type: "Artifact" }],
      queryFn: ({ companyId, ids }, { signal }) =>
        executeBoardlyQuery<Page>(
          () =>
            apiClient
              .get<components["schemas"]["ResolvedArtifacts"]>(
                `/companies/${encodeURIComponent(companyId)}/artifacts/resolve?${new URLSearchParams(ids.map((id) => ["id", id])).toString()}`,
                { signal },
              )
              .then(libraryPage),
          "Selected artifacts could not be checked.",
        ),
    }),

    getArtifactLibrary: builder.infiniteQuery<
      Page,
      LibraryQuery,
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
      },
      providesTags: [{ type: "Artifact" }],
      queryFn: (
        { queryArg: { companyId, q, ...filters }, pageParam },
        { signal },
      ) =>
        executeBoardlyQuery<Page>(
          () =>
            apiClient
              .get<components["schemas"]["ConversationArtifactPage"]>(
                `/companies/${encodeURIComponent(companyId)}/artifacts`,
                {
                  params: {
                    ...filters,
                    search: q,
                    limit: 20,
                    cursor: pageParam ?? undefined,
                  },
                  signal,
                },
              )
              .then(libraryPage),
          "Artifacts could not be loaded.",
        ),
    }),
  }),
});
export const {
  useGetArtifactLibraryInfiniteQuery,
  useLazyGetArtifactSelectionQuery,
} = artifactLibraryApi;
export function artifactSourceLabel(source: LibraryArtifact["source"]) {
  const labels = {
    channel: "Channel",
    direct: "Direct chat",
    task: "Task",
    schedule: "Schedule",
    restricted: "Private source",
  };
  return [
    labels[source.type],
    source.type === "channel" && source.title
      ? `#${source.title}`
      : source.title,
    source.detail,
  ]
    .filter(Boolean)
    .join(" · ");
}
