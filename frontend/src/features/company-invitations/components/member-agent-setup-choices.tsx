import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  agentBindingLabel,
  bindingChoiceStateLabel,
  bindingChoiceWarning,
} from "@/features/agents/model/agent-binding";
import { RuntimeNeededNotice } from "@/features/settings/components/runtime-needed-notice";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";
import { Icon } from "@/shared/ui/icon";
import type { MemberAgentSetup } from "../hooks/use-member-agent-setup";

export function MemberSetupComputers({
  companyId,
  setup,
}: {
  companyId: string;
  setup: MemberAgentSetup;
}) {
  if (!setup.computers.length)
    return <RuntimeNeededNotice companyId={companyId} />;
  return (
    <>
      <fieldset className="member-agent-setup__choices grid min-w-0 gap-[10px] m-0 p-0 [border:0]">
        <legend className="sr-only">Your computers</legend>
        {setup.computers.map((computer) => (
          <label
            className="member-agent-setup__computer"
            key={computer.id}
            data-selected={setup.computer?.id === computer.id}
          >
            <input
              type="radio"
              name="setup-computer"
              value={computer.id}
              checked={setup.computer?.id === computer.id}
              onChange={() => setup.chooseComputer(computer.id)}
            />
            <Icon name="cpu" size={24} aria-hidden="true" />
            <span className="member-agent-setup__identity grid min-w-0 flex-1 gap-[4px] wrap-anywhere">
              <strong>
                {computer.name}
                {computer.id === setup.localDaemonId ? " · This computer" : ""}
              </strong>
              {computer.runtimes.map((runtime) => (
                <span key={runtime.id}>
                  {providerLabel(runtime.provider)} ·{" "}
                  {bindingChoiceStateLabel(runtime)}
                  {runtime.unusableReason
                    ? ` · ${runtime.unusableReason.message}`
                    : ""}
                </span>
              ))}
            </span>
          </label>
        ))}
      </fieldset>
      {setup.computer?.runtimes.map((runtime) => {
        const warning = bindingChoiceWarning(runtime, "Your team");
        return warning ? (
          <p
            className="m-0 p-[14px_16px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] text-workspace-muted text-[length:13px] leading-[1.6]"
            key={runtime.id}
          >
            {warning}
          </p>
        ) : null;
      })}
    </>
  );
}

export function MemberSetupAgents({ setup }: { setup: MemberAgentSetup }) {
  if (!setup.agents.length) {
    return (
      <p className="m-0 p-[14px_16px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] text-workspace-muted text-[length:13px] leading-[1.6]">
        Your company has no active agents yet. You can set them up from Agents
        when your team adds them.
      </p>
    );
  }
  return (
    <>
      <div className="member-agent-setup__toolbar">
        <span role="status">
          {setup.selectedCount} {setup.selectedCount === 1 ? "agent" : "agents"}{" "}
          selected
        </span>
        <button
          type="button"
          className="onboarding-button onboarding-button--ghost"
          disabled={
            setup.saving ||
            !setup.choices.some(
              (choice) => choice.selectable && !choice.selected,
            )
          }
          onClick={setup.selectAll}
        >
          Select all available
        </button>
      </div>
      <fieldset
        className="member-agent-setup__choices grid min-w-0 gap-[10px] m-0 p-0 [border:0]"
        disabled={setup.saving}
      >
        <legend className="sr-only">Agents to set up</legend>
        {setup.choices.map((choice) => {
          const { agent, runtime, saved, selectable, selected, error } = choice;
          const configured = !!agent.binding || saved;
          const providerRuntime = setup.computer?.runtimes.find(
            (item) => item.provider === agent.provider,
          );
          let status = runtime
            ? `On ${setup.computer?.name} · ${bindingChoiceStateLabel(runtime)}`
            : (providerRuntime?.unusableReason?.message ??
              `Install ${providerLabel(agent.provider)} and sign in on this computer to set up ${agent.name}.`);
          if (!setup.computer)
            status = "Choose a computer to set up this agent.";
          if (configured)
            status = agent.binding
              ? `Already set up · ${agentBindingLabel(agent)}`
              : "Set up on your computer";
          if (setup.savingId === agent.id) status = "Setting up…";
          return (
            <label
              key={agent.id}
              className="member-agent-setup__agent"
              data-selected={selected || configured}
              data-disabled={!selectable}
            >
              <input
                type="checkbox"
                checked={selected || configured}
                disabled={!selectable}
                aria-label={`Set up ${agent.name}`}
                onChange={() => setup.toggleAgent(agent.id)}
              />
              <FoldAvatar
                agentId={agent.id}
                paletteIndex={foldPaletteIndexForKey(agent.avatarKey)}
                size={56}
                paused
              />
              <span className="member-agent-setup__identity grid min-w-0 flex-1 gap-[4px] wrap-anywhere">
                <strong>{agent.name}</strong>
                <span>
                  {agent.title} · {providerLabel(agent.provider)}
                </span>
                <span className={error ? "member-agent-setup__error" : ""}>
                  {error ?? status}
                </span>
              </span>
              {configured ? (
                <Icon name="checkmark" size={18} aria-hidden="true" />
              ) : null}
            </label>
          );
        })}
      </fieldset>
      {!setup.computer && !setup.allSetUp ? (
        <p className="m-0 p-[14px_16px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] text-workspace-muted text-[length:13px] leading-[1.6]">
          This computer is no longer available. Go back to choose another
          computer.
        </p>
      ) : setup.choices.some(
          (choice) => !choice.agent.binding && !choice.saved && !choice.runtime,
        ) ? (
        <p className="m-0 p-[14px_16px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] text-workspace-muted text-[length:13px] leading-[1.6]">
          Some agents need another provider on this computer. You can set up the
          available agents now and add the others later from Agents.
        </p>
      ) : null}
    </>
  );
}
