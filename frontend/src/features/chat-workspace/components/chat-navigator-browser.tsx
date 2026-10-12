import { useId } from "react";
import {
  ArrowLeft,
  ChevronRight,
  PencilLine,
  Search,
  X,
} from "@/shared/ui/icons";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import type {
  ChatNavigatorAgent,
  ChatNavigatorBrowseState,
} from "@/features/chat-workspace/model/chat-navigator-browse";
import "@/features/chat-workspace/styles/chat-navigator-browser.css";

export function ChatNavigatorBrowseControls({
  browse,
  onChange,
  onNewAgentChat,
  selectedAgent,
  includeHidden,
}: {
  browse: ChatNavigatorBrowseState;
  onChange: (browse: ChatNavigatorBrowseState) => void;
  onNewAgentChat: (agentId: string) => void;
  selectedAgent?: ChatNavigatorAgent;
  includeHidden?: boolean;
}) {
  const searchId = useId();
  return (
    <div className="workspace-chat-navigator__browse-controls">
      <label className="workspace-chat-navigator__search" htmlFor={searchId}>
        <Search size={15} aria-hidden />
        <span className="sr-only">Search conversations or agents</span>
        <input
          autoComplete="off"
          aria-label="Search conversations or agents"
          id={searchId}
          maxLength={100}
          onChange={(event) =>
            onChange({ ...browse, query: event.target.value })
          }
          placeholder={
            selectedAgent
              ? "Search this agent’s chats…"
              : "Find a conversation…"
          }
          type="search"
          value={browse.query}
        />
        {browse.query ? (
          <button
            aria-label="Clear conversation search"
            onClick={() => {
              onChange({ ...browse, query: "" });
              document.getElementById(searchId)?.focus();
            }}
            type="button"
          >
            <X size={14} aria-hidden />
          </button>
        ) : null}
      </label>
      {browse.view === "agents" || browse.view === "archived" ? (
        <div className="workspace-chat-navigator__view-scope">
          <button
            className="workspace-chat-navigator__all-agents"
            onClick={() =>
              onChange({ view: "recent", agentId: null, query: "" })
            }
            type="button"
          >
            <ArrowLeft size={14} aria-hidden />
            All chats
          </button>
          <span>
            {browse.view === "archived" ? "Archived chats" : "By agent"}
          </span>
        </div>
      ) : null}
      {includeHidden ? (
        <p className="workspace-chat-navigator__status">
          Hidden chats included
        </p>
      ) : null}
      {selectedAgent ? (
        <div className="workspace-chat-navigator__agent-scope">
          <button
            className="workspace-chat-navigator__all-agents"
            onClick={() =>
              onChange({ view: "agents", agentId: null, query: "" })
            }
            type="button"
          >
            <ArrowLeft size={14} aria-hidden />
            All agents
          </button>
          <div className="workspace-chat-navigator__selected-agent">
            <div>
              <strong>{selectedAgent.name}</strong>
              <span>{selectedAgent.title}</span>
            </div>
            {selectedAgent.canStartChat ? (
              <button
                aria-label={"New chat with " + selectedAgent.name}
                className="workspace-chat-navigator__agent-new"
                onClick={() => onNewAgentChat(selectedAgent.id)}
                title={"New chat with " + selectedAgent.name}
                type="button"
              >
                <PencilLine size={16} aria-hidden />
                <span>New chat</span>
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function ChatNavigatorAgents({
  agents,
  avatarMotionPaused,
  counts,
  onSelect,
}: {
  agents: ChatNavigatorAgent[];
  avatarMotionPaused: boolean;
  counts: ReadonlyMap<string, number>;
  onSelect: (agentId: string) => void;
}) {
  return (
    <ul className="workspace-chat-navigator__agents" aria-label="Agents">
      {agents.map((agent) => {
        const count = counts.get(agent.id) ?? 0;
        return (
          <li key={agent.id}>
            <button
              className="workspace-chat-navigator__agent"
              onClick={() => onSelect(agent.id)}
              title={`${agent.name} · ${agent.title}`}
              type="button"
            >
              <FoldAvatar
                agentId={agent.id}
                motion="still"
                size={22}
                paused={avatarMotionPaused}
              />
              <span className="workspace-chat-navigator__agent-copy">
                <strong>{agent.name}</strong>
                {agent.archived ? <span>Archived</span> : null}
              </span>
              <span className="workspace-chat-navigator__agent-count">
                {count} {count === 1 ? "chat" : "chats"}
              </span>
              <ChevronRight size={13} aria-hidden />
            </button>
          </li>
        );
      })}
    </ul>
  );
}
