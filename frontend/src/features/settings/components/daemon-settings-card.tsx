import type {
  DesktopDaemonActionResult,
  DesktopDaemonLogLevel,
  DesktopDaemonLogLine,
  DesktopDaemonStatus,
} from "../../../../electron/shared/desktop-bridge.types";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { useEffect, useId, useRef, useState } from "react";

import {
  getDesktopDaemonStatus,
  restartDesktopDaemon,
  revealDesktopDaemonLog,
  startDesktopDaemon,
  startDesktopDaemonLogs,
  stopDesktopDaemon,
  stopDesktopDaemonLogs,
  subscribeToDesktopDaemonLogLines,
  subscribeToDesktopDaemonStatus,
  WEB_DESKTOP_DAEMON_STATUS_FALLBACK,
} from "@/shared/desktop/desktop-environment.service";
import { readSessionSnapshot } from "@/shared/session/session";
import { agentRuntimeApi } from "@/shared/api/agent-runtime-api";
import {
  daemonActionOutcome,
  isComputerHandoverPending,
  localDaemonRuntimeStatus,
} from "@/features/settings/model/runtime-management";
import type { RuntimeDto } from "@/shared/api/types";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import {
  ChevronRight,
  FileText,
  FolderOpen,
  Info,
  MoreHorizontal,
  Square,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { useAppDispatch } from "@/state/store";

const LOG_RING_SIZE = 500;
const LOG_LEVEL_RANK: Record<DesktopDaemonLogLevel, number> = {
  debug: 0,
  error: 3,
  info: 1,
  warn: 2,
};

type DaemonTone = "checking" | "danger" | "neutral" | "ready" | "warning";

type DaemonAction = "restart" | "start" | "stop";

export function DaemonSettingsCard() {
  const dispatch = useAppDispatch();
  const [status, setStatus] = useState<DesktopDaemonStatus>(
    WEB_DESKTOP_DAEMON_STATUS_FALLBACK,
  );
  const [pendingAction, setPendingAction] = useState<DaemonAction | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionProgress, setActionProgress] = useState<string | null>(null);
  const [pendingStopRunCount, setPendingStopRunCount] = useState<number | null>(
    null,
  );
  const [logSearch, setLogSearch] = useState("");
  const [followLogs, setFollowLogs] = useState(true);
  const logViewerRef = useRef<HTMLPreElement>(null);
  const [logsOpen, setLogsOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsId = useId();
  const logsId = useId();
  const [logLevel, setLogLevel] = useState<DesktopDaemonLogLevel>("info");
  const [logLines, setLogLines] = useState<DesktopDaemonLogLine[]>([]);
  const [logMessage, setLogMessage] = useState<string | null>(null);
  useEffect(() => {
    if (logsOpen && followLogs && logViewerRef.current)
      logViewerRef.current.scrollTop = logViewerRef.current.scrollHeight;
  }, [logsOpen, logLines, logLevel, logSearch, followLogs]);
  const previousStateRef = useRef<DesktopDaemonStatus["state"] | null>(null);
  const lastDaemonIdRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void getDesktopDaemonStatus().then((nextStatus) => {
      if (!cancelled) {
        setStatus(nextStatus);
      }
    });
    const unsubscribe = subscribeToDesktopDaemonStatus(setStatus);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  // The local machine's runtime chip should go grey the moment the daemon
  // goes down, not on the next 30 s roster poll. Only that direction: a
  // stopped daemon is provably not beating, while a started one proves
  // nothing about any single runtime — the server derives availability from
  // the heartbeat, and a provider whose CLI is missing or too old stays
  // offline while the daemon runs. Coming back is therefore the server's
  // word, carried by `daemon.roster_changed` and the roster read it triggers.
  useEffect(() => {
    if (status.daemonId) {
      lastDaemonIdRef.current = status.daemonId;
    }
    const previousState = previousStateRef.current;
    previousStateRef.current = status.state;
    const daemonId = lastDaemonIdRef.current;
    if (previousState === null || previousState === status.state || !daemonId) {
      return;
    }
    const nextRuntimeStatus = localDaemonRuntimeStatus(status.state);
    const companyId = readSessionSnapshot().company?.id;
    if (!nextRuntimeStatus || !companyId) {
      return;
    }
    const patch = (runtime: RuntimeDto) => {
      if (runtime.daemonId === daemonId) {
        runtime.status = nextRuntimeStatus;
      }
    };
    dispatch(
      agentRuntimeApi.util.updateQueryData(
        "listAgentRuntimes",
        companyId,
        (page) => {
          page.items.forEach(patch);
        },
      ),
    );
    // The runtime detail, when open, shows the same machine.
    dispatch((_, getState) => {
      for (const argument of agentRuntimeApi.util.selectCachedArgsForQuery(
        getState(),
        "getRuntime",
      )) {
        if (argument.companyId !== companyId) continue;
        dispatch(
          agentRuntimeApi.util.updateQueryData("getRuntime", argument, patch),
        );
      }
    });
  }, [dispatch, status.daemonId, status.state]);

  useEffect(() => {
    if (!logsOpen) {
      return;
    }
    let cancelled = false;
    const unsubscribe = subscribeToDesktopDaemonLogLines((batch) => {
      setLogLines((current) =>
        (batch.reset ? batch.lines : [...current, ...batch.lines]).slice(
          -LOG_RING_SIZE,
        ),
      );
    });
    void startDesktopDaemonLogs().then((result) => {
      if (!cancelled && !result.ok) {
        setLogMessage(result.reason);
      }
    });
    return () => {
      cancelled = true;
      unsubscribe();
      void stopDesktopDaemonLogs();
    };
  }, [logsOpen]);

  const runAction = async (
    action: DaemonAction,
    perform: () => Promise<DesktopDaemonActionResult>,
  ) => {
    setPendingAction(action);
    setActionError(null);
    setActionProgress(null);
    const result = await perform();
    if (result.status) {
      setStatus(result.status);
    }
    const outcome = daemonActionOutcome(result);
    if (outcome.kind === "confirm_stop") {
      setPendingStopRunCount(outcome.activeRunCount);
    } else if (outcome.kind === "progress") {
      setActionProgress(outcome.message);
    } else if (outcome.kind === "error") {
      setActionError(outcome.message);
    }
    setPendingAction(null);
    return result;
  };

  const stopDaemon = (confirmActive: boolean) =>
    runAction("stop", () => stopDesktopDaemon({ confirmActive }));

  const confirmStop = async () => {
    setPendingStopRunCount(null);
    await stopDaemon(true);
  };

  const revealLog = async () => {
    setLogMessage(null);
    const result = await revealDesktopDaemonLog();
    if (!result.ok) {
      setLogMessage(result.reason);
    }
  };

  const description = describeDaemonState(status);
  const transitioning =
    status.state === "stopping" || status.state === "restarting";
  // The drain line answers the click that started it, and outlives no more
  // than the shutdown itself: once the daemon is gone the state chip says so.
  const progressMessage = status.state === "stopping" ? actionProgress : null;
  const busy = pendingAction !== null || transitioning;
  const daemonMissing =
    status.reason === "daemon_binary_unavailable" ||
    status.reason === "desktop_daemon_unavailable";
  const canStart =
    !busy &&
    !daemonMissing &&
    (status.state === "stopped" || status.state === "unavailable");
  const canStop =
    !busy && (status.state === "running" || status.state === "starting");
  const canRestart = !busy && status.state === "running";
  const visibleLogLines = logLines.filter(
    (line) =>
      LOG_LEVEL_RANK[line.level] >= LOG_LEVEL_RANK[logLevel] &&
      line.text.toLowerCase().includes(logSearch.toLowerCase()),
  );
  const runCount = pendingStopRunCount ?? 0;

  return (
    <div className="support-daemon">
      <div className="support-settings-row">
        <span className="support-settings-icon" aria-hidden="true">
          <Icon name="power-circle" />
        </span>
        <div className="support-settings-row__copy">
          <div className="flex flex-wrap items-center gap-[8px_12px] text-workspace-body leading-workspace-body font-[600]">
            Agent service
            <span
              className="support-daemon__status"
              data-tone={description.tone}
              role="status"
            >
              {description.label}
            </span>
          </div>
          <p>Runs agents on this computer.</p>
          {status.cliVersion || status.agents.length > 0 ? (
            <p>
              {[
                status.cliVersion ? `Version ${status.cliVersion}` : null,
                `${status.agents.length} provider${status.agents.length === 1 ? "" : "s"}`,
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
          ) : null}
        </div>
        <div className="support-settings-row__action">
          {status.state === "running" ||
          status.state === "restarting" ||
          pendingAction === "restart" ? (
            <button
              className="support-settings-button"
              disabled={!canRestart}
              type="button"
              onClick={() => void runAction("restart", restartDesktopDaemon)}
            >
              {pendingAction === "restart" || status.state === "restarting"
                ? "Restarting…"
                : "Restart"}
            </button>
          ) : !daemonMissing &&
            (status.state === "stopped" ||
              status.state === "unavailable" ||
              status.state === "starting") ? (
            <button
              className="support-settings-button"
              disabled={!canStart}
              type="button"
              onClick={() => void runAction("start", startDesktopDaemon)}
            >
              {pendingAction === "start" || status.state === "starting"
                ? "Starting…"
                : "Start"}
            </button>
          ) : null}
          {status.state === "running" || status.state === "starting" ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="support-settings-button support-settings-button--icon"
                  aria-label="Agent service actions"
                  disabled={busy}
                  type="button"
                >
                  <MoreHorizontal size={18} aria-hidden="true" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  disabled={!canStop}
                  onSelect={() => void stopDaemon(false)}
                >
                  <Square size={16} aria-hidden="true" />
                  Stop agent service
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
      </div>

      {description.detail ? (
        <p
          className="support-daemon__notice"
          data-tone={description.tone}
          role="status"
        >
          {description.detail}
        </p>
      ) : null}
      {status.reloadPendingReason ? (
        <p className="support-daemon__notice" data-tone="warning" role="status">
          Restart pending: {status.reloadPendingReason}
        </p>
      ) : null}
      {status.skippedAgents.length > 0 ? (
        <p className="support-daemon__notice" data-tone="warning">
          {status.skippedAgents.length} provider
          {status.skippedAgents.length === 1 ? " was" : "s were"} skipped. See
          Technical details.
        </p>
      ) : status.state === "running" && status.agents.length === 0 ? (
        <p className="support-daemon__notice" data-tone="warning">
          No provider CLI was found on this computer.
        </p>
      ) : null}
      {progressMessage ? (
        <p className="support-daemon__notice" role="status">
          {progressMessage}
        </p>
      ) : null}

      {actionError ? (
        <p className="support-daemon__notice" data-tone="danger" role="alert">
          {actionError}
        </p>
      ) : null}

      <div className="support-daemon__utilities">
        <button
          className="support-daemon__utility"
          aria-expanded={logsOpen}
          aria-controls={logsId}
          disabled={daemonMissing}
          type="button"
          onClick={() => {
            setLogMessage(null);
            setLogsOpen((open) => !open);
          }}
        >
          <FileText size={16} aria-hidden="true" />
          {logsOpen ? "Hide logs" : "View logs"}
        </button>
        <button
          className="support-daemon__utility"
          disabled={daemonMissing}
          type="button"
          onClick={() => void revealLog()}
        >
          <FolderOpen size={16} aria-hidden="true" />
          Show log file
        </button>
        <button
          className="support-daemon__utility"
          aria-expanded={detailsOpen}
          aria-controls={detailsId}
          type="button"
          onClick={() => setDetailsOpen((open) => !open)}
        >
          <Info size={16} aria-hidden="true" />
          Technical details
          <ChevronRight
            size={14}
            className="support-daemon__chevron"
            aria-hidden="true"
          />
        </button>
      </div>

      {detailsOpen ? (
        <div className="support-daemon__details" id={detailsId}>
          <div>
            <h3>Daemon</h3>
            <dl>
              <dt>Version</dt>
              <dd>{status.cliVersion ?? "Unavailable"}</dd>
              <dt>Process ID</dt>
              <dd>{status.pid ?? "Not running"}</dd>
            </dl>
          </div>
          <div>
            <h3>Providers</h3>
            {status.agents.length === 0 ? (
              <p>No providers detected.</p>
            ) : (
              <dl>
                {status.agents.map((agent) => (
                  <div key={agent.provider} className="contents">
                    <dt>
                      {agent.provider === "claude"
                        ? "Claude"
                        : agent.provider === "codex"
                          ? "Codex"
                          : agent.provider}
                    </dt>
                    <dd>{agent.version ?? "Version unknown"}</dd>
                  </div>
                ))}
              </dl>
            )}
          </div>
          {status.skippedAgents.length > 0 ? (
            <div>
              <h3>Skipped providers</h3>
              <dl>
                {status.skippedAgents.map((agent) => (
                  <div key={agent.provider} className="contents">
                    <dt>
                      {agent.provider === "claude"
                        ? "Claude"
                        : agent.provider === "codex"
                          ? "Codex"
                          : agent.provider}
                    </dt>
                    <dd>{agent.reason}</dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : null}
        </div>
      ) : null}

      {logMessage ? (
        <p className="support-daemon__notice" role="status">
          {logMessage}
        </p>
      ) : null}
      {logsOpen ? (
        <div className="support-daemon__logs mt-[16px]" id={logsId}>
          <div className="daemon-card__log-toolbar">
            <label className="daemon-card__log-level inline-flex items-center gap-[6px] text-[var(--workspace-native-text-muted,_rgba(255,_255,_255,_0.56))] text-workspace-body leading-workspace-body">
              <span>Level</span>
              <select
                value={logLevel}
                onChange={(event) =>
                  setLogLevel(event.target.value as DesktopDaemonLogLevel)
                }
              >
                <option value="debug">Debug</option>
                <option value="info">Info</option>
                <option value="warn">Warn</option>
                <option value="error">Error</option>
              </select>
            </label>
            <input
              className="daemon-log-search"
              aria-label="Search agent service logs"
              placeholder="Search logs…"
              type="search"
              value={logSearch}
              onChange={(event) => setLogSearch(event.target.value)}
            />
            <button
              className="support-settings-button"
              type="button"
              aria-pressed={followLogs}
              onClick={() => {
                setFollowLogs(true);
                if (logViewerRef.current)
                  logViewerRef.current.scrollTop =
                    logViewerRef.current.scrollHeight;
              }}
            >
              Jump to latest
            </button>
            <button
              className="support-settings-button"
              type="button"
              disabled={!visibleLogLines.length}
              onClick={() => {
                void copyTextToClipboard(
                  visibleLogLines.map((line) => line.text).join("\n"),
                ).then(
                  () => setLogMessage("Visible log lines copied."),
                  () =>
                    setLogMessage(
                      "Could not copy logs. Select and copy the text below.",
                    ),
                );
              }}
            >
              Copy visible logs
            </button>
            <span className="text-xs text-[var(--workspace-native-text-muted)]">
              {visibleLogLines.length} of {logLines.length} buffered lines ·
              Latest at bottom
            </span>
          </div>
          <pre
            ref={logViewerRef}
            tabIndex={0}
            aria-label="Agent service log"
            className="daemon-log-viewer"
            onScroll={(event) => {
              const element = event.currentTarget;
              setFollowLogs(
                element.scrollHeight -
                  element.scrollTop -
                  element.clientHeight <
                  24,
              );
            }}
          >
            {visibleLogLines.length === 0
              ? logLines.length > 0
                ? "No log lines match these filters."
                : "No log lines yet."
              : visibleLogLines.map((line) => line.text).join("\n")}
          </pre>
        </div>
      ) : null}

      <Dialog
        open={pendingStopRunCount !== null}
        onOpenChange={(open) => {
          if (!open) {
            setPendingStopRunCount(null);
          }
        }}
      >
        <DialogContent
          className="profile-delete-dialog daemon-stop-dialog"
          overlayClassName="profile-delete-dialog__overlay"
          showCloseButton={false}
        >
          <div className="profile-delete-dialog__surface">
            <DialogHeader className="profile-delete-dialog__header">
              <span className="profile-delete-dialog__icon">
                <Icon name="alert" />
              </span>
              <DialogTitle>Stop the agent service?</DialogTitle>
              <DialogDescription>
                {`${runCount} run${runCount === 1 ? "" : "s"} ${
                  runCount === 1 ? "is" : "are"
                } in flight. They will fail and retry when the agent service is back.`}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter className="profile-delete-dialog__footer">
              <DialogClose asChild>
                <button
                  className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
                  type="button"
                >
                  Cancel
                </button>
              </DialogClose>
              <button
                className="profile-delete-dialog__button daemon-stop-dialog__confirm"
                type="button"
                onClick={() => void confirmStop()}
              >
                Stop anyway
              </button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function describeDaemonState(status: DesktopDaemonStatus): {
  detail: string;
  label: string;
  tone: DaemonTone;
} {
  switch (status.state) {
    case "running":
      if (
        status.reason === "version_mismatch_active_runs" ||
        status.reason === "binary_mismatch_active_runs"
      ) {
        const count = status.activeRunCount;
        return {
          detail: `The app updated. The agent service will restart once ${count} active run${count === 1 ? " finishes" : "s finish"}.`,
          label: "Running · restart pending",
          tone: "warning",
        };
      }
      return { detail: "", label: "Running", tone: "ready" };
    case "starting":
      return {
        detail: "Waiting for the agent service to answer its health check.",
        label: "Starting…",
        tone: "checking",
      };
    case "stopping":
      if (status.reason === "stop_draining_runs") {
        const count = status.activeRunCount;
        return {
          detail: "The agent service stops on its own once they finish.",
          label: `Stopping — ${count} run${count === 1 ? "" : "s"} draining`,
          tone: "checking",
        };
      }
      return {
        detail: "Asking the agent service to stop.",
        label: "Stopping…",
        tone: "checking",
      };
    case "restarting":
      return {
        detail: "Stopping the agent service, then starting it again.",
        label: "Restarting…",
        tone: "checking",
      };
    case "blocked":
      return {
        detail:
          "Another agent service owns this port. Stop it from where it was started, then start this one.",
        label: "Blocked",
        tone: "danger",
      };
    case "unavailable":
      return {
        detail: describeUnavailableReason(status.reason),
        label: "Unavailable",
        tone: "danger",
      };
    case "stopped":
    default:
      if (status.reason === "desktop_authentication_unavailable") {
        return {
          detail: "Sign in again to start the agent service.",
          label: "Sign-in expired",
          tone: "warning",
        };
      }
      if (status.reason === "stopped_by_user") {
        return {
          detail: "Start it again when you are ready.",
          label: "Stopped",
          tone: "neutral",
        };
      }
      if (isComputerHandoverPending(status)) {
        return {
          detail:
            "Someone else used this computer last. Start the agent service to use it for your agents.",
          label: "Not started",
          tone: "neutral",
        };
      }
      return {
        detail: "The agent service is not running.",
        label: "Stopped",
        tone: "neutral",
      };
  }
}

function describeUnavailableReason(reason: string | null) {
  switch (reason) {
    case "daemon_start_failed":
      return "The agent service failed to start. Check the log for the cause.";
    case "daemon_health_unavailable":
      return "The agent service started but never answered its health check.";
    case "daemon_session_unavailable":
      return "The agent service could not get its own sign-in. It tries again on its own; check your connection.";
    case "daemon_stop_failed":
      return "The agent service could not be stopped.";
    case "daemon_binary_unavailable":
      return "This build of Zenith Team does not include the agent service.";
    case "desktop_daemon_unavailable":
      return "Available in Zenith Team Desktop.";
    default:
      return reason ?? "The agent service is not available.";
  }
}
