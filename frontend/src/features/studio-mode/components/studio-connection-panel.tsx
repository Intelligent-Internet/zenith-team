import { managedToolObservation } from "@/features/settings/model/managed-tool-observation";
import { useCallback, useEffect, useRef, useState } from "react";
import { useGetRuntimeQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { useOperateManagedToolMutation } from "@/features/settings/api/managed-tools-api";
import {
  getDesktopDaemonStatus,
  subscribeToDesktopDaemonStatus,
} from "@/app/desktop/desktop-environment.service";
import {
  LoaderCircle,
  RefreshCw,
  ShieldCheck,
  TriangleAlert,
  Studio,
  X,
} from "@/shared/ui/icons";
import { StudioStatusArt } from "./studio-status-art";

type StudioConnectionPanelProps = {
  companyId: string;
  runtimeId: string;
  agentId: string;
  reason?: string | null;
  autoRetryOnAuthenticated?: boolean;
  onClose: () => void;
  onRetry: () => void;
  onError: (message: string) => void;
};

type StudioConnectionCopy = {
  title: string;
  description?: string;
  action?: string;
};

function runtimeStatusCopy(
  runtimeId: string,
  local: boolean,
  toolState: string | undefined,
  authState: string | undefined,
  reason?: string | null,
): StudioConnectionCopy {
  if (!runtimeId)
    return {
      title: "Set up this Agent first",
      description: "Choose a computer for this Agent.",
    };
  if (!local)
    return {
      title: "Open Zenith Team Desktop",
      description: "Use the Agent’s computer.",
      action: "Check connection",
    };
  if (toolState === "pending" || toolState === "installing")
    return {
      title: "Preparing Studio",
      action: "Refresh status",
    };
  if (toolState === "error")
    return {
      title: "Studio needs repair",
      description: "Repair Studio in Desktop settings.",
      action: "Try again",
    };
  if (authState === "connecting")
    return {
      title: "Connect to Zenith Studio",
      description: "Continue in your browser.",
      action: "Connecting…",
    };
  if (authState === "authenticated")
    return reason
      ? {
          title: "Reconnect Studio",
          action: "Reconnect",
        }
      : {
          title: "Studio connected",
          action: "Load Studio",
        };
  if (authState === "not_required")
    return {
      title: "Studio ready",
      action: "Load Studio",
    };
  return {
    title: authState === "error" ? "Reconnect Studio" : "Connect Studio",
    action: authState === "error" ? "Reconnect" : "Connect",
  };
}

export function StudioConnectionPanel({
  companyId,
  runtimeId,
  agentId,
  reason,
  autoRetryOnAuthenticated = true,
  onClose,
  onRetry,
  onError,
}: StudioConnectionPanelProps) {
  const [daemonId, setDaemonId] = useState<string | null>(null);
  const [daemonStatusKnown, setDaemonStatusKnown] = useState(false);
  const [connectionFailed, setConnectionFailed] = useState(false);
  const autoConnectKey = useRef<string | null>(null);
  const observedUnauthenticatedState = useRef(false);
  const reloadedAfterAuth = useRef(false);
  const runtime = useGetRuntimeQuery(
    { companyId, runtimeId },
    { skip: !companyId || !runtimeId },
  );
  const [operate, operation] = useOperateManagedToolMutation();
  const selected = runtime.currentData;
  const studio = managedToolObservation(
    selected?.tools,
    "zenith-studio",
    agentId,
  );
  const local = Boolean(
    selected &&
    daemonId &&
    selected.daemonId === daemonId &&
    selected.status === "online",
  );
  const copy: StudioConnectionCopy =
    runtime.isLoading || (Boolean(runtimeId) && !daemonStatusKnown)
      ? {
          title: "Checking Studio connection",
          action: "Checking…",
        }
      : runtime.isError
        ? {
            title: "Studio unavailable",
            action: "Try again",
          }
        : operation.isLoading
          ? { title: "Connecting Studio", action: "Connecting…" }
          : connectionFailed
            ? { title: "Couldn’t connect Studio", action: "Reconnect" }
            : runtimeStatusCopy(
                runtimeId,
                local,
                studio?.state,
                studio?.authState,
                reason,
              );
  const busy =
    !agentId ||
    runtime.isLoading ||
    (Boolean(runtimeId) && !daemonStatusKnown) ||
    operation.isLoading ||
    studio?.authState === "connecting";
  const authenticated = studio?.authState === "authenticated";
  const statusError = runtime.isError
    ? runtime.error?.message || "Studio Runtime status could not be loaded."
    : studio?.state === "error"
      ? "Studio setup failed. Repair Studio in Desktop settings."
      : studio?.authState === "error"
        ? "Studio sign-in did not complete. Reconnect to try again."
        : null;
  const failed = Boolean(reason || connectionFailed || statusError);

  useEffect(() => {
    if (statusError) onError(statusError);
  }, [onError, statusError]);

  useEffect(() => {
    let active = true;
    void getDesktopDaemonStatus()
      .then((status) => {
        if (active) {
          setDaemonId(status.daemonId);
          setDaemonStatusKnown(true);
        }
      })
      .catch(() => {
        if (!active) return;
        setDaemonStatusKnown(true);
        onError("Desktop connection could not be checked. Try again.");
      });
    const unsubscribe = subscribeToDesktopDaemonStatus((status) => {
      setDaemonId(status.daemonId);
      setDaemonStatusKnown(true);
    });
    return () => {
      active = false;
      unsubscribe();
    };
  }, [onError]);

  const connect = useCallback(async () => {
    if (
      !agentId ||
      !selected ||
      !local ||
      !studio ||
      studio.state !== "installed"
    )
      return;
    setConnectionFailed(false);
    const result = await operate({
      companyId,
      runtimeId,
      provider: selected.provider,
      agentId: agentId,
      toolKey: "zenith-studio",
      action: "connect",
    });
    if ("error" in result) {
      setConnectionFailed(true);
      onError(result.error?.message || "Studio sign-in could not start.");
      return;
    }
    void runtime.refetch();
  }, [
    agentId,
    companyId,
    onError,
    operate,
    runtime,
    runtimeId,
    selected,
    studio,
  ]);

  useEffect(() => {
    if (
      !agentId ||
      !runtimeId ||
      runtime.isError ||
      !selected ||
      !local ||
      !studio
    )
      return;
    if (studio.authState === "authenticated") {
      if (
        autoRetryOnAuthenticated &&
        observedUnauthenticatedState.current &&
        !reloadedAfterAuth.current
      ) {
        reloadedAfterAuth.current = true;
        onRetry();
      }
      return;
    }
    observedUnauthenticatedState.current = true;
    reloadedAfterAuth.current = false;
    if (
      autoConnectKey.current ===
        `${runtimeId}:${agentId}:${studio.state}:${studio.authState}` ||
      studio.state !== "installed" ||
      studio.authState === "connecting" ||
      studio.authState === "not_required"
    )
      return;
    autoConnectKey.current = `${runtimeId}:${agentId}:${studio.state}:${studio.authState}`;
    void connect();
  }, [
    agentId,
    autoRetryOnAuthenticated,
    connect,
    local,
    onRetry,
    runtime.isError,
    runtimeId,
    selected,
    studio,
  ]);

  return (
    <section
      className="studio-mode-panel studio-mode-panel--connection"
      aria-label="Studio connection"
      role="status"
      aria-busy={busy}
    >
      <header>
        <h2 className="studio-mode-panel__loading-title">
          <Studio size={16} aria-hidden />
          Studio Mode
        </h2>
        <div className="studio-mode-panel__actions">
          <button type="button" onClick={onClose} aria-label="Close Studio">
            <X size={16} aria-hidden />
          </button>
        </div>
      </header>
      <div className="studio-mode-panel__status-body">
        <StudioStatusArt />
        <div className="studio-mode-panel__status-copy">
          <div
            className="studio-mode-panel__status-icon"
            data-state={busy ? "loading" : failed ? "error" : "ready"}
          >
            {busy ? (
              <LoaderCircle size={18} aria-hidden />
            ) : failed ? (
              <TriangleAlert size={18} aria-hidden />
            ) : authenticated ? (
              <ShieldCheck size={18} aria-hidden />
            ) : (
              <Studio size={18} aria-hidden />
            )}
          </div>
          <h3>{copy.title}</h3>
          {copy.description ? <p>{copy.description}</p> : null}
          {runtimeId && copy.action ? (
            <div className="studio-mode-panel__status-actions">
              <button
                type="button"
                className="studio-mode-panel__status-primary"
                disabled={busy}
                onClick={() => {
                  if (authenticated || studio?.authState === "not_required") {
                    onRetry();
                  } else if (
                    selected &&
                    studio?.state === "installed" &&
                    local
                  ) {
                    void connect();
                  } else onRetry();
                }}
              >
                {busy ? <LoaderCircle size={14} aria-hidden /> : null}
                {!busy && (authenticated || failed) ? (
                  <RefreshCw size={14} aria-hidden />
                ) : null}
                {copy.action}
              </button>
            </div>
          ) : null}
        </div>
      </div>
    </section>
  );
}
