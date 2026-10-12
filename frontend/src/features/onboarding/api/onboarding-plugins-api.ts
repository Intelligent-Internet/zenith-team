import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { pluginService } from "@/shared/api/plugin.service";
import type { PluginCatalogResponseV1 } from "@/shared/api/types";
import { onboardingSources } from "@/features/onboarding/model/onboarding-sources";

type OnboardingPluginsArguments = { companyId: string; runtimeId: string };

const onboardingNativeIds = new Set<string>(
  onboardingSources.map((choice) => choice.nativeId),
);

// Only the plugins the connections step offers: a Codex catalog is several
// thousand rows, which the full listing pages through one request at a time.
// One name search per source runs in parallel and keeps the exact identities.
export const onboardingPluginsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    onboardingPlugins: builder.query<
      PluginCatalogResponseV1,
      OnboardingPluginsArguments
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Plugin" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeBoardlyQuery(
          async () => {
            const pages = await Promise.all(
              onboardingSources.map((choice) =>
                pluginService.listAllPlugins(companyId, {
                  provider: "codex",
                  runtimeId,
                  query: choice.name,
                }),
              ),
            );
            const plugins = new Map(
              pages
                .flatMap((page) => page.plugins)
                .filter(
                  (plugin) =>
                    plugin.provider === "codex" &&
                    onboardingNativeIds.has(plugin.nativeId),
                )
                .map((plugin) => [plugin.id, plugin]),
            );
            return { ...pages[0], plugins: [...plugins.values()] };
          },
          "Plugins could not be loaded.",
          { retry: true },
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useOnboardingPluginsQuery } = onboardingPluginsApi;
