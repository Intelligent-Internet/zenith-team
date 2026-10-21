import { useEffect, useState } from "react";
import { apiClient } from "@/lib/api-client";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import type { CanvasArtifact, CanvasScope } from "./canvas-api";
import { watchCanvasChanges, watchDocumentChanges } from "./canvas-sync";

type Schema = components["schemas"];
export type Page = Schema["Page"];
export type PageChange = Schema["PageChange"];
export type Overview = Schema["ProjectOverview"];
export type Presentation = Schema["ProjectPresentation"];
export type PresentationEntry = Schema["ProjectPresentationEntry"];
export type PageScope = CanvasScope & { pageId: string };
export const projectPath = (scope: CanvasScope) =>
  `/companies/${encodeURIComponent(scope.companyId)}/projects/${encodeURIComponent(scope.projectId)}`;
export const pagePath = (scope: PageScope) =>
  `${projectPath(scope)}/pages/${encodeURIComponent(scope.pageId)}`;
export const presentationPath = (scope: CanvasScope, id: string) =>
  `${projectPath(scope)}/presentations/${encodeURIComponent(id)}`;
const tags = (scope: CanvasScope) => [
  {
    type: "CanvasHistory" as const,
    id: `${scope.companyId}:${scope.projectId}`,
  },
];

const suggestionDetailTag = (scope: PageScope) => ({
  type: "CanvasHistory" as const,
  id: `${scope.companyId}:${scope.projectId}:page:${scope.pageId}:suggestions`,
});

export const pageApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    projectOverview: builder.query<Overview, CanvasScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () => apiClient.get<Overview>(`${projectPath(scope)}/overview`),
          "The Project overview could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(scope, { cacheEntryRemoved, dispatch }) {
        const watcher = watchCanvasChanges(scope, {
          refresh: async () => {
            await dispatch(
              pageApi.endpoints.projectOverview.initiate(scope, {
                subscribe: false,
                forceRefetch: true,
              }),
            ).unwrap();
          },
        });
        watcher.start();
        await cacheEntryRemoved;
        watcher.dispose();
      },
    }),
    updateOverview: builder.mutation<
      Overview,
      CanvasScope & { input: Schema["OverviewInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Overview>(`${projectPath(scope)}/overview`, input),
          "The overview could not be updated.",
        ),
      invalidatesTags: (_r, _e, scope) => [
        ...tags(scope),
        { type: "Canvas", id: `${scope.companyId}:${scope.projectId}` },
      ],
    }),
    projectPages: builder.query<
      CanvasArtifact[],
      CanvasScope & { taskId?: string }
    >({
      queryFn: ({ taskId, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<CanvasArtifact[]>(`${projectPath(scope)}/pages`, {
              params: { taskId },
            }),
          "The Project pages could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(scope, { cacheEntryRemoved, dispatch }) {
        const watcher = watchCanvasChanges(scope, {
          refresh: async () => {
            await dispatch(
              pageApi.endpoints.projectPages.initiate(scope, {
                subscribe: false,
                forceRefetch: true,
              }),
            ).unwrap();
          },
        });
        watcher.start();
        await cacheEntryRemoved;
        watcher.dispose();
      },
    }),
    createProjectPage: builder.mutation<
      Schema["PageCreateResult"],
      CanvasScope & { input: Schema["PageCreateInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["PageCreateResult"]>(
              `${projectPath(scope)}/pages`,
              input,
            ),
          "The Page could not be created.",
        ),
      invalidatesTags: (_r, _e, scope) => [
        ...tags(scope),
        { type: "Canvas", id: `${scope.companyId}:${scope.projectId}` },
      ],
    }),
    projectPage: builder.query<Page, PageScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () => apiClient.get<Page>(pagePath(scope)),
          "This Page is unavailable or your access has changed.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(
        scope,
        { cacheEntryRemoved, getCacheEntry, dispatch },
      ) {
        const refresh = async () => {
          await dispatch(
            pageApi.endpoints.projectPage.initiate(scope, {
              subscribe: false,
              forceRefetch: true,
            }),
          ).unwrap();
        };
        const canvas = watchCanvasChanges(scope, {
          refresh,
        });
        const document = watchDocumentChanges(scope, {
          getPageId: () => scope.pageId,
          getSequence: () => getCacheEntry().data?.sequence ?? 0,
          refresh,
        });
        canvas.start();
        await cacheEntryRemoved;
        canvas.dispose();
        document();
      },
    }),
    pageThreads: builder.query<Schema["CanvasThreadList"], PageScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["CanvasThreadList"]>(
              `${pagePath(scope)}/threads`,
            ),
          "Comments could not be loaded.",
        ),
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(scope, { cacheEntryRemoved, dispatch }) {
        const refresh = async () => {
          await dispatch(
            pageApi.endpoints.pageThreads.initiate(scope, {
              subscribe: false,
              forceRefetch: true,
            }),
          ).unwrap();
        };
        const watcher = watchCanvasChanges(scope, { refresh });
        const stopDocument = watchDocumentChanges(scope, {
          getPageId: () => scope.pageId,
          getSequence: () => -1,
          refresh,
        });
        watcher.start();
        await cacheEntryRemoved;
        watcher.dispose();
        stopDocument();
      },
    }),
    updatePageMetadata: builder.mutation<
      Page,
      PageScope & { input: Schema["PageMetadataInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () => apiClient.patch<Page>(pagePath(scope), input),
          "The Page settings could not be saved.",
        ),
      invalidatesTags: (_r, _e, scope) => [
        ...tags(scope),
        { type: "Canvas", id: `${scope.companyId}:${scope.projectId}` },
      ],
    }),
    previewPresentation: builder.mutation<Presentation, PageScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Presentation>(
              `${pagePath(scope)}/presentation-preview`,
            ),
          "The presentation could not be prepared. Save your draft and try again.",
        ),
    }),
    savePresentation: builder.mutation<
      Presentation,
      PageScope & { input: Schema["PresentationInput"] }
    >({
      queryFn: ({ input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Presentation>(
              `${pagePath(scope)}/presentations`,
              input,
            ),
          "The page or its sources changed. Refresh the preview before saving.",
        ),
      invalidatesTags: (_r, _e, scope) => [
        ...tags(scope),
        { type: "Canvas", id: `${scope.companyId}:${scope.projectId}` },
      ],
    }),
    pagePresentations: builder.query<PresentationEntry[], PageScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<PresentationEntry[]>(
              `${pagePath(scope)}/presentations`,
            ),
          "Saved presentations could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
    }),
    projectPresentations: builder.query<PresentationEntry[], CanvasScope>({
      queryFn: (scope) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<PresentationEntry[]>(
              `${projectPath(scope)}/presentations`,
            ),
          "Saved presentations could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
    }),
    projectPresentation: builder.query<
      Presentation,
      CanvasScope & { id: string }
    >({
      queryFn: ({ id, ...scope }) =>
        executeZenithTeamQuery(
          () => apiClient.get<Presentation>(presentationPath(scope, id)),
          "This presentation is unavailable or your access has changed.",
        ),
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
    }),
    pageChanges: builder.query<
      Schema["PageChangeFeed"],
      PageScope & { cursor?: string }
    >({
      queryFn: ({ cursor, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["PageChangeFeed"]>(
              `${pagePath(scope)}/changes`,
              { params: { cursor } },
            ),
          "Page updates could not be loaded.",
        ),
      serializeQueryArgs: ({
        queryArgs: { companyId, projectId, pageId },
      }) => ({ companyId, projectId, pageId }),
      merge: (current, next, { arg }) => {
        current.pending = next.pending;
        current.history = arg.cursor
          ? [
              ...new Map(
                [...current.history, ...next.history].map((change) => [
                  change.id,
                  change,
                ]),
              ).values(),
            ]
          : next.history;
        current.nextCursor = next.nextCursor;
      },
      forceRefetch: ({ currentArg, previousArg }) =>
        currentArg?.cursor !== previousArg?.cursor,
      providesTags: (_r, _e, scope) => tags(scope),
      keepUnusedDataFor: 0,
      async onCacheEntryAdded(
        { companyId, projectId, pageId },
        { cacheEntryRemoved, dispatch },
      ) {
        const scope = { companyId, projectId, pageId };
        const refresh = async () => {
          await dispatch(
            pageApi.endpoints.pageChanges.initiate(scope, {
              subscribe: false,
              forceRefetch: true,
            }),
          ).unwrap();
          // Every comparison depends on the other proposals and the current document.
          dispatch(
            zenithTeamApi.util.invalidateTags([suggestionDetailTag(scope)]),
          );
        };
        const watcher = watchCanvasChanges(scope, {
          refresh,
        });
        const stopDocument = watchDocumentChanges(scope, {
          getPageId: () => pageId,
          getSequence: () => -1,
          refresh,
        });
        watcher.start();
        await cacheEntryRemoved;
        watcher.dispose();
        stopDocument();
      },
    }),
    pageChangeDetail: builder.query<
      Schema["PageChangeDetail"],
      PageScope & { id: string }
    >({
      queryFn: ({ id, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<Schema["PageChangeDetail"]>(
              `${pagePath(scope)}/changes/${encodeURIComponent(id)}`,
            ),
          "The contribution comparison could not be loaded.",
        ),
      providesTags: (_r, _e, scope) => [
        ...tags(scope),
        suggestionDetailTag(scope),
      ],
      keepUnusedDataFor: 0,
    }),
    reviewPageChange: builder.mutation<
      Schema["PageEditResult"],
      PageScope & { id: string; input: Schema["PageChangeReviewInput"] }
    >({
      queryFn: ({ id, input, ...scope }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<Schema["PageEditResult"]>(
              `${pagePath(scope)}/changes/${encodeURIComponent(id)}`,
              input,
            ),
          "These blocks changed. Review the current page before applying or undoing this contribution.",
        ),
      invalidatesTags: (_r, _e, scope) => tags(scope),
    }),
  }),
});
export const {
  useProjectOverviewQuery,
  useUpdateOverviewMutation,
  useProjectPagesQuery,
  useCreateProjectPageMutation,
  useProjectPageQuery,
  useUpdatePageMetadataMutation,
  usePreviewPresentationMutation,
  useSavePresentationMutation,
  usePagePresentationsQuery,
  useProjectPresentationsQuery,
  useProjectPresentationQuery,
  usePageChangesQuery,
  usePageChangeDetailQuery,
  useReviewPageChangeMutation,
} = pageApi;

// All document surfaces share the same active-run refresh policy.
export function usePageThreadsQuery(
  scope: Parameters<typeof pageApi.usePageThreadsQuery>[0],
  options?: { skip?: boolean; refetchOnMountOrArgChange?: boolean | number },
) {
  const [running, setRunning] = useState(false);
  const query = pageApi.usePageThreadsQuery(scope, {
    ...options,
    pollingInterval: running ? 2000 : 0,
    skipPollingIfUnfocused: true,
  });
  useEffect(() => {
    setRunning(
      query.currentData?.items.some((thread) =>
        thread.agentRuns?.some(
          (run) => !["completed", "failed", "cancelled"].includes(run.status),
        ),
      ) ?? false,
    );
  }, [query.currentData]);
  return query;
}
