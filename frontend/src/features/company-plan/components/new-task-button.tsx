import { useCreateWorkspaceTaskMutation } from "@/shared/api/task-api";
import type { CalendarEntryDraft } from "@/features/calendar/event-editor";
import {
  CalendarEventDialog,
  type CalendarEventAnchor,
} from "@/features/calendar/calendar-event-dialog";
import { CalendarOptionPicker } from "@/features/calendar/calendar-pickers";
import { useCalendarTheme } from "@/features/calendar/calendar-theme";
import {
  TaskScheduleFields,
  taskScheduleValues,
  taskScheduleTrigger,
  localTaskTime,
} from "@/features/calendar/task-schedule-fields";
import {
  milestoneLabel,
  milestoneTypes,
} from "@/features/company-plan/model/milestone-catalog";
import { useListProjectsQuery } from "../api/project-api";
import { useRef, useState, type FormEvent } from "react";
import {
  Bell,
  CalendarDays,
  ChevronDown,
  ListTodo,
  ArrowLeftRight,
  ArrowUpRight,
  ChevronRight,
  Circle,
  FolderOpen,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  Plus,
  SignalMedium,
  Users,
  X,
} from "@/shared/ui/icons";
import { useNavigate, useSearchParams } from "react-router";
import { readSessionSnapshot } from "@/shared/session/session";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import {
  agentAvailabilityHelp,
  isAgentBindingRequired,
  sendWithAgentSetup,
} from "@/features/agents/model/agent-binding";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";

import type { AgentDto, TaskCreateRequest } from "@/shared/api/types";
import { chatConversationTargetKey } from "@/shared/chat/conversation-target";
import { hasProjectPermission, useCan } from "@/shared/permissions";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import {
  selectConversationWorkspaceDraft,
  setConversationWorkspaceDraft,
  useAppDispatch,
  useAppSelector,
} from "@/state";
import { cannotRunTasksHereLabel } from "../model/task-execution";
import { ManageAccessLink } from "./task-access-fix-link";
import { TaskCreationAgentPicker } from "./task-creation-agent-picker";
import "../styles/new-task-dialog.css";

export function NewTaskButton({
  companyId,
  projectId,
}: {
  companyId: string;
  projectId?: string;
}) {
  const [open, setOpen] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [createTask, mutation] = useCreateWorkspaceTaskMutation();
  const navigate = useNavigate();
  // Creating a task you run with an Agent starts its Run on your own
  // computer (ADR 0014), so an Agent you have not set up is set up first.
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isLoading) {
          setOpen(next);
          if (!next) setExpanded(false);
        }
      }}
    >
      <DialogTrigger asChild>
        <button
          className="dashboard-button dashboard-button-primary work-new-task"
          type="button"
        >
          <Plus size={16} aria-hidden="true" /> New task
        </button>
      </DialogTrigger>
      {open ? (
        <DialogContent
          className="new-task-dialog"
          data-expanded={expanded}
          overlayClassName="new-task-overlay"
          showCloseButton={false}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <NewTaskForm
            initialProjectId={projectId}
            companyId={companyId}
            pending={mutation.isLoading}
            expanded={expanded}
            onToggleExpanded={() => setExpanded((value) => !value)}
            onClose={() => {
              setOpen(false);
              setExpanded(false);
            }}
            onCreate={async (request, another) => {
              const task = await createTask({ companyId, request }).unwrap();
              // An agent task is committed in progress with its assignment
              // run already queued, so the receipt says who is on it rather
              // than leaving the person looking for a Start. A task for
              // someone else waits for them to start it.
              toast.success(
                task.executedBy.kind === "agent" &&
                  task.status === "in_progress"
                  ? `Task created — ${task.executedBy.name} is on it`
                  : task.assignee.id !== readSessionSnapshot().userId
                    ? `Task created for ${task.assignee.name}`
                    : "Task created",
              );
              if (!another) {
                setOpen(false);
                setExpanded(false);
                navigate(
                  projectId
                    ? `/projects/${projectId}/tasks?taskId=${task.id}`
                    : `/tasks?taskId=${task.id}&taskView=full`,
                );
              }
            }}
            onSetUpAgent={(agent) =>
              new Promise<boolean>((resolve) => {
                openBindDialog(agent, {
                  onBound: () => resolve(true),
                  onDismiss: () => resolve(false),
                });
              })
            }
          />
          {bindDialog}
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

export function CalendarTaskCreate({
  companyId,
  day,
  hour = 9,
  minute = 0,
  anchor,
  onClose,
  onSelectType,
  draft,
}: {
  companyId: string;
  day: Date;
  hour?: number;
  minute?: number;
  anchor: CalendarEventAnchor;
  onClose: () => void;
  draft?: CalendarEntryDraft;
  onSelectType: (type: "event" | "reminder", draft: CalendarEntryDraft) => void;
}) {
  const theme = useCalendarTheme();
  const [createTask, mutation] = useCreateWorkspaceTaskMutation();
  const [scheduledAt] = useState(() =>
    localTaskTime(
      new Date(
        Math.max(
          new Date(
            day.getFullYear(),
            day.getMonth(),
            day.getDate(),
            hour,
            minute,
          ).getTime(),
          Math.ceil((Date.now() + 3600000) / 60000) * 60000,
        ),
      ),
    ),
  );
  return (
    <CalendarEventDialog
      anchor={anchor}
      title="New task"
      description="Schedule work on the assignee’s computer."
      className="calendar-editor"
      style={theme}
      busy={mutation.isLoading}
      onClose={onClose}
    >
      <NewTaskForm
        companyId={companyId}
        initialScheduledAt={scheduledAt}
        calendar={{ onClose, onSelectType, draft }}
        pending={mutation.isLoading}
        expanded={false}
        onToggleExpanded={() => {}}
        onClose={onClose}
        onSetUpAgent={async () => false}
        onCreate={async (request) => {
          await createTask({ companyId, request }).unwrap();
          toast.success("Task scheduled");
          onClose();
        }}
      />
    </CalendarEventDialog>
  );
}

function NewTaskForm({
  initialProjectId,
  initialScheduledAt,
  calendar,
  companyId,
  pending,
  expanded,
  onToggleExpanded,
  onClose,
  onCreate,
  onSetUpAgent,
}: {
  companyId: string;
  initialProjectId?: string;
  initialScheduledAt?: string;
  calendar?: {
    onClose: () => void;
    draft?: CalendarEntryDraft;
    onSelectType: (
      type: "event" | "reminder",
      draft: CalendarEntryDraft,
    ) => void;
  };
  pending: boolean;
  expanded: boolean;
  onToggleExpanded: () => void;
  onClose: () => void;
  onCreate: (request: TaskCreateRequest, another: boolean) => Promise<void>;
  onSetUpAgent: (agent: AgentDto) => Promise<boolean>;
}) {
  const session = readSessionSnapshot();
  const dispatch = useAppDispatch();
  const navigate = useNavigate();
  const projectsQuery = useListProjectsQuery(companyId);
  // A Task can be created only where the caller holds "Create and edit
  // tasks" (ADR 0017).
  const writableProjects = (projectsQuery.data?.items ?? []).filter(
    (p) =>
      !p.archivedAt && hasProjectPermission(p.access, "project.tasks.write"),
  );
  const [params] = useSearchParams();
  const [requestedProjectId, setProjectId] = useState(
    initialProjectId ?? params.get("projectId") ?? "",
  );
  // A Project passed in or named in the link counts only where the caller
  // may create tasks; otherwise they pick one from the list.
  const projectId = writableProjects.some(
    (project) => project.id === requestedProjectId,
  )
    ? requestedProjectId
    : "";
  const agentsQuery = useListCompanyAgentsQuery(companyId);
  const allAgents =
    agentsQuery.currentData?.items.filter((agent) => !agent.archivedAt) ?? [];
  const taskAgents = allAgents;
  const [mode, setMode] = useState<"agent" | "manual">(
    initialScheduledAt ? "manual" : "agent",
  );
  const [title, setTitle] = useState(calendar?.draft?.title ?? "");
  const [description, setDescription] = useState(
    calendar?.draft?.description ?? "",
  );
  const [proposalOnly, setProposalOnly] = useState(false);
  const [reviewRequired, setReviewRequired] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [agentId, setAgentId] = useState<string>();
  const [creatorId, setCreatorId] = useState<string>();
  const [executorKind, setExecutorKind] = useState<"user" | "agent">("user");
  // A Task is yours unless you may assign Tasks and give it to another
  // member (ADR 0014). Theirs waits for them to start it on their computer.
  const canAssign = useCan("tasks.assign");
  const membersQuery = useListActiveCompanyMembersQuery(companyId, {
    skip: !canAssign,
  });
  const otherMembers = (membersQuery.currentData?.items ?? [])
    .filter(
      (member) => member.actor.id !== session.userId && !member.accountDisabled,
    )
    .map((member) => member.actor);
  const [assigneeId, setAssigneeId] = useState("");
  const otherAssignee = otherMembers.find((member) => member.id === assigneeId);
  // Only a Task of your own with an Agent starts a Run as it is created.
  const [scheduled, setScheduled] = useState(Boolean(initialScheduledAt));
  const [scheduleValues, setScheduleValues] = useState(() =>
    taskScheduleValues(undefined, initialScheduledAt),
  );
  const startsRun = executorKind === "agent" && !otherAssignee && !scheduled;
  // That Run needs "Run tasks with agents" on the chosen Project (ADR 0017).
  // Without it the server refuses the whole create, so the dialog gives that
  // reason and never sends the viewer to the Agent's setup, which would not
  // help. A Task they do themself, or one for someone else, still works.
  const chosenAccess = writableProjects.find(
    (project) => project.id === projectId,
  )?.access;
  const canRunHere = hasProjectPermission(
    chosenAccess,
    "project.tasks.execute",
  );
  const projectRefusesRun = Boolean(projectId) && !canRunHere;
  const runRefused = startsRun && projectRefusesRun;
  // Someone who manages a Project's access fixes either refusal themself,
  // so the reason carries the link to Team › Project access instead.
  const managesChosenAccess = hasProjectPermission(
    chosenAccess,
    "project.grants.manage",
  );
  const managesSomeAccess = (projectsQuery.data?.items ?? []).some(
    (project) =>
      !project.archivedAt &&
      hasProjectPermission(project.access, "project.grants.manage"),
  );
  const [priority, setPriority] =
    useState<NonNullable<TaskCreateRequest["priority"]>>("medium");
  const [milestone, setMilestone] =
    useState<TaskCreateRequest["milestoneType"]>("product");
  const [more, setMore] = useState(false);
  const [another, setAnother] = useState(false);
  const [error, setError] = useState<string>();
  const titleRef = useRef<HTMLInputElement>(null);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const submitting = useRef(false);
  const selectedAgent =
    taskAgents.find((agent) => agent.id === agentId) ?? taskAgents[0];
  const selectedCreator =
    allAgents.find((agent) => agent.id === creatorId) ??
    allAgents.find((agent) => !agent.reportsTo) ??
    allAgents[0];
  const delegateTarget = {
    kind: "direct" as const,
    agentId: selectedCreator?.id ?? "",
  };
  const existingDraft = useAppSelector((state) =>
    selectConversationWorkspaceDraft(
      state,
      companyId,
      chatConversationTargetKey(delegateTarget),
    ),
  );
  const loading = !agentsQuery.currentData && agentsQuery.isFetching;
  const unavailable = agentsQuery.isError;
  // A task for yourself needs no Agent, so the roster failing to load must
  // not hold that path hostage; the agent paths still wait for it.
  const needsAgents = mode === "agent" || executorKind === "agent";
  const canSubmit = Boolean(
    session.userId &&
    !(needsAgents && (loading || unavailable)) &&
    (mode === "agent"
      ? prompt.trim() && selectedCreator
      : title.trim() &&
        projectId &&
        !runRefused &&
        (executorKind === "user" || selectedAgent)),
  );

  function switchMode() {
    setError(undefined);
    if (mode === "manual") {
      if (!prompt.trim())
        setPrompt([title, description].filter(Boolean).join("\n\n"));
      setMode("agent");
      requestAnimationFrame(() => promptRef.current?.focus());
    } else {
      if (!title.trim()) setTitle(prompt.trim().split("\n")[0].slice(0, 240));
      if (!description.trim()) setDescription(prompt);
      setMode("manual");
      requestAnimationFrame(() => titleRef.current?.focus());
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit || !session.userId || submitting.current || pending) return;
    if (mode === "agent") {
      if (!selectedCreator) return;
      const project = projectsQuery.currentData?.items.find(
        (item) => item.id === projectId,
      );
      let schedulingNote = "";
      if (scheduled) {
        try {
          const trigger = taskScheduleTrigger(scheduleValues);
          schedulingNote = `\n\nSchedule these tasks with zen task create using this trigger: ${JSON.stringify(trigger)}. Do not start them immediately.${otherAssignee ? ` Assign to member ${otherAssignee.id}; they must accept first.` : ""}`;
        } catch {
          setError("Choose a valid future run time before continuing.");
          return;
        }
      }
      const request = `${project ? `Create the tasks in Project ${project.key} (${project.id}).\n\n` : ""}Help me turn this request into tasks. Clarify the outcome and delegate to the appropriate agents.\n\n${prompt.trim()}\n\nPriority: ${priority}. Milestone: ${milestone.replaceAll("_", " ")}.${schedulingNote}`;
      dispatch(
        setConversationWorkspaceDraft({
          companyId,
          targetKey: chatConversationTargetKey(delegateTarget),
          message: existingDraft.trim()
            ? `${existingDraft}\n\n${request}`
            : request,
        }),
      );
      onClose();
      navigate(createChatWorkspaceHref(delegateTarget));
      return;
    }
    if ((executorKind === "agent" && !selectedAgent) || runRefused) return;
    submitting.current = true;
    setError(undefined);
    const assigneeUserId = otherAssignee?.id ?? session.userId;
    // No key: the server mints {prefix}-{number} from the number it
    // allocates, so the task is known by the one label the board shows.
    const create = () =>
      onCreate(
        {
          projectId,
          ...(scheduled
            ? { schedule: taskScheduleTrigger(scheduleValues) }
            : {}),
          title: title.trim(),
          description: description.trim(),
          milestoneType: milestone,
          resultPolicy: { proposalOnly, reviewRequired },
          status: "todo",
          priority,
          // A Task runs on its assignee's computer (ADR 0014); the choice
          // is whether an Agent does the work or the assignee does it
          // themself.
          assignee: { kind: "user", id: assigneeUserId },
          executedBy:
            executorKind === "user"
              ? { kind: "user", id: assigneeUserId }
              : { kind: "agent", id: selectedAgent!.id },
        },
        another,
      );
    try {
      // Without a setup the server refuses the whole create, so creating
      // again once the setup is done is safe. A Task for someone else
      // starts nothing, so it needs no setup of yours.
      if (startsRun && selectedAgent) {
        await sendWithAgentSetup(
          create,
          { bound: Boolean(selectedAgent.binding), name: selectedAgent.name },
          () => onSetUpAgent(selectedAgent),
        );
      } else {
        await create();
      }
      if (another) {
        setTitle("");
        setDescription("");
        requestAnimationFrame(() => titleRef.current?.focus());
      }
    } catch (caught) {
      if (startsRun && isAgentBindingRequired(caught) && selectedAgent) {
        setError(
          `${selectedAgent.name} is not set up on your computer yet, so the task was not created.`,
        );
        return;
      }
      setError(
        caught &&
          typeof caught === "object" &&
          "message" in caught &&
          typeof caught.message === "string"
          ? caught.message
          : "Task could not be created. Please try again.",
      );
    } finally {
      submitting.current = false;
    }
  }

  if (calendar)
    return (
      <form className="personal-form" onSubmit={submit}>
        <fieldset className="calendar-task-fields" disabled={pending}>
          <div className="personal-tabs" aria-label="Entry type">
            <button
              type="button"
              aria-pressed="false"
              onClick={() =>
                calendar.onSelectType("event", {
                  title,
                  description,
                  startLocal: scheduleValues.runAtLocal,
                })
              }
            >
              <CalendarDays size={14} />
              Event
            </button>
            <button
              type="button"
              aria-pressed="false"
              onClick={() =>
                calendar.onSelectType("reminder", {
                  title,
                  description,
                  startLocal: scheduleValues.runAtLocal,
                })
              }
            >
              <Bell size={14} />
              Reminder
            </button>
            <button type="button" aria-pressed="true">
              <ListTodo size={14} />
              Task
            </button>
          </div>
          <input
            autoFocus
            required
            className="personal-title-input"
            aria-label="Task title"
            placeholder="Add a task"
            maxLength={200}
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
          <TaskScheduleFields
            appearance="calendar"
            values={scheduleValues}
            onChange={setScheduleValues}
          />
          <div className="calendar-task-property flex items-center gap-[10px] min-w-0 text-[var(--workspace-text-muted)]">
            <FolderOpen size={17} />
            <CalendarOptionPicker
              label="Project"
              value={projectId}
              options={[
                { value: "", label: "Select a project" },
                ...writableProjects.map((project) => ({
                  value: project.id,
                  label: project.title,
                })),
              ]}
              onChange={setProjectId}
            />
          </div>
          {projectsQuery.isError ? (
            <p
              role="alert"
              className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
            >
              Projects could not be loaded.{" "}
              <button
                type="button"
                className="calendar-text-button"
                onClick={() => void projectsQuery.refetch()}
              >
                Retry
              </button>
            </p>
          ) : !projectsQuery.isLoading && !writableProjects.length ? (
            <p className="personal-note">
              You need a project with permission to create tasks.
            </p>
          ) : null}
          <div className="calendar-task-property flex items-center gap-[10px] min-w-0 text-[var(--workspace-text-muted)]">
            <Users size={17} />
            <CalendarOptionPicker
              label="Assignee"
              value={otherAssignee?.id ?? ""}
              options={[
                { value: "", label: "Me" },
                ...otherMembers.map((member) => ({
                  value: member.id,
                  label: member.name,
                })),
              ]}
              disabled={!canAssign}
              onChange={setAssigneeId}
            />
          </div>
          <div className="calendar-task-property flex items-center gap-[10px] min-w-0 text-[var(--workspace-text-muted)]">
            <span className="calendar-field-label">Execute with</span>
            <TaskCreationAgentPicker
              agents={taskAgents}
              selected={executorKind === "agent" ? selectedAgent : undefined}
              onSelect={(id) => {
                setAgentId(id);
                setExecutorKind("agent");
              }}
              onSelectSelf={() => setExecutorKind("user")}
              selfLabel={otherAssignee?.name}
              showAvailability={!otherAssignee}
              label="Executed by"
            />
          </div>
          <p className="personal-note">
            Runs on{" "}
            {otherAssignee
              ? `${otherAssignee.name}’s computer after they accept`
              : "your computer"}{" "}
            at the scheduled time.
          </p>
          <button
            type="button"
            className="calendar-more-toggle"
            aria-expanded={more}
            onClick={() => setMore(!more)}
          >
            {more ? "Fewer details" : "More details"}
            <ChevronDown size={14} />
          </button>
          {more && (
            <div className="calendar-more-fields">
              <label>
                Notes
                <textarea
                  aria-label="Task description"
                  placeholder="Add a little context…"
                  maxLength={50000}
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                />
              </label>
              <CalendarOptionPicker
                label="Priority"
                value={priority}
                options={["low", "medium", "high", "urgent"].map((value) => ({
                  value,
                  label: value[0].toUpperCase() + value.slice(1),
                }))}
                onChange={(value) => setPriority(value as typeof priority)}
              />
              <CalendarOptionPicker
                label="Milestone"
                value={milestone}
                options={milestoneTypes.map((value) => ({
                  value,
                  label: milestoneLabel(value),
                }))}
                onChange={(value) => setMilestone(value as typeof milestone)}
              />
            </div>
          )}
          {error && (
            <p
              className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
              role="alert"
            >
              {error}
            </p>
          )}
          <footer className="personal-dialog-actions">
            <button
              type="button"
              className="personal-button"
              onClick={calendar.onClose}
            >
              Cancel
            </button>
            <button
              className="personal-button personal-button--primary"
              disabled={!canSubmit || pending}
            >
              {pending ? "Saving…" : "Save"}
            </button>
          </footer>
        </fieldset>
      </form>
    );

  return (
    <form
      className="task-create-form"
      onSubmit={submit}
      onKeyDown={(event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault();
          event.currentTarget.requestSubmit();
        }
      }}
    >
      <header className="new-task-header flex shrink-0 items-center justify-between gap-[16px] p-[12px_20px]">
        <span>
          {session.company?.workspaceName}
          <ChevronRight size={12} />
          <DialogTitle>
            {mode === "agent" ? "Create with agent" : "Create manually"}
          </DialogTitle>
        </span>
        <div>
          <button
            type="button"
            className="new-task-close"
            aria-label={expanded ? "Collapse new task" : "Expand new task"}
            onClick={onToggleExpanded}
          >
            {expanded ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
          </button>
          <DialogClose
            className="new-task-close"
            aria-label="Close new task"
            disabled={pending}
          >
            <X size={15} />
          </DialogClose>
        </div>
      </header>
      <DialogDescription className="sr-only">
        {mode === "agent"
          ? "Describe the outcome and choose an agent to help create and delegate tasks in chat."
          : "Add a task title, description, and properties."}
      </DialogDescription>
      <fieldset className="new-task-fields" disabled={pending}>
        {mode === "manual" ? (
          <div className="flex items-center flex-wrap gap-[7px]">
            {/* The same chip as the properties below, so the form reads as
                one set of controls. */}
            <label className="task-create-chip">
              <FolderOpen size={14} aria-hidden="true" />
              Project
              <select
                aria-label="Task Project"
                disabled={
                  Boolean(initialProjectId) && projectId === initialProjectId
                }
                required
                value={projectId}
                onChange={(e) => setProjectId(e.target.value)}
              >
                <option value="">Select a project</option>
                {writableProjects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.title}
                  </option>
                ))}
              </select>
            </label>
            {!projectsQuery.isLoading && writableProjects.length === 0 ? (
              <span className="new-task-hint">
                {projectsQuery.data?.items.some((p) => !p.archivedAt) ? (
                  <>
                    You can't create tasks yet
                    {managesSomeAccess ? (
                      <>
                        {" "}
                        <ManageAccessLink onNavigate={onClose} />
                      </>
                    ) : null}
                  </>
                ) : (
                  "Create a project from Projects in the sidebar first."
                )}
              </span>
            ) : null}
          </div>
        ) : null}
        {mode === "agent" ? (
          <>
            <div className="task-create-agent flex items-center gap-[10px] text-workspace-body leading-workspace-body text-workspace-muted">
              <span>Created with</span>
              <TaskCreationAgentPicker
                agents={allAgents}
                selected={selectedCreator}
                onSelect={setCreatorId}
                label="Choose creating agent"
              />
            </div>
            <textarea
              className="task-create-prompt"
              ref={promptRef}
              autoFocus
              aria-label="Task request"
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              placeholder={
                "Tell the agent what you want done…\nFor example: ask Eli to build the signup flow and Susan to write the welcome email."
              }
            />
          </>
        ) : (
          <>
            <input
              className="task-create-title"
              ref={titleRef}
              autoFocus
              aria-label="Task title"
              autoComplete="off"
              maxLength={240}
              required
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Task title"
            />
            <textarea
              className="task-create-description"
              aria-label="Task description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Add a description, context, or acceptance criteria…"
            />
          </>
        )}
        {mode === "manual" ? (
          <div className="task-schedule-create grid gap-[12px] [justify-items:start]">
            <button
              type="button"
              className="task-create-chip"
              aria-pressed={scheduled}
              onClick={() => setScheduled(!scheduled)}
            >
              <CalendarDays size={14} />{" "}
              {scheduled ? "Scheduled run" : "Schedule task"}
            </button>
            {scheduled ? (
              <TaskScheduleFields
                values={scheduleValues}
                onChange={setScheduleValues}
              />
            ) : null}
          </div>
        ) : null}
        {loading ? (
          <p role="status" className="new-task-hint">
            Loading agents…
          </p>
        ) : unavailable ? (
          <p role="alert" className="new-task-error">
            Agents could not be loaded.{" "}
            <button
              type="button"
              onClick={() => {
                void agentsQuery.refetch();
              }}
            >
              Retry
            </button>
          </p>
        ) : (mode === "agent" && !allAgents.length) ||
          (mode === "manual" &&
            executorKind === "agent" &&
            !taskAgents.length) ? (
          <p className="new-task-hint">Add an agent before creating a task.</p>
        ) : (
          <>
            <p className="new-task-hint task-create-behavior">
              {mode === "agent"
                ? existingDraft.trim()
                  ? `You have an unsent draft with ${selectedCreator?.name}. This request will be added below it.`
                  : "Continue in chat to review and send this request. Tasks are created through the agent conversation."
                : scheduled
                  ? otherAssignee
                    ? `Saved for ${otherAssignee.name} to accept. The task starts at its scheduled time on their computer.`
                    : "The task starts at its scheduled time on your computer."
                  : otherAssignee
                    ? executorKind === "user"
                      ? `${otherAssignee.name} does this task themself. No agent run starts.`
                      : `${otherAssignee.name} will be asked to start it on their computer.`
                    : executorKind === "user"
                      ? "Yours to do. No agent run starts when you create this task."
                      : runRefused
                        ? cannotRunTasksHereLabel()
                        : selectedAgent
                          ? newAgentTaskHint(selectedAgent)
                          : null}
              {mode === "manual" && runRefused && managesChosenAccess ? (
                <>
                  {" "}
                  <ManageAccessLink
                    projectId={projectId}
                    onNavigate={onClose}
                  />
                </>
              ) : null}
            </p>
            <div className="flex items-center flex-wrap gap-[7px]">
              {mode === "manual" && executorKind === "agent" ? (
                <span className="task-create-chip">
                  <Circle size={14} /> To do
                </span>
              ) : null}
              <label className="task-create-chip">
                <SignalMedium size={14} />
                <select
                  aria-label="Priority"
                  value={priority}
                  onChange={(event) =>
                    setPriority(
                      event.target.value as NonNullable<
                        TaskCreateRequest["priority"]
                      >,
                    )
                  }
                >
                  {["low", "medium", "high", "urgent"].map((value) => (
                    <option key={value} value={value}>
                      {value[0].toUpperCase() + value.slice(1)}
                    </option>
                  ))}
                </select>
              </label>
              {/* Whose Task it is: yours, or, for someone who may assign
                  Tasks, another member's. */}
              {mode === "manual" ? (
                <label
                  className="task-create-chip"
                  title={
                    canAssign
                      ? undefined
                      : "Only a company owner or admin can give a task to someone else."
                  }
                >
                  <Users size={14} aria-hidden="true" />
                  Assignee
                  <select
                    aria-label="Assignee"
                    disabled={!canAssign}
                    value={otherAssignee?.id ?? ""}
                    onChange={(event) => setAssigneeId(event.target.value)}
                  >
                    <option value="">Me</option>
                    {otherMembers.map((member) => (
                      <option key={member.id} value={member.id}>
                        {member.name}
                      </option>
                    ))}
                  </select>
                </label>
              ) : null}
              {/* One control says who does the work: the assignee, or an
                  Agent. It is the only place that choice is made, so the
                  chip, the line above and the button below always agree. */}
              {mode === "manual" ? (
                <TaskCreationAgentPicker
                  agents={taskAgents}
                  selected={
                    executorKind === "agent" ? selectedAgent : undefined
                  }
                  onSelect={(id) => {
                    setAgentId(id);
                    setExecutorKind("agent");
                  }}
                  onSelectSelf={() => setExecutorKind("user")}
                  selfLabel={otherAssignee?.name}
                  selfHint={
                    otherAssignee
                      ? "Does it themself. No agent runs."
                      : undefined
                  }
                  showAvailability={!otherAssignee && !projectRefusesRun}
                  label="Executed by"
                />
              ) : null}
              <button
                className="task-create-chip"
                type="button"
                aria-label="More task properties"
                aria-expanded={more}
                onClick={() => setMore((value) => !value)}
              >
                <MoreHorizontal size={16} />
              </button>
            </div>
            {more ? (
              <div className="task-create-more">
                <label>
                  <input
                    type="checkbox"
                    checked={proposalOnly}
                    onChange={(event) => setProposalOnly(event.target.checked)}
                  />
                  <span>Proposal only — applying changes is not required</span>
                </label>
                <label>
                  <input
                    type="checkbox"
                    checked={reviewRequired}
                    onChange={(event) =>
                      setReviewRequired(event.target.checked)
                    }
                  />
                  <span>Require Task result approval</span>
                </label>
                <label>
                  <span>Milestone</span>
                  <select
                    aria-label="Milestone"
                    value={milestone}
                    onChange={(event) =>
                      setMilestone(
                        event.target
                          .value as TaskCreateRequest["milestoneType"],
                      )
                    }
                  >
                    {milestoneTypes.map((value) => (
                      <option value={value} key={value}>
                        {milestoneLabel(value)}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            ) : null}
          </>
        )}
        {error ? (
          <p className="new-task-error" role="alert">
            {error}
          </p>
        ) : null}
      </fieldset>
      <footer className="new-task-footer">
        <button
          className="task-create-switch"
          type="button"
          disabled={pending}
          onClick={switchMode}
        >
          <ArrowLeftRight size={15} />
          {mode === "agent" ? "Switch to manual" : "Switch to agent"}
        </button>
        {mode === "manual" ? (
          <label className="task-create-another flex items-center gap-[6px] whitespace-nowrap text-workspace-caption leading-workspace-caption text-workspace-muted cursor-pointer">
            <input
              type="checkbox"
              checked={another}
              onChange={(event) => setAnother(event.target.checked)}
              disabled={pending}
            />{" "}
            Create another
          </label>
        ) : null}
        <button
          className="dashboard-button dashboard-button-primary"
          type="submit"
          disabled={!canSubmit || pending}
        >
          {pending
            ? "Creating…"
            : mode === "agent"
              ? existingDraft.trim()
                ? "Append & open chat"
                : "Continue in chat"
              : startsRun
                ? "Create & start task"
                : "Create task"}
          {mode === "agent" ? <ArrowUpRight size={14} /> : null}
        </button>
      </footer>
    </form>
  );
}

// What creating the task does with the chosen Agent, said to the member who
// runs it: it starts right away, it is set up first, or it waits for the
// computer it is set up on.
function newAgentTaskHint(agent: AgentDto): string {
  if (agent.runtimeAvailable) {
    return `${agent.name} will start on your computer right after creation.`;
  }
  if (!agent.binding) {
    return `You'll set ${agent.name} up on your computer first, then it starts.`;
  }
  return agentAvailabilityHelp(agent);
}
