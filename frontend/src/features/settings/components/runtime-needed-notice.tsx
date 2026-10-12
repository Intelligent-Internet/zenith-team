import { useEffect, useState } from "react";

import {
  getDesktopDaemonStatus,
  shouldUseDesktopHashRouter,
  subscribeToDesktopDaemonStatus,
  WEB_DESKTOP_DAEMON_STATUS_FALLBACK,
  type DesktopDaemonStatus,
} from "@/app/desktop/desktop-environment.service";
import { useRuntimeSetupRefresh } from "@/features/settings/model/use-runtime-setup-refresh";
import type { AgentProvider } from "@/shared/api/types";

const PROVIDER_LABEL: Record<AgentProvider, string> = {
  claude: "Claude Code",
  codex: "Codex",
};

type RuntimeNeededNoticeProps = {
  companyId: string;
  // The provider the caller needs a computer for. Without one the notice
  // speaks about agents in general.
  provider?: AgentProvider;
  // Any online runtime of this machine, when there is one: refreshing asks
  // that computer to look at its provider setup again.
  refreshRuntimeId?: string;
  onSelectComputer?: () => void;
};

// The one "you need a computer for this" state (ADR 0014). An Agent runs on
// a machine its member registered, so every surface that offers to set one
// up — the Agents list, an Agent's page, the run button, a new task, a chat
// composer — ends here when the viewer has no runtime to offer. The desktop
// app can say what its own daemon is doing; the web app can only explain
// where to start one.
export function RuntimeNeededNotice({
  companyId,
  provider,
  refreshRuntimeId,
  onSelectComputer,
}: RuntimeNeededNoticeProps) {
  const desktop = shouldUseDesktopHashRouter();
  const [status, setStatus] = useState<DesktopDaemonStatus>(
    WEB_DESKTOP_DAEMON_STATUS_FALLBACK,
  );
  const { isPending, refresh } = useRuntimeSetupRefresh(companyId);
  useEffect(() => {
    if (!desktop) return;
    let cancelled = false;
    let pushed = false;
    void getDesktopDaemonStatus().then((next) => {
      if (!cancelled && !pushed) setStatus(next);
    });
    const unsubscribe = subscribeToDesktopDaemonStatus((next) => {
      pushed = true;
      setStatus(next);
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [desktop]);

  const tool = provider ? PROVIDER_LABEL[provider] : "an agent provider";
  if (!desktop) {
    return (
      <aside className="agent-onboarding-notice">
        <p>No computer of yours can run this yet.</p>
        <span>
          Install Zenith Team for desktop, or run <code>zen daemon</code> on the
          computer that should do the work, then refresh this page.
        </span>
      </aside>
    );
  }

  const refreshTarget = status.daemonId ?? "local-daemon";
  return (
    <aside className="agent-onboarding-notice">
      <p>No computer of yours can run this yet.</p>
      <span>
        {status.state === "stopped"
          ? onSelectComputer
            ? "Select this computer to start its agent service and check your installed providers."
            : "Start the agent service in Settings to connect this computer."
          : status.state === "running"
            ? `This computer is connected, but ${tool} was not found on it. Install ${tool} and sign in, then check again.`
            : `Start Zenith Team on this computer, install ${tool} and sign in, then check again.`}
      </span>
      {status.state === "stopped" && onSelectComputer ? (
        <button
          className="agent-onboarding-notice__action"
          onClick={onSelectComputer}
          type="button"
        >
          Select computer
        </button>
      ) : null}
      {refreshRuntimeId ? (
        <button
          className="agent-onboarding-notice__action"
          disabled={isPending(refreshTarget)}
          onClick={() => void refresh(refreshTarget, refreshRuntimeId)}
          type="button"
        >
          {isPending(refreshTarget) ? "Checking…" : "Check again"}
        </button>
      ) : null}
    </aside>
  );
}
