import { useUpdateWorkspaceTaskMutation } from "@/shared/api/task-api";
import { useCallback, useState, type ReactNode } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import {
  isAgentBindingRequired,
  sendWithAgentSetup,
} from "@/features/agents/model/agent-binding";
import { TaskHandoffDialog } from "@/features/company-plan/components/task-handoff-dialog";
import type { ZenithTeamApiError } from "@/shared/api/zenith-team-api";

import type {
  AgentDto,
  TaskDto,
  TaskHandoffConfirmationErrorDto,
  TaskUpdateRequest,
  TaskUpdateResponse,
} from "@/shared/api/types";
import { toast } from "@/shared/ui/sonner";

type HandoffRequest = {
  task: TaskDto;
  request: TaskUpdateRequest;
};

// Changing who owns a Task, or who does its work, is a handoff (ADR 0014).
// While the Task has never run it is an ordinary edit; once it has, the
// server asks for confirmation first, because confirming cancels the run in
// flight and starts the work again from the project's main branch on the new
// assignee's computer. This hook is that round trip: send, and if the server
// asks, show what the handoff costs and send again with the confirmation.
// It says how it went in a toast, so a caller only offers the choice.
export function useTaskHandoff(companyId: string): {
  dialog: ReactNode;
  saving: boolean;
  submitTaskChange: (
    task: TaskDto,
    request: TaskUpdateRequest,
  ) => Promise<boolean>;
} {
  const viewerUserId = useSessionSnapshot().userId;
  const [updateTask, mutation] = useUpdateWorkspaceTaskMutation();
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  const [pending, setPending] = useState<HandoffRequest | null>(null);
  const [runLive, setRunLive] = useState(false);

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

  // A change that lands the Task on the viewer with an Agent starts its Run
  // on their computer, so the server refuses it until they have set that
  // Agent up. Only the server knows whether this change starts a Run, so
  // the setup is offered on its refusal, and the same change is sent again:
  // the refusal changed nothing.
  const send = useCallback(
    async (task: TaskDto, request: TaskUpdateRequest) => {
      const patch = () =>
        updateTask({ companyId, request, taskId: task.id }).unwrap();
      const executor = request.executedBy ?? task.executedBy;
      const agent =
        executor.kind === "agent"
          ? agentsQuery.currentData?.items.find(
              (candidate) => candidate.id === executor.id,
            )
          : undefined;
      const result = agent
        ? await sendWithAgentSetup(
            patch,
            { bound: true, name: agent.name },
            () => setUpAgent(agent),
          )
        : await patch();
      toast.success(handoffReceipt(task, result, viewerUserId));
    },
    [agentsQuery.currentData, companyId, setUpAgent, updateTask, viewerUserId],
  );

  const submitTaskChange = useCallback(
    async (task: TaskDto, request: TaskUpdateRequest) => {
      try {
        await send(task, request);
        return true;
      } catch (caught) {
        if (handoffConfirmationRequired(caught)) {
          // The refusal says whether a run is in flight right now, which is
          // what confirming throws away. The Task's status cannot: one in
          // progress may be paused on a request, or handed off once already,
          // with nothing live.
          setRunLive(handoffRunLive(caught));
          setPending({ request, task });
        } else {
          toast.error(taskChangeErrorMessage(caught));
        }
        return false;
      }
    },
    [send],
  );

  const dialog = (
    <>
      {pending ? (
        <TaskHandoffDialog
          onCancel={() => setPending(null)}
          onConfirm={async () => {
            try {
              await send(pending.task, {
                ...pending.request,
                confirmHandoff: true,
              });
              setPending(null);
            } catch (caught) {
              // Somebody changed the Task, the viewer lost the right to
              // hand it over, or they closed the setup: what the dialog
              // asked about is gone, so it closes and says why. Anything
              // else stays in the dialog to try again.
              if (!closesHandoff(caught)) throw caught;
              setPending(null);
              toast.error(taskChangeErrorMessage(caught));
            }
          }}
          open
          runLive={runLive}
          task={pending.task}
          request={pending.request}
        />
      ) : null}
      {bindDialog}
    </>
  );

  return { dialog, saving: mutation.isLoading, submitTaskChange };
}

// The mutation rejects with the ZenithTeamApiError itself, whose `code` is the
// API's; there is no `data` wrapper around it.
function errorCode(caught: unknown): string {
  return (caught as ZenithTeamApiError | undefined)?.code ?? "";
}

function handoffConfirmationRequired(caught: unknown): boolean {
  return errorCode(caught) === "task_handoff_confirmation_required";
}

function closesHandoff(caught: unknown): boolean {
  return (
    errorCode(caught) === "task_conflict" ||
    errorCode(caught) === "company_permission_required" ||
    isAgentBindingRequired(caught)
  );
}

// `runLive` rides flat on the refusal's error body (docs/api/errors.md).
// Confirming cancels whatever is live, so a body that does not say reads as
// live: the dialog warns too much rather than too little.
function handoffRunLive(caught: unknown): boolean {
  const body = (caught as ZenithTeamApiError | undefined)?.body as
    Partial<TaskHandoffConfirmationErrorDto> | null | undefined;
  return body?.runLive !== false;
}

// What the change did, said to the person who made it.
function handoffReceipt(
  before: TaskDto,
  after: TaskUpdateResponse,
  viewerUserId: string | undefined,
): string {
  if (after.runId && after.executedBy.kind === "agent") {
    return `Task updated — ${after.executedBy.name} is on it`;
  }
  if (after.assignee.id !== before.assignee.id) {
    return after.assignee.id === viewerUserId
      ? "Task handed to you"
      : `Task handed to ${after.assignee.name}`;
  }
  return "Task updated";
}

// A refused change, in plain words. The Task list refetches after every
// write, refusals included, so a stale Task is already being replaced when
// this shows.
function taskChangeErrorMessage(caught: unknown): string {
  switch (errorCode(caught)) {
    case "task_conflict":
      return "This task changed since you opened it. It has been refreshed; try again.";
    case "company_permission_required":
      return "Only the task's assignee, or a company owner or admin, can hand it over.";
  }
  const message = (caught as ZenithTeamApiError | undefined)?.message?.trim();
  return message || "The task could not be changed. Please try again.";
}
