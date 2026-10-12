import { useEffect, useRef, useState } from "react";
import {
  getCeoAgentApiErrorMessage,
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/features/agent-setup/api/ceo-agent-api";
import { useBindAgentMutation } from "@/features/agents/api/agent-binding-api";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import {
  memberSetupComputers,
  memberSetupRuntime,
} from "../model/member-agent-setup";

export function useMemberAgentSetup(companyId: string) {
  const roster = useListCompanyAgentsQuery(companyId, {
    refetchOnMountOrArgChange: true,
  });
  const runtimes = useListAgentRuntimesQuery(companyId, {
    refetchOnMountOrArgChange: true,
    pollingInterval: 5000,
    skipPollingIfUnfocused: true,
  });
  const [bindAgent] = useBindAgentMutation();
  const localDaemon = useLocalDaemonId();
  const [step, setStep] = useState<"computer" | "agents">("computer");
  const [computerId, setComputerId] = useState<string | null>(null);
  const [excludedIds, setExcludedIds] = useState<Set<string>>(new Set());
  const [savedIds, setSavedIds] = useState<Set<string>>(new Set());
  const [agentErrors, setAgentErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [savingId, setSavingId] = useState<string | null>(null);
  const submitting = useRef(false);
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);

  const agents = (roster.currentData?.items ?? []).filter(
    (agent) => !agent.archivedAt,
  );
  const computers = memberSetupComputers(
    runtimes.currentData?.items ?? [],
    localDaemon.daemonId,
  );
  const preferredComputer =
    computers.find((computer) =>
      agents.some(
        (agent) => !agent.binding && memberSetupRuntime(agent, computer),
      ),
    ) ?? computers[0];
  const computer =
    computerId === null
      ? localDaemon.known
        ? preferredComputer
        : undefined
      : computers.find((item) => item.id === computerId);
  const choices = agents.map((agent) => {
    const runtime = memberSetupRuntime(agent, computer);
    const saved = savedIds.has(agent.id);
    const selectable = !agent.binding && !saved && !!runtime;
    return {
      agent,
      runtime,
      saved,
      selectable,
      selected: selectable && !excludedIds.has(agent.id),
      error: agentErrors[agent.id],
    };
  });
  const selected = choices.filter((choice) => choice.selected);
  const allSetUp =
    agents.length > 0 &&
    choices.every(({ agent, saved }) => agent.binding || saved);
  const loading =
    (!roster.currentData && roster.isFetching) ||
    (!runtimes.currentData && runtimes.isFetching);
  const loadError = roster.error ?? runtimes.error;

  function toggleAgent(agentId: string) {
    setExcludedIds((current) => {
      const next = new Set(current);
      if (next.has(agentId)) next.delete(agentId);
      else next.add(agentId);
      return next;
    });
  }

  function chooseComputer(id: string) {
    setComputerId(id);
    setAgentErrors({});
    setError("");
  }

  function reviewAgents() {
    if (computer) setComputerId(computer.id);
    setStep("agents");
  }

  async function refresh() {
    await Promise.all([roster.refetch(), runtimes.refetch()]);
  }

  async function save() {
    if (submitting.current || !selected.length || !computer) return false;
    submitting.current = true;
    setSaving(true);
    setError("");
    setAgentErrors({});
    try {
      // Read again before writing: another tab may already have set up an
      // Agent, archived it, or removed the selected computer.
      const [freshRoster, freshRuntimes] = await Promise.all([
        roster.refetch().unwrap(),
        runtimes.refetch().unwrap(),
      ]);
      const failures: Record<string, string> = {};
      for (const choice of selected) {
        if (!active.current) return false;
        const agent = freshRoster.items.find(
          (item) => item.id === choice.agent.id && !item.archivedAt,
        );
        if (agent?.binding) continue;
        const runtime = freshRuntimes.items.find(
          (item) =>
            item.id === choice.runtime?.id &&
            !item.unusableReason &&
            item.provider === agent?.provider,
        );
        if (!agent || !runtime) {
          failures[choice.agent.id] =
            "This setup changed. Refresh and choose a computer again.";
          continue;
        }
        setSavingId(agent.id);
        try {
          await bindAgent({
            companyId,
            agentId: agent.id,
            runtimeId: runtime.id,
          }).unwrap();
          if (!active.current) return false;
          // Batch progress records only successful server acknowledgements;
          // it keeps a partial retry from moving an Agent a second time.
          setSavedIds((current) => new Set(current).add(agent.id));
        } catch (cause) {
          failures[agent.id] = getCeoAgentApiErrorMessage(
            cause,
            "Could not set up this agent. Try again.",
          );
        }
      }
      if (!active.current) return false;
      setAgentErrors(failures);
      if (Object.keys(failures).length) {
        setError(
          "Some agents could not be set up. Your saved setup is kept; retry the remaining agents or continue later.",
        );
        return false;
      }
      return true;
    } catch (cause) {
      if (active.current)
        setError(
          getCeoAgentApiErrorMessage(
            cause,
            "Could not refresh your setup. Try again.",
          ),
        );
      return false;
    } finally {
      submitting.current = false;
      if (active.current) {
        setSaving(false);
        setSavingId(null);
      }
    }
  }

  return {
    agents,
    choices,
    computers,
    computer,
    step,
    setStep,
    chooseComputer,
    reviewAgents,
    localDaemonId: localDaemon.daemonId,
    toggleAgent,
    selectAll: () => setExcludedIds(new Set()),
    selectedCount: selected.length,
    allSetUp,
    loading,
    loadError,
    error,
    saving,
    savingId,
    refreshing: roster.isFetching || runtimes.isFetching,
    refresh,
    save,
  };
}

export type MemberAgentSetup = ReturnType<typeof useMemberAgentSetup>;
