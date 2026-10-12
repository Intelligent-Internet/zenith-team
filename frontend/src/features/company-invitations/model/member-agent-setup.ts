import type { AgentDto, RuntimeDto } from "@/shared/api/types";
import {
  readStorageItem,
  removeStorageItem,
  writeStorageItem,
} from "@/shared/lib/browser-storage";
import { runtimeComputerName } from "@/features/settings/model/runtime-management";

function setupStorageKey(userId: string, companyId: string) {
  return `zenith.member-agent-setup.v1:${userId}:${companyId}`;
}

export function memberAgentSetupPath(companyId: string) {
  return `/companies/${encodeURIComponent(companyId)}/setup-agents`;
}

// This is a dismissible navigation preference. The API's viewer bindings
// alone establish which Agents the member has set up and may run.
export function beginMemberAgentSetup(userId: string, companyId: string) {
  writeStorageItem("local", setupStorageKey(userId, companyId), "pending");
}

export function finishMemberAgentSetup(userId: string, companyId: string) {
  removeStorageItem("local", setupStorageKey(userId, companyId));
}

export function pendingMemberAgentSetupPath(
  userId?: string,
  companyId?: string,
) {
  if (!userId || !companyId) return null;
  return readStorageItem("local", setupStorageKey(userId, companyId)) ===
    "pending"
    ? memberAgentSetupPath(companyId)
    : null;
}

export type SetupComputer = {
  id: string;
  name: string;
  runtimes: RuntimeDto[];
};

export function memberSetupComputers(
  runtimes: RuntimeDto[],
  localDaemonId: string | null,
) {
  const computers = new Map<string, SetupComputer>();
  for (const runtime of runtimes) {
    const computer = computers.get(runtime.daemonId);
    if (computer) computer.runtimes.push(runtime);
    else
      computers.set(runtime.daemonId, {
        id: runtime.daemonId,
        name: runtimeComputerName(runtime),
        runtimes: [runtime],
      });
  }
  return [...computers.values()].sort(
    (left, right) =>
      Number(right.id === localDaemonId) - Number(left.id === localDaemonId) ||
      Number(
        right.runtimes.some(
          (runtime) => runtime.status === "online" && !runtime.unusableReason,
        ),
      ) -
        Number(
          left.runtimes.some(
            (runtime) => runtime.status === "online" && !runtime.unusableReason,
          ),
        ) ||
      left.name.localeCompare(right.name),
  );
}

export function memberSetupRuntime(agent: AgentDto, computer?: SetupComputer) {
  return computer?.runtimes.find(
    (runtime) => runtime.provider === agent.provider && !runtime.unusableReason,
  );
}
