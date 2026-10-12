import { attachmentService } from "@/shared/api/attachment.service";
import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
export type SocialPlatform = components["schemas"]["SocialPlatform"];
export type SocialConnection = components["schemas"]["SocialConnection"];
const path = "/me/social";
const api = boardlyApi.injectEndpoints({
  endpoints: (build) => ({
    socialMediaPreview: build.query<
      string,
      { companyId: string; attachmentId: string; attempt: number }
    >({
      keepUnusedDataFor: 0,
      queryFn: ({ companyId, attachmentId }, { signal }) =>
        executeBoardlyQuery(async () => {
          const blob = await attachmentService.download(
            companyId,
            attachmentId,
            signal,
          );
          return URL.createObjectURL(blob);
        }, "Could not load post media."),
      async onCacheEntryAdded(_arg, { cacheDataLoaded, cacheEntryRemoved }) {
        try {
          const { data: url } = await cacheDataLoaded;
          await cacheEntryRemoved;
          URL.revokeObjectURL(url);
        } catch {
          // Failed or cancelled downloads do not create an object URL.
        }
      },
    }),
    socialPlatforms: build.query<
      components["schemas"]["SocialPlatforms"],
      void
    >({
      queryFn: () =>
        executeBoardlyQuery(
          () =>
            apiClient.get<components["schemas"]["SocialPlatforms"]>(
              `${path}/platforms`,
            ),
          "Could not load platforms.",
        ),
      providesTags: ["Social"],
    }),
    socialConnections: build.query<
      components["schemas"]["SocialConnections"],
      void
    >({
      queryFn: () =>
        executeBoardlyQuery(
          () =>
            apiClient.get<components["schemas"]["SocialConnections"]>(
              `${path}/connections`,
            ),
          "Could not load your connections.",
        ),
      providesTags: ["Social"],
    }),
    startSocialOAuth: build.mutation<
      components["schemas"]["SocialOAuthStartResponse"],
      {
        platform: string;
        desktop?: components["schemas"]["SocialDesktopRequest"];
      }
    >({
      queryFn: ({ platform, desktop }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<components["schemas"]["SocialOAuthStartResponse"]>(
              `${path}/oauth/${platform}${desktop ? "/desktop" : ""}`,
              desktop,
            ),
          "Could not start this connection.",
        ),
    }),
    disconnectSocial: build.mutation<void, { id: string; revision: number }>({
      queryFn: ({ id, revision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<void>(`${path}/connections/${id}`, {
              params: { expected_revision: revision },
            }),
          "Could not disconnect this account.",
        ),
      invalidatesTags: ["Social"],
    }),
  }),
});
export const {
  useSocialMediaPreviewQuery,
  useSocialPlatformsQuery,
  useSocialConnectionsQuery,
  useStartSocialOAuthMutation,
  useDisconnectSocialMutation,
} = api;
