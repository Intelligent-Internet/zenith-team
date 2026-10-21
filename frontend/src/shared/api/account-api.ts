import { authService } from "@/shared/api/auth.service";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type {
  AuthProviderCapabilitiesResponse,
  AuthUserResponse,
} from "@/shared/api/types";

export const accountApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getAuthProviders: builder.query<AuthProviderCapabilitiesResponse, void>({
      keepUnusedDataFor: 3_600,
      queryFn: () =>
        executeZenithTeamQuery(
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
        executeZenithTeamQuery(
          () => authService.getMe(),
          "Your profile could not be loaded.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useGetAuthProvidersQuery, useGetCurrentUserQuery } = accountApi;
