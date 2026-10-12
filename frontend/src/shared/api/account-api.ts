import { authService } from "@/shared/api/auth.service";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type {
  AuthProviderCapabilitiesResponse,
  AuthUserResponse,
} from "@/shared/api/types";

export const accountApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getAuthProviders: builder.query<AuthProviderCapabilitiesResponse, void>({
      keepUnusedDataFor: 3_600,
      queryFn: () =>
        executeBoardlyQuery(
          () => authService.getProviders(),
          "Sign-in providers could not be loaded.",
        ),
    }),
    getCurrentUser: builder.query<AuthUserResponse, void>({
      merge: (current, incoming) => {
        if (current.revision <= incoming.revision) {
          Object.assign(current, incoming);
        }
      },
      providesTags: ["Account"],
      queryFn: () =>
        executeBoardlyQuery(
          () => authService.getMe(),
          "Your profile could not be loaded.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useGetAuthProvidersQuery, useGetCurrentUserQuery } = accountApi;
