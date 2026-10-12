import { useRef, useState, type FormEvent } from "react";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { findCeoAgentInPage } from "@/features/agent-setup/model/ceo-agent-setup";
import { NewAgentDialog } from "@/features/agents/components/new-agent-dialog";
import { useBindAgentMutation } from "@/features/agents/api/agent-binding-api";
import {
  AGENT_INSTRUCTIONS_MAX_LENGTH,
  AGENT_NAME_MAX_LENGTH,
  AGENT_TITLE_MAX_LENGTH,
} from "@/features/agents/model/agent-draft";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { AgentDto } from "@/shared/api/types";
import {
  useArchiveWorkspaceAgentMutation,
  useRestoreWorkspaceAgentMutation,
  useUpdateWorkspaceAgentMutation,
} from "@/shared/api/workspace-api";
import {
  readStorageItem,
  removeStorageItem,
  writeStorageItem,
} from "@/shared/lib/browser-storage";
import {
  avatarPalettes,
  FoldAvatar,
  getAvatarPaletteIndex,
  type FoldAvatarProps,
} from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";
import {
  ArrowLeft,
  ArrowRight,
  Pencil,
  Plus,
  RefreshCwIcon,
  X,
} from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogClose,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { OnboardingActions } from "@/features/onboarding/company/components/onboarding-actions";
import "@/features/agents/styles/new-agent-dialog.css";
import "@/features/onboarding/styles/onboarding-team.css";

// The glow behind a chosen teammate is their own colour, resolved the way
// FoldAvatar resolves it, so selection reads from the character itself.
function cardGlow(agent: AgentDto) {
  const index =
    foldPaletteIndexForKey(agent.avatarKey) ?? getAvatarPaletteIndex(agent.id);
  return avatarPalettes[index]?.color ?? "transparent";
}

type TeamDraft = Pick<
  AgentDto,
  "name" | "title" | "instructions" | "revision"
> & { selected: boolean };

function agentDraft(agent: AgentDto): TeamDraft {
  return {
    name: agent.name,
    title: agent.title,
    instructions: agent.instructions,
    revision: agent.revision,
    selected: !agent.archivedAt,
  };
}

export function OnboardingTeamPanel({
  companyId,
  completionError,
  onComplete,
  onBack,
}: {
  companyId: string;
  completionError: string;
  onComplete: () => void | Promise<void>;
  onBack: () => void;
}) {
  const roster = useListCompanyAgentsQuery(companyId, {
    refetchOnMountOrArgChange: true,
  });
  const root = findCeoAgentInPage(roster.currentData);
  const agents = (roster.currentData?.items ?? []).filter(
    (agent) => agent.id !== root?.id,
  );
  const displayedAgents = root
    ? [...agents.slice(0, 2), root, ...agents.slice(2)]
    : agents;
  const storageKey = `boardly:onboarding:team:${companyId}`;
  const [drafts, setDrafts] = useState<Record<string, TeamDraft>>(() => {
    try {
      return JSON.parse(readStorageItem("session", storageKey) || "{}") ?? {};
    } catch {
      return {};
    }
  });
  const editTrigger = useRef<HTMLButtonElement | null>(null);
  const [editing, setEditing] = useState<{
    agent: AgentDto;
    draft: TeamDraft;
  } | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [updateAgent] = useUpdateWorkspaceAgentMutation();
  const [archiveAgent] = useArchiveWorkspaceAgentMutation();
  const [restoreAgent] = useRestoreWorkspaceAgentMutation();
  const [bindAgent] = useBindAgentMutation();
  const selectedCount = agents.filter(
    (agent) => (drafts[agent.id] ?? agentDraft(agent)).selected,
  ).length;

  const ceoName =
    (root && drafts[root.id]?.name.trim()) || root?.name || "your CEO";
  const editingCeo = Boolean(editing && editing.agent.id === root?.id);

  const updateDraft = (agent: AgentDto, patch: Partial<TeamDraft>) => {
    setDrafts((current) => {
      const next = {
        ...current,
        [agent.id]: { ...(current[agent.id] ?? agentDraft(agent)), ...patch },
      };
      writeStorageItem("session", storageKey, JSON.stringify(next));
      return next;
    });
  };

  const save = async () => {
    if (saving || !root || !roster.currentData) return;
    setSaving(true);
    setError("");
    let savingName = "your team";
    try {
      const ceoDraft = drafts[root.id] ?? agentDraft(root);
      savingName = ceoDraft.name || root.name;
      if (!ceoDraft.name.trim()) throw new Error("Enter a name for your CEO.");
      if (ceoDraft.name.trim() !== root.name) {
        const savedCeo = await updateAgent({
          companyId,
          agentId: root.id,
          request: {
            expectedRevision: ceoDraft.revision,
            name: ceoDraft.name.trim(),
          },
        }).unwrap();
        updateDraft(root, { ...agentDraft(savedCeo), selected: true });
      }
      for (const agent of agents) {
        const draft = drafts[agent.id] ?? agentDraft(agent);
        savingName = draft.name || agent.name;
        let saved = agent;
        if (draft.selected) {
          if (!draft.name.trim() || !draft.title.trim()) {
            throw new Error("Enter a name and role for each selected agent.");
          }
          if (saved.archivedAt) {
            saved = await restoreAgent({
              companyId,
              agentId: agent.id,
              request: { expectedRevision: draft.revision },
            }).unwrap();
            updateDraft(agent, { revision: saved.revision });
          }
          if (
            draft.name.trim() !== saved.name ||
            draft.title.trim() !== saved.title ||
            draft.instructions !== saved.instructions
          ) {
            saved = await updateAgent({
              companyId,
              agentId: agent.id,
              request: {
                expectedRevision:
                  saved === agent ? draft.revision : saved.revision,
                name: draft.name.trim(),
                title: draft.title.trim(),
                instructions: draft.instructions,
              },
            }).unwrap();
            updateDraft(agent, { ...agentDraft(saved), selected: true });
          }
          if (
            !saved.binding &&
            root.binding &&
            saved.provider === root.provider
          ) {
            saved = await bindAgent({
              companyId,
              agentId: agent.id,
              runtimeId: root.binding.runtime.id,
            }).unwrap();
            updateDraft(agent, { revision: saved.revision });
          }
        } else if (!agent.archivedAt) {
          saved = await archiveAgent({
            companyId,
            agentId: agent.id,
            request: { expectedRevision: draft.revision },
          }).unwrap();
          updateDraft(agent, { revision: saved.revision });
        }
      }
      await roster.refetch().unwrap();
      // Keep the saved choices if Andrew's kickoff fails and needs a retry.
      await onComplete();
      removeStorageItem("session", storageKey);
    } catch (caught) {
      setError(
        `Could not finish saving ${savingName}. ${getBoardlyQueryErrorMessage(caught)} Your other saved choices are kept.`,
      );
    } finally {
      setSaving(false);
    }
  };

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void save();
  };

  if (!roster.currentData)
    return (
      <div className="onboarding-connections__state" role="status">
        {roster.isError ? (
          <>
            <p>{getBoardlyQueryErrorMessage(roster.error)}</p>
            <button type="button" onClick={() => void roster.refetch()}>
              Try again
            </button>
          </>
        ) : (
          <>
            <RefreshCwIcon
              className="animate-spin motion-reduce:animate-none"
              size={20}
              aria-hidden="true"
            />
            <span className="sr-only">Loading your team</span>
          </>
        )}
      </div>
    );

  return (
    <>
      <form className="onboarding-team" onSubmit={submit}>
        <div className="onboarding-team__grid">
          {displayedAgents.map((agent, index) => {
            const isCeo = agent.id === root?.id;
            // Starter looks follow their palette, so editing a name or role keeps the artwork.
            const starterLooks: Record<string, FoldAvatarProps["appearance"]> =
              {
                "fold-periwinkle": "engineer",
                "fold-orchid": "product",
                "fold-coral": "marketing",
                "fold-lagoon": "operations",
              };
            const appearance = isCeo
              ? "ceo"
              : starterLooks[agent.avatarKey ?? ""];
            const draft = isCeo
              ? { ...(drafts[agent.id] ?? agentDraft(agent)), selected: true }
              : (drafts[agent.id] ?? agentDraft(agent));
            return (
              <article
                className="onboarding-team__card"
                data-selected={draft.selected}
                key={agent.id}
                style={
                  {
                    "--card-index": index,
                    "--card-glow": cardGlow(agent),
                  } as never
                }
              >
                <button
                  type="button"
                  className="onboarding-team__choice"
                  role="checkbox"
                  aria-checked={draft.selected}
                  aria-label={
                    isCeo
                      ? `${draft.name || agent.name}, CEO, always included in your team`
                      : `Include ${draft.name || agent.name} in your team`
                  }
                  disabled={saving || isCeo}
                  onClick={() => {
                    if (!isCeo)
                      updateDraft(agent, { selected: !draft.selected });
                  }}
                >
                  <span className="onboarding-team__portrait">
                    <FoldAvatar
                      agentId={agent.id}
                      paletteIndex={foldPaletteIndexForKey(agent.avatarKey)}
                      size={220}
                      appearance={appearance}
                      paused={!appearance}
                    />
                  </span>
                  <span className="onboarding-team__identity">
                    <strong>{draft.name || "Your agent"}</strong>
                    <span>
                      {isCeo
                        ? "CEO · Always on your team"
                        : draft.title || "Choose a role"}
                    </span>
                  </span>
                </button>
                <button
                  type="button"
                  className="onboarding-team__edit"
                  aria-label={`Customize ${draft.name || agent.name}`}
                  title={`Customize ${draft.name || agent.name}`}
                  disabled={saving || (isCeo && !agent.access.configure)}
                  onClick={(event) => {
                    editTrigger.current = event.currentTarget;
                    setEditing({ agent, draft: { ...draft } });
                  }}
                >
                  <Pencil size={20} aria-hidden="true" />
                </button>
              </article>
            );
          })}
        </div>
        {error || completionError ? (
          <p className="workspace-onboarding__error" role="alert">
            {error || completionError}
          </p>
        ) : null}
        {!root ? (
          <p className="workspace-onboarding__error" role="alert">
            Andrew could not be found. Return to agent setup to continue.
          </p>
        ) : null}
        <OnboardingActions
          start={
            <>
              <button
                type="button"
                className="onboarding-button onboarding-button--ghost"
                disabled={saving}
                onClick={onBack}
              >
                <ArrowLeft size={17} aria-hidden="true" />
                Back
              </button>
              <div className="onboarding-team__selection" aria-live="polite">
                <strong>
                  {selectedCount
                    ? `${selectedCount} teammate${selectedCount === 1 ? "" : "s"} selected`
                    : `Starting with just ${ceoName}`}
                </strong>
                <span>{ceoName} is already on your team.</span>
              </div>
            </>
          }
        >
          {error ? (
            <button
              type="button"
              className="onboarding-button onboarding-button--ghost"
              disabled={saving}
              onClick={() => {
                setDrafts({});
                removeStorageItem("session", storageKey);
                setError("");
                void roster.refetch();
              }}
            >
              Reload saved team
            </button>
          ) : null}
          <button
            className="onboarding-button onboarding-button--secondary"
            type="button"
            disabled={saving || !root}
            onClick={() => setCreateOpen(true)}
          >
            <Plus size={17} aria-hidden="true" />
            Create your own agent
          </button>
          <button
            type="submit"
            className="onboarding-button workspace-onboarding__submit"
            disabled={saving || !root || createOpen || Boolean(editing)}
          >
            {saving ? (
              <>
                <RefreshCwIcon
                  size={17}
                  className="animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
                <span className="sr-only">Saving your team</span>
              </>
            ) : (
              <>
                {completionError
                  ? "Try again"
                  : selectedCount
                    ? `Continue with ${selectedCount + 1} agents`
                    : `Continue with ${ceoName}`}
                <ArrowRight size={17} aria-hidden="true" />
              </>
            )}
          </button>
        </OnboardingActions>
      </form>
      {editing ? (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open) setEditing(null);
          }}
        >
          <DialogContent
            className="new-agent-dialog onboarding-team__dialog"
            overlayClassName="new-agent-overlay"
            showCloseButton={false}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              editTrigger.current?.focus();
            }}
          >
            <form
              className="new-agent-form"
              onSubmit={(event) => {
                event.preventDefault();
                if (
                  !editing.draft.name.trim() ||
                  (!editingCeo && !editing.draft.title.trim())
                )
                  return;
                updateDraft(editing.agent, {
                  name: editing.draft.name.trim(),
                  ...(!editingCeo
                    ? {
                        title: editing.draft.title.trim(),
                        instructions: editing.draft.instructions,
                      }
                    : {}),
                });
                setEditing(null);
              }}
            >
              <header className="new-agent-header">
                <div className="new-agent-heading">
                  <DialogTitle className="new-agent-title">
                    Customize {editing.agent.name}
                  </DialogTitle>
                  <DialogDescription className="new-agent-description">
                    {editingCeo
                      ? "Choose a name for your CEO. They are always part of your team."
                      : "Give your teammate a name, a focus, and a way of working."}
                  </DialogDescription>
                </div>
                <DialogClose className="new-agent-close" aria-label="Close">
                  <X size={18} aria-hidden="true" />
                </DialogClose>
              </header>
              <div className="new-agent-body">
                <div className="new-agent-fields">
                  <label className="new-agent-field">
                    <span>Name</span>
                    <input
                      required
                      maxLength={AGENT_NAME_MAX_LENGTH}
                      value={editing.draft.name}
                      onChange={(event) =>
                        setEditing({
                          ...editing,
                          draft: { ...editing.draft, name: event.target.value },
                        })
                      }
                    />
                  </label>
                  {!editingCeo ? (
                    <>
                      <label className="new-agent-field">
                        <span>Role</span>
                        <input
                          required
                          maxLength={AGENT_TITLE_MAX_LENGTH}
                          value={editing.draft.title}
                          onChange={(event) =>
                            setEditing({
                              ...editing,
                              draft: {
                                ...editing.draft,
                                title: event.target.value,
                              },
                            })
                          }
                        />
                      </label>
                      <label className="new-agent-field">
                        <span>Instructions</span>
                        <textarea
                          rows={6}
                          maxLength={AGENT_INSTRUCTIONS_MAX_LENGTH}
                          value={editing.draft.instructions}
                          onChange={(event) =>
                            setEditing({
                              ...editing,
                              draft: {
                                ...editing.draft,
                                instructions: event.target.value,
                              },
                            })
                          }
                        />
                      </label>
                    </>
                  ) : null}
                </div>
              </div>
              <div className="new-agent-footer onboarding-team__dialog-actions">
                <button
                  type="button"
                  className="dashboard-button"
                  onClick={() => setEditing(null)}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="dashboard-button dashboard-button-primary"
                  disabled={
                    !editing.draft.name.trim() ||
                    (!editingCeo && !editing.draft.title.trim())
                  }
                >
                  Save changes
                </button>
              </div>
            </form>
          </DialogContent>
        </Dialog>
      ) : null}
      {createOpen ? (
        <NewAgentDialog
          companyId={companyId}
          onClose={() => setCreateOpen(false)}
          onCreated={(agent) => updateDraft(agent, agentDraft(agent))}
        />
      ) : null}
    </>
  );
}
