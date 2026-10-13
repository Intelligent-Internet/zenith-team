import type { DesktopDaemonStatus } from "../../../../electron/shared/desktop-bridge.types";
import { useEffect, useState } from "react";

import {
  getDesktopDaemonStatus,
  shouldUseDesktopHashRouter,
  subscribeToDesktopDaemonStatus,
  WEB_DESKTOP_DAEMON_STATUS_FALLBACK,
} from "@/shared/desktop/desktop-environment.service";
import { useRuntimeSetupRefresh } from "@/features/settings/model/use-runtime-setup-refresh";
import type { AgentProvider } from "@/shared/api/types";
import { Button } from "@/shared/ui/button";
import { Monitor, RefreshCw } from "@/shared/ui/icons";

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
  const refreshTarget = status.daemonId ?? "local-daemon";
  const checking = isPending(refreshTarget);
  const canSelectComputer =
    desktop && status.state === "stopped" && onSelectComputer;

  return (
    <aside className="flex min-w-0 flex-wrap items-start gap-3 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-panel-raised-bg)] p-4">
      <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]">
        <Monitor size={18} aria-hidden="true" />
      </div>
      <div className="min-w-0 flex-1 basis-48">
        <div className="text-sm font-semibold leading-5 text-[var(--workspace-native-text)]">
          {desktop && status.state === "running"
            ? `Set up ${provider ? tool : "an agent provider"}`
            : "Connect a computer"}
        </div>
        <div className="mt-1 max-w-prose text-[13px] leading-5 text-[var(--workspace-native-text-muted)]">
          {!desktop ? (
            <>
              Open Zenith Team on the computer you want to use, or run{" "}
              <code>zen daemon</code>, then refresh this page.
            </>
          ) : status.state === "stopped" ? (
            onSelectComputer ? (
              "Choose a computer to run your agent. We’ll check which providers are ready."
            ) : (
              "Start the agent service in Settings to connect this computer."
            )
          ) : status.state === "running" ? (
            `This computer is connected. Install ${tool} and sign in, then check again.`
          ) : (
            `Open Zenith Team on this computer, then install ${tool} and sign in.`
          )}
        </div>
        {canSelectComputer || (desktop && refreshRuntimeId) ? (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {canSelectComputer ? (
              <Button
                variant="default"
                size="sm"
                onClick={onSelectComputer}
                type="button"
              >
                Select computer
              </Button>
            ) : null}
            {desktop && refreshRuntimeId ? (
              <Button
                variant="outline"
                size="sm"
                disabled={checking}
                aria-busy={checking}
                onClick={() => void refresh(refreshTarget, refreshRuntimeId)}
                type="button"
              >
                <RefreshCw
                  size={14}
                  aria-hidden="true"
                  className={checking ? "animate-spin" : undefined}
                />
                {checking ? "Checking…" : "Check again"}
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </aside>
  );
}
