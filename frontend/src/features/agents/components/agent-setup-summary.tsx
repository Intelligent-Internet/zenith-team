import { useId, useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Check, ChevronDown, RefreshCw } from "@/shared/ui/icons";
import type { AgentDto } from "@/shared/api/types";
import {
  getCeoAgentApiErrorMessage,
  useRefreshRuntimeMutation,
} from "@/shared/api/agent-runtime-api";
import {
  providerLabel,
  runtimeDisplayName,
  runtimeMachineName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { useBindAgentDialog } from "../hooks/use-bind-agent-dialog";
import {
  runtimeCatalogQueryOptions,
  useRuntimeConnectorsQuery,
} from "@/features/settings/api/runtime-capabilities-api";
import { connectorSetup } from "@/features/settings/model/connector-setup";
import "../styles/agent-setup-summary.css";

export function AgentSetupSummary({
  agent,
  collapsible = false,
  variant = "default",
}: {
  agent: AgentDto;
  collapsible?: boolean;
  variant?: "default" | "compact";
}) {
  return (
    <AgentSetupContent
      key={`${agent.id}:${agent.binding?.runtime.id ?? "unbound"}`}
      agent={agent}
      collapsible={collapsible}
      variant={variant}
    />
  );
}

function AgentSetupContent({
  agent,
  collapsible,
  variant,
}: {
  agent: AgentDto;
  collapsible: boolean;
  variant: "default" | "compact";
}) {
  const compact = variant === "compact";
  const runtime = agent.binding?.runtime;
  const provider = providerLabel(agent.provider);
  const { dialog, openBindDialog } = useBindAgentDialog(agent.companyId);
  const [refreshRuntime, refresh] = useRefreshRuntimeMutation();
  const computerPath = runtime
    ? `/settings/agents/${runtime.id}?returnAgentId=${agent.id}`
    : "/settings/agents";
  const online = runtime?.status === "online";
  const signedIn = runtime?.signIn?.state;
  const confirmed =
    agent.runtimeAvailable &&
    online &&
    signedIn === "signed_in" &&
    !runtime?.unusableReason &&
    !agent.archivedAt;
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsId = useId();
  const expanded = (!collapsible && !compact) || !confirmed || detailsOpen;
  const status = agent.archivedAt
    ? "Archived"
    : !runtime
      ? "Choose a computer"
      : runtime.unusableReason
        ? "Computer needs attention"
        : !online
          ? "Computer offline"
          : signedIn === "signed_out"
            ? `${provider} sign-in required`
            : signedIn !== "signed_in"
              ? `${provider} sign-in unconfirmed`
              : !agent.runtimeAvailable
                ? "Checking availability"
                : "Connected";
  const steps = [
    {
      title: "Computer",
      status: !runtime
        ? "Not selected"
        : runtime.unusableReason
          ? "Needs attention"
          : online
            ? "Online"
            : "Offline",
      detail: !runtime
        ? "Choose one of your own computers."
        : `${runtimeDisplayName(runtime)}${online || runtime.unusableReason ? "" : ". Open Zenith Team there to reconnect."}`,
      tone: online && !runtime?.unusableReason ? "ready" : "pending",
    },
    {
      title: provider,
      status: !runtime
        ? "Not checked"
        : runtime.unusableReason
          ? "Unavailable"
          : !online
            ? "Not checked"
            : signedIn === "signed_in"
              ? "Signed in"
              : signedIn === "signed_out"
                ? "Sign in required"
                : "Sign-in unknown",
      detail:
        runtime?.unusableReason?.message ||
        (!runtime
          ? "Select a computer to check provider sign-in."
          : !online
            ? "Reconnect your computer to check provider sign-in."
            : signedIn === "signed_out"
              ? `Sign in to ${provider} on that computer.`
              : signedIn === "signed_in"
                ? "Provider sign-in was confirmed by your computer."
                : "Your computer has not confirmed sign-in. Refresh to check again."),
      tone:
        online && signedIn === "signed_in" && !runtime?.unusableReason
          ? "ready"
          : "pending",
    },
    {
      title: "Agent setup",
      status: agent.archivedAt
        ? "Archived"
        : runtime
          ? "Configured"
          : "Not set up",
      detail: agent.archivedAt
        ? "Restore this agent before setting it up."
        : runtime
          ? `${agent.name} is assigned to your selected computer.`
          : "This setup is yours; other members choose their own computers.",
      tone: runtime && !agent.archivedAt ? "ready" : "pending",
    },
  ];

  return (
    <section
      className="agent-setup-summary"
      aria-label={`Your setup for ${agent.name}`}
      data-collapsible={collapsible || undefined}
      data-compact={compact || undefined}
    >
      <header>
        <h2>
          {collapsible && confirmed && !compact ? (
            <button
              type="button"
              className="agent-setup-toggle"
              aria-expanded={expanded}
              aria-controls={detailsId}
              onClick={() => setDetailsOpen((value) => !value)}
            >
              Your setup <ChevronDown aria-hidden size={14} />
            </button>
          ) : (
            "Your setup"
          )}
        </h2>
        {runtime && expanded ? (
          <button
            type="button"
            className="agent-setup-refresh"
            disabled={!online || refresh.isLoading}
            aria-label="Refresh computer and provider status"
            title={
              online
                ? "Ask your computer to check its status"
                : "Your computer must be online to refresh"
            }
            onClick={() =>
              void refreshRuntime({
                companyId: agent.companyId,
                runtimeId: runtime.id,
              })
            }
          >
            <RefreshCw aria-hidden size={14} />{" "}
            {refresh.isLoading ? "Requesting…" : "Refresh"}
          </button>
        ) : null}
      </header>
      {compact ? (
        <div className="agent-setup-compact-row flex flex-wrap items-center gap-[10px_14px] mt-[16px]">
          {runtime ? (
            <span className="agent-setup-computer-name">
              {runtimeMachineName(runtime)}
            </span>
          ) : null}
          <div
            className="agent-setup-status"
            data-confirmed={confirmed}
            role="status"
          >
            {confirmed ? <Check aria-hidden size={14} /> : null}
            <span>{status}</span>
          </div>
          {confirmed ? (
            <button
              type="button"
              className="dashboard-button agent-setup-toggle"
              aria-expanded={expanded}
              aria-controls={detailsId}
              onClick={() => setDetailsOpen((value) => !value)}
            >
              Details <ChevronDown aria-hidden size={14} />
            </button>
          ) : null}
        </div>
      ) : collapsible ? (
        <div
          className="agent-setup-status"
          data-confirmed={confirmed}
          role="status"
        >
          {confirmed ? <Check aria-hidden size={14} /> : null}
          <span>{status}</span>
          {confirmed ? <span>{provider} signed in</span> : null}
        </div>
      ) : null}
      <div id={detailsId} hidden={!expanded}>
        <ol>
          {steps.map((step, index) => (
            <li key={step.title} data-tone={step.tone}>
              {!compact ? (
                <span
                  className="grid w-[24px] h-[24px] place-items-center rounded-[50%] [background:var(--workspace-control-bg)] text-workspace-caption text-workspace-muted"
                  aria-hidden
                >
                  {index + 1}
                </span>
              ) : null}
              <div>
                <div className="agent-setup-step-heading">
                  <strong>{step.title}</strong>
                  <span>{step.status}</span>
                </div>
                {!compact || step.tone !== "ready" ? (
                  <p>{step.detail}</p>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
        {runtime?.signIn?.checkedAt ? (
          <p className="agent-setup-checked">
            Sign-in checked{" "}
            <time dateTime={runtime.signIn.checkedAt}>
              {new Date(runtime.signIn.checkedAt).toLocaleString(undefined, {
                month: "short",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </time>
          </p>
        ) : null}
        <div className="agent-setup-actions">
          {!agent.archivedAt ? (
            <button
              type="button"
              className="dashboard-button"
              onClick={() => openBindDialog(agent)}
            >
              {runtime ? "Change computer" : "Choose computer"}
            </button>
          ) : null}
          <Link to={computerPath}>
            {runtime ? "Computer details" : "Open Computers"}{" "}
            <ArrowUpRight aria-hidden size={13} />
          </Link>
        </div>
        {refresh.isError ? (
          <p role="alert">
            {getCeoAgentApiErrorMessage(
              refresh.error,
              "Could not request a status refresh.",
            )}
          </p>
        ) : refresh.isSuccess ? (
          <p role="status">
            Refresh requested. Status updates when your computer responds.
          </p>
        ) : null}
        {!compact ? <AgentConnectionSummary agent={agent} /> : null}
      </div>
      {compact ? <AgentConnectionSummary agent={agent} compact /> : null}
      {dialog}
    </section>
  );
}

function AgentConnectionSummary({
  agent,
  compact = false,
}: {
  agent: AgentDto;
  compact?: boolean;
}) {
  const runtime = agent.binding?.runtime;
  const query = useRuntimeConnectorsQuery(
    {
      companyId: agent.companyId,
      provider: agent.provider,
      runtimeId: runtime?.id ?? "",
    },
    { ...runtimeCatalogQueryOptions, skip: !runtime },
  );
  const catalog = query.currentData;
  const connections =
    catalog?.connectors.filter((item) => item.source.endsWith("_mcp")) ?? [];
  const allowed = connections.filter(
    (item) => !agent.capabilities.disabledMcpServers.includes(item.id),
  );
  const ready = allowed.filter((item) => connectorSetup(item).ready).length;
  const unavailable = allowed.length - ready;
  const disabled = connections.length - allowed.length;
  const detail = !runtime
    ? "Choose a computer to check its connections."
    : runtime.status !== "online"
      ? "Reconnect your computer to check connection status."
      : runtime.unusableReason
        ? runtime.unusableReason.message
        : !agent.runtimeAvailable
          ? `Confirm ${providerLabel(agent.provider)} sign-in on your computer before using its connections.`
          : query.isError
            ? "Connection status could not be loaded."
            : !catalog
              ? "Checking connections…"
              : catalog.runtime?.id !== runtime.id ||
                  catalog.stale ||
                  !catalog.runtime.online
                ? "Connection status needs a fresh report from your computer."
                : connections.length === 0
                  ? "No MCP connections reported. Add one when a task needs a source."
                  : `${ready} ready · ${unavailable} not ready${disabled ? ` · ${disabled} disabled for this agent` : ""}.`;
  return (
    <div
      className="agent-setup-connections"
      data-compact={compact || undefined}
    >
      <strong>{compact ? "Connections" : "MCP connections"}</strong>
      <p role="status">{detail}</p>
      <Link to={`/agents/${agent.id}?tab=capabilities&section=connections`}>
        {compact ? "View in Capabilities" : "View connections"}{" "}
        <ArrowUpRight aria-hidden size={13} />
      </Link>
      {query.isError ? (
        <button
          className="agent-setup-refresh"
          type="button"
          onClick={() => void query.refetch()}
        >
          Retry
        </button>
      ) : null}
    </div>
  );
}
