import {
  listDesktopWorkspaceAssets,
  type DesktopWorkspaceAssetList,
  type DesktopWorkspaceAssetListRequest,
} from "@/app/desktop/desktop-environment.service";
import { boardlyApi } from "@/shared/api/boardly-api";

export type WorkspaceAssetListQuery = Omit<
  DesktopWorkspaceAssetListRequest,
  "cursor"
>;

async function readWorkspaceAssets(request: DesktopWorkspaceAssetListRequest) {
  const result = await listDesktopWorkspaceAssets(request);
  if (result.ok) return { data: result.value } as const;
  return {
    error: {
      code: result.code,
      message: result.reason,
      status: null,
    },
  } as const;
}

export const workspaceAssetsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceAssets: builder.query<
      DesktopWorkspaceAssetList,
      DesktopWorkspaceAssetListRequest
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "WorkspaceAsset" },
      ],
      queryFn: readWorkspaceAssets,
    }),
    browseWorkspaceAssets: builder.infiniteQuery<
      DesktopWorkspaceAssetList,
      WorkspaceAssetListQuery,
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (page) => page.nextCursor ?? undefined,
      },
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "WorkspaceAsset" },
      ],
      queryFn: ({ pageParam, queryArg }) =>
        readWorkspaceAssets({
          ...queryArg,
          ...(pageParam ? { cursor: pageParam } : {}),
        }),
    }),
  }),
  overrideExisting: false,
});

export const {
  useBrowseWorkspaceAssetsInfiniteQuery,
  useListWorkspaceAssetsQuery,
} = workspaceAssetsApi;
