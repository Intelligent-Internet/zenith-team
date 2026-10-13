import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { companyService } from "@/shared/api/company.service";
import { attachmentService } from "@/shared/api/attachment.service";
import type {
  CompanyDetail,
  CompanyPage,
  CompanySettingsProfileRequest,
  ProactiveModeDto,
  UpdateProactiveModeRequest,
} from "@/shared/api/types";

export const companyApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceCompanies: builder.query<CompanyPage, void>({
      queryFn: () =>
        executeBoardlyQuery(
          () => companyService.listCompanies(),
          "Companies could not be loaded.",
        ),
      providesTags: ["Company"],
    }),
    getWorkspaceCompany: builder.query({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Company" },
      ],
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => companyService.getCompanyById(companyId),
          "Company could not be loaded.",
        ),
    }),
    getWorkspaceCompanyAvatar: builder.query<
      string,
      { companyId: string; attachmentId: string }
    >({
      keepUnusedDataFor: 60,
      queryFn: ({ companyId, attachmentId }, { signal }) =>
        executeBoardlyQuery(async () => {
          const blob = await attachmentService.download(
            companyId,
            attachmentId,
            signal,
          );
          return URL.createObjectURL(blob);
        }, "Company avatar could not be loaded."),
      async onCacheEntryAdded(_arg, { cacheDataLoaded, cacheEntryRemoved }) {
        try {
          const { data: url } = await cacheDataLoaded;
          await cacheEntryRemoved;
          URL.revokeObjectURL(url);
        } catch {
          // No object URL exists when the image request failed or was cancelled.
        }
      },
    }),
    deleteWorkspaceCompany: builder.mutation<
      void,
      { companyId: string; expectedRevision: number }
    >({
      invalidatesTags: ["Company"],
      queryFn: ({ companyId, expectedRevision }) =>
        executeBoardlyQuery(
          () => companyService.deleteCompany(companyId, expectedRevision),
          "Company could not be deleted.",
        ),
    }),
    getWorkspaceProactiveMode: builder.query<ProactiveModeDto, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "ProactiveMode" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => companyService.getProactiveMode(companyId),
          "Proactive Mode could not be loaded.",
        ),
    }),
    updateWorkspaceProactiveMode: builder.mutation<
      ProactiveModeDto,
      { companyId: string; request: UpdateProactiveModeRequest }
    >({
      invalidatesTags: (_result, error, { companyId }) =>
        error ? [{ id: companyId, type: "ProactiveMode" }] : [],
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            companyApi.util.upsertQueryData(
              "getWorkspaceProactiveMode",
              companyId,
              data,
            ),
          );
        } catch {
          // The failed setting is invalidated above so active readers refetch.
        }
      },
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () => companyService.updateProactiveMode(companyId, request),
          "Proactive Mode could not be updated.",
        ),
    }),
    updateWorkspaceCompanyProfile: builder.mutation<
      CompanyDetail,
      { companyId: string; payload: CompanySettingsProfileRequest }
    >({
      invalidatesTags: ["Company"],
      queryFn: ({ companyId, payload }) =>
        executeBoardlyQuery(
          () => companyService.updateCompanyProfile(companyId, payload),
          "Company settings could not be saved.",
        ),
    }),
  }),
});

export const {
  useListWorkspaceCompaniesQuery,
  useLazyListWorkspaceCompaniesQuery,
  useDeleteWorkspaceCompanyMutation,
  useGetWorkspaceCompanyQuery,
  useGetWorkspaceCompanyAvatarQuery,
  useLazyGetWorkspaceCompanyQuery,
  useGetWorkspaceProactiveModeQuery,
  useUpdateWorkspaceCompanyProfileMutation,
  useUpdateWorkspaceProactiveModeMutation,
} = companyApi;
