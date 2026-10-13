import { ChatAgentAvatar } from "./chat-agent-avatar";

export function ChatThinkingIndicator({ agentId }: { agentId?: string }) {
  return (
    <div
      aria-label="Agent is thinking"
      className="flex items-center gap-[8px] min-h-[32px] p-[2px_0] text-workspace-text text-[length:var(--workspace-text-body,_13px)] font-[500] leading-[20px]"
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
