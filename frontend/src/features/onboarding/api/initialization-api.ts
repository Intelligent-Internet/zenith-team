import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import { companyService } from "@/shared/api/company.service";
import { conversationService } from "@/shared/api/conversation.service";
import type { operations } from "@/shared/api/generated/api.generated";

export type CompanyInitialization =
  operations["getCompanyInitialization"]["responses"][200]["content"]["application/json"];
type InitializationRequest =
  operations["advanceCompanyInitialization"]["requestBody"]["content"]["application/json"];

const path = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}/initialization`;

export const initializationApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getCompanyInitialization: builder.query<CompanyInitialization, string>({
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => apiClient.get<CompanyInitialization>(path(companyId)),
          "Your saved progress could not be checked.",
        ),
    }),
    advanceCompanyInitialization: builder.mutation<
      CompanyInitialization,
      { companyId: string; request: InitializationRequest; start?: boolean }
    >({
      queryFn: ({ companyId, request, start }) =>
        executeZenithTeamQuery(async () => {
          if (start) {
            const bootstrap = await companyService.getBootstrap(companyId);
            if (!bootstrap.ceoAgent)
              throw new Error("Set up your CEO before continuing.");
            request = {
              ...request,
              conversationId:
                bootstrap.ceoConversationId ??
                (
                  await conversationService.createDirectConversation(
                    companyId,
                    { agentId: bootstrap.ceoAgent.id },
                  )
                ).id,
            };
          }
          return apiClient.post<CompanyInitialization>(
            path(companyId),
            request,
          );
        }, "Your company could not finish initializing."),
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            initializationApi.util.upsertQueryData(
              "getCompanyInitialization",
              companyId,
              data,
            ),
          );
        } catch {
          /* The mutation exposes the error to the initialization screen. */
        }
      },
    }),
  }),
});

export const {
  useGetCompanyInitializationQuery,
  useAdvanceCompanyInitializationMutation,
} = initializationApi;
