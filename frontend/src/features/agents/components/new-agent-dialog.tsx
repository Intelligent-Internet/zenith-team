import { useCreateWorkspaceAgentMutation } from "@/shared/api/agent-api";
import {
  Bot,
  ChevronDown,
  Clock3,
  FileText,
  Monitor,
  X,
} from "@/shared/ui/icons";
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Link, useNavigate } from "react-router";
import {
  getCeoAgentApiErrorMessage,
  useListAgentRuntimesQuery,
  useListCompanyAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import {
  useRuntimeModelDiscovery,
  type ModelDiscoveryStatus,
} from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import {
  findCeoAgent,
  findRuntime,
  providerLabel,
  providerThinkingLevels,
  runtimeDisplayName,
  runtimeReadyToRun,
  runtimeSignInLabel,
  runtimeStateLabel,
} from "@/features/agent-setup/model/ceo-agent-setup";
import {
  AGENT_CAP_MAX,
  AGENT_CAP_MIN,
  AGENT_INSTRUCTIONS_MAX_LENGTH,
  AGENT_NAME_MAX_LENGTH,
  AGENT_TITLE_MAX_LENGTH,
  agentDraftProblems,
  agentLookChoices,
  applyDraftLookChange,
  applyDraftProviderChange,
  applyDraftRuntimeChange,
  buildAgentCreateRequest,
  createAgentDraft,
  type AgentDraft,
} from "@/features/agents/model/agent-draft";

import type { AgentDto, AgentProvider, RuntimeDto } from "@/shared/api/types";
import { publicAssetPath } from "@/shared/lib/public-assets";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import "@/features/agents/styles/agents.css";
import "@/features/agents/styles/new-agent-dialog.css";

const CAP_CHOICES = Array.from(
  { length: AGENT_CAP_MAX - AGENT_CAP_MIN + 1 },
  (_, index) => AGENT_CAP_MIN + index,
);

const PROVIDER_CHOICES: ReadonlyArray<{
  label: string;
  value: AgentProvider;
}> = [
  { label: "Codex", value: "codex" },
  { label: "Claude Code", value: "claude" },
];

function providerChoiceLabel(provider: AgentProvider): string {
  return (
    PROVIDER_CHOICES.find((choice) => choice.value === provider)?.label ??
    provider
  );
}

/**
 * Creates an Agent from the Agents page, or duplicates one from its profile.
 * The member names it, gives it a role, picks its manager and provider, may
 * set it up on one of their online computers for that provider, and may set
 * the model, thinking level, appearance, standing instructions and task
 * capacity — everything the API takes at creation. A duplicate starts from
 * the template's portable fields, and its provider can still change.
 */
export function NewAgentDialog({
  companyId,
  onClose,
  template,
  preset,
  onCreated,
}: {
  companyId: string;
  onClose: () => void;
  template?: AgentDto | null;
  preset?: "secretary";
  onCreated?: (agent: AgentDto) => void;
}) {
  const agentsQuery = useListCompanyAgentsQuery(companyId);
  const runtimesQuery = useListAgentRuntimesQuery(companyId);
  const [createAgent, mutation] = useCreateWorkspaceAgentMutation();
  const navigate = useNavigate();
  const id = useId();
  const agents = useMemo(
    () =>
      (agentsQuery.currentData?.items ?? []).filter(
        (agent) => !agent.archivedAt,
      ),
    [agentsQuery.currentData],
  );
  const runtimes = useMemo(
    () => runtimesQuery.currentData?.items ?? [],
    [runtimesQuery.currentData],
  );
  const root = useMemo(() => findCeoAgent(agents), [agents]);
  const [draft, setDraft] = useState<AgentDraft | null>(null);
  const [error, setError] = useState<string>();
  const [instructionsOpen, setInstructionsOpen] = useState(Boolean(template));
  const submitting = useRef(false);
  const nameRef = useRef<HTMLInputElement>(null);

  // Seed once both rosters are known; a later roster refresh must never
  // overwrite what the founder has typed.
  useEffect(() => {
    if (draft || !agentsQuery.currentData || !runtimesQuery.currentData) {
      return;
    }
    const initial = createAgentDraft({ root, runtimes, template });
    setDraft(
      preset === "secretary" && !template
        ? {
            ...applyDraftLookChange(initial, "fold-lagoon"),
            name: "Helia",
            title: "Meeting secretary",
            instructions:
              "Help the team run thoughtful, focused meetings. Clarify decisions, identify open questions, and summarize action items with owners when stated. Be warm, concise, and discreet. Ask rather than invent missing details. Distinguish suggestions from agreed decisions. Use the team's language. Never claim an action was completed without evidence from an available tool.",
          }
        : initial,
    );
  }, [
    agentsQuery.currentData,
    draft,
    root,
    runtimes,
    runtimesQuery.currentData,
    template,
    preset,
  ]);

  // Only the caller's own computers of the Agent's provider can run it
  // (ADR 0014); the composite keys behind a binding make anything else
  // unrepresentable, so the picker never offers one.
  const providerRuntimes = useMemo(
    () =>
      draft
        ? runtimes.filter((runtime) => runtime.provider === draft.provider)
        : [],
    [draft, runtimes],
  );
  const selectedRuntime = draft ? findRuntime(runtimes, draft.runtimeId) : null;
  const discovery = useRuntimeModelDiscovery({
    companyId,
    runtime: selectedRuntime,
  });
  const modelOptions = discovery.models;
  // Discovery is optional: when no computer listed any models, the model is
  // typed and thinking offers the provider's levels.
  const modelIsTyped =
    !modelOptions.length &&
    (selectedRuntime?.status !== "online" ||
      !["idle", "pending", "running"].includes(discovery.status));
  const selectedModel = draft
    ? (modelOptions.find((option) => option.id === draft.model) ??
      (!draft.model
        ? modelOptions.find((option) => option.default)
        : undefined))
    : undefined;
  const thinkingOptions =
    draft && modelIsTyped
      ? providerThinkingLevels(draft.provider)
      : (selectedModel?.reasoning?.levels ?? []);
  const problems = draft ? agentDraftProblems(draft, selectedRuntime) : [];
  const loadError = agentsQuery.isError || runtimesQuery.isError;

  function update(patch: Partial<AgentDraft>) {
    setDraft((current) => (current ? { ...current, ...patch } : current));
    setError(undefined);
  }

  function selectModel(model: string) {
    // A typed model keeps the thinking level: the provider's levels do not
    // depend on the model.
    if (modelIsTyped) {
      update({ model });
      return;
    }
    const option =
      modelOptions.find((candidate) => candidate.id === model) ??
      (!model
        ? modelOptions.find((candidate) => candidate.default)
        : undefined);
    update({ model, thinkingLevel: option?.reasoning?.defaultLevel ?? "" });
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || problems.length || submitting.current || mutation.isLoading) {
      return;
    }
    submitting.current = true;
    setError(undefined);
    try {
      const created = await createAgent({
        companyId,
        request: buildAgentCreateRequest(draft),
      }).unwrap();
      toast.success(`${created.name} joined the team`);
      onClose();
      if (onCreated) {
        onCreated(created);
      } else {
        navigate(
          `/agents/${created.id}${preset === "secretary" ? "?tab=voice" : ""}`,
        );
      }
    } catch (caught) {
      setError(
        getCeoAgentApiErrorMessage(
          caught,
          "The agent could not be created. Review the details and try again.",
        ),
      );
    } finally {
      submitting.current = false;
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isLoading) onClose();
      }}
    >
      <DialogContent
        className="new-agent-dialog"
        overlayClassName="new-agent-overlay"
        showCloseButton={false}
        onInteractOutside={(event) => event.preventDefault()}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          nameRef.current?.focus();
        }}
      >
        <form
          className="new-agent-form"
          onSubmit={submit}
          onKeyDown={(event) => {
            if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
              event.preventDefault();
              event.currentTarget.requestSubmit();
            }
          }}
        >
          <header className="flex shrink-0 items-start gap-[14px] p-[22px_24px_18px] [border-bottom:1px_solid_var(--workspace-divider)]">
            <span
              className="grid shrink-0 place-items-center w-[44px] h-[44px] [border:1px_solid_var(--workspace-divider)] rounded-workspace-panel [background:var(--workspace-control-bg)] text-workspace-accent"
              aria-hidden="true"
            >
              <Bot size={22} />
            </span>
            <div className="flex-1 min-w-0">
              <DialogTitle className="new-agent-title">
                {template ? `Duplicate ${template.name}` : "New agent"}
              </DialogTitle>
              <DialogDescription className="m-[5px_0_0] text-workspace-muted text-workspace-caption leading-workspace-caption">
                {template
                  ? `Instructions, appearance and run settings are copied from ${template.name}. The copy starts with no work of its own.`
                  : "Give it a name, a role, a manager and a provider. You can set it up on your computer now or later."}
              </DialogDescription>
            </div>
            <DialogClose
              aria-label="Close"
              className="new-agent-close"
              disabled={mutation.isLoading}
            >
              <X size={18} />
            </DialogClose>
          </header>
          <div className="flex-1 min-w-0 min-h-0 overflow-y-auto overscroll-contain">
            <fieldset
              className="grid gap-[22px] min-w-0 m-0 p-[20px_24px_24px] [border:0]"
              disabled={mutation.isLoading}
            >
              {!draft ? (
                loadError ? (
                  <p className="new-agent-error" role="alert">
                    Agents and runtimes could not be loaded.{" "}
                    <button
                      type="button"
                      onClick={() => {
                        void agentsQuery.refetch();
                        void runtimesQuery.refetch();
                      }}
                    >
                      Retry
                    </button>
                  </p>
                ) : (
                  <p className="new-agent-hint" role="status">
                    Loading agents and runtimes…
                  </p>
                )
              ) : (
                <>
                  <section className="new-agent-identity">
                    <DraftAvatar draft={draft} />
                    <div className="grid flex-1 gap-[12px] min-w-0">
                      <label className="new-agent-field">
                        <span>Name</span>
                        <input
                          ref={nameRef}
                          autoComplete="off"
                          maxLength={AGENT_NAME_MAX_LENGTH}
                          placeholder="e.g. Nova"
                          required
                          value={draft.name}
                          onChange={(event) =>
                            update({ name: event.target.value })
                          }
                        />
                      </label>
                      <label className="new-agent-field">
                        <span>Role</span>
                        <input
                          autoComplete="off"
                          maxLength={AGENT_TITLE_MAX_LENGTH}
                          placeholder="e.g. Research Agent"
                          required
                          value={draft.title}
                          onChange={(event) =>
                            update({ title: event.target.value })
                          }
                        />
                      </label>
                    </div>
                  </section>

                  <section
                    className="new-agent-section"
                    aria-labelledby={`${id}-look`}
                  >
                    <h3 id={`${id}-look`}>Look</h3>
                    <div
                      className="flex flex-wrap gap-[8px]"
                      role="radiogroup"
                      aria-label="Look"
                    >
                      <button
                        aria-checked={!draft.avatarKey}
                        aria-label="Chosen automatically"
                        className="new-agent-look-choice"
                        role="radio"
                        title="Chosen automatically from the agent's id"
                        type="button"
                        onClick={() =>
                          setDraft((current) =>
                            current
                              ? applyDraftLookChange(current, "")
                              : current,
                          )
                        }
                      >
                        <span className="text-workspace-caption font-[600]">
                          Auto
                        </span>
                      </button>
                      {agentLookChoices.map((look) => (
                        <button
                          aria-checked={draft.avatarKey === look.key}
                          aria-label={look.name}
                          className="new-agent-look-choice"
                          key={look.key}
                          role="radio"
                          title={look.name}
                          type="button"
                          onClick={() =>
                            setDraft((current) =>
                              current
                                ? applyDraftLookChange(current, look.key)
                                : current,
                            )
                          }
                        >
                          <FoldAvatar
                            agentId={`look:${look.key}`}
                            paletteIndex={look.paletteIndex}
                            paused
                            size={40}
                          />
                        </button>
                      ))}
                    </div>
                  </section>

                  <section className="new-agent-section">
                    <label
                      className="new-agent-section-label"
                      htmlFor={`${id}-manager`}
                    >
                      Reports to
                    </label>
                    <AgentRuntimeSelect
                      id={`${id}-manager`}
                      value={draft.reportsToAgentId}
                      onChange={(event) =>
                        update({ reportsToAgentId: event.target.value })
                      }
                    >
                      {draft.reportsToAgentId ? null : (
                        <option value="">Choose a manager</option>
                      )}
                      {agents.map((agent) => (
                        <option key={agent.id} value={agent.id}>
                          {agent.name} · {agent.title}
                        </option>
                      ))}
                    </AgentRuntimeSelect>
                  </section>

                  <section
                    className="new-agent-section"
                    aria-labelledby={`${id}-provider`}
                  >
                    <h3 id={`${id}-provider`}>Provider</h3>
                    <div
                      className="agent-runtime-options new-agent-providers"
                      role="radiogroup"
                      aria-labelledby={`${id}-provider`}
                    >
                      {PROVIDER_CHOICES.map((choice) => (
                        <ProviderChoice
                          checked={draft.provider === choice.value}
                          key={choice.value}
                          label={choice.label}
                          name={`${id}-provider-choice`}
                          provider={choice.value}
                          runtimes={runtimes}
                          onSelect={() =>
                            setDraft((current) =>
                              current
                                ? applyDraftProviderChange(
                                    current,
                                    choice.value,
                                    runtimes,
                                  )
                                : current,
                            )
                          }
                        />
                      ))}
                    </div>
                  </section>

                  <section
                    className="new-agent-section"
                    aria-labelledby={`${id}-runtime`}
                  >
                    <div className="new-agent-section-heading flex items-center justify-between gap-[12px]">
                      <h3 id={`${id}-runtime`}>Set it up on your computer</h3>
                      <Link to="/settings/agents">
                        <Monitor size={14} />
                        Computers
                      </Link>
                    </div>
                    {/* Optional: without a computer the Agent is a template
                        anyone can set up later (ADR 0014). */}
                    {providerRuntimes.length ? (
                      <div
                        className="agent-runtime-options new-agent-runtimes"
                        role="radiogroup"
                        aria-label="Runtime"
                      >
                        {providerRuntimes.map((runtime) => (
                          <RuntimeChoice
                            checked={draft.runtimeId === runtime.id}
                            key={runtime.id}
                            name={`${id}-runtime-choice`}
                            runtime={runtime}
                            onSelect={() =>
                              setDraft((current) =>
                                current
                                  ? applyDraftRuntimeChange(current, runtime.id)
                                  : current,
                              )
                            }
                          />
                        ))}
                        <label
                          className="agent-runtime-option new-agent-runtime-later"
                          data-selected={!draft.runtimeId}
                        >
                          <input
                            checked={!draft.runtimeId}
                            name={`${id}-runtime-choice`}
                            type="radio"
                            value=""
                            onChange={() =>
                              setDraft((current) =>
                                current
                                  ? applyDraftRuntimeChange(current, "")
                                  : current,
                              )
                            }
                          />
                          <Clock3 aria-hidden="true" size={20} />
                          <span>
                            <strong>Don’t set it up now</strong>
                            <small>
                              Create the agent only. You can set it up from its
                              profile later.
                            </small>
                          </span>
                        </label>
                      </div>
                    ) : (
                      <p className="new-agent-hint">
                        None of your computers can run{" "}
                        {providerChoiceLabel(draft.provider)} yet. You can still
                        create the agent and set it up later.
                      </p>
                    )}
                  </section>

                  <section
                    className="new-agent-section new-agent-model-grid"
                    aria-label="Model and thinking"
                  >
                    <div className="new-agent-field">
                      <label htmlFor={`${id}-model`}>
                        <span>Model</span>
                      </label>
                      {modelIsTyped ? (
                        <input
                          autoComplete="off"
                          id={`${id}-model`}
                          placeholder="Provider default model"
                          spellCheck={false}
                          value={draft.model}
                          onChange={(event) => selectModel(event.target.value)}
                        />
                      ) : (
                        <AgentRuntimeSelect
                          id={`${id}-model`}
                          disabled={selectedRuntime?.status !== "online"}
                          value={draft.model}
                          onChange={(event) => selectModel(event.target.value)}
                        >
                          <option value="">Runtime default model</option>
                          {draft.model &&
                          !modelOptions.some(
                            (option) => option.id === draft.model,
                          ) ? (
                            <option value={draft.model}>
                              {draft.model}{" "}
                              {template ? "(copied)" : "(current)"}
                            </option>
                          ) : null}
                          {modelOptions.map((option) => (
                            <option key={option.id} value={option.id}>
                              {option.label}
                              {option.default ? " (default)" : ""}
                            </option>
                          ))}
                        </AgentRuntimeSelect>
                      )}
                    </div>
                    <div className="new-agent-field">
                      <label htmlFor={`${id}-thinking`}>
                        <span>Thinking level</span>
                      </label>
                      <AgentRuntimeSelect
                        id={`${id}-thinking`}
                        disabled={!thinkingOptions.length}
                        value={draft.thinkingLevel}
                        onChange={(event) =>
                          update({ thinkingLevel: event.target.value })
                        }
                      >
                        <option value="">Runtime default thinking level</option>
                        {draft.thinkingLevel &&
                        !thinkingOptions.some(
                          (level) => level.value === draft.thinkingLevel,
                        ) ? (
                          <option value={draft.thinkingLevel}>
                            {draft.thinkingLevel}{" "}
                            {template ? "(copied)" : "(current)"}
                          </option>
                        ) : null}
                        {thinkingOptions.map((level) => (
                          <option key={level.value} value={level.value}>
                            {level.label}
                          </option>
                        ))}
                      </AgentRuntimeSelect>
                    </div>
                    <DiscoveryStatus
                      message={discovery.message}
                      modelCount={modelOptions.length}
                      runtime={selectedRuntime}
                      status={discovery.status}
                    />
                  </section>

                  <section className="new-agent-section">
                    <button
                      aria-controls={`${id}-instructions`}
                      aria-expanded={instructionsOpen}
                      className="new-agent-disclosure"
                      type="button"
                      onClick={() => setInstructionsOpen((open) => !open)}
                    >
                      <FileText size={15} />
                      Standing instructions
                      <small>
                        {draft.instructions.trim()
                          ? `${draft.instructions.trim().split(/\s+/).length} words`
                          : "Optional"}
                      </small>
                      <ChevronDown size={14} />
                    </button>
                    {instructionsOpen ? (
                      <label
                        className="new-agent-field"
                        id={`${id}-instructions`}
                      >
                        <span className="sr-only">Standing instructions</span>
                        <textarea
                          maxLength={AGENT_INSTRUCTIONS_MAX_LENGTH}
                          placeholder="What this agent owns, how it works, what a good deliverable includes, and when to ask you…"
                          rows={8}
                          value={draft.instructions}
                          onChange={(event) =>
                            update({ instructions: event.target.value })
                          }
                        />
                        <span className="new-agent-hint">
                          Markdown. You can refine these on the profile later;
                          edits apply to future runs.
                        </span>
                      </label>
                    ) : null}
                  </section>

                  <section className="new-agent-section new-agent-capacity">
                    <div className="new-agent-field">
                      <label htmlFor={`${id}-capacity`}>
                        <span>Task runs at once</span>
                      </label>
                      <AgentRuntimeSelect
                        id={`${id}-capacity`}
                        value={String(draft.maxConcurrentRuns)}
                        onChange={(event) =>
                          update({
                            maxConcurrentRuns: Number(event.target.value),
                          })
                        }
                      >
                        {CAP_CHOICES.map((count) => (
                          <option key={count} value={count}>
                            {count}
                          </option>
                        ))}
                      </AgentRuntimeSelect>
                    </div>
                  </section>
                </>
              )}
              {error ? (
                <p className="new-agent-error" role="alert">
                  {error}
                </p>
              ) : null}
            </fieldset>
          </div>
          <footer className="new-agent-footer">
            <span className="new-agent-hint">
              {selectedRuntime && selectedRuntime.status !== "online"
                ? `${runtimeDisplayName(selectedRuntime)} is offline. Bring it online or pick another runtime.`
                : ""}
            </span>
            <DialogClose
              className="dashboard-button"
              disabled={mutation.isLoading}
            >
              Cancel
            </DialogClose>
            <button
              className="dashboard-button dashboard-button-primary"
              disabled={!draft || problems.length > 0 || mutation.isLoading}
              type="submit"
            >
              {mutation.isLoading
                ? "Creating…"
                : template
                  ? "Create copy"
                  : "Create agent"}
            </button>
          </footer>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DraftAvatar({ draft }: { draft: AgentDraft }) {
  // Until the agent exists its id-derived palette is unknown, so "Auto"
  // previews the character in a neutral palette; the roster shows the real
  // pick once the agent is created.
  return (
    <span
      aria-hidden="true"
      className="grid shrink-0 place-items-center w-[64px] h-[64px]"
    >
      <FoldAvatar
        agentId={draft.name.trim() || "new-agent"}
        paletteIndex={foldPaletteIndexForKey(draft.avatarKey)}
        paused
        size={64}
      />
    </span>
  );
}

// One provider the new Agent can be written for, with how many of the
// viewer's own computers can run it right now: online and not signed out.
function ProviderChoice({
  checked,
  label,
  name,
  onSelect,
  provider,
  runtimes,
}: {
  checked: boolean;
  label: string;
  name: string;
  onSelect: () => void;
  provider: AgentProvider;
  runtimes: readonly RuntimeDto[];
}) {
  const own = runtimes.filter((runtime) => runtime.provider === provider);
  const ready = own.filter(runtimeReadyToRun).length;
  const online = own.some((runtime) => runtime.status === "online");
  return (
    <label className="agent-runtime-option" data-selected={checked}>
      <input
        checked={checked}
        name={name}
        type="radio"
        value={provider}
        onChange={onSelect}
      />
      <img alt="" src={publicAssetPath(`images/settings/${provider}.svg`)} />
      <span>
        <strong>{label}</strong>
        <small>
          {ready
            ? `Ready on ${ready} of your computers`
            : online
              ? "Not signed in on your computer"
              : own.length
                ? "Offline on your computers"
                : "No computer yet"}
        </small>
      </span>
    </label>
  );
}

function RuntimeChoice({
  checked,
  name,
  onSelect,
  runtime,
}: {
  checked: boolean;
  name: string;
  onSelect: () => void;
  runtime: RuntimeDto;
}) {
  // Every computer on this list is the viewer's own (ADR 0014).
  const online = runtime.status === "online";
  return (
    <label
      className="agent-runtime-option"
      data-offline={!online}
      data-selected={checked}
    >
      <input
        checked={checked}
        disabled={!online}
        name={name}
        type="radio"
        value={runtime.id}
        onChange={onSelect}
      />
      <img
        alt={providerLabel(runtime.provider)}
        src={publicAssetPath(`images/settings/${runtime.provider}.svg`)}
      />
      <span>
        <strong>{runtimeDisplayName(runtime)}</strong>
        <small>
          {providerLabel(runtime.provider)}
          {online ? "" : " · Reconnect to select"}
        </small>
      </span>
      <span
        className="agent-availability"
        data-online={online && !runtimeSignInLabel(runtime)}
        title={runtime.signIn?.detail}
      >
        {online
          ? (runtimeSignInLabel(runtime) ?? runtimeStateLabel(runtime))
          : runtimeStateLabel(runtime)}
      </span>
    </label>
  );
}

function DiscoveryStatus({
  message,
  modelCount,
  runtime,
  status,
}: {
  message: string;
  modelCount: number;
  runtime: RuntimeDto | null;
  status: ModelDiscoveryStatus;
}) {
  if (!runtime || runtime.status !== "online") {
    return (
      <p className="new-agent-status">
        {modelCount
          ? "Select an online runtime to load its models."
          : "Type a model name, or leave it empty for the provider default."}
      </p>
    );
  }
  if (status === "pending" || status === "running") {
    return (
      <p className="new-agent-status" role="status">
        Discovering models on {runtimeDisplayName(runtime)}…
      </p>
    );
  }
  if (status === "completed") {
    return (
      <p className="new-agent-status" role="status">
        {modelCount === 1
          ? "1 model available"
          : `${modelCount} models available`}
      </p>
    );
  }
  if (status === "failed" || status === "timeout" || status === "unavailable") {
    return (
      <p className="new-agent-status" data-tone="warning" role="status">
        {message ||
          "Models could not be discovered. The runtime default is used."}
      </p>
    );
  }
  return null;
}
