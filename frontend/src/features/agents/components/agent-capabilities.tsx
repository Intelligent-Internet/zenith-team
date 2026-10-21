import { useUpdateWorkspaceAgentMutation } from "@/shared/api/agent-api";
import {
  BookOpen,
  Layers,
  Monitor,
  Search,
  Plug,
  Puzzle,
  RefreshCw,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState } from "react";

import { Link, useSearchParams } from "react-router";
import type { AgentDto, AgentProvider } from "@/shared/api/types";
import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { useLocalRuntime } from "../hooks/use-local-runtime";
import {
  agentAvailabilityHelp,
  agentAvailabilityLabel,
  agentBindingLabel,
} from "@/features/agents/model/agent-binding";
import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  usePlatformSkillsQuery,
  runtimeCatalogQueryOptions,
  useRuntimeConnectorsQuery,
  useRuntimePluginsQuery,
} from "@/features/settings/api/runtime-capabilities-api";
import { useRefreshPluginsMutation } from "@/features/settings/api/plugins-api";
import type { LocalPluginIconContext } from "@/shared/lib/plugin-icons";
import { pluginAvatarProps } from "@/shared/ui/app-avatar";
import { toast } from "@/shared/ui/sonner";
import {
  CapabilityCards,
  builtInToolGroups,
  platformSkillCards,
} from "./capability-cards";
import { CustomSkills } from "./custom-skills";
import { ManagedTools } from "./managed-tools";
import { connectorSetup } from "@/features/settings/model/connector-setup";
import "../styles/agent-capabilities.css";

export function AgentCapabilities({ agent }: { agent: AgentDto }) {
  if (agent.provider !== "codex" && agent.provider !== "claude") {
    return <p>Capabilities are not available for this provider.</p>;
  }
  // The capabilities shown are the ones the viewer's own computer provides,
  // so the panel remounts when they move the Agent (ADR 0014).
  return (
    <AgentCapabilityContent
      key={`${agent.id}:${agent.binding?.runtime.id ?? "unbound"}`}
      agent={agent}
      provider={agent.provider}
    />
  );
}

function AgentCapabilityContent({
  agent,
  provider,
}: {
  agent: AgentDto;
  provider: AgentProvider;
}) {
  // Editing an Agent's capabilities is configuring it — the creator's and
  // the agents.manage holders' (ADR 0013), which the server says per viewer.
  // Refreshing the plugin and connector catalogs is not: the computer is the
  // viewer's own, so any member refreshes it (ADR 0014), as on the runtime
  // page. Every affordance below is gated on the fact it depends on.
  const owner = agent.access.configure;
  // Discovery is asked of THIS desktop's daemon, which re-probes only the
  // machine it runs on; the daemon refuses any other runtime id.
  const localRuntime = useLocalRuntime(agent, true);
  const canRefreshDiscovery = localRuntime.local;
  // Plugins and connectors are read off the computer the VIEWER runs this
  // Agent on (ADR 0014): the same Agent can have the GitHub plugin here and
  // not there. Any member reads their own computer's catalog; until they
  // set the Agent up there is nothing to read.
  const boundRuntimeId = agent.binding?.runtime.id ?? "";
  const platformSkills = usePlatformSkillsQuery(agent.companyId);
  const plugins = useRuntimePluginsQuery(
    { companyId: agent.companyId, provider, runtimeId: boundRuntimeId },
    { ...runtimeCatalogQueryOptions, skip: !boundRuntimeId },
  );
  const connectors = useRuntimeConnectorsQuery(
    { companyId: agent.companyId, provider, runtimeId: boundRuntimeId },
    { ...runtimeCatalogQueryOptions, skip: !boundRuntimeId },
  );
  const [search, setSearch] = useState("");
  const [activeSection, setActiveSection] = useState("capabilities-built-in");
  const [params] = useSearchParams();
  const mainRef = useRef<HTMLDivElement>(null);
  const navigationRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const requested = params.get("section");
    if (requested !== "connections" && requested !== "skills") return;
    const section = mainRef.current?.querySelector<HTMLElement>(
      `#capabilities-${requested}`,
    );
    const frame = requestAnimationFrame(() => {
      section?.scrollIntoView({ block: "start" });
      section?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [
    params,
    platformSkills.isLoading,
    plugins.isLoading,
    connectors.isLoading,
  ]);
  useEffect(() => {
    const main = mainRef.current;
    const navigation = navigationRef.current;
    const scroller = main?.closest<HTMLElement>(
      ".workspace-page-frame__content",
    );
    const tabs = scroller?.querySelector<HTMLElement>(".agent-profile-tabs");
    if (!main || !navigation || !scroller || !tabs) return;
    const sections = Array.from(
      main.querySelectorAll<HTMLElement>(".agent-capabilities-section"),
    );
    let frame = 0;
    const update = () => {
      const tabsHeight = tabs.getBoundingClientRect().height;
      const navigationHeight = navigation.getBoundingClientRect().height;
      main.style.setProperty("--capabilities-tabs-height", `${tabsHeight}px`);
      main.style.setProperty(
        "--capabilities-navigation-height",
        `${navigationHeight}px`,
      );
      const threshold =
        scroller.getBoundingClientRect().top +
        tabsHeight +
        navigationHeight +
        17;
      let active = sections[0];
      for (const section of sections) {
        if (section.getBoundingClientRect().top <= threshold) active = section;
      }
      // The last section may be too short to reach the sticky navigation.
      if (
        scroller.scrollTop > 0 &&
        scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2
      ) {
        active = sections.at(-1) ?? active;
      }
      if (active) setActiveSection(active.id);
    };
    const scheduleUpdate = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    const observer = new ResizeObserver(scheduleUpdate);
    [main, navigation, tabs, scroller, ...sections].forEach((element) =>
      observer.observe(element),
    );
    scroller.addEventListener("scroll", scheduleUpdate, { passive: true });
    update();
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      scroller.removeEventListener("scroll", scheduleUpdate);
    };
  }, []);
  const [refresh, refreshState] = useRefreshPluginsMutation();
  const [save, saveState] = useUpdateWorkspaceAgentMutation();
  const saving = useRef(false);
  const [saved, setSaved] = useState(agent);
  const current = saved.revision > agent.revision ? saved : agent;
  const capabilities = current.capabilities;
  const overrides = Object.fromEntries([
    [
      "tool:zenith-studio",
      capabilities.enabledTools?.includes("zenith-studio") ?? false,
    ],
    ...capabilities.disabledBuiltIns.map((id) => [`builtin:${id}`, false]),
    ...capabilities.disabledPlugins.map((id) => [`plugin:${id}`, false]),
    ...capabilities.disabledMcpServers.map((id) => [`mcp:${id}`, false]),
  ]);
  const runtimeLink = boundRuntimeId
    ? `/settings/agents/${boundRuntimeId}?returnAgentId=${agent.id}`
    : undefined;
  const pluginItems =
    boundRuntimeId &&
    !plugins.isError &&
    plugins.currentData?.runtime?.id === boundRuntimeId
      ? plugins.currentData.plugins
      : [];
  const connectorItems =
    boundRuntimeId &&
    !connectors.isError &&
    connectors.currentData?.runtime?.id === boundRuntimeId
      ? connectors.currentData.connectors
      : [];
  // Local plugin logos come through the desktop app's zenith-team-plugin-icon
  // scheme, so they are only asked for while the runtime is online.
  const iconContext: LocalPluginIconContext | undefined =
    shouldUseDesktopHashRouter() && plugins.currentData?.runtime?.online
      ? { companyId: agent.companyId, runtimeId: boundRuntimeId }
      : undefined;
  const pluginCards = pluginItems.map((item) => ({
    id: `plugin:${item.id}`,
    name: item.name,
    description: item.description || item.nativeId,
    avatar: pluginAvatarProps(item, iconContext),
    enabled: item.enabled && item.installed,
    detail:
      [
        item.componentCounts.skills > 0 &&
          `${item.componentCounts.skills} ${item.componentCounts.skills === 1 ? "skill" : "skills"}`,
        item.componentCounts.mcpServers > 0 &&
          `${item.componentCounts.mcpServers} MCP`,
        item.componentCounts.apps > 0 &&
          `${item.componentCounts.apps} ${item.componentCounts.apps === 1 ? "app" : "apps"}`,
      ]
        .filter(Boolean)
        .join(" · ") || "No components reported",
  }));
  const skillCards = pluginItems
    .filter((item) => item.componentCounts.skills > 0)
    .map((item) => ({
      id: `plugin:${item.id}`,
      name: item.name,
      description: item.description || item.nativeId,
      avatar: pluginAvatarProps(item, iconContext),
      enabled:
        agent.runtimeAvailable &&
        !plugins.currentData?.stale &&
        Boolean(plugins.currentData?.runtime?.online) &&
        item.enabled &&
        item.installed,
      detail: !agent.runtimeAvailable
        ? agentAvailabilityLabel(agent)
        : plugins.currentData?.stale || !plugins.currentData?.runtime?.online
          ? "Status needs refresh"
          : item.enabled && item.installed
            ? "Available"
            : "Unavailable on runtime",
    }));
  const mcpCards = connectorItems
    .filter((item) => item.source.endsWith("_mcp"))
    .map((item) => {
      const status = connectorSetup(item);
      const fresh =
        !connectors.currentData?.stale &&
        connectors.currentData?.runtime?.online &&
        agent.runtimeAvailable;
      const query = new URLSearchParams({
        runtimeId: boundRuntimeId,
        returnAgentId: agent.id,
      });
      const setupLink = !agent.runtimeAvailable
        ? runtimeLink
        : fresh && item.reviewRequired
          ? `/settings/plugins?${query}`
          : `${runtimeLink}&tab=mcp&q=${encodeURIComponent(item.name)}`;
      return {
        id: `mcp:${item.id}`,
        name:
          (
            {
              codex_app: "Codex app",
              cua_repl: "Computer use",
              "computer-use": "Computer use",
            } as Record<string, string>
          )[item.name] ?? item.name,
        description: item.description || item.nativeId,
        avatar: { name: item.name, iconUrl: item.iconUrl },
        enabled: Boolean(fresh) && status.ready,
        detail: !agent.runtimeAvailable
          ? agentAvailabilityLabel(agent)
          : fresh
            ? status.label
            : "Status needs refresh",
        setupLink: fresh && status.ready ? undefined : setupLink,
        setupLabel:
          fresh && item.reviewRequired
            ? "Review connection"
            : "Connection details",
      };
    });
  async function toggleCapability(key: string, enabled: boolean) {
    if (!owner || saving.current) return;
    const [source, id] = key.split(":");
    const field =
      source === "builtin"
        ? "disabledBuiltIns"
        : source === "plugin"
          ? "disabledPlugins"
          : "disabledMcpServers";
    const next =
      source === "tool"
        ? {
            ...capabilities,
            enabledTools: enabled ? [] : ["zenith-studio" as const],
          }
        : {
            ...capabilities,
            [field]: enabled
              ? [...capabilities[field], id]
              : capabilities[field].filter((value) => value !== id),
          };
    saving.current = true;
    try {
      const updated = await save({
        companyId: agent.companyId,
        agentId: agent.id,
        request: { expectedRevision: current.revision, capabilities: next },
      }).unwrap();
      setSaved(updated);
      toast.success("Capability settings saved for new runs.");
    } catch {
      toast.error("Settings were not saved. Refresh the agent and try again.");
    } finally {
      saving.current = false;
    }
  }
  const controls = {
    editable: owner,
    busy: saveState.isLoading,
    overrides,
    onToggle: (id: string, enabled: boolean) =>
      void toggleCapability(id, enabled),
  };
  function discoveryNote(query: typeof plugins | typeof connectors) {
    if (!boundRuntimeId)
      return `Set ${agent.name} up on one of your computers to see what it can use there.`;
    if (query.isLoading) return "Loading runtime capabilities…";
    if (query.isError)
      return "Discovery is unavailable. Check the runtime connection and try Refresh.";
    if (query.currentData?.runtime?.id !== boundRuntimeId)
      return "Discovery for this computer is not available yet.";
    if (query.currentData.stale)
      return "Showing the last reported discovery. Refresh for the latest state.";
    return undefined;
  }
  // What a section read off the viewer's computer says when it has no rows,
  // so it never looks like it failed to load. The notices above say why.
  function emptyNote(query: typeof plugins | typeof connectors, title: string) {
    const noun = title.toLowerCase();
    if (!boundRuntimeId)
      return `Set ${agent.name} up on your computer to see its ${noun}.`;
    if (query.isLoading) return `Loading ${noun}…`;
    if (query.isError) return `${title} could not be loaded.`;
    if (query.currentData?.runtime?.id !== boundRuntimeId)
      return `This computer has not reported its ${noun} yet.`;
    if (query.currentData.stale)
      return `No ${noun} in the last report from this computer. Refresh for the latest.`;
    return `No ${noun} on this computer yet.`;
  }
  async function refreshCatalog() {
    try {
      const result = await refresh({
        companyId: agent.companyId,
        runtimeId: boundRuntimeId,
        provider,
      }).unwrap();
      if (result.state === "failed") {
        toast.error("Runtime discovery could not be refreshed.");
        return;
      }
      // A skipped query has nothing to refetch — RTK Query throws on it.
      if (boundRuntimeId) {
        await Promise.all([plugins.refetch(), connectors.refetch()]);
      }
      toast.success(
        result.state === "complete"
          ? "Runtime discovery refreshed"
          : "Refresh requested. Discovery will update shortly.",
      );
    } catch {
      toast.error("Check the runtime’s computer connection and try again.");
    }
  }
  const notices = [
    ...new Set(
      [discoveryNote(plugins), discoveryNote(connectors)].filter(
        (note): note is string => Boolean(note),
      ),
    ),
  ];
  // Discovery is read from the viewer's own computer (ADR 0014), so there is
  // nothing to refresh until the Agent is set up there and it is online.
  const refreshDisabled =
    !canRefreshDiscovery ||
    !boundRuntimeId ||
    !agent.runtimeAvailable ||
    !shouldUseDesktopHashRouter() ||
    refreshState.isLoading;
  const refreshHint = !shouldUseDesktopHashRouter()
    ? "Open Zenith Team Desktop on your computer to refresh"
    : !boundRuntimeId
      ? `Set ${agent.name} up on your computer to refresh`
      : !agent.runtimeAvailable
        ? "Your computer must be online to refresh"
        : !canRefreshDiscovery
          ? `Open Zenith Team Desktop on ${agentBindingLabel(agent)} to refresh`
          : "Refresh what your computer provides";
  return (
    <div className="agent-capabilities-layout">
      <div className="agent-capabilities-main" ref={mainRef}>
        <div className="flex items-center gap-[16px] flex-wrap">
          <label className="agent-capabilities-search">
            <Search size={17} />
            <input
              aria-label="Search capabilities"
              placeholder="Search capabilities…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <div className="agent-capabilities-source">
            <Monitor size={16} />
            {runtimeLink ? (
              <Link to={runtimeLink}>
                {providerLabel(provider)} · {agentBindingLabel(agent)}
              </Link>
            ) : (
              <span>
                {providerLabel(provider)} · {agentBindingLabel(agent)}
              </span>
            )}
            <span
              className="agent-capabilities-runtime-status"
              data-online={agent.runtimeAvailable}
              title={agentAvailabilityHelp(agent)}
            >
              {agentAvailabilityLabel(agent)}
            </span>
          </div>
          <button
            type="button"
            className="dashboard-button"
            title={refreshHint}
            disabled={refreshDisabled}
            onClick={() => void refreshCatalog()}
          >
            <RefreshCw size={14} />
            {refreshState.isLoading ? "Refreshing…" : "Refresh"}
          </button>
        </div>
        <nav
          ref={navigationRef}
          className="agent-capabilities-jumps"
          aria-label="Jump to capability section"
        >
          {["Built-in", "Skills", "Connections", "Plugins"].map((title) => (
            <button
              type="button"
              key={title}
              aria-current={
                activeSection === `capabilities-${title.toLowerCase()}`
                  ? "location"
                  : undefined
              }
              onClick={() =>
                mainRef.current
                  ?.querySelector(`#capabilities-${title.toLowerCase()}`)
                  ?.scrollIntoView({
                    block: "start",
                    behavior: window.matchMedia(
                      "(prefers-reduced-motion: reduce)",
                    ).matches
                      ? "instant"
                      : "smooth",
                  })
              }
            >
              {title}
            </button>
          ))}
        </nav>
        {notices.length > 0 && (
          <div className="agent-capabilities-notices" role="status">
            {notices.map((note) => (
              <p key={note}>{note}</p>
            ))}
            {boundRuntimeId && (plugins.isError || connectors.isError) ? (
              <button
                type="button"
                className="dashboard-button"
                onClick={() =>
                  void Promise.all([plugins.refetch(), connectors.refetch()])
                }
              >
                Retry loading
              </button>
            ) : null}
          </div>
        )}
        {!owner && (
          <p className="agent-capabilities-note">
            You can view this agent’s capabilities. Configuration access is
            required to change them.
          </p>
        )}
        <CapabilityCards
          sectionId="capabilities-built-in"
          title="Built-in"
          badge="Zenith Team"
          icon={Layers}
          description="Tools included with Zenith Team."
          builtIn
          search={search}
          items={[
            ...platformSkillCards(platformSkills.currentData?.items ?? []),
            ...builtInToolGroups,
          ]}
          note={
            platformSkills.isError
              ? "Built-in tools could not be loaded."
              : platformSkills.isLoading
                ? "Loading built-in tools…"
                : undefined
          }
          {...controls}
        >
          {"zenith studio create images video voice audio music".includes(
            search.trim().toLowerCase(),
          ) && (
            <ManagedTools
              agent={current}
              editable={owner}
              busy={saveState.isLoading}
              onToggle={controls.onToggle}
            />
          )}
        </CapabilityCards>
        <CustomSkills agent={current} search={search} />
        <CapabilityCards
          sectionId="capabilities-plugin-skills"
          title="Plugin skills"
          managedInPlugins
          badge="Inherited"
          icon={BookOpen}
          description="Skills supplied by installed plugins. Manage each plugin once in Plugins; its setting applies to all of its skills."
          empty={emptyNote(plugins, "Skills")}
          items={skillCards}
          search={search}
          runtimeLink={runtimeLink && `${runtimeLink}&tab=skills`}
          {...controls}
        />
        <CapabilityCards
          sectionId="capabilities-connections"
          title="Connections"
          badge="MCP"
          icon={Plug}
          description="Allow a connection for future runs. Its status also depends on company access and setup on your computer."
          empty={emptyNote(connectors, "Connections")}
          items={mcpCards}
          search={search}
          runtimeLink={runtimeLink && `${runtimeLink}&tab=mcp`}
          {...controls}
        />
        <CapabilityCards
          sectionId="capabilities-plugins"
          title="Plugins"
          badge="From runtime"
          icon={Puzzle}
          description="Installed extensions. Enabling a plugin does not grant connection access."
          empty={emptyNote(plugins, "Plugins")}
          items={pluginCards}
          search={search}
          runtimeLink={runtimeLink && `${runtimeLink}&tab=plugins`}
          {...controls}
        />
        <details className="agent-capabilities-help mt-[28px] text-workspace-caption text-workspace-muted max-w-[640px]">
          <summary>When changes apply</summary>
          <p>
            Settings apply to new task and direct-chat runs. Queued runs keep
            their settings. Scheduled work keeps its reviewed access.
          </p>
          <Link to={`/agents/${agent.id}?tab=settings`}>Change runtime</Link>
        </details>
      </div>
    </div>
  );
}
