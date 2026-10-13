import type {
  DesktopDaemonActionResult,
  DesktopDaemonState,
  DesktopDaemonStatus,
} from "../../../../electron/shared/desktop-bridge.types";

import {
  runtimeDisplayName,
  runtimeOptionLabel,
} from "@/features/agent-setup/model/ceo-agent-setup";
import type {
  AgentBindingDto,
  RuntimeDto,
  RuntimeRemovalRequest,
} from "@/shared/api/types";

// The server's rule for a machine name: trimmed, at most 100 characters
// counted in code points (runes), and the empty string clears it.
export const RUNTIME_CUSTOM_NAME_MAX_LENGTH = 100;

export function normalizeRuntimeCustomName(raw: string): {
  error: string | null;
  value: string;
} {
  const value = raw.trim();
  if ([...value].length > RUNTIME_CUSTOM_NAME_MAX_LENGTH) {
    return {
      error: `Use at most ${RUNTIME_CUSTOM_NAME_MAX_LENGTH} characters.`,
      value,
    };
  }
  return { error: null, value };
}

// Where the agents this member runs on the removed computer can move: any
// other computer of theirs — every runtime on their roster is (ADR 0014) —
// online or not. A binding must keep the provider, so only same-provider
// targets can be chosen; within them online computers lead, then the oldest
// registration. Never the computer being removed.
export function orderRemoveTargets(
  source: Pick<RuntimeDto, "id" | "provider">,
  runtimes: readonly RuntimeDto[],
): RuntimeDto[] {
  return runtimes
    .filter(
      (candidate) =>
        candidate.id !== source.id && candidate.provider === source.provider,
    )
    .sort(
      (left, right) =>
        rank(right.provider === source.provider) -
          rank(left.provider === source.provider) ||
        rank(right.status === "online") - rank(left.status === "online") ||
        left.createdAt.localeCompare(right.createdAt) ||
        left.id.localeCompare(right.id),
    );
}

export function removeTargetOptionLabel(
  source: Pick<RuntimeDto, "provider">,
  target: RuntimeDto,
): string {
  const label = runtimeOptionLabel(target);
  return target.provider === source.provider
    ? label
    : `${label} · different provider, model and thinking level are cleared`;
}

export type RemovePlan =
  | { kind: "delete" }
  | { kind: "drop"; payload: RuntimeRemovalRequest }
  | { kind: "needs_target" }
  | { kind: "remove"; payload: RuntimeRemovalRequest };

// The Remove dialog's answer for the agents set up on the computer: the id
// of another computer to move them to, DROP_BINDINGS to take them off, or
// "" before the member has chosen.
export const DROP_BINDINGS = "drop";

// What the confirm sends. Nothing bound: the plan-less DELETE. Agents bound:
// POST …/remove says where this member's agents go: another computer of
// theirs, or nowhere (`null`, which cancels their queued work there). The
// computer is their own, so there is no plan to agree on with anybody else
// (ADR 0014); a member with nothing set up here simply deletes the row.
export function buildRemovePlan(
  blockers: ReadonlyArray<Pick<AgentBindingDto, "agentId">>,
  destination: string,
): RemovePlan {
  if (blockers.length === 0) return { kind: "delete" };
  if (destination === DROP_BINDINGS) {
    return { kind: "drop", payload: { targetRuntimeId: null } };
  }
  if (!destination) return { kind: "needs_target" };
  return {
    kind: "remove",
    payload: { targetRuntimeId: destination },
  };
}

export function launchedByLabel(launchedBy: RuntimeDto["launchedBy"]): string {
  return launchedBy === "desktop" ? "Desktop app" : "CLI";
}

// The computer a runtime runs on, named the way the Computers
// card heads it: the name a person gave the machine, else the device name its
// daemon reported, else the runtime's own name. A daemon runs on a computer,
// not on a runtime, so every "on that computer" sentence uses this name.
export function runtimeComputerName(
  runtime: Pick<RuntimeDto, "customName" | "deviceInfo" | "name">,
): string {
  return (
    runtime.customName?.trim() ||
    runtime.deviceInfo?.split(" · ")[0]?.trim() ||
    runtimeDisplayName(runtime)
  );
}

// What the desktop daemon card may assert about this machine's runtimes
// before the server answers. Availability is derived from the heartbeat and
// only the server derives it: a provider whose CLI was uninstalled or sits
// below the minimum version has a runtime the server holds offline while the
// daemon runs happily, so "the supervisor started" is no evidence that "this
// runtime is online". A daemon that is stopping or stopped is not beating,
// though, and that is the one direction the client can prove — everything
// else waits for `daemon.roster_changed` and the roster read it triggers.
export function localDaemonRuntimeStatus(
  state: DesktopDaemonState,
): "offline" | null {
  return state === "stopping" || state === "stopped" ? "offline" : null;
}

// The desktop's agent service waits for the signed-in person's word before
// it starts on a computer somebody else used last. Start is that word; Stop
// leaves it stopped until they start it from Settings.
export const COMPUTER_HANDOVER_PENDING_REASON = "computer_handover_pending";

export function isComputerHandoverPending(
  status: Pick<DesktopDaemonStatus, "reason" | "state">,
): boolean {
  return (
    status.state === "stopped" &&
    status.reason === COMPUTER_HANDOVER_PENDING_REASON
  );
}

export type DaemonActionOutcome =
  | { activeRunCount: number; kind: "confirm_stop" }
  | { kind: "error"; message: string }
  | { kind: "progress"; message: string }
  | { kind: "settled" };

// What the daemon card does with a panel action's answer. Two of the
// supervisor's refusals are not failures. `active_runs` is a question: the
// stop has not been attempted and needs the owner's word. `stop_draining` is
// the stop working exactly as designed — the daemon holds its health port
// for the whole shutdown and exits once its last run finishes, so there is
// nothing to fix and nothing to retry, only something to wait for. Showing
// either in red would tell the owner their machine broke when it did not.
// Everything else is a failure and is shown as one.
export function daemonActionOutcome(
  result: DesktopDaemonActionResult,
): DaemonActionOutcome {
  if (result.ok) {
    return { kind: "settled" };
  }
  if (result.code === "active_runs") {
    return { activeRunCount: result.activeRunCount ?? 1, kind: "confirm_stop" };
  }
  if (result.code === "stop_draining") {
    return { kind: "progress", message: result.reason };
  }
  return { kind: "error", message: result.reason };
}

function rank(value: boolean): number {
  return value ? 1 : 0;
}
