import { commerceService } from "@/features/revenue/api/commerce.service";
import type {
  AcceptCommerceTermsRequest,
  ApproveCommerceManifestRequest,
  CommerceManifestPage,
  CommerceManifestStatus,
  CommerceSummary,
  CommerceTermsAcceptance,
} from "@/features/revenue/model/commerce";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";

type CompanyCommerceArgument = { companyId: string };

const commerceSummaryTag = (companyId: string) =>
  `${companyId}:commerce-summary`;
const commerceManifestTag = (companyId: string) =>
  `${companyId}:commerce-manifests`;

export const commerceApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getCommerceSummary: builder.query<CommerceSummary, string>({
      providesTags: (_result, _error, companyId) => [
        { id: commerceSummaryTag(companyId), type: "Commerce" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => commerceService.getSummary(companyId),
          "Revenue could not be loaded.",
          { retry: true },
        ),
    }),
    acceptCommerceTerms: builder.mutation<
      CommerceTermsAcceptance,
      CompanyCommerceArgument & { request: AcceptCommerceTermsRequest }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: commerceSummaryTag(companyId), type: "Commerce" },
      ],
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () => commerceService.acceptTerms(companyId, request),
          "The Commerce Addendum could not be accepted.",
        ),
    }),
    getCommerceManifests: builder.infiniteQuery<
      CommerceManifestPage,
      string,
      string | null
    >({
      infiniteQueryOptions: {
        getNextPageParam: (lastPage) => lastPage.nextCursor,
        initialPageParam: null,
      },
      providesTags: (_result, _error, companyId) => [
        { id: commerceManifestTag(companyId), type: "Commerce" },
      ],
      queryFn: ({ pageParam, queryArg: companyId }) =>
        executeBoardlyQuery(
          () => commerceService.listManifests(companyId, pageParam),
          "Payment integrations could not be loaded.",
          { retry: true },
        ),
    }),
    approveCommerceManifest: builder.mutation<
      CommerceManifestStatus,
      CompanyCommerceArgument & {
        manifestId: string;
        request: ApproveCommerceManifestRequest;
      }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: commerceManifestTag(companyId), type: "Commerce" },
      ],
      queryFn: ({ companyId, manifestId, request }) =>
        executeBoardlyQuery(
          () =>
            commerceService.approveAndPublishManifest(
              companyId,
              manifestId,
              request,
            ),
          "The payment integration could not be published.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const {
  useAcceptCommerceTermsMutation,
  useApproveCommerceManifestMutation,
  useGetCommerceManifestsInfiniteQuery,
  useGetCommerceSummaryQuery,
} = commerceApi;
