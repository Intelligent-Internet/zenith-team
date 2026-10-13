import { useStartWorkspaceChatSessionMutation } from "@/shared/api/conversation-api";
import {
  ArrowRight,
  ArrowUp,
  Bot,
  CircleHelp,
  Lightbulb,
  ListTodo,
  LockKeyhole,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState } from "react";
import {
  isAgentBindingRequired,
  sendWithAgentSetup,
} from "@/features/agents/model/agent-binding";

import { Link } from "react-router";
import type { ChatWorkspaceAgent } from "./active-conversation-pane";
import { NewChatModelSettings } from "./new-chat-model-settings";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

export function NewChatSessionPreview({
  agent,
  companyId,
  onSetUpAgent,
  initialDraft,
  onStarted,
}: {
  agent: ChatWorkspaceAgent;
  companyId: string;
  // Opens the setup of this Agent on the viewer's own computer and answers
  // whether they finished it (ADR 0014).
  onSetUpAgent: (agentId: string) => Promise<boolean>;
  /** Text the link that opened this chat put in the composer; never sent. */
  initialDraft?: string;
  onStarted: (conversationId: string) => void;
}) {
  const [startSession, mutation] = useStartWorkspaceChatSessionMutation();
  const [requestKey] = useState(() => crypto.randomUUID());
  const message = useRef<{ key: string; content: string } | null>(null);
  const [modelSaving, setModelSaving] = useState(false);
  const [error, setError] = useState<string>();
  // Not set up is known from the roster, or learned when the server refused
  // a send; an Agent seen only through an old chat reads as set up.
  const [refused, setRefused] = useState(false);
  const needsSetup = agent.bound === false || refused;
  async function setUp() {
    const bound = await onSetUpAgent(agent.id);
    if (bound) setRefused(false);
    return bound;
  }
  async function send() {
    const content = draft.trim();
    if (!content || mutation.isLoading || modelSaving) return;
    if (message.current?.content !== content)
      message.current = { key: crypto.randomUUID(), content };
    const messageKey = message.current.key;
    setError(undefined);
    try {
      // The chat runs on the viewer's own computer, so an Agent they have
      // not set up is set up first — or on the server's refusal — and the
      // same words go once it is. The keys stay the same across the detour:
      // the refused send wrote nothing, and the session is reused.
      const conversation = await sendWithAgentSetup(
        () =>
          startSession({
            companyId,
            agentId: agent.id,
            requestKey,
            messageKey,
            content,
          }).unwrap(),
        { bound: !needsSetup, name: agent.name },
        setUp,
      );
      onStarted(conversation.id);
    } catch (error) {
      // The setup was closed unfinished: the notice below says why nothing
      // was sent, and the draft is still in the box.
      if (isAgentBindingRequired(error)) {
        setRefused(true);
        return;
      }
      setError(
        error instanceof Error
          ? error.message
          : "The chat could not be started. Try again.",
      );
    }
  }
  const [draft, setDraft] = useState(initialDraft ?? "");
  const composer = useRef<HTMLTextAreaElement>(null);
  // A prefilled draft is ready to finish: focus it with the caret at the end.
  useEffect(() => {
    const textarea = composer.current;
    if (!initialDraft || !textarea) return;
    textarea.focus({ preventScroll: true });
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);
  }, [initialDraft]);
  const starters = [
    "What can you help me with?",
    `Suggest a first task for ${agent.title.toLowerCase()}.`,
    "Help me plan my next steps.",
  ];
  return (
    <section
      className="new-chat-session"
      aria-label={`New chat with ${agent.name}`}
    >
      <header>
        <FoldAvatar agentId={agent.id} size={40} motion="expressive" paused />
        <div>
          <strong>New chat</strong>
          <small>
            {agent.name} · {agent.title} · <LockKeyhole size={11} /> Private to
            you
          </small>
        </div>
        <Link to={`/agents/${agent.id}`} className="work-view-selector">
          <Bot size={14} />
          Agent profile
        </Link>
      </header>
      <div className="new-chat-session-welcome">
        <FoldAvatar
          agentId={agent.id}
          size={88}
          motion="expressive"
          state={mutation.isLoading ? "thinking" : "idle"}
        />
        <h1>Chat with {agent.name}</h1>
        <p>{agent.title}</p>
        <p>Explore an idea, plan your day, or hand off a task.</p>
        <div className="new-chat-session-starters">
          {starters.map((starter, index) => (
            <button
              key={starter}
              type="button"
              onClick={() => {
                setDraft(starter);
                composer.current?.focus();
              }}
            >
              {index === 0 ? (
                <CircleHelp size={15} />
              ) : index === 1 ? (
                <ListTodo size={15} />
              ) : (
                <Lightbulb size={15} />
              )}
              <span>{starter}</span>
              <ArrowRight size={15} />
            </button>
          ))}
        </div>
      </div>
      <div className="new-chat-session-composer">
        {error ? <p role="alert">{error}</p> : null}
        {needsSetup ? (
          <p role="note">
            {agent.name} runs on your own computer and is not set up there yet.{" "}
            <button onClick={() => void setUp()} type="button">
              Set up {agent.name} on your computer
            </button>
          </p>
        ) : null}
        <label className="sr-only" htmlFor="new-session-message">
          Message {agent.name}
        </label>
        <div>
          <textarea
            id="new-session-message"
            ref={composer}
            disabled={mutation.isLoading}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (
                event.key !== "Enter" ||
                event.shiftKey ||
                event.nativeEvent.isComposing ||
                event.nativeEvent.keyCode === 229
              )
                return;
              event.preventDefault();
              if (!event.repeat) void send();
            }}
            placeholder={`Message ${agent.name}…`}
            rows={3}
          />
          <div className="new-chat-session-model-controls">
            <NewChatModelSettings
              companyId={companyId}
              agentId={agent.id}
              requestKey={requestKey}
              disabled={mutation.isLoading}
              onSavingChange={setModelSaving}
            />
          </div>
          <button
            className="new-chat-session-send workspace-chat-send-button"
            disabled={mutation.isLoading || modelSaving || !draft.trim()}
            onClick={() => void send()}
            aria-label="Send message in a new chat"
            title="Send message in a new chat"
            type="button"
          >
            <ArrowUp size={18} />
          </button>
        </div>
      </div>
    </section>
  );
}
