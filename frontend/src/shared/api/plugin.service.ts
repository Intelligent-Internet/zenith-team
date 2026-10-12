import { apiClient } from "@/lib/api-client";
import type {
  PluginCatalogRequest,
  PluginCatalogResponseV1,
  PluginDetailResponseV1,
} from "@/shared/api/types";

class PluginService {
  // One Plugin's page as `runtimeId` observed it; without one the server
  // uses the catalog's default Runtime.
  async getPlugin(
    companyId: string,
    pluginId: string,
    runtimeId?: string,
  ): Promise<PluginDetailResponseV1> {
    return apiClient.get<PluginDetailResponseV1>(
      `/companies/${encodeURIComponent(companyId)}/plugins/${encodeURIComponent(pluginId)}`,
      { params: { runtime_id: runtimeId || undefined } },
    );
  }

  async listAllPlugins(
    companyId: string,
    request: Omit<PluginCatalogRequest, "cursor" | "limit">,
  ): Promise<PluginCatalogResponseV1> {
    const firstPage = await this.listPlugins(companyId, {
      ...request,
      limit: 100,
    });
    const plugins = [...firstPage.plugins];
    const pluginIds = new Set(plugins.map((plugin) => plugin.id));
    const seenCursors = new Set<string>();
    let cursor = firstPage.nextCursor;

    while (cursor) {
      if (seenCursors.has(cursor)) {
        throw new Error("Plugin catalog returned a repeated cursor.");
      }
      seenCursors.add(cursor);

      const page = await this.listPlugins(companyId, {
        ...request,
        cursor,
        limit: 100,
      });
      for (const plugin of page.plugins) {
        if (!pluginIds.has(plugin.id)) {
          pluginIds.add(plugin.id);
          plugins.push(plugin);
        }
      }
      cursor = page.nextCursor;
    }

    return { ...firstPage, nextCursor: null, plugins };
  }

  async listPlugins(
    companyId: string,
    request: PluginCatalogRequest,
  ): Promise<PluginCatalogResponseV1> {
    return apiClient.get<PluginCatalogResponseV1>(
      `/companies/${encodeURIComponent(companyId)}/plugins`,
      {
        params: {
          category: request.category,
          cursor: request.cursor,
          featured: request.featured,
          limit: request.limit,
          provider: request.provider,
          runtime_id: request.runtimeId,
          query: request.query,
          status: request.status,
        },
      },
    );
  }
}

export const pluginService = new PluginService();
