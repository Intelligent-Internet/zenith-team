import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type {
  components,
  operations,
} from "@/shared/api/generated/api.generated";

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

export const wikiApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWikiCollections: builder.query<WikiCollectionList, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<WikiCollectionList>(`${root(companyId)}/collections`),
          "Collections could not be loaded.",
        ),
      providesTags: (_result, _error, companyId) => [
        { type: "Wiki", id: companyId },
      ],
    }),
    listWikiPages: builder.query<
      WikiPageList,
      { companyId: string } & Omit<ListPagesQuery, "limit">
    >({
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<WikiPageList>(`${root(companyId)}/pages`, {
              params: { ...query, limit: WIKI_PAGE_LIMIT },
            }),
          "Pages could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId }) => [
        { type: "Wiki", id: companyId },
      ],
    }),
    getWikiPage: builder.query<
      WikiPageDetail,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeBoardlyQuery(
          () => apiClient.get<WikiPageDetail>(pagePath(companyId, pageId)),
          "Page could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
      ],
    }),
    getWikiPageLinks: builder.query<
      WikiPageLinks,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<WikiPageLinks>(
              `${pagePath(companyId, pageId)}/links`,
            ),
          "Page links could not be loaded.",
        ),
      providesTags: (_result, _error, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
      ],
    }),
    getWikiGraph: builder.query<WikiGraphData, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => apiClient.get<WikiGraphData>(`${root(companyId)}/graph`),
          "Graph could not be loaded.",
        ),
      providesTags: (_result, _error, companyId) => [
        { type: "Wiki", id: companyId },
      ],
    }),
    listWikiRevisions: builder.query<
      WikiRevisionList,
      { companyId: string; pageId: string } & ListRevisionsQuery
    >({
      queryFn: ({ companyId, pageId, ...query }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<WikiRevisionList>(
              `${pagePath(companyId, pageId)}/revisions`,
              { params: query },
            ),
          "History could not be loaded.",
        ),
      providesTags: (_result, _error, { pageId }) => [
        { type: "Wiki", id: pageId },
      ],
    }),
    createWikiPage: builder.mutation<
      WikiPageDetail,
      { companyId: string; request: PageCreate }
    >({
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<WikiPageDetail>(`${root(companyId)}/pages`, request),
          "Page could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { type: "Wiki", id: companyId },
      ],
    }),
    getWikiDocument: builder.query<
      WikiDocument,
      { companyId: string; pageId: string }
    >({
      queryFn: ({ companyId, pageId }) =>
        executeBoardlyQuery(
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
      providesTags: (_r, _e, { pageId }) => [
        { type: "Wiki", id: `${pageId}:draft` },
      ],
      keepUnusedDataFor: 0,
    }),
    updateWikiDraftMetadata: builder.mutation<
      WikiDocument,
      { companyId: string; pageId: string; request: WikiDraftMetadata }
    >({
      queryFn: ({ companyId, pageId, request }) =>
        executeBoardlyQuery(
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
      invalidatesTags: (_r, _e, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: `${pageId}:draft` },
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
        executeBoardlyQuery(
          () =>
            apiClient.post<WikiPageDetail>(
              `${pagePath(companyId, pageId)}/publish`,
              request,
            ),
          "The draft could not be published. It may have changed; review it again.",
        ),
      invalidatesTags: (_r, _e, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
        { type: "Wiki", id: `${pageId}:draft` },
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
        executeBoardlyQuery(
          () =>
            apiClient.post<WikiPageDetail>(
              `${pagePath(companyId, pageId)}/${archive ? "archive" : "restore"}`,
              { expectedRevision },
            ),
          "Page status could not be changed.",
        ),
      invalidatesTags: (_result, _error, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
      ],
    }),
    setWikiFavorite: builder.mutation<
      void,
      { companyId: string; pageId: string; favorite: boolean }
    >({
      queryFn: ({ companyId, pageId, favorite }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<void>(`${pagePath(companyId, pageId)}/favorite`, {
              favorite,
            }),
          "Favorite could not be changed.",
        ),
      invalidatesTags: (_result, _error, { companyId, pageId }) => [
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
      ],
    }),
    createWikiCollection: builder.mutation<
      WikiCollection,
      { companyId: string; name: string }
    >({
      queryFn: ({ companyId, name }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<WikiCollection>(`${root(companyId)}/collections`, {
              name,
            }),
          "Collection could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { type: "Wiki", id: companyId },
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
        executeBoardlyQuery(
          () =>
            apiClient.patch<WikiCollection>(
              `${root(companyId)}/collections/${encodeURIComponent(collectionId)}`,
              request,
            ),
          "Collection could not be renamed.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { type: "Wiki", id: companyId },
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
        executeBoardlyQuery(
          () =>
            apiClient.delete<void>(
              `${root(companyId)}/collections/${encodeURIComponent(collectionId)}`,
              { data: request },
            ),
          "Collection could not be deleted.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { type: "Wiki", id: companyId },
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
        executeBoardlyQuery(
          () =>
            apiClient.post<WikiDocument>(
              `${pagePath(companyId, pageId)}/restore-revision`,
              { revisionId, expectedRevision, expectedSequence },
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
        { type: "Wiki", id: companyId },
        { type: "Wiki", id: pageId },
        { type: "Wiki", id: `${pageId}:draft` },
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
