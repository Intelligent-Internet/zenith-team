import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  useCreateCeoAgentMutation,
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
  useUpdateCeoAgentMutation,
} from "@/features/agent-setup/api/ceo-agent-api";
import { useRuntimeModelDiscovery } from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import { useBindAgentMutation } from "@/features/agents/api/agent-binding-api";
import {
  createAgentRuntimeUpdate,
  createCeoAgentRequest,
  findCeoAgentInPage,
  findRuntime,
  providerThinkingLevels,
  runtimeReadyToRun,
} from "@/features/agent-setup/model/ceo-agent-setup";
import type { AgentDto, AgentProvider, RuntimeDto } from "@/shared/api/types";

// The computer a picker should propose for a provider: one of the viewer's
// own that is online and signed in, else any online one (ADR 0014). Every
// runtime on the roster is already theirs, so there is nobody else's to
// filter out.
function defaultRuntimeId(
  runtimes: RuntimeDto[],
  provider: AgentProvider,
): string {
  const ready = runtimes.find(
    (runtime) => runtime.provider === provider && runtimeReadyToRun(runtime),
  );
  const online = runtimes.find(
    (runtime) => runtime.provider === provider && runtime.status === "online",
  );
  return (ready ?? online)?.id ?? "";
}

export function useCeoAgentRuntimeController({
  companyId,
  agentId,
  requiredProvider,
  onSaved,
}: {
  companyId: string;
  agentId?: string;
  requiredProvider?: AgentProvider;
  onSaved?: () => void;
}) {
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    refetchOnMountOrArgChange: 10,
    skip: !companyId,
  });
  const runtimesQuery = useListAgentRuntimesQuery(companyId, {
    refetchOnMountOrArgChange: 10,
    skip: !companyId,
  });
  const [createCeoAgent, createMutation] = useCreateCeoAgentMutation();
  const [updateCeoAgent, updateMutation] = useUpdateCeoAgentMutation();
  const [bindAgent, bindMutation] = useBindAgentMutation();
  const agent = useMemo(
    () =>
      agentId
        ? agentsQuery.currentData?.items.find(
            (agent) => agent.id === agentId && !agent.archivedAt,
          )
        : findCeoAgentInPage(agentsQuery.currentData),
    [agentId, agentsQuery.currentData],
  );
  const runtimes = useMemo(
    () =>
      (runtimesQuery.currentData?.items ?? []).filter(
        (runtime) => !requiredProvider || runtime.provider === requiredProvider,
      ),
    [requiredProvider, runtimesQuery.currentData?.items],
  );
  // On an Agent's own page (agentId) this is only where model discovery
  // asks: the viewer's binding while it is online, else another of their
  // online computers for the Agent's provider, else none. Saving the
  // template never binds it and needs no computer; the bind dialog is what
  // moves the binding (ADR 0014). The onboarding panel picks the CEO's
  // computer itself, so there it is the picker's draft, and saving applies
  // it.
  const [pickedRuntimeId, setPickedRuntimeId] = useState("");
  const bindingRuntime = agent?.binding
    ? findRuntime(runtimes, agent.binding.runtime.id)
    : null;
  const selectedRuntimeId = agentId
    ? bindingRuntime?.status === "online"
      ? bindingRuntime.id
      : agent
        ? defaultRuntimeId(runtimes, agent.provider)
        : ""
    : pickedRuntimeId;
  const [model, setModel] = useState("");
  const [thinkingLevel, setThinkingLevelState] = useState("");
  const [savedMessage, setSavedMessage] = useState("");
  const draftTouched = useRef(false);
  const selectedRuntime = useMemo(
    () => findRuntime(runtimes, selectedRuntimeId),
    [runtimes, selectedRuntimeId],
  );
  const modelDiscovery = useRuntimeModelDiscovery({
    companyId,
    runtime: selectedRuntime,
  });
  const modelOptions = modelDiscovery.models;
  const modelIsReported = modelOptions.some((option) => option.id === model);
  // Discovery is optional on an Agent's page. When no computer listed any
  // models, the model is typed and thinking offers the provider's levels.
  const modelIsTyped =
    Boolean(agentId && agent) &&
    !runtimesQuery.isLoading &&
    !modelOptions.length &&
    (selectedRuntime?.status !== "online" ||
      !["idle", "pending", "running"].includes(modelDiscovery.status));
  const selectedModel =
    modelOptions.find((option) => option.id === model) ??
    (!model ? modelOptions.find((option) => option.default) : undefined);
  const thinkingOptions =
    modelIsTyped && agent
      ? providerThinkingLevels(agent.provider)
      : (selectedModel?.reasoning?.levels ?? []);

  useEffect(() => {
    if (draftTouched.current) {
      return;
    }

    if (agent) {
      setPickedRuntimeId(
        agent.binding?.runtime.id ?? defaultRuntimeId(runtimes, agent.provider),
      );
      setModel(agent.model ?? "");
      setThinkingLevelState(agent.thinkingLevel ?? "");
      return;
    }

    setPickedRuntimeId(defaultRuntimeId(runtimes, "codex"));
    setModel("");
    setThinkingLevelState("");
  }, [agent, runtimes]);

  const selectRuntime = useCallback(
    (runtimeId: string) => {
      if (runtimeId === selectedRuntimeId) {
        return;
      }
      draftTouched.current = true;
      setPickedRuntimeId(runtimeId);
      setModel("");
      setThinkingLevelState("");
      setSavedMessage("");
    },
    [selectedRuntimeId],
  );

  const selectModel = useCallback(
    (nextModel: string) => {
      draftTouched.current = true;
      setModel(nextModel);
      setSavedMessage("");
      // A typed model keeps the thinking level: the provider's levels do not
      // depend on the model.
      if (modelIsTyped) {
        return;
      }
      const nextOption =
        modelOptions.find((option) => option.id === nextModel) ??
        (!nextModel
          ? modelOptions.find((option) => option.default)
          : undefined);
      setThinkingLevelState(nextOption?.reasoning?.defaultLevel ?? "");
    },
    [modelIsTyped, modelOptions],
  );

  const setThinkingLevel = useCallback((nextLevel: string) => {
    draftTouched.current = true;
    setThinkingLevelState(nextLevel);
    setSavedMessage("");
  }, []);

  const saveIsLoading =
    createMutation.isLoading ||
    updateMutation.isLoading ||
    bindMutation.isLoading;
  // On an Agent's page saving writes only the template, so it needs no
  // computer. The onboarding panel creates or binds the CEO Agent on the
  // picked one, so there it must be online.
  const canSave =
    Boolean(companyId) &&
    (!requiredProvider || !agent || agent.provider === requiredProvider) &&
    (agentId ? Boolean(agent) : selectedRuntime?.status === "online") &&
    !saveIsLoading;

  const save = useCallback(async () => {
    if (!canSave) {
      return;
    }

    setSavedMessage("");
    let savedAgent: AgentDto;
    if (agent) {
      savedAgent = await updateCeoAgent({
        agentId: agent.id,
        companyId,
        payload: createAgentRuntimeUpdate(agent, { model, thinkingLevel }),
      }).unwrap();
      // Moving the Agent to another computer of this member's is a separate
      // write, because it is their setup rather than the Agent's (ADR 0014).
      // Only the onboarding picker makes it here; on the Agent's own page
      // the bind dialog does, and the runtime above is only where the models
      // were listed.
      if (
        !agentId &&
        selectedRuntime &&
        savedAgent.binding?.runtime.id !== selectedRuntime.id
      ) {
        savedAgent = await bindAgent({
          agentId: savedAgent.id,
          companyId,
          runtimeId: selectedRuntime.id,
        }).unwrap();
      }
    } else if (selectedRuntime) {
      const payload = createCeoAgentRequest({
        model,
        provider: selectedRuntime.provider,
        runtimeId: selectedRuntime.id,
        thinkingLevel,
      });
      savedAgent = await createCeoAgent({
        companyId,
        payload,
      }).unwrap();
    } else {
      return;
    }

    draftTouched.current = false;
    setPickedRuntimeId(
      savedAgent.binding?.runtime.id ?? selectedRuntime?.id ?? "",
    );
    setModel(savedAgent.model ?? "");
    setThinkingLevelState(savedAgent.thinkingLevel ?? "");
    setSavedMessage(
      agent ? `${agent.name} settings saved.` : "CEO Agent created.",
    );
    onSaved?.();
  }, [
    agent,
    agentId,
    bindAgent,
    canSave,
    companyId,
    createCeoAgent,
    model,
    onSaved,
    selectedRuntime,
    thinkingLevel,
    updateCeoAgent,
  ]);

  const refresh = useCallback(async () => {
    if (!companyId) {
      return;
    }
    await Promise.all([agentsQuery.refetch(), runtimesQuery.refetch()]);
    modelDiscovery.refresh();
  }, [agentsQuery.refetch, companyId, modelDiscovery, runtimesQuery.refetch]);

  return {
    agent,
    agents: agentsQuery.currentData?.items ?? [],
    canSave,
    connectionsAreRefreshing: runtimesQuery.isFetching,
    isLoading:
      (agentsQuery.currentData === undefined && agentsQuery.isFetching) ||
      (runtimesQuery.currentData === undefined && runtimesQuery.isFetching),
    loadError: agentsQuery.error ?? runtimesQuery.error,
    // The roster alone: a settings view that already holds its Agent needs
    // to know whether an empty roster is a fact or a failed read.
    rosterUnavailable:
      runtimesQuery.currentData === undefined && runtimesQuery.isError,
    model,
    modelDiscoveryError: modelDiscovery.message,
    modelDiscoveryStatus: modelDiscovery.status,
    modelIsReported,
    modelIsTyped,
    modelOptions,
    provider: selectedRuntime?.provider ?? null,
    refresh,
    runtimes,
    save,
    saveError:
      createMutation.error ?? updateMutation.error ?? bindMutation.error,
    saveIsLoading,
    savedMessage,
    selectedRuntime,
    selectedRuntimeId,
    selectModel,
    selectRuntime,
    setThinkingLevel,
    thinkingLevel,
    thinkingOptions,
  };
}
