import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import {
  RuntimeCapabilitiesPanel,
  type CapabilitySection,
} from "../components/runtime-capabilities-panel";
import "../styles/settings.css";
import "../styles/runtime-directory.css";
import {
  LayoutGrid,
  ListTodo,
  Puzzle,
  BookOpen,
  Plug,
} from "@/shared/ui/icons";
import { type ReactNode, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";

import { readSessionSnapshot } from "@/shared/session/session";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { viewerIsRegistrant } from "@/features/settings/model/runtime-registrant";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import {
  getCeoAgentApiErrorMessage,
  useGetRuntimeQuery,
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
  useListRuntimeActiveRunsQuery,
  useListRuntimeAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import {
  providerLabel,
  runtimeDisplayName,
  runtimeIdentity,
  runtimeSignInLabel,
  runtimeStateLabel,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { RemoveRuntimeDialog } from "@/features/settings/components/remove-runtime-dialog";
import { RenameRuntimeDialog } from "@/features/settings/components/rename-runtime-dialog";

import { useRuntimeSetupRefresh } from "@/features/settings/model/use-runtime-setup-refresh";
import {
  launchedByLabel,
  runtimeComputerName,
} from "@/features/settings/model/runtime-management";
import { getSettingsTab } from "@/features/settings/model/settings-navigation";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { AgentDto, RunDto, RuntimeDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { AgentSetupReturn } from "../components/agent-setup-return";

const AGENT_SETTINGS_HREF =
  getSettingsTab("agents")?.href ?? "/settings/agents";

const timestampFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

type RuntimeDialog = "remove" | "rename" | null;

// Settings → Computers: one runtime, read with one request each for the
// row, the viewer's bindings on it, and the runs on the machine. Only its
// registrant can open it (ADR 0014). Keyed by company and runtime, so
// switching either remounts the reads and the dialogs' local state.
export function RuntimeDetailPage({ runtimeId }: { runtimeId: string }) {
  const companyId = readSessionSnapshot().company?.id;

  if (!companyId) {
    return (
      <RuntimeDetailFrame>
        <p className="agent-runtime-empty" role="status">
          Company context is unavailable.
        </p>
      </RuntimeDetailFrame>
    );
  }

  return (
    <RuntimeDetailContent
      companyId={companyId}
      key={`${companyId}:${runtimeId}`}
      runtimeId={runtimeId}
    />
  );
}

function RuntimeDetailContent({
  companyId,
  runtimeId,
}: {
  companyId: string;
  runtimeId: string;
}) {
  const { daemonId: localDaemonId } = useLocalDaemonId();
  const navigate = useNavigate();
  const session = useSessionSnapshot();
  const [params, setParams] = useSearchParams();
  const tab = ["overview", "work", "plugins", "skills", "mcp"].includes(
    params.get("tab") ?? "",
  )
    ? params.get("tab")!
    : "overview";
  const [dialog, setDialog] = useState<RuntimeDialog>(null);
  const setupRefresh = useRuntimeSetupRefresh(companyId);
  const refreshPending = setupRefresh.isPending(runtimeId);
  const refreshSetup = () => setupRefresh.refresh(runtimeId, runtimeId);
  const runtimeQuery = useGetRuntimeQuery(
    { companyId, runtimeId },
    { refetchOnMountOrArgChange: 10 },
  );
  // The viewer's own bindings on this computer, named from the company
  // roster (ADR 0014): nobody else's setup is ever on it.
  const bindingsQuery = useListRuntimeAgentsQuery(
    { companyId, runtimeId },
    { refetchOnMountOrArgChange: 10 },
  );
  const agentRosterQuery = useListCompanyAgentsQuery(companyId);
  // Work on the machine is an execution read; every runtime the viewer can
  // open is one they registered (ADR 0014), so no further gate is needed.
  const runsQuery = useListRuntimeActiveRunsQuery(
    { companyId, runtimeId },
    { refetchOnMountOrArgChange: 10 },
  );
  const rosterQuery = useListAgentRuntimesQuery(companyId);

  // A refetch after the roster's `remove` answers 404 while the last good
  // row is still cached, so the error is judged before the data.
  const runtimeError = runtimeQuery.error as BoardlyApiError | undefined;
  if (runtimeError?.code === "runtime_not_found") {
    return (
      <RuntimeDetailFrame>
        <p className="agent-runtime-empty" role="status">
          This runtime is no longer registered. It was removed, or its machine
          registered it again under a new id.
        </p>
      </RuntimeDetailFrame>
    );
  }

  const runtime = runtimeQuery.currentData;
  // A machine is its registrant's alone (ADR 0014): nobody else can see this
  // page, so renaming, refreshing and removing it need no Company
  // permission — being the registrant is the permission.
  const canManageRuntimes = viewerIsRegistrant(runtime ?? {}, session.userId);
  if (!runtime) {
    return (
      <RuntimeDetailFrame>
        {runtimeQuery.isError ? (
          <p className="agent-runtime-error" role="alert">
            {getCeoAgentApiErrorMessage(
              runtimeError,
              "The runtime could not be loaded.",
            )}{" "}
            <button
              className="runtime-detail__retry"
              type="button"
              onClick={() => void runtimeQuery.refetch()}
            >
              Retry
            </button>
          </p>
        ) : (
          <p className="agent-runtime-empty" role="status">
            Loading runtime…
          </p>
        )}
      </RuntimeDetailFrame>
    );
  }

  const displayName = runtimeDisplayName(runtime);
  const computerName = runtimeComputerName(runtime);
  const online = runtime.status === "online";
  const agentById = new Map(
    (agentRosterQuery.currentData?.items ?? []).map((agent) => [
      agent.id,
      agent,
    ]),
  );
  const agents = (bindingsQuery.currentData?.items ?? []).flatMap(
    (binding) => agentById.get(binding.agentId) ?? [],
  );
  const runs = runsQuery.currentData ?? [];
  const runtimes = rosterQuery.currentData?.items ?? [];
  const siblings = runtimes.filter(
    (candidate) =>
      candidate.daemonId === runtime.daemonId && candidate.id !== runtime.id,
  );

  return (
    <RuntimeDetailFrame>
      <header className="agent-glass-card runtime-detail__header">
        <div className="runtime-detail__heading">
          <span
            aria-hidden="true"
            className={`agent-provider-logo agent-provider-logo--${runtime.provider}`}
          >
            <img alt="" src={`/images/settings/${runtime.provider}.svg`} />
          </span>
          <div className="min-w-0 flex-1">
            <h1 title={displayName}>{providerLabel(runtime.provider)}</h1>
            <p>
              {runtimeDisplayName(runtime)} · {runtimeIdentity(runtime)}
              {runtime.customName ? ` · daemon name ${runtime.name}` : ""}
            </p>
          </div>
          <RuntimeStatusPill runtime={runtime} />
        </div>
        {runtime.unusableReason ? (
          <p className="runtime-detail__note" role="note">
            {runtime.unusableReason.message}
          </p>
        ) : null}
        <div className="runtime-detail__actions">
          <button
            className="support-action-button"
            disabled={!online || refreshPending}
            title={
              online
                ? "Check the provider CLIs on this computer again: version and sign-in."
                : "The computer is offline, so it cannot check its setup."
            }
            type="button"
            onClick={() => void refreshSetup()}
          >
            {refreshPending ? "Checking setup…" : "Refresh setup"}
          </button>
          <button
            className="support-action-button"
            type="button"
            onClick={() => setDialog("rename")}
          >
            Rename computer
          </button>
          {canManageRuntimes ? (
            <button
              className="support-action-button support-action-button--danger"
              title={
                online
                  ? "Stop the agent service on its computer before removing this runtime"
                  : "Remove runtime"
              }
              disabled={online}
              type="button"
              onClick={() => setDialog("remove")}
            >
              Remove
            </button>
          ) : null}
        </div>
        {online && canManageRuntimes ? (
          // The daemon runs on the computer, so the hint names the computer
          // and the way its daemon was started: the desktop app stops it
          // when it quits, a terminal start needs the CLI. On this computer
          // it is named as Updates & Support labels it: the Agent service.
          <p className="runtime-detail__note">
            {runtime.launchedBy !== "desktop" ? (
              <>
                To remove this runtime, first run <code>zen daemon stop</code>{" "}
                on {computerName}.
              </>
            ) : localDaemonId === runtime.daemonId ? (
              <>
                To remove this runtime, first stop the agent service on this
                computer.{" "}
                <Link
                  className="text-[var(--workspace-native-accent)] hover:underline"
                  to="/settings/support"
                >
                  Open Updates &amp; Support
                </Link>
              </>
            ) : (
              `To remove this runtime, first quit Zenith Team on ${computerName}.`
            )}
          </p>
        ) : null}
        <div className="runtime-provider-switcher">
          {[runtime, ...siblings]
            .sort((a, b) => a.provider.localeCompare(b.provider))
            .map((candidate) => {
              // An online provider that cannot run — signed out, or its
              // sign-in unknown — shows that verdict instead of a green
              // Online, as the Computers card does.
              const signIn =
                candidate.status === "online"
                  ? runtimeSignInLabel(candidate)
                  : null;
              return (
                <Link
                  key={candidate.id}
                  className="dashboard-button"
                  aria-current={
                    candidate.id === runtime.id ? "page" : undefined
                  }
                  to={`/settings/agents/${candidate.id}?${params}`}
                >
                  <img
                    alt=""
                    src={`/images/settings/${candidate.provider}.svg`}
                  />
                  {providerLabel(candidate.provider)}
                  <span
                    className={cx(
                      "runtime-provider-status",
                      signIn && "runtime-provider-sign-in",
                    )}
                    data-online={candidate.status === "online" && !signIn}
                    data-sign-in={signIn ? candidate.signIn?.state : undefined}
                    title={signIn ? candidate.signIn?.detail : undefined}
                  >
                    {signIn ?? runtimeStateLabel(candidate)}
                  </span>
                </Link>
              );
            })}
        </div>
      </header>

      <nav className="runtime-detail-tabs" aria-label="Runtime sections">
        {[
          { id: "overview", label: "Overview", Icon: LayoutGrid },
          { id: "work", label: "Work", Icon: ListTodo },
          { id: "plugins", label: "Plugins", Icon: Puzzle },
          { id: "skills", label: "Skill bundles", Icon: BookOpen },
          { id: "mcp", label: "MCP", Icon: Plug },
        ].map(({ id, label, Icon }) => (
          <button
            key={id}
            aria-current={tab === id ? "page" : undefined}
            onClick={() =>
              setParams((current) => {
                const next = new URLSearchParams(current);
                next.set("tab", id);
                next.delete("q");
                return next;
              })
            }
          >
            <Icon size={14} />
            {label}
          </button>
        ))}
      </nav>
      {tab === "plugins" || tab === "skills" || tab === "mcp" ? (
        <RuntimeCapabilitiesPanel
          key={`${runtime.id}:${tab}`}
          companyId={companyId}
          runtime={runtime}
          section={tab as CapabilitySection}
        />
      ) : null}
      <div className="runtime-overview-layout">
        {tab === "overview" ? (
          <section
            aria-labelledby="runtime-detail-facts-title"
            className="runtime-detail__section runtime-overview-facts"
          >
            <h3 id="runtime-detail-facts-title">Connection details</h3>
            <dl className="runtime-detail__facts">
              <Fact label="Provider">{providerLabel(runtime.provider)}</Fact>
              <Fact label="Provider CLI version">
                {runtime.providerVersion || "Not reported"}
              </Fact>
              <Fact label="Provider sign-in">
                {runtime.signIn
                  ? `${runtime.signIn.detail} Checked ${formatTimestamp(runtime.signIn.checkedAt)}.`
                  : "Not checked yet"}
              </Fact>
              <Fact label="Zenith Team CLI version">
                {runtime.cliVersion || "Not reported"}
              </Fact>
              <Fact label="Launched by">
                {launchedByLabel(runtime.launchedBy)}
              </Fact>
              <Fact label="Registered by">
                {runtime.owner?.name?.trim() || "Unknown"}
              </Fact>
              <Fact label="Device">
                {runtime.deviceInfo?.trim() || "Not reported"}
              </Fact>
              <Fact label="Last seen">
                {formatTimestamp(runtime.lastSeenAt)}
              </Fact>
              <Fact label="Registered">
                {formatTimestamp(runtime.createdAt)}
              </Fact>
              <Fact label="Also on this machine">
                {siblings.length > 0
                  ? siblings
                      .map(
                        (sibling) =>
                          `${runtimeDisplayName(sibling)} (${providerLabel(sibling.provider)})`,
                      )
                      .join(", ")
                  : "No other runtime"}
              </Fact>
            </dl>
          </section>
        ) : null}
        {tab === "overview" ? (
          <section
            aria-labelledby="runtime-detail-agents-title"
            className="agent-glass-card runtime-detail__section"
          >
            <h3 id="runtime-detail-agents-title">Configured agents</h3>
            <ListState
              empty="You have no agents set up on this runtime."
              error={bindingsQuery.isError || agentRosterQuery.isError}
              fallback="The agents could not be loaded."
              items={agents}
              loading={
                bindingsQuery.currentData === undefined ||
                agentRosterQuery.currentData === undefined
              }
              render={(agent) => <BoundAgentRow agent={agent} />}
            />
          </section>
        ) : null}
      </div>
      {tab === "work" ? (
        <section
          aria-labelledby="runtime-detail-runs-title"
          className="agent-glass-card runtime-detail__section"
        >
          <h3 id="runtime-detail-runs-title">Active runs</h3>
          <ListState
            empty="No runs are active on this machine."
            error={runsQuery.isError}
            fallback="The runs could not be loaded."
            items={runs}
            loading={runsQuery.currentData === undefined}
            render={(run) => (
              <ActiveRunRow
                agentName={agentById.get(run.agentId)?.name}
                run={run}
              />
            )}
          />
        </section>
      ) : null}

      <RenameRuntimeDialog
        companyId={companyId}
        open={dialog === "rename"}
        runtime={runtime}
        onOpenChange={(open) => setDialog(open ? "rename" : null)}
      />

      <RemoveRuntimeDialog
        companyId={companyId}
        open={dialog === "remove"}
        runtime={runtime}
        runtimes={runtimes}
        onOpenChange={(open) => setDialog(open ? "remove" : null)}
        onRemoved={() => void navigate(AGENT_SETTINGS_HREF)}
      />
    </RuntimeDetailFrame>
  );
}

function RuntimeDetailFrame({ children }: { children: ReactNode }) {
  return (
    <main className="runtime-detail" aria-label="Runtime details">
      <AgentSetupReturn />
      {children}
    </main>
  );
}

// The header says what the switcher chip for this runtime says: an online
// provider that cannot run — signed out, or its sign-in unknown — shows that
// verdict, never a green Online.
function RuntimeStatusPill({ runtime }: { runtime: RuntimeDto }) {
  const online = runtime.status === "online";
  const signIn = online ? runtimeSignInLabel(runtime) : null;
  return (
    <span
      className={cx(
        "agent-runtime-connection-status inline-flex min-h-6 shrink-0 items-center gap-1.5 rounded-full border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 text-xs font-medium",
        online && !signIn
          ? "text-[var(--workspace-native-success)]"
          : online && runtime.signIn?.state === "signed_out"
            ? "text-[var(--workspace-native-danger)]"
            : "text-[var(--workspace-native-text-muted)]",
      )}
      data-state={online ? "connected" : "disconnected"}
      data-sign-in={signIn ? runtime.signIn?.state : undefined}
      role="status"
      title={signIn ? runtime.signIn?.detail : undefined}
    >
      <span aria-hidden="true" className="size-1.5 rounded-full bg-current" />
      {signIn ?? runtimeStateLabel(runtime)}
    </span>
  );
}

function Fact({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function ListState<Item extends { id: number | string }>({
  empty,
  error,
  fallback,
  items,
  loading,
  render,
}: {
  empty: string;
  error: boolean;
  fallback: string;
  items: Item[];
  loading: boolean;
  render: (item: Item) => ReactNode;
}) {
  if (error && items.length === 0) {
    return (
      <p className="agent-runtime-error" role="alert">
        {fallback}
      </p>
    );
  }
  if (loading) {
    return (
      <p className="runtime-detail__empty" role="status">
        Loading…
      </p>
    );
  }
  if (items.length === 0) {
    return <p className="runtime-detail__empty">{empty}</p>;
  }
  return (
    <ul className="runtime-detail__list">
      {items.map((item) => (
        <li key={item.id}>{render(item)}</li>
      ))}
    </ul>
  );
}

// An agent the viewer runs here. Configuring the template is its creator's
// and the agents.manage holders' (ADR 0013); anyone else gets its
// capabilities to read, where the page says who can change it.
function BoundAgentRow({ agent }: { agent: AgentDto }) {
  const configure = agent.access.configure;
  const href = `/agents/${agent.id}?tab=${configure ? "settings" : "capabilities"}`;
  return (
    <>
      <FoldAvatar agentId={agent.id} size={32} paused />
      <div className="min-w-0 flex-1">
        <Link to={href}>
          <strong>{agent.name}</strong>
        </Link>
        <span>{agent.title}</span>
      </div>
      <span>
        {agent.activeRunCount}/{agent.maxConcurrentRuns} task runs
      </span>
      <Link
        className="dashboard-button"
        title={
          configure
            ? undefined
            : "Only the agent's creator or a company owner or admin can change it."
        }
        to={href}
      >
        {configure ? "Configure agent" : "View capabilities"}
      </Link>
    </>
  );
}

function ActiveRunRow({
  agentName,
  run,
}: {
  agentName: string | undefined;
  run: RunDto;
}) {
  return (
    <>
      <div className="min-w-0">
        <strong>Run {run.id}</strong>
        <span>
          {agentName ?? `Agent ${run.agentId.slice(0, 8)}`} ·{" "}
          {run.taskId ? "task" : "conversation"} · started{" "}
          {formatTimestamp(run.createdAt)}
        </span>
      </div>
      <span>{run.status.replaceAll("_", " ")}</span>
    </>
  );
}

function formatTimestamp(value: string | null | undefined): string {
  if (!value) return "Never";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : timestampFormatter.format(date);
}

export default RuntimeDetailPage;
