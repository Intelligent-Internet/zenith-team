import * as Popover from "@radix-ui/react-popover";
import { Check, ChevronDown, Search, UserRound } from "@/shared/ui/icons";
import { useState } from "react";
import {
  agentAvailabilityHelp,
  agentAvailabilityLabel,
} from "@/features/agents/model/agent-binding";
import type { AgentDto } from "@/shared/api/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

export function TaskCreationAgentPicker({
  agents,
  selected,
  onSelect,
  onSelectSelf,
  selfLabel = "Me",
  selfHint = "I do it myself. No agent runs.",
  showAvailability = true,
  label,
}: {
  agents: AgentDto[];
  selected?: AgentDto;
  onSelect: (id: string) => void;
  // Offers "Me" above the Agents, for a Task the viewer does themself. With
  // it, an undefined `selected` means "Me" is the choice, and the chip says
  // so rather than naming an Agent that will not run.
  onSelectSelf?: () => void;
  // Who "Me" is when the Task is for another member, who then does it
  // themself.
  selfLabel?: string;
  selfHint?: string;
  // Whether each Agent says if the viewer has it set up. That is only worth
  // saying when the Task will run on the viewer's own computer.
  showAvailability?: boolean;
  label: string;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const query = search.trim().toLocaleLowerCase();
  const matches = agents.filter((agent) =>
    `${agent.name} ${agent.title}`.toLocaleLowerCase().includes(query),
  );
  // "Me" answers to the start of "me" or "myself", not to any letter in them;
  // another member answers to their name.
  const offerSelf =
    Boolean(onSelectSelf) &&
    (selfLabel === "Me"
      ? ["me", "myself"].some((word) => word.startsWith(query))
      : selfLabel.toLocaleLowerCase().includes(query));
  const selfSelected = Boolean(onSelectSelf) && !selected;
  // With "Me" in the list it is not only Agents any more, so the search and
  // the list say what the choice is instead.
  const searchLabel = onSelectSelf
    ? "Search who does the work"
    : "Search agents";
  function close() {
    setOpen(false);
    setSearch("");
  }
  return (
    <Popover.Root
      open={open}
      onOpenChange={(value) => {
        setOpen(value);
        if (!value) setSearch("");
      }}
    >
      <Popover.Trigger
        type="button"
        className="task-create-chip task-create-agent-picker"
        aria-label={`${label}${selected ? `, ${selected.name}` : selfSelected ? `, ${selfLabel}` : ""}`}
      >
        {selected ? (
          <FoldAvatar paused agentId={selected.id} size={21} />
        ) : selfSelected ? (
          <span className="task-create-self-avatar" aria-hidden="true">
            <UserRound size={13} />
          </span>
        ) : null}
        {selected?.name ?? (selfSelected ? selfLabel : "Choose agent")}
        <ChevronDown size={12} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="task-create-agent-menu z-[230] w-[min(340px,_calc(100vw_-_40px))] p-[8px] [border:1px_solid_var(--workspace-divider)] rounded-[8px] [background:var(--workspace-overlay-bg)] text-workspace-text [box-shadow:0_12px_40px_rgb(0_0_0_/_16%)] font-workspace-body"
          align="start"
          sideOffset={7}
          collisionPadding={12}
        >
          <label>
            <Search size={15} />
            <input
              autoFocus
              aria-label={searchLabel}
              placeholder={onSelectSelf ? "Search…" : "Search agents…"}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <div
            className="task-create-agent-options"
            aria-label={onSelectSelf ? "Who does the work" : "Available agents"}
          >
            {offerSelf ? (
              <button
                type="button"
                aria-pressed={selfSelected}
                onClick={() => {
                  onSelectSelf?.();
                  close();
                }}
              >
                <span className="task-create-self-avatar" aria-hidden="true">
                  <UserRound size={16} />
                </span>
                <span>
                  <strong>{selfLabel}</strong>
                  <small>{selfHint}</small>
                </span>
                {selfSelected ? <Check size={14} /> : null}
              </button>
            ) : null}
            {matches.map((agent) => {
              // Every Agent can be chosen: it is a template the company
              // shares (ADR 0014). What the chip says is whether the viewer
              // has it set up on a computer of theirs, which is what decides
              // whether the new Task starts straight away.
              return (
                <button
                  type="button"
                  key={agent.id}
                  aria-pressed={agent.id === selected?.id}
                  title={
                    showAvailability ? agentAvailabilityHelp(agent) : undefined
                  }
                  onClick={() => {
                    onSelect(agent.id);
                    close();
                  }}
                >
                  <FoldAvatar paused agentId={agent.id} size={30} />
                  <span>
                    <strong>{agent.name}</strong>
                    <small>{agent.title}</small>
                  </span>
                  {showAvailability ? (
                    <span
                      className="task-create-agent-status"
                      data-online={agent.runtimeAvailable}
                    >
                      {agentAvailabilityLabel(agent)}
                    </span>
                  ) : null}
                  {agent.id === selected?.id ? <Check size={14} /> : null}
                </button>
              );
            })}
            {matches.length === 0 && !offerSelf ? (
              <p>
                {onSelectSelf
                  ? "Nothing matches your search."
                  : "No agents match your search."}
              </p>
            ) : null}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
