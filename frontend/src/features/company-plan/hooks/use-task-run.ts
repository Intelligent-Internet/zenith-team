import { useCallback, useRef, useState, type ReactNode } from "react";

import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import { taskStartAction } from "@/features/company-plan/model/task-execution";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { AgentDto, TaskDto } from "@/shared/api/types";
import { useStartWorkspaceTaskExecutionMutation } from "@/shared/api/workspace-api";
import { toast } from "@/shared/ui/sonner";

// Starting a Task is its assignee's own act, on their own computer (ADR
// 0014). This is that one flow for every screen that shows a Task: set the
// Agent up first when the viewer has not yet, start the Run, and say why
// the server refused in the words of whoever pressed the button. It
// resolves to the started Task, or to nothing when no Run started.
export function useTaskRun(companyId: string): {
  dialog: ReactNode;
  pendingTaskId: string | undefined;
  runTask: (task: TaskDto) => Promise<TaskDto | undefined>;
} {
  const viewerUserId = useSessionSnapshot().userId;
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const [startExecution] = useStartWorkspaceTaskExecutionMutation();
  const { dialog, openBindDialog } = useBindAgentDialog(companyId);
  const inFlight = useRef(false);
  const [pendingTaskId, setPendingTaskId] = useState<string>();

  const setUpAgent = useCallback(
    (agent: AgentDto) =>
      new Promise<boolean>((resolve) => {
        openBindDialog(agent, {
          onBound: () => resolve(true),
          onDismiss: () => resolve(false),
        });
      }),
    [openBindDialog],
  );

  const runTask = useCallback(
    async (task: TaskDto) => {
      const action = taskStartAction(task, viewerUserId);
      if (!action || inFlight.current) return undefined;
      inFlight.current = true;
      try {
        // Not set up yet: the button sets the Agent up first and the run
        // follows. The Task in hand still says "not set up" after the setup
        // succeeds, so the start does not ask again.
        const agent = agentsQuery.currentData?.items.find(
          (candidate) => candidate.id === task.execution.agent?.id,
        );
        if (action.kind === "set_up" && agent && !(await setUpAgent(agent)))
          return undefined;
        setPendingTaskId(task.id);
        const startedTask = await startExecution({
          companyId,
          request: { expectedRevision: task.revision },
          taskId: task.id,
        }).unwrap();
        toast.success(taskRunStartedMessage(startedTask, agent));
        return startedTask;
      } catch (error) {
        toast.error(runTaskErrorMessage(error, task));
        return undefined;
      } finally {
        inFlight.current = false;
        setPendingTaskId(undefined);
      }
    },
    [
      agentsQuery.currentData,
      companyId,
      setUpAgent,
      startExecution,
      viewerUserId,
    ],
  );

  return { dialog, pendingTaskId, runTask };
}

// What the start did, in words that stay true once the run is under way.
// The start answers before the run is claimed, so its status is always
// queued; what the viewer can know is whether their computer is ready and
// whether the Agent has a free task slot there (the slots are per binding,
// ADR 0014). With both, the run starts at once; without, it waits.
function taskRunStartedMessage(
  task: TaskDto,
  agent: AgentDto | undefined,
): string {
  if (!task.execution.assigneeReady) {
    return "Task run queued. It starts when your computer can run it.";
  }
  if (agent && agent.activeRunCount >= agent.maxConcurrentRuns) {
    return `Task run queued. ${agent.name} starts it when one of its task runs on your computer finishes.`;
  }
  return "Task run started on your computer.";
}

// The two refusals a Task run can meet under ADR 0014, in the words of the
// person who pressed the button. The mutation rejects with the
// BoardlyApiError itself, whose `code` is the API's.
function runTaskErrorMessage(error: unknown, task: TaskDto): string {
  const apiError = error as BoardlyApiError | undefined;
  if (apiError?.code === "task_assignee_required") {
    return `Only ${task.assignee.name} can start this task; it runs on their computer.`;
  }
  if (apiError?.code === "agent_binding_required") {
    return `Set ${task.execution.agent?.name ?? "the agent"} up on one of your computers first.`;
  }
  const message =
    typeof apiError?.message === "string" ? apiError.message.trim() : "";
  return message || "Task execution could not be started.";
}
