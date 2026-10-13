import {
  BookOpen,
  Plug,
  Puzzle,
  RefreshCw,
  Search,
  Server,
} from "@/shared/ui/icons";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useCan } from "@/shared/permissions";
import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import {
  useRuntimeConnectorsQuery,
  runtimeCatalogQueryOptions,
  useRuntimePluginsQuery,
} from "../api/runtime-capabilities-api";
import { useRefreshPluginsMutation } from "../api/plugins-api";
import { runtimeComputerName } from "../model/runtime-management";
import { connectorSetup } from "../model/connector-setup";
import {
  providerLabel,
  runtimeDisplayName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import type { RuntimeDto } from "@/shared/api/types";
import type { LocalPluginIconContext } from "@/shared/lib/plugin-icons";
import { AppAvatar, pluginAvatarProps } from "@/shared/ui/app-avatar";
import { toast } from "@/shared/ui/sonner";
import "../styles/runtime-capabilities.css";

export type CapabilitySection = "plugins" | "skills" | "mcp";

export function RuntimeCapabilitiesPanel({
  companyId,
  runtime,
  section,
  agentName,
}: {
  companyId: string;
  runtime: Pick<
    RuntimeDto,
    | "id"
    | "name"
    | "customName"
    | "daemonId"
    | "deviceInfo"
    | "provider"
    | "status"
    | "signIn"
    | "unusableReason"
  >;
  section: CapabilitySection;
  agentName?: string;
}) {
  // The catalog is the registrant's to read and to refresh, whatever their
  // role, because the computer is theirs (ADR 0014). A refresh asks this
  // desktop's own daemon, so it works only on the computer the viewer is
  // on. Installing plugins stays a manage permission.
  const canManagePlugins = useCan("plugins.manage");
  const localDaemon = useLocalDaemonId();
  const refreshBlocked =
    runtime.daemonId !== localDaemon.daemonId
      ? `Open Zenith Team Desktop on ${runtimeComputerName(runtime)} to refresh this list.`
      : runtime.status !== "online"
        ? "This computer is offline, so it cannot report its list."
        : null;
  const plugins = useRuntimePluginsQuery(
    { companyId, provider: runtime.provider, runtimeId: runtime.id },
    { ...runtimeCatalogQueryOptions, skip: section === "mcp" },
  );
  const connectors = useRuntimeConnectorsQuery(
    { companyId, provider: runtime.provider, runtimeId: runtime.id },
    { ...runtimeCatalogQueryOptions, skip: section !== "mcp" },
  );
  const [refresh, refreshState] = useRefreshPluginsMutation();
  const [params] = useSearchParams();
  const [search, setSearch] = useState(() => params.get("q") ?? "");
  const pluginsParams = new URLSearchParams({ runtimeId: runtime.id });
  const returnAgentId = params.get("returnAgentId");
  if (returnAgentId) pluginsParams.set("returnAgentId", returnAgentId);
  const query = section === "mcp" ? connectors : plugins;
  const catalog = query.currentData;
  const matchesRuntime = catalog?.runtime?.id === runtime.id;
  const pluginItems =
    !plugins.isError && plugins.currentData?.runtime?.id === runtime.id
      ? plugins.currentData.plugins
      : [];
  const connectorItems =
    !connectors.isError && connectors.currentData?.runtime?.id === runtime.id
      ? connectors.currentData.connectors
      : [];
  const isMcp = section === "mcp";
  const Icon = isMcp ? Plug : section === "skills" ? BookOpen : Puzzle;
  // Local plugin logos come through the desktop app's boardly-plugin-icon
  // scheme, so they are only asked for while the runtime is online.
  const iconContext: LocalPluginIconContext | undefined =
    shouldUseDesktopHashRouter() && plugins.currentData?.runtime?.online
      ? { companyId, runtimeId: runtime.id }
      : undefined;
  const rows = isMcp
    ? connectorItems
        .filter((item) => item.source.endsWith("_mcp"))
        .map((item) => ({
          id: item.id,
          name: item.name,
          description: item.description || item.nativeId,
          avatar: { name: item.name, iconUrl: item.iconUrl },
          enabled:
            runtime.status === "online" &&
            !runtime.unusableReason &&
            runtime.signIn?.state === "signed_in" &&
            !catalog?.stale &&
            Boolean(catalog?.runtime?.online) &&
            connectorSetup(item).ready,
          detail:
            runtime.status !== "online" || !catalog?.runtime?.online
              ? "Reconnect this computer to check connection status."
              : runtime.unusableReason
                ? runtime.unusableReason.message
                : runtime.signIn?.state !== "signed_in"
                  ? `Confirm ${providerLabel(runtime.provider)} sign-in on this computer before using its connections.`
                  : catalog.stale
                    ? "Status needs refresh. Check again on this computer."
                    : `${connectorSetup(item).label}. ${connectorSetup(item).detail}`,
        }))
    : pluginItems
        .filter(
          (item) => section !== "skills" || item.componentCounts.skills > 0,
        )
        .map((item) => ({
          id: item.id,
          name: item.name,
          description: item.description || item.nativeId,
          avatar: pluginAvatarProps(item, iconContext),
          enabled:
            runtime.status === "online" &&
            !catalog?.stale &&
            Boolean(catalog?.runtime?.online) &&
            item.enabled &&
            item.installed,
          detail:
            section === "skills"
              ? `${item.componentCounts.skills} ${item.componentCounts.skills === 1 ? "skill" : "skills"} · Plugin bundle`
              : `${item.componentCounts.skills} ${item.componentCounts.skills === 1 ? "skill" : "skills"} · ${item.componentCounts.mcpServers} MCP · ${item.componentCounts.apps} ${item.componentCounts.apps === 1 ? "app" : "apps"}`,
        }));
  const displayed = rows.filter((item) =>
    `${item.name} ${item.description}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );

  async function refreshCatalog() {
    try {
      const result = await refresh({
        companyId,
        runtimeId: runtime.id,
        provider: runtime.provider,
      }).unwrap();
      if (result.state === "failed") {
        toast.error("Runtime discovery could not be refreshed.");
        return;
      }
      if (section === "mcp") await connectors.refetch();
      else await plugins.refetch();
      toast.success(
        result.state === "complete"
          ? "Runtime discovery refreshed"
          : "Refresh requested. The list will update when discovery completes.",
      );
    } catch {
      toast.error(
        "This runtime could not be refreshed. Check its computer connection.",
      );
    }
  }

  return (
    <section
      className="runtime-capabilities"
      aria-label={`${agentName ? "Inherited " : "Runtime "}${section}`}
    >
      {agentName ? (
        <div className="capability-assigned">
          <h2>Assigned to agent</h2>
          <p>
            Additional {isMcp ? "MCP connections" : "skills"} for {agentName}.
          </p>
          <div className="capability-empty">
            <Icon size={24} />
            <strong>Agent-specific {isMcp ? "MCP" : "skills"}</strong>
            <span>
              Direct assignment is not available yet. Inherited runtime
              capabilities appear below.
            </span>
          </div>
        </div>
      ) : null}
      <header className="capability-heading">
        <div>
          <h2>
            {agentName
              ? "Inherited from runtime"
              : section === "plugins"
                ? "Installed plugins"
                : section === "skills"
                  ? "Plugin skill bundles"
                  : "MCP servers"}
          </h2>
          <p>
            {providerLabel(runtime.provider)} · {runtimeDisplayName(runtime)}
            {agentName
              ? ` · Used when ${agentName} runs here.`
              : " · Synced from the connected computer."}
          </p>
        </div>
        <button
          className="dashboard-button"
          title={refreshBlocked ?? undefined}
          disabled={refreshBlocked !== null || refreshState.isLoading}
          onClick={() => void refreshCatalog()}
        >
          <RefreshCw size={14} />
          {refreshState.isLoading ? "Refreshing…" : "Refresh"}
        </button>
      </header>
      <div className="capability-toolbar">
        <label>
          <Search size={14} />
          <input
            type="search"
            aria-label={`Search ${section === "skills" ? "plugin bundles" : section}`}
            placeholder={`Search ${section === "skills" ? "plugin bundles" : section}…`}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
      </div>
      {query.isLoading ? (
        <p role="status">Loading runtime discovery…</p>
      ) : query.isError ? (
        <p className="capability-note" role="status">
          Discovery is unavailable.{" "}
          <button
            type="button"
            className="dashboard-button"
            onClick={() => void query.refetch()}
          >
            Retry loading
          </button>
        </p>
      ) : catalog && !matchesRuntime ? (
        <p className="capability-note">
          Waiting for discovery from this computer.
        </p>
      ) : catalog?.stale ? (
        <p className="capability-note">
          Showing the last reported discovery. Refresh for the latest state.
        </p>
      ) : null}
      {section === "skills" && matchesRuntime ? (
        <p className="capability-note">
          This view shows skill counts by plugin. Search finds plugin bundles.
          Open the provider on this computer to inspect individual skill names,
          descriptions, and instructions.
        </p>
      ) : null}
      {displayed.length ? (
        <div className="capability-list">
          {displayed.map((item) => (
            <article key={item.id} data-enabled={item.enabled}>
              <AppAvatar {...item.avatar} size={40} />
              <div className="capability-row-copy">
                <strong>{item.name}</strong>
                <p title={item.description}>{item.description}</p>
                <small>
                  <Server size={11} />
                  {item.detail}
                </small>
              </div>
              <span className="capability-state" data-enabled={item.enabled}>
                {item.enabled
                  ? isMcp
                    ? "Allowed in company"
                    : "Enabled"
                  : isMcp
                    ? "Not ready"
                    : catalog?.stale || !catalog?.runtime?.online
                      ? "Status unknown"
                      : "Disabled"}
              </span>
            </article>
          ))}
        </div>
      ) : !query.isLoading && !query.isError && matchesRuntime ? (
        <div className="capability-empty">
          <Icon size={24} />
          <strong>
            {search
              ? "No matching capabilities"
              : `No ${section === "mcp" ? "MCP servers" : section} reported`}
          </strong>
          <span>
            {search
              ? "Try a different search."
              : "Refresh after configuring this provider on the connected computer."}
          </span>
        </div>
      ) : null}
      {/* The Plugins settings page installs plugins and connects apps on
          the viewer's own computer (ADR 0014). It takes plugins.manage,
          which every role holds since D28; the check hides the link if
          that grant ever narrows. */}
      {!agentName && canManagePlugins ? (
        <Link
          className="capability-runtime-link"
          to={`/settings/plugins?${pluginsParams}`}
        >
          Manage plugins and connections
        </Link>
      ) : null}
      {agentName ? (
        <Link
          className="capability-runtime-link"
          to={`/settings/agents/${runtime.id}?tab=${section}`}
        >
          <Server size={14} /> Open runtime {section}
        </Link>
      ) : null}
    </section>
  );
}
