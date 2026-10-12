import { apiClient } from "@/lib/api-client";
import type {
  ConnectorCatalogRequest,
  ConnectorCatalogResponseV1,
  ConnectorOperationResponse,
} from "@/shared/api/types";

class ConnectorService {
  async listAllConnectors(
    companyId: string,
    request: Omit<ConnectorCatalogRequest, "cursor" | "limit">,
  ): Promise<ConnectorCatalogResponseV1> {
    const firstPage = await this.listConnectors(companyId, {
      ...request,
      limit: 100,
    });
    const connectors = [...firstPage.connectors];
    const connectorIds = new Set(connectors.map((connector) => connector.id));
    const seenCursors = new Set<string>();
    let cursor = firstPage.nextCursor;

    while (cursor) {
      if (seenCursors.has(cursor)) {
        throw new Error("Connector catalog returned a repeated cursor.");
      }
      seenCursors.add(cursor);

      const page = await this.listConnectors(companyId, {
        ...request,
        cursor,
        limit: 100,
      });
      for (const connector of page.connectors) {
        if (!connectorIds.has(connector.id)) {
          connectorIds.add(connector.id);
          connectors.push(connector);
        }
      }
      cursor = page.nextCursor;
    }

    return { ...firstPage, connectors, nextCursor: null };
  }

  async listConnectors(
    companyId: string,
    request: ConnectorCatalogRequest,
  ): Promise<ConnectorCatalogResponseV1> {
    return apiClient.get<ConnectorCatalogResponseV1>(
      `/companies/${encodeURIComponent(companyId)}/connectors`,
      {
        params: {
          category: request.category,
          cursor: request.cursor,
          limit: request.limit,
          provider: request.provider,
          source: request.source,
          runtime_id: request.runtimeId,
          query: request.query,
          review_required: request.reviewRequired,
          status: request.status,
        },
      },
    );
  }

  async reconnect(
    companyId: string,
    connectorId: string,
  ): Promise<ConnectorOperationResponse> {
    return apiClient.post<ConnectorOperationResponse>(
      `/companies/${encodeURIComponent(companyId)}/connectors/${encodeURIComponent(connectorId)}/connect`,
      { schemaVersion: 1 },
    );
  }
}

export const connectorService = new ConnectorService();
