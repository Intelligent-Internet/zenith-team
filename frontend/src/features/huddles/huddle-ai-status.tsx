import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import {
  ChevronDown,
  Info,
  LoaderCircle,
  MessageSquareText,
  Mic,
  RefreshCw,
  Sparkles,
  Wallet,
  X,
} from "@/shared/ui/icons";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { useCompanyPermissions } from "@/shared/permissions";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import {
  useGetHuddleAIQuery,
  useSetHuddleAIMutation,
  useGetHuddleBudgetQuery,
  useSetHuddleBudgetMutation,
} from "./huddle-ai-api";
import "./huddle-ai.css";
import { HuddleAgentAvatar } from "./huddle-agent-avatar";

export type HeliaDisplayState =
  | "pending"
  | "starting"
  | "listening"
  | "speaking"
  | "unavailable"
  | "disabled"
  | "ended";

export const huddleAgentStateLabels: Record<HeliaDisplayState, string> = {
  pending: "Joining…",
  starting: "Connecting…",
  listening: "Listening",
  speaking: "Speaking",
  unavailable: "Unavailable",
  disabled: "Not in call",
  ended: "Call ended",
};

const unavailableDescriptions = {
  budget:
    "This call’s AI allowance has run out, or the Company has too little AI budget available. An admin can review the budget.",
  service_disabled:
    "The voice agent service is turned off on this server. Ask your administrator to enable it.",
  connection:
    "The Agent could not connect or lost the connection. Check that the voice agent and LiveKit are running, then try again.",
  agent_unavailable:
    "The Agent or its shared-context permission is no longer available. Check Voice in the Agent profile and Company access, then invite again.",
  context_sharer_left:
    "Company context is off: the person who shared it left the call.",
};

const stopDescriptions = {
  provider_error: "The voice provider reported an unrecoverable error.",
  provider_closed: "The voice provider closed this session.",
  control_unavailable:
    "The Agent could not complete an API request safely. Check the API connection and try again.",
  authority_revoked: "This voice session no longer has permission to continue.",
  lease_expired:
    "The voice session reached its authorization or call deadline.",
  reasoning_budget: "This call’s reasoning allowance reached its safety limit.",
  microphone_error: "The Agent’s audio input stopped unexpectedly.",
  worker_shutdown: "The voice worker was stopped or restarted.",
  internal_error: "The voice worker encountered an internal error.",
  agent_left: "The Agent left at a participant’s request.",
};

/** Shared Helia status for the header pill, the stage tile and hints. */
// The status poll is only a fallback for frames lost while realtime was down.
export const HUDDLE_STATUS_FALLBACK_POLL_MS = 15_000;

export function useHeliaStatus(companyId: string, huddleId: string) {
  const query = useGetHuddleAIQuery(
    { companyId, huddleId },
    {
      pollingInterval: HUDDLE_STATUS_FALLBACK_POLL_MS,
      refetchOnFocus: true,
      refetchOnReconnect: true,
      refetchOnMountOrArgChange: true,
      skip: !companyId || !huddleId,
    },
  );
  const { refetch } = query;
  // The server signals call and voice agent status changes to the people in
  // the call; a roster change can also move the agent between states.
  useEffect(() => {
    if (!companyId || !huddleId) return undefined;
    const removeRealtime = boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === companyId &&
        (event.type === "huddle.status_changed" ||
          event.type === "huddle.roster_changed") &&
        (event.payload as { huddleId?: string }).huddleId === huddleId
      )
        void refetch();
    });
    const removeRecovery = boardlyRealtimeClient.onRecovery(
      () => void refetch(),
    );
    return () => {
      removeRealtime();
      removeRecovery();
    };
  }, [companyId, huddleId, refetch]);
  const status = query.currentData;
  const stale =
    query.isError ||
    Boolean(
      !status?.disabled &&
      status?.deadlineAt &&
      Date.parse(status.deadlineAt) <= Date.now(),
    );
  const state: HeliaDisplayState | undefined =
    status?.state === "ended" ? "ended" : stale ? "unavailable" : status?.state;
  return { query, status, state, stale };
}

export function HuddleAIStatus({
  companyId,
  huddleId,
}: {
  companyId: string;
  huddleId: string;
}) {
  const { query, status, state, stale } = useHeliaStatus(companyId, huddleId);
  const [setEnabled, mutation] = useSetHuddleAIMutation();
  const { permissions } = useCompanyPermissions();
  const [open, setOpen] = useState(false);
  const [budgetOpen, setBudgetOpen] = useState(false);
  const [error, setError] = useState("");
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !open,
    refetchOnFocus: true,
    refetchOnMountOrArgChange: true,
  });
  const agents =
    agentsQuery.currentData?.items.filter(
      (agent) => !agent.archivedAt && agent.voice?.enabled,
    ) ?? [];
  const [selectedId, setSelectedId] = useState("");
  const selected =
    agents.find((agent) => agent.id === selectedId) ??
    agents.find((agent) => agent.id === status?.agent?.id) ??
    agents[0];
  const profile =
    status && !status.disabled ? status.agent : (selected ?? status?.agent);
  const name = profile?.name ?? "Agent";
  const rootRef = useRef<HTMLDivElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) return undefined;
    const onPointer = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const canModerate = Boolean(status?.canModerate) && state !== "ended";
  // Company context makes the inviter the Agent's Authorizer, so only
  // someone in the call can share it; anyone else invites without it.
  const canShareContext = Boolean(status?.canShareContext);
  const sharerLeft =
    state === "unavailable" &&
    status?.unavailableReason === "context_sharer_left";
  const inCall = Boolean(status && !status.disabled && state !== "ended");
  const busy = mutation.isLoading;
  const activity = status?.toolActivity;
  const lookupExpired =
    activity?.state === "reading" &&
    Date.now() - Date.parse(activity.at) > 10000;
  const checking =
    inCall && !stale && activity?.state === "reading" && !lookupExpired;
  const invitePill = canModerate && status?.disabled;
  const label = checking
    ? "Checking Company context…"
    : state
      ? huddleAgentStateLabels[state]
      : "Checking…";

  async function changeParticipation(enabled: boolean) {
    if (!status || (enabled && !selected)) return;
    setError("");
    try {
      await setEnabled({
        companyId,
        huddleId,
        body: {
          enabled,
          allowCompanyContext: enabled && canShareContext,
          ...(enabled ? { agentId: selected!.id } : {}),
          expectedRevision: status.revision,
          requestKey: crypto.randomUUID(),
        },
      }).unwrap();
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
    void query.refetch();
  }

  return (
    <div className="huddle-ai-status" data-state={state} ref={rootRef}>
      <button
        className="huddle-ai-pill"
        type="button"
        data-invite={invitePill}
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={
          invitePill ? "Invite an Agent to this call" : `${name}: ${label}`
        }
        onClick={() => setOpen(!open)}
      >
        <span className="huddle-helia-mark" aria-hidden>
          <HuddleAgentAvatar size={26} profile={profile} />
        </span>
        {invitePill ? (
          <strong>Invite Agent</strong>
        ) : (
          <>
            <strong>{name}</strong>
            <span className="huddle-ai-indicator" aria-hidden />
            <span role="status">{label}</span>
          </>
        )}
        <ChevronDown size={14} className="huddle-ai-chevron" />
      </button>

      {open && (
        <div
          id={panelId}
          className="huddle-ai-panel"
          role="dialog"
          aria-label={`${name} voice agent`}
        >
          <header className="huddle-ai-panel-head">
            <span className="huddle-helia-mark" data-size="lg" aria-hidden>
              <HuddleAgentAvatar
                profile={profile}
                size={48}
                state={
                  state === "speaking"
                    ? "working"
                    : state === "pending" || state === "starting"
                      ? "thinking"
                      : "idle"
                }
              />
            </span>
            <div>
              <strong>{name}</strong>
              <span>{profile?.title ?? "Your AI meeting assistant"}</span>
            </div>
            <button
              type="button"
              className="huddle-ai-close"
              aria-label="Close voice agent panel"
              onClick={() => setOpen(false)}
            >
              <X size={16} />
            </button>
          </header>

          {query.isError ? (
            <div className="huddle-ai-callout" data-tone="warning">
              <p>
                Unable to refresh the Agent’s status. Check your connection.
              </p>
              <button type="button" onClick={() => void query.refetch()}>
                <RefreshCw size={14} /> Retry
              </button>
            </div>
          ) : state === "disabled" ? (
            <>
              <p className="huddle-ai-lead">
                Bring an Agent into the call to answer questions out loud while
                you talk.
              </p>
              <ul className="huddle-ai-facts">
                <li>
                  <Mic size={16} />
                  Hears microphones that are unmuted
                </li>
                <li>
                  <MessageSquareText size={16} />
                  Reads chat messages sent after joining
                </li>
                <li>
                  <Sparkles size={16} />
                  Uses the Agent’s saved instructions and voice preferences
                </li>
              </ul>
            </>
          ) : state === "unavailable" ? (
            <div className="huddle-ai-callout" data-tone="warning">
              <p>
                {!stale && status?.unavailableReason
                  ? unavailableDescriptions[status.unavailableReason]
                  : "The Agent is unavailable right now."}{" "}
                Your call continues without AI.
              </p>
              {!stale && status?.stopReason && (
                <p>{stopDescriptions[status.stopReason]}</p>
              )}
            </div>
          ) : state === "ended" ? (
            <p className="huddle-ai-lead">
              This call has ended. Start a new Huddle to invite an Agent.
            </p>
          ) : null}

          {error && (
            <p className="huddle-ai-error" role="alert">
              {error}
            </p>
          )}

          {inCall && !stale && activity && (
            <div className="huddle-ai-tool-activity" role="status">
              <strong>
                {lookupExpired
                  ? "Lookup did not finish"
                  : checking
                    ? "Checking Company context…"
                    : "Lookup complete"}
              </strong>
              {lookupExpired ? (
                <span>
                  Your call is unaffected. Ask the Agent to try the lookup
                  again.
                </span>
              ) : activity.state === "completed" ? (
                <>
                  <small>
                    Retrieved at {new Date(activity.at).toLocaleTimeString()}.
                    Up to five sources shown.
                  </small>
                  {activity.sources.length === 0 ? (
                    <span>No matching shared records.</span>
                  ) : (
                    <ul>
                      {activity.sources.map((source) => (
                        <li key={source.id}>
                          {source.type === "task" ||
                          source.type === "project" ? (
                            <Link
                              target="_blank"
                              rel="noopener noreferrer"
                              to={
                                source.type === "task"
                                  ? `/tasks?taskId=${encodeURIComponent(source.id)}`
                                  : `/projects/${encodeURIComponent(source.id)}`
                              }
                            >
                              {source.key ? `${source.key} · ` : ""}
                              {source.title}
                            </Link>
                          ) : (
                            source.title
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </>
              ) : null}
            </div>
          )}

          {canModerate && status && (
            <>
              {(status.disabled || state === "unavailable") && (
                <div className="huddle-ai-agent-picker">
                  <label htmlFor={`${panelId}-agent`}>Voice Agent</label>
                  <select
                    id={`${panelId}-agent`}
                    value={selected?.id ?? ""}
                    disabled={busy || agentsQuery.isFetching}
                    onChange={(event) => setSelectedId(event.target.value)}
                  >
                    {!selected && (
                      <option value="">
                        {agentsQuery.isFetching
                          ? "Loading Agents…"
                          : "No Voice Agents available"}
                      </option>
                    )}
                    {agents.map((agent) => (
                      <option key={agent.id} value={agent.id}>
                        {agent.name} · {agent.title}
                      </option>
                    ))}
                  </select>
                  {agentsQuery.isError && (
                    <p role="alert">
                      Could not load Agents.{" "}
                      <button
                        type="button"
                        onClick={() => void agentsQuery.refetch()}
                      >
                        Retry
                      </button>
                    </p>
                  )}
                  <Link to="/agents" target="_blank" rel="noopener noreferrer">
                    Configure Voice in Agents
                  </Link>
                </div>
              )}
              <div className="huddle-ai-actions">
                {(status.disabled || state === "unavailable") && (
                  <button
                    type="button"
                    className="huddle-ai-primary"
                    disabled={
                      busy || query.isError || agentsQuery.isError || !selected
                    }
                    onClick={() => void changeParticipation(true)}
                  >
                    {busy ? (
                      <LoaderCircle size={16} className="huddle-spin" />
                    ) : (
                      <Sparkles size={16} />
                    )}
                    {busy
                      ? "Inviting…"
                      : status.disabled
                        ? `Invite ${selected?.name ?? "Agent"}`
                        : sharerLeft && canShareContext
                          ? "Share Company context"
                          : "Try again"}
                  </button>
                )}
                {inCall && (
                  <button
                    type="button"
                    className="huddle-ai-secondary"
                    disabled={busy || query.isError}
                    onClick={() => void changeParticipation(false)}
                  >
                    {busy ? "Updating…" : `Remove ${name}`}
                  </button>
                )}
              </div>
              {!canShareContext &&
                (status.disabled || state === "unavailable") && (
                  <p className="huddle-ai-note huddle-ai-context-note">
                    <Info size={14} aria-hidden />
                    Only someone in this call can share Company context.
                  </p>
                )}
            </>
          )}
          {status && !canModerate && status.disabled && state !== "ended" && (
            <p className="huddle-ai-note">
              Only the person who started the call or a Company admin can invite
              an Agent.
            </p>
          )}

          {permissions.includes("company.profile.update") && (
            <footer className="huddle-ai-footer">
              <button type="button" onClick={() => setBudgetOpen(true)}>
                <Wallet size={14} /> AI budget
              </button>
            </footer>
          )}
        </div>
      )}

      <Dialog open={budgetOpen} onOpenChange={setBudgetOpen}>
        <DialogContent className="huddle-dialog huddle-ai-dialog">
          <DialogHeader>
            <DialogTitle>Company AI budget</DialogTitle>
            <DialogDescription>
              A shared monthly budget for AI in Company calls. Resets on the
              first of each month (UTC).
            </DialogDescription>
          </DialogHeader>
          <HuddleBudgetSettings companyId={companyId} />
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function HuddleBudgetSettings({ companyId }: { companyId: string }) {
  const query = useGetHuddleBudgetQuery(companyId, {
    refetchOnMountOrArgChange: true,
    pollingInterval: 5000,
  });
  const [save, mutation] = useSetHuddleBudgetMutation();
  const [amount, setAmount] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const inputId = useId();
  const budget = query.currentData;
  const usd = (ticks: string) => `$${(Number(ticks) / 1e10).toFixed(2)}`;
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSaved("");
    setError("");
    if (!budget) return;
    if (!/^\d+(\.\d{1,2})?$/.test(amount)) {
      setError("Enter a USD amount with up to two decimal places.");
      return;
    }
    const [dollars, cents = ""] = amount.split(".");
    const ticks =
      BigInt(dollars) * 10000000000n +
      BigInt(cents.padEnd(2, "0")) * 100000000n;
    if (ticks > 9007199254740991n) {
      setError("This amount exceeds the supported budget limit.");
      return;
    }
    try {
      await save({
        companyId,
        monthlyLimitUsdTicks: ticks.toString(),
        expectedRevision: budget.revision,
      }).unwrap();
      setError("");
      setAmount("");
      setSaved(`Monthly budget saved: $${Number(amount).toFixed(2)}.`);
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
    void query.refetch();
  }
  return (
    <form className="huddle-ai-budget" onSubmit={(event) => void submit(event)}>
      {budget && (
        <dl className="huddle-ai-budget-stats">
          <div>
            <dt>Limit</dt>
            <dd>{usd(budget.monthlyLimitUsdTicks)}</dd>
          </div>
          <div>
            <dt>Recorded</dt>
            <dd>{usd(budget.spentUsdTicks)}</dd>
          </div>
          <div>
            <dt>Held</dt>
            <dd>{usd(budget.reservedUsdTicks)}</dd>
          </div>
          <div className="huddle-ai-budget-available">
            <dt>Available</dt>
            <dd>{usd(budget.remainingUsdTicks)}</dd>
          </div>
        </dl>
      )}
      {budget?.warning && (
        <p className="huddle-ai-error">
          80% or more of the budget is committed.
        </p>
      )}
      <label htmlFor={inputId}>Monthly limit (USD)</label>
      <div className="huddle-ai-budget-row">
        <input
          id={inputId}
          inputMode="decimal"
          value={amount}
          onChange={(event) => {
            setAmount(event.target.value);
            setSaved("");
          }}
          placeholder={
            budget
              ? (Number(budget.monthlyLimitUsdTicks) / 1e10).toFixed(2)
              : "0.00"
          }
          aria-describedby={`${inputId}-hint`}
        />
        <Button
          className="huddle-action-primary"
          type="submit"
          disabled={!budget || mutation.isLoading || !amount}
        >
          {mutation.isLoading ? "Saving…" : "Save limit"}
        </Button>
      </div>
      {saved && <p role="status">{saved}</p>}
      {(error || query.isError) && (
        <p role="alert" className="huddle-ai-error">
          {error || "Budget unavailable. Please retry."}
        </p>
      )}
      <small id={`${inputId}-hint`}>
        Set to $0 to pause AI spending. Saving does not invite an Agent.
      </small>
      <p className="huddle-ai-budget-note">
        Held funds are temporary reservations, released when a call ends and
        final usage arrives. Recorded usage is not a provider invoice.
      </p>
    </form>
  );
}
