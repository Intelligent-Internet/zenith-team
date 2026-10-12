import { ChatAgentAvatar } from "./chat-agent-avatar";

export function ChatThinkingIndicator({ agentId }: { agentId?: string }) {
  return (
    <div
      aria-label="Agent is thinking"
      className="chat-runtime-thinking"
      role="status"
    >
      {agentId ? (
        <ChatAgentAvatar
          agentId={agentId}
          priority="foreground"
          size={32}
          state="thinking"
        />
      ) : null}
      <span className="chat-runtime-thinking__text">
        <span className="chat-runtime-thinking__label">Thinking</span>
        <span aria-hidden="true" className="chat-runtime-thinking__dots">
          <span>.</span>
          <span>.</span>
          <span>.</span>
        </span>
      </span>
    </div>
  );
}
