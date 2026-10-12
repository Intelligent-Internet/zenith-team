import { useCallback, useEffect, useMemo, useReducer, useState } from "react";

import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { useContextChatPanel } from "@/features/chat-panel/hooks/use-context-chat-panel";
import {
  initialScheduleRunChatState,
  resolveScheduleRunChatSelection,
  resolveScheduleRunChatTarget,
  scheduleRunChatReducer,
} from "@/features/scheduled/model/schedule-run-chat";
import type { TeamNodeData } from "@/features/home/types";
import { isRootAgent } from "@/shared/api/adapters";
import { agentChatTeamId } from "@/shared/chat/chat-team";
import type {
  ScheduleRunRecord,
  ScheduledTask,
} from "@/shared/api/schedule.service";

const ignoreScheduleMessage = () => undefined;

export function useScheduleRunChatPanel({
  companyId,
  runs,
  schedule,
}: {
  companyId: string;
  runs: readonly ScheduleRunRecord[];
  schedule?: ScheduledTask;
}) {
  const [chatState, dispatchChat] = useReducer(
    scheduleRunChatReducer,
    initialScheduleRunChatState,
  );
  const [draftMessage, setDraftMessage] = useState("");
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId || !schedule,
  });
  const agent = useMemo(
    () =>
      agentsQuery.currentData?.items.find(
        (candidate) => candidate.id === schedule?.agentId,
      ),
    [agentsQuery.currentData?.items, schedule?.agentId],
  );
  const team = agent
    ? agentChatTeamId(agent.id, isRootAgent(agent))
    : (schedule?.agentId ?? "scheduled-agent");
  const activeTeam = useMemo<TeamNodeData>(
    () => ({
      agentId: agent?.id ?? schedule?.agentId,
      agentRole:
        agent?.title ??
        `${schedule?.provider === "claude" ? "Claude" : "Codex"} Agent`,
      available: agent?.runtimeAvailable ?? true,
      currentWork: schedule?.name ?? "Scheduled task",
      href: schedule ? `/scheduled/${schedule.id}` : "/scheduled",
      iconSrc: "",
      label: agent?.name ?? "Scheduled Agent",
      notificationCount: 0,
      status: agent?.runtimeAvailable === false ? "idle" : "active",
      teamId: team,
    }),
    [agent, schedule, team],
  );
  const teams = useMemo(() => [activeTeam], [activeTeam]);
  const selectedRun = runs.find(
    (run) => run.firingId === chatState.selectedFiringId,
  );
  const chatSelection = useMemo(
    () =>
      resolveScheduleRunChatSelection(
        chatState,
        selectedRun ? [selectedRun] : [],
      ),
    [
      chatState.retryRevision,
      chatState.selectedFiringId,
      selectedRun?.conversationId,
      selectedRun?.disposition,
      selectedRun?.firingId,
      selectedRun?.messageId,
      selectedRun?.messageSeq,
      selectedRun?.runId,
      selectedRun?.runStatus,
    ],
  );
  const closeChat = useCallback(() => {
    dispatchChat({ type: "closed" });
  }, []);
  const openRunChat = useCallback((run: ScheduleRunRecord) => {
    if (!resolveScheduleRunChatTarget(run)) return;
    dispatchChat({ firingId: run.firingId, type: "opened" });
  }, []);
  const handleHistoryAvailable = useCallback(() => {
    dispatchChat({ type: "load_succeeded" });
  }, []);
  const handleHistoryUnavailable = useCallback(() => {
    dispatchChat({
      message: "This scheduled run conversation could not be loaded.",
      type: "load_failed",
    });
  }, []);
  const retryHistory = useCallback(() => {
    dispatchChat({ type: "retried" });
  }, []);
  useEffect(() => {
    dispatchChat({ type: "closed" });
  }, [companyId, schedule?.id]);
  const chatPanel = useContextChatPanel({
    activeTeam,
    conversationId: chatSelection?.target.conversationId,
    conversationSlot: chatState.loadError ? (
      <ScheduleRunChatLoadError
        message={chatState.loadError}
        onRetry={retryHistory}
      />
    ) : undefined,
    draftMessage,
    enabled: Boolean(chatSelection),
    focusRequestKey: 0,
    focusedHistoryRequestKey: chatSelection?.focusedHistoryRequestKey,
    focusedMessage: chatSelection?.target.focusedMessage,
    isExpanded: false,
    messages: [],
    onAppendMessage: ignoreScheduleMessage,
    onClose: closeChat,
    onDraftMessageChange: setDraftMessage,
    onExitFocusedHistory: closeChat,
    onFocusedHistoryAvailable: handleHistoryAvailable,
    onFocusedHistoryUnavailable: handleHistoryUnavailable,
    readOnly: true,
    selectedTeam: team,
    surface: chatSelection?.target.surface,
    teams,
  });

  return { chatPanel, openRunChat };
}

function ScheduleRunChatLoadError({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <div className="flex items-center justify-between gap-3" role="alert">
      <p className="text-xs font-semibold leading-5 text-red-50/72">
        {message}
      </p>
      <button
        className="shrink-0 rounded-lg border border-white/14 bg-white/[0.06] px-3 py-2 text-xs font-semibold text-white/75 transition hover:bg-white/10 hover:text-white"
        type="button"
        onClick={onRetry}
      >
        Try again
      </button>
    </div>
  );
}
