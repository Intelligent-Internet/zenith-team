import {
  useDeleteWorkspaceResourceMutation,
  useListWorkspaceResourcesQuery,
} from "@/shared/api/resource-api";
import {
  FolderOpen,
  Monitor,
  RefreshCw,
  Search,
  ChevronRight,
  Puzzle,
} from "@/shared/ui/icons";
import { useState } from "react";
import { Link } from "react-router";
import { readSessionSnapshot } from "@/shared/session/session";
import {
  getCeoAgentApiErrorMessage,
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import {
  providerLabel,
  runtimeDisplayName,
  runtimeSignInLabel,
  runtimeStateLabel,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { useRuntimeSetupRefresh } from "@/features/settings/model/use-runtime-setup-refresh";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import { runtimeDetailHref } from "@/features/settings/model/settings-navigation";
import type { CompanyResourceDto, RuntimeDto } from "@/shared/api/types";

import { publicAssetPath } from "@/shared/lib/public-assets";
import { useCan } from "@/shared/permissions";
import { toast } from "@/shared/ui/sonner";
import "../styles/runtime-directory.css";

export function AgentSettingsPage() {
  const companyId = readSessionSnapshot().company?.id;
  return (
    <AgentSettingsContent
      key={companyId ?? "no-company"}
      companyId={companyId ?? ""}
    />
  );
}

function AgentSettingsContent({ companyId }: { companyId: string }) {
  const canManagePlugins = useCan("plugins.manage");
  const runtimesQuery = useListAgentRuntimesQuery(companyId, {
    skip: !companyId,
  });
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const [search, setSearch] = useState("");
  const localDaemon = useLocalDaemonId();
  const setupRefresh = useRuntimeSetupRefresh(companyId);
  // Existing local-directory bindings, shown per computer with unlink.
  // Binding is not offered here: the managed-workspace direction retires
  // it (Workspaces PRD §7), and a bound directory is a machine-wide rule.
  const resourcesQuery = useListWorkspaceResourcesQuery(companyId, {
    skip: !companyId,
  });
  const boundDirectories = (resourcesQuery.currentData?.items ?? []).filter(
    (resource) => resource.resourceType === "local_directory",
  );
  const runtimes = runtimesQuery.currentData?.items ?? [];
  const isReloading =
    runtimesQuery.isFetching ||
    agentsQuery.isFetching ||
    resourcesQuery.isFetching;
  // Archived agents remain in the shell cache, but do not use this runtime.
  const agentCounts = new Map<string, number>();
  for (const agent of agentsQuery.currentData?.items ?? []) {
    if (!agent.archivedAt && agent.binding) {
      const runtimeId = agent.binding.runtime.id;
      agentCounts.set(runtimeId, (agentCounts.get(runtimeId) ?? 0) + 1);
    }
  }
  const searchTerm = search.trim().toLowerCase();
  const filteredRuntimes = runtimes.filter((runtime) =>
    `${runtimeDisplayName(runtime)} ${runtime.provider} ${runtime.deviceInfo}`
      .toLowerCase()
      .includes(searchTerm),
  );
  const groups = new Map<string, RuntimeDto[]>();
  for (const runtime of filteredRuntimes) {
    groups.set(runtime.daemonId, [
      ...(groups.get(runtime.daemonId) ?? []),
      runtime,
    ]);
  }
  return (
    <section className="runtime-directory-content" aria-label="Computers">
      <div className="runtime-directory-toolbar">
        <label className="runtime-directory-search">
          <Search size={18} aria-hidden="true" />
          <input
            type="search"
            aria-label="Search computers and runtimes"
            placeholder="Search computers or runtimes…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        <span className="runtime-directory-count" role="status">
          {runtimesQuery.currentData ? (
            <>
              {groups.size} {groups.size === 1 ? "computer" : "computers"} ·{" "}
              {filteredRuntimes.length}{" "}
              {filteredRuntimes.length === 1 ? "runtime" : "runtimes"}
            </>
          ) : null}
        </span>
        <div className="runtime-directory-actions">
          {canManagePlugins ? (
            <Link className="collection-toolbar-control" to="/settings/plugins">
              <Puzzle size={18} aria-hidden="true" />
              Plugins
            </Link>
          ) : null}
          <button
            aria-label={isReloading ? "Reloading list…" : "Reload list"}
            className="collection-toolbar-control collection-toolbar-control--icon"
            disabled={isReloading || !companyId}
            title="Reload list"
            type="button"
            onClick={() => {
              void runtimesQuery.refetch();
              void agentsQuery.refetch();
              void resourcesQuery.refetch();
            }}
          >
            <RefreshCw size={18} aria-hidden="true" />
          </button>
        </div>
      </div>
      {runtimesQuery.isError ? (
        <p role="alert">
          Computers could not be loaded. Reload the list to try again.
        </p>
      ) : runtimesQuery.isLoading ? (
        <p role="status">Loading connected computers…</p>
      ) : groups.size === 0 ? (
        <div className="dashboard-empty">
          <Monitor size={28} aria-hidden="true" />
          <p>
            {searchTerm
              ? "No matching computers or runtimes."
              : "No computers connected. Start the Zenith Team daemon to connect this computer."}
          </p>
        </div>
      ) : null}
      <div className="runtime-computer-list">
        {[...groups].map(([daemonId, group]) => {
          // One request covers every provider on the machine; any online
          // runtime of the computer names it. The API only lists the viewer's
          // own runtimes (ADR 0014), so the machine RPC needs no further gate.
          const onlineRuntime = runtimes.find(
            (runtime) =>
              runtime.daemonId === daemonId && runtime.status === "online",
          );
          const computerName =
            group[0].customName?.trim() ||
            group[0].deviceInfo?.split(" · ")[0]?.trim() ||
            runtimeDisplayName(group[0]);
          return (
            <section
              aria-labelledby={`runtime-computer-${daemonId}`}
              className="runtime-computer-group"
              key={daemonId}
            >
              <header className="runtime-computer-heading">
                <span className="runtime-computer-icon" aria-hidden="true">
                  <Monitor size={24} />
                </span>
                <div className="runtime-computer-identity">
                  <h2 id={`runtime-computer-${daemonId}`}>{computerName}</h2>
                  <p>
                    {daemonId === localDaemon.daemonId
                      ? "This computer"
                      : (group[0].owner?.name ?? "Registered computer")}{" "}
                    · {group.length}{" "}
                    {group.length === 1 ? "runtime" : "runtimes"}
                  </p>
                </div>
                <button
                  className="runtime-computer-refresh"
                  disabled={!onlineRuntime || setupRefresh.isPending(daemonId)}
                  title={
                    onlineRuntime
                      ? "Check provider versions and sign-in on this computer."
                      : "This computer is offline, so it cannot check its setup."
                  }
                  type="button"
                  onClick={() => {
                    if (onlineRuntime) {
                      void setupRefresh.refresh(daemonId, onlineRuntime.id);
                    }
                  }}
                >
                  <RefreshCw size={16} aria-hidden="true" />
                  {setupRefresh.isPending(daemonId)
                    ? "Checking setup…"
                    : "Check setup"}
                </button>
              </header>
              <div className="runtime-table-scroll">
                <table
                  aria-label={`Runtimes on ${computerName}`}
                  className="runtime-table collection-list"
                >
                  <colgroup>
                    <col />
                    <col className="runtime-table-agents-column" />
                    <col className="runtime-table-status-column" />
                    <col className="runtime-table-action-column" />
                  </colgroup>
                  <thead className="collection-list__header">
                    <tr>
                      <th scope="col">Runtime</th>
                      <th scope="col">Your agents</th>
                      <th scope="col">Status</th>
                      <th scope="col">
                        <span className="sr-only">Details</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...group]
                      .sort((a, b) => a.provider.localeCompare(b.provider))
                      .map((runtime) => (
                        <RuntimeRow
                          key={runtime.id}
                          runtime={runtime}
                          agentCount={
                            agentsQuery.currentData
                              ? (agentCounts.get(runtime.id) ?? 0)
                              : undefined
                          }
                        />
                      ))}
                  </tbody>
                </table>
              </div>
              {boundDirectories
                .filter(
                  (resource) => resource.resourceRef.daemonId === daemonId,
                )
                .map((resource) => (
                  <BoundDirectoryRow
                    companyId={companyId}
                    key={resource.id}
                    resource={resource}
                  />
                ))}
            </section>
          );
        })}
      </div>
    </section>
  );
}

function RuntimeRow({
  agentCount,
  runtime,
}: {
  agentCount: number | undefined;
  runtime: RuntimeDto;
}) {
  const signInLabel = runtimeSignInLabel(runtime);
  return (
    <tr className="runtime-provider-row collection-list__row">
      <td>
        <div className="collection-list__identity">
          <img
            className="runtime-provider-logo"
            alt=""
            src={publicAssetPath(`images/settings/${runtime.provider}.svg`)}
          />
          <div className="runtime-provider-identity">
            <Link
              aria-label={`View ${providerLabel(runtime.provider)} runtime`}
              className="runtime-provider-link collection-list__primary"
              to={runtimeDetailHref(runtime.id)}
            >
              {providerLabel(runtime.provider)}
            </Link>
            <p>{runtime.providerVersion || "Version not reported"}</p>
            {runtime.unusableReason ? (
              <p>{runtime.unusableReason.message}</p>
            ) : null}
            {runtime.signIn && runtime.signIn.state !== "signed_in" ? (
              <p className="runtime-provider-sign-in-detail">
                {runtime.signIn.detail}
              </p>
            ) : null}
          </div>
        </div>
      </td>
      <td className="runtime-provider-count">
        <span
          title={
            agentCount === undefined ? "Agent count unavailable" : undefined
          }
        >
          {agentCount ?? "—"}
        </span>
      </td>
      <td>
        <div className="runtime-provider-states">
          <span
            className="runtime-provider-status"
            data-online={runtime.status === "online"}
          >
            {runtimeStateLabel(runtime)}
          </span>
          {signInLabel ? (
            <span
              className="runtime-provider-status runtime-provider-sign-in"
              data-sign-in={runtime.signIn?.state}
              title={runtime.signIn?.detail}
            >
              {signInLabel}
            </span>
          ) : null}
        </div>
      </td>
      <td className="runtime-provider-chevron">
        <ChevronRight size={18} aria-hidden="true" />
      </td>
    </tr>
  );
}

// A local directory a Company Resource binds to this computer: every Run of
// the company on this machine executes in place there. The path itself is
// never shown (the API redacts it); the person sees the label they gave it
// and can unlink it, which removes the binding without touching the files.
function BoundDirectoryRow({
  companyId,
  resource,
}: {
  companyId: string;
  resource: CompanyResourceDto;
}) {
  const [confirming, setConfirming] = useState(false);
  const [unlink, unlinkState] = useDeleteWorkspaceResourceMutation();
  return (
    <article className="runtime-bound-directory">
      <FolderOpen size={28} aria-hidden="true" />
      <div className="runtime-provider-identity">
        <strong>{resource.label || "Bound directory"}</strong>
        <p>
          Runs of this company on this computer execute in place here, one at a
          time.
        </p>
      </div>
      {confirming ? (
        <>
          <button
            className="dashboard-button"
            disabled={unlinkState.isLoading}
            type="button"
            onClick={async () => {
              try {
                await unlink({
                  companyId,
                  resourceId: resource.id,
                  request: { expectedRevision: resource.revision },
                }).unwrap();
                toast.success(
                  "Directory unlinked. Its files were not touched.",
                );
              } catch (error) {
                toast.error(
                  getCeoAgentApiErrorMessage(
                    error,
                    "The directory could not be unlinked.",
                  ),
                );
              } finally {
                setConfirming(false);
              }
            }}
          >
            {unlinkState.isLoading ? "Unlinking…" : "Confirm unlink"}
          </button>
          <button
            className="dashboard-button"
            disabled={unlinkState.isLoading}
            type="button"
            onClick={() => setConfirming(false)}
          >
            Keep
          </button>
        </>
      ) : (
        <button
          className="dashboard-button"
          title="Stop running this company's work in this directory. The files stay where they are."
          type="button"
          onClick={() => setConfirming(true)}
        >
          Unlink
        </button>
      )}
    </article>
  );
}
