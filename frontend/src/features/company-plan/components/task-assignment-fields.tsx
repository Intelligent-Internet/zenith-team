import * as Popover from "@radix-ui/react-popover";
import {
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useTaskHandoff } from "@/features/company-plan/hooks/use-task-handoff";
import type { TaskDto } from "@/shared/api/types";
import { hasProjectPermission, useCan } from "@/shared/permissions";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Check, ChevronDown, LockKeyhole } from "@/shared/ui/icons";
import { TaskAssignee, TaskMemberAvatar } from "./task-assignee";
// The pickers are drawn as property inputs, whose rule lives with the task
// page; chat's task peek shows them without ever opening that page. Their
// list is the one New task offers Agents in.
import "@/features/company-plan/styles/task-detail-page.css";
import "@/features/company-plan/styles/new-task-dialog.css";
import "@/features/company-plan/styles/task-handoff.css";

// Who a Task belongs to and who does its work, as the values of two property
// rows (ADR 0014). Whoever may hand the Task over — a tasks.assign holder,
// or its assignee — picks from a list; everyone else reads the names. Both
// save through the handoff round trip, which asks first once the Task has
// run.

// The executedBy value that means "the assignee does it themself".
const SELF = "self";

function useCanHandOver(task: TaskDto): boolean {
  const viewerUserId = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  // A handoff is a Task edit too, so it also needs "Create and edit tasks"
  // on the Task's Project (ADR 0017).
  return (
    hasProjectPermission(task.access, "project.tasks.write") &&
    (canAssign || (Boolean(viewerUserId) && task.assignee.id === viewerUserId))
  );
}

export function TaskAssigneeField({ task }: { task: TaskDto }) {
  const viewerUserId = useSessionSnapshot().userId;
  const canHandOver = useCanHandOver(task);
  const membersQuery = useListActiveCompanyMembersQuery(task.companyId, {
    skip: !canHandOver,
  });
  const { dialog, saving, submitTaskChange } = useTaskHandoff(task.companyId);
  if (!canHandOver) {
    return <TaskAssignee assignee={task.assignee} />;
  }
  // A disabled account cannot run anything, so it is not offered; the
  // current assignee stays listed whatever their state, so the list can say
  // who has the Task now.
  const members = (membersQuery.currentData?.items ?? [])
    .filter(
      (member) =>
        !member.accountDisabled || member.actor.id === task.assignee.id,
    )
    .map((member) => member.actor);
  const listed = members.some((member) => member.id === task.assignee.id);
  // Taking an open Agent's Task for themself starts its Run, the same rule
  // as Executed by below.
  const selfLocked =
    task.executedBy.kind === "agent" &&
    task.assignee.id !== viewerUserId &&
    task.status !== "done" &&
    task.status !== "cancelled" &&
    !hasProjectPermission(task.access, "project.tasks.execute");
  return (
    <span className="task-assignment-field">
      <TaskMemberAvatar member={task.assignee} />
      <TaskAssignmentPicker
        label="Assignee"
        value={task.assignee.id}
        valueLabel={task.assignee.name}
        saving={saving}
        options={(listed ? members : [task.assignee, ...members]).map(
          (member) => ({
            value: member.id,
            label:
              member.id === viewerUserId ? `${member.name} (you)` : member.name,
            ...(selfLocked && member.id === viewerUserId
              ? { hint: "Needs Run tasks", locked: true }
              : {}),
            avatar: <TaskMemberAvatar member={member} />,
          }),
        )}
        onPick={(id) =>
          void submitTaskChange(task, {
            expectedRevision: task.revision,
            assignee: { kind: "user", id },
          })
        }
      />
      {dialog}
    </span>
  );
}

export function TaskExecutorField({ task }: { task: TaskDto }) {
  const viewerUserId = useSessionSnapshot().userId;
  const canHandOver = useCanHandOver(task);
  const agentsQuery = useListCompanyAgentsQuery(task.companyId, {
    skip: !canHandOver,
  });
  const { dialog, saving, submitTaskChange } = useTaskHandoff(task.companyId);
  // Work the assignee does themself shows their face: a person never
  // carries an Agent's avatar, and an Agent never a person's.
  const avatar =
    task.executedBy.kind === "agent" ? (
      <FoldAvatar paused agentId={task.executedBy.id} size={22} />
    ) : (
      <TaskMemberAvatar member={task.assignee} />
    );
  // Work the assignee does themself names them, as the Assignee row does.
  const executorName =
    task.executedBy.kind === "agent"
      ? task.executedBy.name
      : task.assignee.name;
  if (!canHandOver) {
    return (
      <span className="task-assignment-field">
        {avatar}
        <span>{executorName}</span>
      </span>
    );
  }
  const agents =
    agentsQuery.currentData?.items.filter((agent) => !agent.archivedAt) ?? [];
  const current = task.executedBy.kind === "agent" ? task.executedBy.id : SELF;
  const viewerIsAssignee = task.assignee.id === viewerUserId;
  // Giving their own open Task to an Agent starts its Run, which needs "Run
  // tasks with agents" on the Project (ADR 0017); without it the Agents
  // stay listed but cannot be picked. Moving someone else's Task, or a
  // closed one, to an Agent starts nothing, so it is not gated.
  const agentsLocked =
    viewerIsAssignee &&
    task.status !== "done" &&
    task.status !== "cancelled" &&
    !hasProjectPermission(task.access, "project.tasks.execute");
  // An archived Agent can still be doing a Task; it stays named until the
  // Task moves, but cannot be chosen again. Until the roster loads, the
  // current Agent is simply named.
  const archived =
    Boolean(agentsQuery.currentData) &&
    current !== SELF &&
    !agents.some((agent) => agent.id === current);
  return (
    <span className="task-assignment-field">
      {avatar}
      <TaskAssignmentPicker
        label="Executed by"
        value={current}
        valueLabel={`${executorName}${archived ? " (archived)" : ""}`}
        saving={saving}
        options={[
          {
            value: SELF,
            label: viewerIsAssignee
              ? `${task.assignee.name} (you)`
              : task.assignee.name,
            hint: viewerIsAssignee
              ? "I do it myself. No agent runs."
              : "Does it themself. No agent runs.",
            avatar: <TaskMemberAvatar member={task.assignee} />,
          },
          ...agents.map((agent) => {
            const locked = agentsLocked && agent.id !== current;
            return {
              value: agent.id,
              label: agent.name,
              hint: locked ? "Needs Run tasks" : agent.title,
              locked,
              avatar: <FoldAvatar paused agentId={agent.id} size={30} />,
            };
          }),
        ]}
        onPick={(id) =>
          void submitTaskChange(task, {
            expectedRevision: task.revision,
            executedBy:
              id === SELF
                ? { kind: "user", id: task.assignee.id }
                : { kind: "agent", id },
          })
        }
      />
      {dialog}
    </span>
  );
}

type AssignmentOption = {
  value: string;
  label: string;
  hint?: string;
  /** Listed but not offered: a lock where the tick goes; the hint says why. */
  locked?: boolean;
  avatar: ReactNode;
};

// The list both rows pick from. It saves only on a choice: a click, or Enter
// on an option. A native select would not do: on Windows, arrowing through a
// closed one already changes it, and every change here is a handoff.
function TaskAssignmentPicker({
  label,
  value,
  valueLabel,
  options,
  saving,
  onPick,
}: {
  label: string;
  value: string;
  valueLabel: string;
  options: AssignmentOption[];
  saving: boolean;
  onPick: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  return (
    <Popover.Root open={open} onOpenChange={(next) => setOpen(next && !saving)}>
      {/* aria-disabled, not disabled: a disabled button drops focus to the
          page while the change saves. */}
      <Popover.Trigger
        ref={triggerRef}
        type="button"
        className="task-property-input task-assignment-trigger"
        aria-label={`${label}, ${valueLabel}`}
        aria-disabled={saving || undefined}
      >
        <span>{valueLabel}</span>
        <ChevronDown size={12} aria-hidden="true" />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="task-create-agent-menu task-assignment-menu"
          aria-label={label}
          align="start"
          sideOffset={4}
          collisionPadding={12}
          onKeyDown={moveFocusWithArrows}
        >
          <div className="task-create-agent-options">
            {options.map((option) => {
              const chosen = option.value === value;
              return (
                <button
                  type="button"
                  key={option.value}
                  aria-pressed={chosen}
                  // aria-disabled, not disabled: the arrows still reach it,
                  // and its hint says why it can't be picked.
                  aria-disabled={option.locked || undefined}
                  data-locked={option.locked ? "" : undefined}
                  // The list opens on the current choice, so arrowing
                  // starts from there.
                  autoFocus={chosen}
                  onClick={() => {
                    if (option.locked) return;
                    // Focus goes back to the row before the change is sent,
                    // so the handoff dialog it may open returns it there.
                    triggerRef.current?.focus();
                    setOpen(false);
                    if (!chosen) onPick(option.value);
                  }}
                >
                  {option.avatar}
                  <span>
                    <strong>{option.label}</strong>
                    {option.hint ? <small>{option.hint}</small> : null}
                  </span>
                  {chosen ? (
                    <Check size={14} />
                  ) : option.locked ? (
                    <LockKeyhole aria-hidden="true" size={13} />
                  ) : null}
                </button>
              );
            })}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

// Up and down arrows move between the options; only a click or Enter picks.
function moveFocusWithArrows(event: ReactKeyboardEvent<HTMLElement>) {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  event.preventDefault();
  const options = Array.from(
    event.currentTarget.querySelectorAll<HTMLButtonElement>("button"),
  );
  const step = event.key === "ArrowDown" ? 1 : -1;
  const index = options.findIndex((option) => option === event.target);
  const next =
    index < 0
      ? options[step > 0 ? 0 : options.length - 1]
      : options[(index + step + options.length) % options.length];
  next?.focus();
}
