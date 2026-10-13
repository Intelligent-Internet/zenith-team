import {
  createContext,
  useCallback,
  useContext,
  useId,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  FoldAvatar,
  type FoldAvatarProps,
} from "@/shared/ui/agent-avatar/fold-avatar";
import "../styles/chat-agent-avatar.css";

const Presence = createContext({
  hasForeground: false,
  paused: false,
  setVisible: (_id: string, _visible: boolean) => {},
});

export function ChatAvatarPresence({
  children,
  paused = false,
}: {
  children: ReactNode;
  paused?: boolean;
}) {
  const [visible, setVisible] = useState<ReadonlySet<string>>(() => new Set());
  const register = useCallback((id: string, shown: boolean) => {
    setVisible((current) => {
      if (current.has(id) === shown) return current;
      const next = new Set(current);
      if (shown) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  const hasForeground = visible.size > 0;
  const presence = useMemo(
    () => ({ hasForeground, paused, setVisible: register }),
    [hasForeground, paused, register],
  );
  return <Presence.Provider value={presence}>{children}</Presence.Provider>;
}

export function ChatAgentAvatar({
  priority = "header",
  ...props
}: Omit<
  FoldAvatarProps,
  "motion" | "motionIntensity" | "onVisibilityChange"
> & {
  priority?: "header" | "foreground";
}) {
  const id = useId();
  const { hasForeground, paused, setVisible } = useContext(Presence);
  const onVisibilityChange = useCallback(
    (visible: boolean) => setVisible(id, visible),
    [id, setVisible],
  );
  return (
    <FoldAvatar
      {...props}
      paused={paused || props.paused}
      motion="expressive"
      motionIntensity={
        priority === "header" && hasForeground ? "quiet" : "full"
      }
      onVisibilityChange={
        priority === "foreground" ? onVisibilityChange : undefined
      }
    />
  );
}

export function ChatAgentWelcome({
  agentId,
  name,
  role,
}: {
  agentId: string;
  name: string;
  role?: string;
}) {
  return (
    <div className="chat-agent-welcome flex flex-1 min-h-[240px] flex-col items-center justify-center p-[32px_20px_48px] text-center text-[var(--workspace-native-text,_currentColor)]">
      <ChatAgentAvatar agentId={agentId} priority="foreground" size={88} />
      <h2>Chat with {name}</h2>
      {role ? <p className="chat-agent-welcome__role">{role}</p> : null}
      <p>Explore an idea, ask a question, or plan your next step.</p>
    </div>
  );
}

export function ChatAgentAttention({
  agentId,
  name,
}: {
  agentId: string;
  name: string;
}) {
  return (
    <div className="flex items-center gap-[8px] mb-[8px] text-[length:13px] font-[500] text-[var(--workspace-native-text-muted,_#7a8391)]">
      <ChatAgentAvatar
        agentId={agentId}
        priority="foreground"
        size={32}
        state="needs-input"
      />
      <span>{name} needs your input</span>
    </div>
  );
}
