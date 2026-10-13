import { useEffect, useState } from "react";

import {
  getCeoAgentApiErrorMessage,
  useListAgentRuntimesQuery,
} from "@/shared/api/agent-runtime-api";
import {
  useBindAgentMutation,
  useUnbindAgentMutation,
} from "@/features/agents/api/agent-binding-api";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  agentBindingLabel,
  bindingChoiceStateLabel,
  bindingChoiceWarning,
  bindingRuntimeChoices,
  runtimeChoiceName,
  type BindingRuntimeChoice,
} from "@/features/agents/model/agent-binding";
import { RuntimeNeededNotice } from "@/features/settings/components/runtime-needed-notice";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { AgentDto } from "@/shared/api/types";
import { publicAssetPath } from "@/shared/lib/public-assets";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";

// The dialog opens from Projects and chat as well as the Agents pages, so it
// brings the dialog shell and the computer rows it is drawn with.
import "@/features/agents/styles/agents.css";
import "@/features/settings/styles/settings.css";

type BindAgentDialogProps = {
  agent: AgentDto;
  companyId: string;
  onOpenChange: (open: boolean) => void;
  // Called once the Agent is set up, so the caller can do what the viewer
  // actually came for — usually starting the run the button offered.
  onBound?: () => void;
  open: boolean;
};

// "Set <agent> up on your computer", or "Change where <agent> runs" once it
// is (ADR 0014). The list is the viewer's own runtimes of the Agent's
// provider, and nothing else: another member's machine is not theirs to
// choose, and the API would not name it anyway.
export function BindAgentDialog({
  agent,
  companyId,
  onBound,
  onOpenChange,
  open,
}: BindAgentDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="profile-delete-dialog runtime-dialog bind-agent-dialog"
        overlayClassName="profile-delete-dialog__overlay bind-agent-dialog__overlay"
        showCloseButton={false}
      >
        <BindAgentForm
          agent={agent}
          companyId={companyId}
          onBound={onBound}
          onDone={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function BindAgentForm({
  agent,
  companyId,
  onBound,
  onDone,
}: {
  agent: AgentDto;
  companyId: string;
  onBound?: () => void;
  onDone: () => void;
}) {
  const runtimes = useListAgentRuntimesQuery(companyId);
  const choices = bindingRuntimeChoices(
    agent.provider,
    runtimes.data?.items ?? [],
  );
  const currentRuntimeId = agent.binding?.runtime.id ?? null;
  const [selected, setSelected] = useState<string | null>(currentRuntimeId);
  const [error, setError] = useState<string | null>(null);
  const [bindAgent, binding] = useBindAgentMutation();
  const [unbindAgent, unbinding] = useUnbindAgentMutation();
  const busy = binding.isLoading || unbinding.isLoading;
  const localDaemon = useLocalDaemonId();

  // When the viewer has not set this Agent up anywhere yet, the computer
  // they are on is the sensible default if it can take the Agent, and the
  // first computer that can otherwise. The default waits for the desktop to
  // say which computer it is, so it does not settle on another one first.
  useEffect(() => {
    if (selected || currentRuntimeId || !localDaemon.known) return;
    const preferred =
      choices.find(
        (choice) =>
          choice.selectable && choice.runtime.daemonId === localDaemon.daemonId,
      ) ?? choices.find((choice) => choice.selectable);
    if (preferred) {
      setSelected(preferred.runtime.id);
    }
  }, [
    choices,
    currentRuntimeId,
    localDaemon.daemonId,
    localDaemon.known,
    selected,
  ]);

  async function submit() {
    if (!selected) return;
    setError(null);
    try {
      await bindAgent({
        agentId: agent.id,
        companyId,
        runtimeId: selected,
      }).unwrap();
      onBound?.();
      onDone();
    } catch (caught) {
      setError(bindErrorMessage(caught));
    }
  }

  async function remove() {
    setError(null);
    try {
      await unbindAgent({ agentId: agent.id, companyId }).unwrap();
      onDone();
    } catch (caught) {
      setError(
        getCeoAgentApiErrorMessage(
          caught,
          "The agent could not be removed from your computer.",
        ),
      );
    }
  }

  const tool = agent.provider === "claude" ? "Claude Code" : "Codex";
  // Once the Agent is set up, the dialog is about moving it. When the
  // computer it is on is the viewer's only one of that provider, there is
  // nowhere to move it, and the dialog says so instead of offering a list
  // with one row and a button that can never be pressed.
  const currentName = agentBindingLabel(agent);
  const otherChoices = choices.filter(
    (choice) => choice.runtime.id !== currentRuntimeId,
  );
  const onlyComputer =
    choices.length === 1 && choices[0].runtime.id === currentRuntimeId;
  const selectedChoice =
    selected && selected !== currentRuntimeId
      ? choices.find((choice) => choice.runtime.id === selected)
      : undefined;
  const warning = selectedChoice
    ? bindingChoiceWarning(selectedChoice.runtime, agent.name)
    : "";

  return (
    <div className="profile-delete-dialog__surface">
      <DialogHeader className="profile-delete-dialog__header">
        <span className="profile-delete-dialog__icon runtime-dialog__icon">
          <Icon name="cpu" />
        </span>
        <DialogTitle>
          {currentRuntimeId
            ? `Change where ${agent.name} runs`
            : `Set ${agent.name} up on your computer`}
        </DialogTitle>
        <DialogDescription>
          {!currentRuntimeId
            ? `${agent.name} runs where you put it. Pick one of your computers with ${tool} installed; its work, files and provider sign-in stay there.`
            : onlyComputer
              ? `${agent.name} already runs on ${currentName}, your only computer with ${tool}. To move it, add another computer with ${tool} first.`
              : `${agent.name} runs on ${currentName} now. Pick another of your computers with ${tool} installed: waiting runs move with it, and a run already going finishes where it is.`}
        </DialogDescription>
      </DialogHeader>

      {runtimes.isLoading ? (
        <p className="bind-agent-dialog__hint">Loading your computers…</p>
      ) : choices.length === 0 ? (
        <RuntimeNeededNotice companyId={companyId} provider={agent.provider} />
      ) : onlyComputer ? null : (
        <fieldset className="agent-runtime-options bind-agent-dialog__choices">
          <legend className="sr-only">Your computers</legend>
          {choices.map((choice) => (
            <BindingChoice
              agentName={agent.name}
              busy={busy}
              checked={selected === choice.runtime.id}
              choice={choice}
              current={choice.runtime.id === currentRuntimeId}
              key={choice.runtime.id}
              local={choice.runtime.daemonId === localDaemon.daemonId}
              onSelect={() => setSelected(choice.runtime.id)}
            />
          ))}
        </fieldset>
      )}

      {currentRuntimeId &&
      otherChoices.length > 0 &&
      otherChoices.every((choice) => !choice.selectable) ? (
        <p className="bind-agent-dialog__hint">
          None of your other {tool} computers can run {agent.name} right now.
        </p>
      ) : null}

      {warning ? (
        <p className="bind-agent-dialog__note" role="note">
          <Icon name="info" size={16} />
          <span>{warning}</span>
        </p>
      ) : null}

      {currentRuntimeId ? (
        <button
          className="bind-agent-dialog__remove"
          disabled={busy}
          onClick={() => void remove()}
          type="button"
        >
          {unbinding.isLoading
            ? "Removing…"
            : `Remove ${agent.name} from my computer`}
        </button>
      ) : null}

      {error ? (
        <p className="profile-delete-dialog__error" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter className="profile-delete-dialog__footer">
        <DialogClose asChild>
          <button
            className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
            disabled={busy}
            type="button"
          >
            {onlyComputer ? "Close" : "Cancel"}
          </button>
        </DialogClose>
        {onlyComputer ? null : (
          <button
            className="profile-delete-dialog__button runtime-dialog__submit"
            disabled={busy || !selected || selected === currentRuntimeId}
            onClick={() => void submit()}
            type="button"
          >
            {binding.isLoading
              ? "Setting up…"
              : currentRuntimeId
                ? "Move it here"
                : "Set it up"}
          </button>
        )}
      </DialogFooter>
    </div>
  );
}

// One of the viewer's computers, drawn like the New agent picker: the name,
// whether it is the computer they are on, why it cannot be chosen if it
// cannot, and its state in the words the Agent's own chip uses. What a
// not-ready choice means for the runs is said once, under the list, for the
// row that is selected.
function BindingChoice({
  agentName,
  busy,
  checked,
  choice: { reason, runtime, selectable },
  current,
  local,
  onSelect,
}: {
  agentName: string;
  busy: boolean;
  checked: boolean;
  choice: BindingRuntimeChoice;
  current: boolean;
  local: boolean;
  onSelect: () => void;
}) {
  const state = bindingChoiceStateLabel(runtime);
  const detail = [
    local ? "This computer" : "",
    current ? `${agentName} runs here now` : providerLabel(runtime.provider),
    reason,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <label
      className="agent-runtime-option"
      data-offline={!selectable}
      data-selected={checked}
    >
      <input
        checked={checked}
        disabled={busy || !selectable}
        name="binding-runtime"
        onChange={onSelect}
        type="radio"
        value={runtime.id}
      />
      <img
        alt={providerLabel(runtime.provider)}
        src={publicAssetPath(`images/settings/${runtime.provider}.svg`)}
      />
      <span>
        <strong>{runtimeChoiceName(runtime)}</strong>
        <small>{detail}</small>
      </span>
      <span
        className="agent-availability"
        data-online={state === "Ready"}
        title={runtime.signIn?.detail}
      >
        {state}
      </span>
    </label>
  );
}

// The two refusals a member can actually act on, in their own words. The
// mutation rejects with the BoardlyApiError itself, whose `code` is the API's.
function bindErrorMessage(caught: unknown): string {
  const code = (caught as BoardlyApiError | undefined)?.code;
  if (code === "agent_binding_provider_mismatch") {
    return "That computer runs a different provider. Pick one that matches this agent.";
  }
  if (code === "runtime_not_found") {
    return "That computer is no longer connected. Pick another one.";
  }
  return getCeoAgentApiErrorMessage(
    caught,
    "The agent could not be set up on that computer.",
  );
}
