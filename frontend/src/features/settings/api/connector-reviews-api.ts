import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import { connectorService } from "@/shared/api/connector.service";
import type {
  ConnectorCatalogResponseV1,
  ConnectorCatalogRequest,
  ConnectorItemV1,
  ConnectorOperationResponse,
} from "@/shared/api/types";

type ConnectorReviewArguments = {
  companyId: string;
  connectorId: string;
};

export const connectorReviewsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listConnectorReviews: builder.query<
      ConnectorCatalogResponseV1,
      { companyId: string } & Pick<
        ConnectorCatalogRequest,
        "provider" | "runtimeId"
      >
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
      ],
      queryFn: ({ companyId, provider, runtimeId }) =>
        executeZenithTeamQuery(
          () =>
            connectorService.listAllConnectors(companyId, {
              reviewRequired: true,
              provider,
              runtimeId,
            }),
          "Connector reviews could not be loaded.",
          { retry: true },
        ),
    }),
    reconnectConnector: builder.mutation<
      ConnectorOperationResponse,
      ConnectorReviewArguments
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
      ],
      queryFn: ({ companyId, connectorId }) =>
        executeZenithTeamQuery(
          () => connectorService.reconnect(companyId, connectorId),
          "Connector reconnect could not be completed.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useListConnectorReviewsQuery, useReconnectConnectorMutation } =
  connectorReviewsApi;

/**
 * Whether Reconnect can act on a listed review: it accepts the changed tools
 * of an app still connected and usable on its computer, as the server does.
 * An app listed because it lost its connection needs connecting again.
 */
export function reconnectableConnector(connector: ConnectorItemV1) {
  return (
    connector.reviewRequired === true &&
    connector.status === "connected" &&
    connector.canExecute
  );
}
