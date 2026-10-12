import { useAppDispatch, useAppSelector } from "@/state/store";
import { AgentVoiceSettings } from "./agent-voice-settings";
import {
  setInstructionDraft,
  acknowledgeInstructionSave,
  clearInstructionDraft,
} from "@/state/slice/agent-drafts";
import { AgentCapabilities } from "./agent-capabilities";
import {
  ceoAgentApi,
  getCeoAgentApiErrorMessage,
} from "@/features/agent-setup/api/ceo-agent-api";
import {
  BookOpen,
  ChevronRight,
  ListTodo,
  Check,
  FileText,
  Info,
  Undo2,
} from "@/shared/ui/icons";
import { useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { AgentSetupSummary } from "./agent-setup-summary";
import { useCeoAgentRuntimeController } from "@/features/agent-setup/hooks/use-ceo-agent-runtime-controller";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  useListWorkspaceTasksQuery,
  useUpdateWorkspaceAgentMutation,
} from "@/shared/api/workspace-api";
import { AgentContextSidebar, AgentOverview } from "./agent-overview";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";
import type { AgentDto } from "@/shared/api/types";
import { publicAssetPath } from "@/shared/lib/public-assets";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";
import { AgentWorkHistory } from "./agent-work-history";
import { useTaskLabel } from "@/shared/lib/task-label";
import {
  TaskPriorityIcon,
  TaskStatusIcon,
} from "@/features/company-plan/components/task-metadata-icons";
import { TaskDescription } from "@/features/company-plan/components/task-description";

export function AgentProfileContent({
  agent,
  tab,
}: {
  agent: AgentDto;
  tab: string;
}) {
  if (tab === "work") return <AgentWork agent={agent} />;
  if (
    tab === "overview" ||
    !["instructions", "settings", "capabilities", "voice"].includes(tab)
  ) {
    return <AgentOverview agent={agent} />;
  }
  // Configuration is the creator's and the agents.manage holders' (ADR 0013);
  // the server says so per viewer, and everyone else reads the same tabs with
  // their controls disabled rather than a different page. The root agent is
  // the company owners' and admins' alone, whoever created it.
  const readOnly = !agent.access.configure;
  const body =
    tab === "instructions" ? (
      <AgentInstructions agent={agent} />
    ) : tab === "voice" ? (
      <AgentVoiceSettings agent={agent} />
    ) : tab === "settings" ? (
      // Settings disables only the template's controls: where the viewer
      // runs the Agent is their own setup, open to every member (ADR 0014).
      <AgentExecutionSettings agent={agent} readOnly={readOnly} />
    ) : (
      // Capabilities disables only its switches: refreshing what the
      // viewer's own computer provides is open to every member (ADR 0014).
      <AgentCapabilities agent={agent} />
    );
  if (!readOnly) return body;
  const root = agent.reportsTo === null;
  return (
    <>
      <p className="agent-readonly-note" role="note">
        {root
          ? "Only a company owner or admin can change the CEO agent."
          : `Only ${agent.createdBy?.name ?? "the agent's creator"}, or a company owner or admin, can change this agent.`}{" "}
        You can still set it up on your computer, chat with it and give it work.
      </p>
      {tab === "instructions" || tab === "voice" ? (
        <fieldset className="agent-readonly-scope" disabled>
          {body}
        </fieldset>
      ) : (
        body
      )}
    </>
  );
}

function AgentInstructions({ agent }: { agent: AgentDto }) {
  const dispatch = useAppDispatch();
  const draftKey = `${agent.companyId}:${agent.id}`;
  const savedDraft = useAppSelector((state) => state.agentDrafts[draftKey]);
  const draft = savedDraft?.value ?? agent.instructions;
  const baseline = {
    value: savedDraft?.baseline ?? agent.instructions,
    revision: savedDraft?.revision ?? agent.revision,
  };
  const setDraft = (value: string) => {
    if (value === agent.instructions) dispatch(clearInstructionDraft(draftKey));
    else
      dispatch(
        setInstructionDraft({
          key: draftKey,
          draft: {
            value,
            baseline: baseline.value,
            revision: baseline.revision,
          },
        }),
      );
  };
  const [preview, setPreview] = useState(false);
  const [save, mutation] = useUpdateWorkspaceAgentMutation();
  const saving = useRef(false);
  async function submit() {
    if (saving.current || draft === baseline.value || !draft.trim()) return;
    saving.current = true;
    try {
      const updated = await save({
        companyId: agent.companyId,
        agentId: agent.id,
        request: { expectedRevision: baseline.revision, instructions: draft },
      }).unwrap();
      dispatch(
        ceoAgentApi.util.updateQueryData(
          "listCompanyAgents",
          agent.companyId,
          (page) => {
            const index = page.items.findIndex(
              (item) => item.id === updated.id,
            );
            if (index >= 0) page.items[index] = updated;
          },
        ),
      );
      dispatch(
        acknowledgeInstructionSave({
          key: draftKey,
          submitted: draft,
          instructions: updated.instructions,
          revision: updated.revision,
        }),
      );
      toast.success("Instructions saved for future runs.");
    } catch {
      toast.error(
        "Instructions were not saved. Review the error and try again.",
      );
    } finally {
      saving.current = false;
    }
  }
  return (
    <div className="agent-overview-layout agent-instructions-layout">
      <section className="agent-overview-main">
        <div className="agent-section-heading">
          <div>
            <h2>
              <FileText size={15} /> Instructions
            </h2>
            <p>
              Define {agent.name}’s role, working approach, and the quality you
              expect.
            </p>
          </div>
        </div>
        <div className="agent-instructions-editor">
          <div
            className="agent-instructions-toolbar"
            role="group"
            aria-label="Instruction editor mode"
          >
            <button
              type="button"
              aria-pressed={!preview}
              onClick={() => setPreview(false)}
            >
              Write
            </button>
            <button
              type="button"
              aria-pressed={preview}
              onClick={() => setPreview(true)}
            >
              Preview
            </button>
            <span className="agent-instructions-format">Markdown</span>
            <span className="agent-instructions-word-count">
              {draft.trim() ? draft.trim().split(/\s+/).length : 0} words
            </span>
          </div>
          {preview ? (
            <div className="agent-instructions-preview">
              <TaskDescription description={draft} />
            </div>
          ) : (
            <label className="agent-form-field">
              <span className="sr-only">Agent instructions</span>
              <textarea
                value={draft}
                disabled={mutation.isLoading}
                onChange={(event) => setDraft(event.target.value)}
                rows={15}
              />
            </label>
          )}
        </div>
        {mutation.error ? (
          <p role="alert">
            {"message" in mutation.error
              ? mutation.error.message
              : "Unable to save."}{" "}
            <button
              onClick={() => {
                dispatch(clearInstructionDraft(draftKey));
              }}
            >
              Load current instructions
            </button>
          </p>
        ) : null}
        <footer className="agent-form-footer">
          <span>
            {draft === baseline.value
              ? "No unsaved changes"
              : "Draft saved on this device · not applied"}
          </span>
          <div className="agent-settings-save-actions">
            <button
              className="dashboard-button"
              type="button"
              disabled={mutation.isLoading || draft === baseline.value}
              onClick={() => dispatch(clearInstructionDraft(draftKey))}
            >
              <Undo2 size={14} />
              Discard
            </button>
            <button
              className="dashboard-button dashboard-button-primary"
              disabled={
                mutation.isLoading || draft === baseline.value || !draft.trim()
              }
              onClick={() => void submit()}
            >
              {mutation.isLoading ? "Saving…" : "Save instructions"}
              <Check size={14} />
            </button>
          </div>
        </footer>
      </section>
      <aside
        className="agent-overview-sidebar"
        aria-label="Instruction guidance"
      >
        <section>
          <BookOpen size={20} className="agent-guidance-icon" />
          <h2>Give {agent.name} a clear brief</h2>
          <p>
            Use these instructions for expectations that apply across tasks.
          </p>
          <ul>
            <li>The role and outcomes {agent.name} owns</li>
            <li>How to make and explain decisions</li>
            <li>What a useful deliverable includes</li>
            <li>When to ask you for input</li>
          </ul>
        </section>
        <section>
          <h2>When changes apply</h2>
          <p>
            Saved instructions are used for future runs. Work already running
            keeps the instructions it started with.
          </p>
        </section>
      </aside>
    </div>
  );
}

// The Agent's own settings, plus the one fact that belongs to the viewer:
// which of their computers it runs on (ADR 0014). The template's provider
// never changes here — duplicating the Agent is how it moves to the other
// provider. Models are listed by one of the viewer's online computers when
// there is one; otherwise the model is typed, and saving never needs a
// computer. `readOnly` disables the template's controls and nothing else,
// so a member who cannot change the Agent can still set it up on their
// computer.
function AgentExecutionSettings({
  agent,
  readOnly,
}: {
  agent: AgentDto;
  readOnly: boolean;
}) {
  const controller = useCeoAgentRuntimeController({
    companyId: agent.companyId,
    agentId: agent.id,
  });
  const changed =
    controller.model !== (agent.model ?? "") ||
    controller.thinkingLevel !== (agent.thinkingLevel ?? "");
  return (
    <section className="agent-profile-content agent-execution-layout">
      <div className="agent-settings-form">
        <div className="agent-settings-section">
          <AgentSetupSummary agent={agent} variant="compact" />
        </div>
        <div className="agent-settings-section">
          <h2>Agent defaults</h2>
          <div className="agent-settings-provider">
            <span className="agent-settings-label">
              Provider
              <AgentSettingsHelp label="About provider">
                This agent uses {providerLabel(agent.provider)}. Duplicate it to
                use another provider.
              </AgentSettingsHelp>
            </span>
            <div className="agent-settings-context-runtime">
              <img
                alt=""
                src={publicAssetPath(`images/settings/${agent.provider}.svg`)}
              />
              <strong>{providerLabel(agent.provider)}</strong>
            </div>
          </div>
          <div className="agent-settings-model-grid">
            <div className="agent-form-field">
              <label htmlFor="agent-model">Model</label>
              {controller.modelIsTyped ? (
                <input
                  autoComplete="off"
                  className="agent-settings-model-input"
                  disabled={readOnly || controller.saveIsLoading}
                  id="agent-model"
                  aria-describedby="agent-model-help"
                  placeholder="Provider default model"
                  spellCheck={false}
                  value={controller.model}
                  onChange={(event) =>
                    controller.selectModel(event.target.value)
                  }
                />
              ) : (
                <AgentRuntimeSelect
                  id="agent-model"
                  aria-describedby="agent-model-help"
                  value={controller.model}
                  disabled={
                    readOnly ||
                    !controller.selectedRuntime ||
                    controller.saveIsLoading
                  }
                  onChange={(event) =>
                    controller.selectModel(event.target.value)
                  }
                >
                  <option value="">Provider default model</option>
                  {controller.model && !controller.modelIsReported ? (
                    <option value={controller.model}>
                      {controller.model} (saved)
                    </option>
                  ) : null}
                  {controller.modelOptions.map((model) => (
                    <option key={model.id} value={model.id}>
                      {model.label}
                    </option>
                  ))}
                </AgentRuntimeSelect>
              )}
            </div>
            <div className="agent-form-field">
              <label htmlFor="agent-thinking">Thinking level</label>
              <AgentRuntimeSelect
                id="agent-thinking"
                aria-describedby="agent-model-help"
                value={controller.thinkingLevel}
                disabled={
                  readOnly ||
                  !controller.thinkingOptions.length ||
                  controller.saveIsLoading
                }
                onChange={(event) =>
                  controller.setThinkingLevel(event.target.value)
                }
              >
                <option value="">Provider default thinking level</option>
                {controller.thinkingLevel &&
                !controller.thinkingOptions.some(
                  (level) => level.value === controller.thinkingLevel,
                ) ? (
                  <option value={controller.thinkingLevel}>
                    {controller.thinkingLevel} (saved)
                  </option>
                ) : null}
                {controller.thinkingOptions.map((level) => (
                  <option key={level.value} value={level.value}>
                    {level.label}
                  </option>
                ))}
              </AgentRuntimeSelect>
            </div>
          </div>
          <p className="agent-settings-model-note" id="agent-model-help">
            Defaults for new chats.
            {controller.modelIsTyped
              ? " Enter a model name, or leave blank for the provider default."
              : null}
          </p>
          <div className="agent-settings-limit">
            <span className="agent-settings-label">
              Task concurrency
              <AgentSettingsHelp label="About task concurrency">
                The limit is per person. Direct chats and schedules don’t count.
              </AgentSettingsHelp>
            </span>
            <div className="agent-concurrency-summary">
              <strong>{agent.maxConcurrentRuns} at once</strong>
              <span>· {agent.activeRunCount} of yours active</span>
            </div>
          </div>
        </div>
        {controller.loadError ||
        controller.saveError ||
        controller.modelDiscoveryError ? (
          <p role="alert">
            {getCeoAgentApiErrorMessage(
              controller.loadError ||
                controller.saveError ||
                controller.modelDiscoveryError,
              "Agent settings could not be loaded.",
            )}
          </p>
        ) : null}
        <div className="agent-form-footer">
          <span>{changed ? "Unsaved changes" : "No changes"}</span>
          <div className="agent-settings-save-actions">
            {changed ? (
              <button
                className="dashboard-button"
                disabled={readOnly || controller.saveIsLoading}
                onClick={() => {
                  controller.selectModel(agent.model ?? "");
                  controller.setThinkingLevel(agent.thinkingLevel ?? "");
                }}
              >
                Discard
              </button>
            ) : null}
            <button
              className="dashboard-button dashboard-button-primary"
              disabled={readOnly || !controller.canSave || !changed}
              onClick={() => void controller.save().catch(() => undefined)}
            >
              {controller.saveIsLoading ? "Saving…" : "Save changes"}
            </button>
          </div>
        </div>
        {controller.savedMessage ? (
          <p role="status">{controller.savedMessage}</p>
        ) : null}
      </div>
    </section>
  );
}

function AgentSettingsHelp({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Tooltip
      open={open}
      onOpenChange={setOpen}
      label={children}
      contentClassName="agent-settings-help-tooltip"
    >
      <button
        className="agent-settings-help"
        type="button"
        aria-label={label}
        onClick={() => setOpen(true)}
      >
        <Info aria-hidden size={15} />
      </button>
    </Tooltip>
  );
}

function AgentWork({ agent }: { agent: AgentDto }) {
  const query = useListWorkspaceTasksQuery({ companyId: agent.companyId });
  const labelTask = useTaskLabel(agent.companyId);
  const tasks =
    query.currentData?.items.filter(
      (task) =>
        task.executedBy.kind === "agent" && task.executedBy.id === agent.id,
    ) ?? [];
  return (
    <div className="agent-overview-layout">
      <section className="agent-overview-main">
        <div className="agent-section-heading agent-work-heading">
          <h2>
            <ListTodo size={15} />
            Assigned tasks{" "}
            <span className="agent-work-badge">{tasks.length}</span>
          </h2>
        </div>
        {query.isLoading ? (
          <p role="status" className="agent-inline-empty">
            Loading tasks…
          </p>
        ) : query.isError ? (
          <p role="alert" className="agent-inline-empty">
            Tasks could not be loaded.{" "}
            <button type="button" onClick={() => void query.refetch()}>
              Retry
            </button>
          </p>
        ) : tasks.length ? (
          <div className="agent-responsible-work">
            {tasks.map((task) => (
              <Link key={task.id} to={`/tasks?taskId=${task.id}&taskView=full`}>
                <TaskStatusIcon status={task.status} />
                <span className="agent-work-task-id">{labelTask(task)}</span>
                <span className="agent-history-copy">
                  <strong>{task.title}</strong>
                  <small>
                    {taskWorkStatusLabel(task)}
                    {task.assignee.kind !== "agent" ||
                    task.assignee.id !== agent.id
                      ? ` · Assignee ${task.assignee.name}`
                      : null}
                  </small>
                </span>
                <TaskPriorityIcon priority={task.priority} />
                <ChevronRight size={13} />
              </Link>
            ))}
          </div>
        ) : (
          <p className="agent-inline-empty">No tasks assigned yet.</p>
        )}
        <AgentWorkHistory
          agent={agent}
          tasks={tasks}
          title="Run history"
          compact
        />
      </section>
      <AgentContextSidebar agent={agent} />
    </div>
  );
}
