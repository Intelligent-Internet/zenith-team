import type { DesktopPluginOperation } from "../../../../electron/shared/desktop-bridge.types";
import {
  installDesktopPlugin,
  refreshDesktopPlugins,
  uninstallDesktopPlugin,
} from "@/shared/desktop/desktop-environment.service";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import { pluginService } from "@/shared/api/plugin.service";
import type {
  PluginCatalogRequest,
  PluginCatalogResponseV1,
  PluginDetailResponseV1,
  PluginOperationResponse,
  PluginProvider,
} from "@/shared/api/types";

type PluginCatalogArguments = PluginCatalogRequest & {
  companyId: string;
};

type PluginDetailArguments = {
  companyId: string;
  pluginId: string;
  runtimeId?: string;
};

type PluginActionArguments = {
  action: "install" | "uninstall";
  companyId: string;
  nativeId: string;
  provider: PluginProvider;
  runtimeId?: string;
};

/**
 * A Desktop plugin change's answer. An install also carries the provider's
 * auth policy and the apps it still needs connected, when it answers them.
 */
export type PluginChangeResponse = PluginOperationResponse &
  Pick<DesktopPluginOperation, "appsNeedingAuth" | "authPolicy">;

type PluginRefreshArguments = {
  companyId: string;
  provider: PluginProvider;
  runtimeId?: string;
  /** False republishes the daemon's catalog instead of refetching it. */
  force?: boolean;
};

export const pluginsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listAllPlugins: builder.query<
      PluginCatalogResponseV1,
      Omit<PluginCatalogArguments, "cursor" | "limit">
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Plugin" },
      ],
      queryFn: ({ companyId, ...request }) =>
        executeZenithTeamQuery(
          () => pluginService.listAllPlugins(companyId, request),
          "Plugins could not be loaded.",
          { retry: true },
        ),
    }),
    listPlugins: builder.query<PluginCatalogResponseV1, PluginCatalogArguments>(
      {
        providesTags: (_result, _error, { companyId }) => [
          { id: companyId, type: "Plugin" },
        ],
        queryFn: ({ companyId, ...request }) =>
          executeZenithTeamQuery(
            () => pluginService.listPlugins(companyId, request),
            "Plugins could not be loaded.",
            { retry: true },
          ),
      },
    ),
    // Shares the catalog's tag, so an install, uninstall or rediscovery
    // refetches an open plugin page too.
    getPlugin: builder.query<PluginDetailResponseV1, PluginDetailArguments>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Plugin" },
      ],
      queryFn: ({ companyId, pluginId, runtimeId }) =>
        executeZenithTeamQuery(
          () => pluginService.getPlugin(companyId, pluginId, runtimeId),
          "The plugin could not be loaded.",
        ),
    }),
    changePluginInstallation: builder.mutation<
      PluginChangeResponse,
      PluginActionArguments
    >({
      queryFn: async ({ action, companyId, nativeId, provider, runtimeId }) => {
        if (!runtimeId) {
          return pluginActionError(
            "Plugin management requires Zenith Team Desktop with the selected provider runtime online.",
          );
        }

        const request = { companyId, nativeId, provider, runtimeId };
        const local =
          action === "install"
            ? await installDesktopPlugin(request)
            : await uninstallDesktopPlugin(request);
        if (local.ok) {
          return pluginOperationResult(local.value);
        }
        return pluginActionError(local.reason);
      },
    }),
    refreshPlugins: builder.mutation<
      PluginOperationResponse,
      PluginRefreshArguments
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
        { id: companyId, type: "Plugin" },
      ],
      queryFn: async ({ companyId, force, provider, runtimeId }) => {
        if (!runtimeId) {
          return pluginActionError(
            "Plugin discovery requires Zenith Team Desktop with the selected provider runtime online.",
          );
        }
        const local = await refreshDesktopPlugins({
          companyId,
          provider,
          runtimeId,
          ...(force === undefined ? {} : { force }),
        });
        return local.ok
          ? pluginOperationResult(local.value)
          : pluginActionError(local.reason);
      },
    }),
  }),
  overrideExisting: false,
});

function pluginActionError(message: string) {
  return { error: { message, status: null } } as const;
}

function pluginOperationResult(operation: DesktopPluginOperation) {
  const hasContinueUrl = operation.continueUrl !== undefined;
  const continueUrl = hasContinueUrl
    ? safePluginContinueUrl(operation.continueUrl)
    : undefined;
  if (
    (hasContinueUrl && !continueUrl) ||
    (hasContinueUrl && operation.state !== "awaiting_user")
  ) {
    return pluginActionError("Plugin operation response is invalid.");
  }
  const answer = {
    ...(operation.authPolicy ? { authPolicy: operation.authPolicy } : {}),
    ...(operation.appsNeedingAuth
      ? { appsNeedingAuth: operation.appsNeedingAuth }
      : {}),
  };
  if (operation.state === "awaiting_user") {
    return {
      data: {
        state: operation.state,
        ...(continueUrl ? { continueUrl } : {}),
        ...answer,
      },
    } as const;
  }
  return {
    data: {
      state: operation.state,
      ...answer,
    },
  } as const;
}

function safePluginContinueUrl(value: string | undefined) {
  if (!value || value !== value.trim() || value.length > 4096) return undefined;
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" || url.username || url.password || url.port) {
      return undefined;
    }
    if (url.hash && !isConnectorSetupUrl(url)) return undefined;
    return url.href;
  } catch {
    return undefined;
  }
}

function isConnectorSetupUrl(url: URL) {
  if (
    url.hostname !== "chatgpt.com" ||
    url.search ||
    (url.pathname !== "/apps" && !url.pathname.startsWith("/apps/")) ||
    !url.hash.startsWith("#settings/Connectors?")
  ) {
    return false;
  }
  const params = new URLSearchParams(
    url.hash.slice("#settings/Connectors?".length),
  );
  const keys = [...params.keys()];
  return (
    keys.length === 4 &&
    new Set(keys).size === 4 &&
    params.get("add-connector-link") === "true" &&
    params.get("product-sku") === "CODEX" &&
    params.get("referrer") === "codex" &&
    /^[A-Za-z0-9._:@+-]{1,240}$/.test(params.get("connector") ?? "")
  );
}

export const {
  useChangePluginInstallationMutation,
  useGetPluginQuery,
  useLazyListPluginsQuery,
  useListPluginsQuery,
  useListAllPluginsQuery,
  useRefreshPluginsMutation,
} = pluginsApi;
