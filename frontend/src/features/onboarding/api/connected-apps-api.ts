import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { connectorService } from "@/shared/api/connector.service";
import type { ConnectorCatalogResponseV1 } from "@/shared/api/types";

type ConnectedAppsArguments = { companyId: string; runtimeId: string };

// Only the apps Andrew can already use: an installed plugin says nothing
// about whether its app was ever signed in, and the full catalog is several
// thousand rows the connections step does not need.
export const connectedAppsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    connectedApps: builder.query<
      ConnectorCatalogResponseV1,
      ConnectedAppsArguments
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
      ],
      queryFn: ({ companyId, runtimeId }) =>
        executeBoardlyQuery(
          () =>
            connectorService.listAllConnectors(companyId, {
              provider: "codex",
              runtimeId,
              status: "connected",
            }),
          "Connected apps could not be loaded.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useConnectedAppsQuery } = connectedAppsApi;
