import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { connectorService } from "@/shared/api/connector.service";
import { platformSkillService } from "@/shared/api/platform-skill.service";
import { pluginService } from "@/shared/api/plugin.service";
import type {
  AgentProvider,
  ConnectorCatalogResponseV1,
  PlatformSkill,
  PluginCatalogResponseV1,
} from "@/shared/api/types";

type CatalogArguments = {
  companyId: string;
  provider: AgentProvider;
  runtimeId: string;
};

// Committed provider discovery invalidates these caches through realtime.
// Reuse fresh reads across tabs; reconnect converges subscribed catalogs.
export const runtimeCatalogQueryOptions = {
  refetchOnMountOrArgChange: 60,
};

export const runtimeCapabilitiesApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    platformSkills: builder.query<{ items: PlatformSkill[] }, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => platformSkillService.list(companyId),
          "Built-in skills could not be loaded.",
        ),
    }),
    runtimePlugins: builder.query<PluginCatalogResponseV1, CatalogArguments>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Plugin" },
      ],
      queryFn: ({ companyId, provider, runtimeId }) =>
        executeBoardlyQuery(
          () =>
            pluginService.listAllPlugins(companyId, {
              provider,
              runtimeId,
              status: "installed",
            }),
          "Installed plugins could not be loaded.",
        ),
    }),
    runtimeConnectors: builder.query<
      ConnectorCatalogResponseV1,
      CatalogArguments
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
      ],
      queryFn: ({ companyId, provider, runtimeId }) =>
        executeBoardlyQuery(
          () =>
            connectorService.listAllConnectors(companyId, {
              provider,
              runtimeId,
              source: provider === "codex" ? "codex_mcp" : "claude_mcp",
            }),
          "MCP connections could not be loaded.",
        ),
    }),
  }),
});

export const {
  usePlatformSkillsQuery,
  useRuntimePluginsQuery,
  useRuntimeConnectorsQuery,
} = runtimeCapabilitiesApi;
