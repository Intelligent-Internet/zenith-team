import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type {
  components,
  operations,
} from "@/shared/api/generated/api.generated";
import { wikiTags } from "@/shared/api/wiki-tags";

import {
  notifyDocumentMutation,
  watchDocumentChanges,
} from "../project-canvas/api/canvas-sync";

type Schemas = components["schemas"];

export type WikiPageDetail = Schemas["WikiPage"];
export type WikiPageList = Schemas["WikiPageList"];
export type WikiPageSummary = Schemas["WikiPageSummary"];
export type WikiCollection = Schemas["WikiCollection"];
export type WikiCollectionList = Schemas["WikiCollectionList"];
export type WikiGraphData = Schemas["WikiGraph"];
export type WikiPageLinks = Schemas["WikiPageLinks"];
export type WikiRevision = Schemas["WikiRevision"];
export type WikiRevisionList = Schemas["WikiRevisionList"];

type PageCreate = Schemas["WikiPageCreate"];
export type WikiDocument = Schemas["WikiDocument"];
export type WikiDraftMetadata = Schemas["WikiDraftMetadata"];

type ListPagesQuery = NonNullable<
  operations["listWikiPages"]["parameters"]["query"]
>;
type ListRevisionsQuery = NonNullable<
  operations["listWikiRevisions"]["parameters"]["query"]
>;

/** Page size the Library and link picker request and paginate by. */
export const WIKI_PAGE_LIMIT = 100;
/** Page size the server uses for revision history. */
export const WIKI_REVISION_LIMIT = 50;

const root = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}/wiki`;
const pagePath = (companyId: string, pageId: string) =>
  `${root(companyId)}/pages/${encodeURIComponent(pageId)}`;

export const wikiApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listWikiCollections: builder.query<WikiCollectionList, string>({
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiCollectionList>(`${root(companyId)}/collections`),
          "Collections could not be loaded.",
        ),
      providesTags: (_result, _error, companyId) => [
        wikiTags.collections(companyId),
      ],
    }),
    listWikiPages: builder.query<
      WikiPageList,
      { companyId: string } & Omit<ListPagesQuery, "limit">
    >({
      queryFn: ({ companyId, ...query }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiPageList>(`${root(companyId)}/pages`, {
              params: { ...query, limit: WIKI_PAGE_LIMIT },
            }),
          "Pages could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId }) => [
        wikiTags.pages(companyId),
      ],
    }),
    getWikiPage: builder.query<
      WikiPageDetail,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiPageDetail>(pagePath(companyId, pageId), {
              signal,
            }),
          "Page could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.pageDetails(companyId),
      ],
    }),
    getWikiPageLinks: builder.query<
      WikiPageLinks,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiPageLinks>(
              `${pagePath(companyId, pageId)}/links`,
            ),
          "Page links could not be loaded.",
        ),
      // Link rows carry the titles of the pages on the other end.
      providesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.pages(companyId),
      ],
    }),
    getWikiGraph: builder.query<WikiGraphData, string>({
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => apiClient.get<WikiGraphData>(`${root(companyId)}/graph`),
          "Graph could not be loaded.",
        ),
      providesTags: (_result, _error, companyId) => [wikiTags.pages(companyId)],
    }),
    listWikiRevisions: builder.query<
      WikiRevisionList,
      { companyId: string; pageId: string } & ListRevisionsQuery
    >({
      queryFn: ({ companyId, pageId, ...query }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiRevisionList>(
              `${pagePath(companyId, pageId)}/revisions`,
              { params: query },
            ),
          "History could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
      ],
    }),
    createWikiPage: builder.mutation<
      WikiPageDetail,
      { companyId: string; request: PageCreate }
    >({
      queryFn: ({ companyId, request }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<WikiPageDetail>(`${root(companyId)}/pages`, request),
          "Page could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        wikiTags.pages(companyId),
      ],
    }),
    getWikiDocument: builder.query<
      WikiDocument,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<WikiDocument>(
              `${pagePath(companyId, pageId)}/document`,
            ),
          "The working draft could not be loaded.",
        ),
      async onCacheEntryAdded(
        args,
        { dispatch, getCacheEntry, cacheEntryRemoved },
      ) {
        const dispose = watchDocumentChanges(
          { companyId: args.companyId, wikiPageId: args.pageId },
          {
            getPageId: () => args.pageId,
            getSequence: () => getCacheEntry().data?.sequence ?? -1,
            refresh: async () => {
              await dispatch(
                wikiApi.endpoints.getWikiDocument.initiate(args, {
                  subscribe: false,
                  forceRefetch: true,
                }),
              ).unwrap();
            },
          },
        );
        await cacheEntryRemoved;
        dispose();
      },
      providesTags: (_r, _e, { companyId, pageId }) => [
        wikiTags.draft(companyId, pageId),
      ],
      keepUnusedDataFor: 0,
    }),
    updateWikiDraftMetadata: builder.mutation<
      WikiDocument,
      { companyId: string; pageId: string; request: WikiDraftMetadata }
    >({
      queryFn: ({ companyId, pageId, request }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<WikiDocument>(
              `${pagePath(companyId, pageId)}/document/metadata`,
              request,
            ),
          "Draft details could not be saved.",
        ),
      async onQueryStarted(
        { companyId, pageId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            wikiApi.util.upsertQueryData(
              "getWikiDocument",
              { companyId, pageId },
              data,
            ),
          );
          notifyDocumentMutation(
            { companyId, wikiPageId: pageId },
            pageId,
            data.sequence,
          );
        } catch {
          /* The mutation exposes the error. */
        }
      },
      // A published page shows its draft details only after the next publish,
      // but an unpublished page shows its draft's title, summary and collection
      // in the page lists and its own header, and no realtime frame follows.
      invalidatesTags: (_r, _e, { companyId, pageId }) => [
        wikiTags.draft(companyId, pageId),
        wikiTags.pages(companyId),
        wikiTags.page(companyId, pageId),
      ],
    }),
    publishWikiPage: builder.mutation<
      WikiPageDetail,
      {
        companyId: string;
        pageId: string;
        request: Schemas["WikiPublishRequest"];
      }
    >({
      queryFn: ({ companyId, pageId, request }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<WikiPageDetail>(
              `${pagePath(companyId, pageId)}/publish`,
              request,
            ),
          "The draft could not be published. It may have changed; review it again.",
        ),
      invalidatesTags: (_r, _e, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.pages(companyId),
        wikiTags.draft(companyId, pageId),
        { type: "Canvas" },
      ],
    }),
    setWikiArchive: builder.mutation<
      WikiPageDetail,
      {
        companyId: string;
        pageId: string;
        expectedRevision: number;
        archive: boolean;
      }
    >({
      queryFn: ({ companyId, pageId, expectedRevision, archive }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<WikiPageDetail>(
              `${pagePath(companyId, pageId)}/${archive ? "archive" : "restore"}`,
              { expectedRevision },
            ),
          "Page status could not be changed.",
        ),
      invalidatesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.pages(companyId),
      ],
    }),
    setWikiFavorite: builder.mutation<
      void,
      { companyId: string; pageId: string; favorite: boolean }
    >({
      queryFn: ({ companyId, pageId, favorite }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<void>(`${pagePath(companyId, pageId)}/favorite`, {
              favorite,
            }),
          "Favorite could not be changed.",
        ),
      invalidatesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.pages(companyId),
      ],
    }),
    createWikiCollection: builder.mutation<
      WikiCollection,
      { companyId: string; name: string }
    >({
      queryFn: ({ companyId, name }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<WikiCollection>(`${root(companyId)}/collections`, {
              name,
            }),
          "Collection could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        wikiTags.collections(companyId),
      ],
    }),
    renameWikiCollection: builder.mutation<
      WikiCollection,
      {
        companyId: string;
        collectionId: string;
        request: Schemas["WikiCollectionRename"];
      }
    >({
      queryFn: ({ companyId, collectionId, request }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<WikiCollection>(
              `${root(companyId)}/collections/${encodeURIComponent(collectionId)}`,
              request,
            ),
          "Collection could not be renamed.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        wikiTags.collections(companyId),
      ],
    }),
    deleteWikiCollection: builder.mutation<
      void,
      {
        companyId: string;
        collectionId: string;
        request: Schemas["WikiRevisionRequest"];
      }
    >({
      queryFn: ({ companyId, collectionId, request }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.delete<void>(
              `${root(companyId)}/collections/${encodeURIComponent(collectionId)}`,
              { data: request },
            ),
          "Collection could not be deleted.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        wikiTags.collections(companyId),
        // The server moves the collection's pages to Unfiled.
        wikiTags.pages(companyId),
        wikiTags.pageDetails(companyId),
      ],
    }),
    restoreWikiRevision: builder.mutation<
      WikiDocument,
      {
        companyId: string;
        pageId: string;
        revisionId: string;
        expectedRevision: number;
        expectedSequence: number;
      }
    >({
      queryFn: ({
        companyId,
        pageId,
        revisionId,
        expectedRevision,
        expectedSequence,
      }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<WikiDocument>(
              `${pagePath(companyId, pageId)}/restore-revision`,
              {
                revisionId,
                expectedRevision,
                expectedSequence,
                schemaVersion: 2,
              },
            ),
          "Revision could not be restored.",
        ),
      async onQueryStarted(
        { companyId, pageId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            wikiApi.util.upsertQueryData(
              "getWikiDocument",
              { companyId, pageId },
              data,
            ),
          );
          notifyDocumentMutation(
            { companyId, wikiPageId: pageId },
            pageId,
            data.sequence,
          );
        } catch {
          /* The mutation exposes the error. */
        }
      },
      invalidatesTags: (_result, _error, { companyId, pageId }) => [
        wikiTags.page(companyId, pageId),
        wikiTags.draft(companyId, pageId),
      ],
    }),
  }),
});

export const {
  useListWikiCollectionsQuery,
  useListWikiPagesQuery,
  useGetWikiPageQuery,
  useLazyGetWikiPageQuery,
  useGetWikiPageLinksQuery,
  useGetWikiGraphQuery,
  useListWikiRevisionsQuery,
  useCreateWikiPageMutation,
  useGetWikiDocumentQuery,
  useLazyGetWikiDocumentQuery,
  useUpdateWikiDraftMetadataMutation,
  usePublishWikiPageMutation,
  useSetWikiArchiveMutation,
  useSetWikiFavoriteMutation,
  useCreateWikiCollectionMutation,
  useRenameWikiCollectionMutation,
  useDeleteWikiCollectionMutation,
  useRestoreWikiRevisionMutation,
} = wikiApi;
