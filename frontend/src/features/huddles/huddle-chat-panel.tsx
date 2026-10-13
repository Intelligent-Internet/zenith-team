import { MessageSquareText, Send } from "@/shared/ui/icons";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { DiscussionAvatar } from "@/features/discussions/components/discussion-identity";
import { DiscussionMessageBody } from "@/features/discussions/components/discussion-message-body";
import { ChatComposerFormatting } from "@/features/chat-panel/components/chat-composer-formatting";
import { ChatComposerEmoji } from "@/features/chat-panel/components/chat-composer-emoji";
import "@/features/discussions/styles/discussions.css";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { HuddleTranscriptMessage } from "@/features/discussions/components/huddle-transcript-message";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import {
  boardlyRealtimeClient,
  realtimeEventConversationId,
} from "@/shared/realtime/realtime-client.service";
import {
  useGetHuddleChatQuery,
  useSendHuddleChatMutation,
} from "./huddle-ai-api";

type HuddleChatPanelProps = { companyId: string; conversationId: string };

export function HuddleChatPanel({
  companyId,
  conversationId,
}: HuddleChatPanelProps) {
  const session = useSessionSnapshot();
  const [admissionWaitExpired, setAdmissionWaitExpired] = useState(false);
  const [admitted, setAdmitted] = useState(false);
  // New messages arrive as conversation.discussion_changed for this chat, so
  // the poll is a fallback, except while waiting for the chat grant.
  const history = useGetHuddleChatQuery(
    { companyId, conversationId, userId: session.userId ?? "" },
    {
      pollingInterval: admitted || admissionWaitExpired ? 15_000 : 2000,
      skip: !session.userId,
    },
  );
  const { refetch } = history;
  const [sendMessage, mutation] = useSendHuddleChatMutation();
  useEffect(() => {
    const timer = window.setTimeout(() => setAdmissionWaitExpired(true), 10000);
    return () => window.clearTimeout(timer);
  }, []);
  useEffect(() => {
    if (history.currentData && !history.isError) setAdmitted(true);
  }, [history.currentData, history.isError]);
  useEffect(() => {
    if (!session.userId) return undefined;
    const removeRealtime = boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === companyId &&
        event.type === "conversation.discussion_changed" &&
        realtimeEventConversationId(event) === conversationId
      )
        void refetch();
    });
    const removeRecovery = boardlyRealtimeClient.onRecovery(
      () => void refetch(),
    );
    return () => {
      removeRealtime();
      removeRecovery();
    };
  }, [companyId, conversationId, refetch, session.userId]);
  const accessError =
    history.isError &&
    "status" in history.error &&
    [401, 403, 404].includes(history.error.status ?? 0);
  const [deniedVersion, setDeniedVersion] = useState<number | null>(null);
  useEffect(() => {
    if (accessError) setDeniedVersion(history.fulfilledTimeStamp ?? 0);
  }, [accessError, history.fulfilledTimeStamp]);
  const denied =
    accessError || deniedVersion === (history.fulfilledTimeStamp ?? 0);
  const hasHistory = Boolean(history.currentData) && !denied;
  // Media can connect before the signed webhook grants access to chat.
  // Poll during that bounded admission window instead of flashing an error.
  const connecting = !hasHistory && !admissionWaitExpired;
  const unavailable = !hasHistory && admissionWaitExpired && history.isError;
  const canSend = hasHistory && !history.isError;
  const messages = denied
    ? []
    : [...(history.currentData?.items ?? [])].reverse();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const pending = useRef<{ text: string; key: string } | null>(null);
  const timeline = useRef<HTMLDivElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const followLatest = useRef(true);
  const latestMessageId = messages.at(-1)?.id;
  useLayoutEffect(() => {
    if (!textarea.current) return;
    textarea.current.style.height = "auto";
    textarea.current.style.height = `${Math.min(textarea.current.scrollHeight, 180)}px`;
  }, [draft]);
  useLayoutEffect(() => {
    if (followLatest.current && timeline.current)
      timeline.current.scrollTop = timeline.current.scrollHeight;
  }, [latestMessageId]);
  const busy = mutation.isLoading;
  const send = async () => {
    const markdown = draft.trim();
    if (!markdown || busy || !canSend || draft.length > 4000) return;
    if (pending.current?.text !== markdown)
      pending.current = { text: markdown, key: crypto.randomUUID() };
    setError("");
    try {
      await sendMessage({
        companyId,
        conversationId,
        body: {
          requestKey: pending.current.key,
          body: { version: 1, markdown, references: [] },
          attachmentIds: [],
          artifactIds: [],
        },
      }).unwrap();
      setDraft((current) => (current.trim() === markdown ? "" : current));
      followLatest.current = true;
      pending.current = null;
      void history.refetch();
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
  };

  return (
    <div className="huddle-chat" aria-label="Huddle chat">
      <div
        className="flex-1 min-h-0 p-[8px_0_16px] overflow-y-auto overscroll-contain"
        ref={timeline}
        onScroll={(event) => {
          const list = event.currentTarget;
          followLatest.current =
            list.scrollHeight - list.scrollTop - list.clientHeight < 48;
        }}
      >
        {unavailable && (
          <div
            className="huddle-chat-status flex items-center justify-between gap-[12px] p-[12px_16px] text-[var(--discussion-muted)] text-[length:var(--workspace-text-caption,_12px)]"
            role="alert"
          >
            <span>Couldn’t connect to chat.</span>
            <button
              type="button"
              disabled={history.isFetching}
              onClick={() => void history.refetch()}
            >
              {history.isFetching ? "Retrying…" : "Retry"}
            </button>
          </div>
        )}
        {hasHistory && history.isError && (
          <div
            className="huddle-chat-status flex items-center justify-between gap-[12px] p-[12px_16px] text-[var(--discussion-muted)] text-[length:var(--workspace-text-caption,_12px)]"
            role="status"
          >
            Reconnecting to chat…
          </div>
        )}
        {hasHistory && !history.isError && messages.length === 0 && (
          <div className="huddle-chat-empty flex flex-col items-start justify-start h-full min-h-[160px] p-[20px] text-[var(--muted-foreground)] text-left">
            <MessageSquareText size={28} />
            <h3>Keep the conversation going</h3>
            <p>Share a thought, a link, or a quick note with everyone here.</p>
          </div>
        )}
        {(connecting || (!hasHistory && !history.isError)) && (
          <div
            className="p-[16px]"
            role="status"
            aria-label="Connecting to chat"
          >
            {[0, 1, 2].map((row) => (
              <div
                className="huddle-chat-placeholder flex gap-[12px] mb-[24px]"
                aria-hidden="true"
                key={row}
              >
                <span />
                <div>
                  <span />
                  <span />
                  <span />
                </div>
              </div>
            ))}
          </div>
        )}
        {messages.map((message, index) => {
          const previous = messages[index - 1];
          const compact =
            !message.system &&
            previous &&
            !previous.system &&
            message.author.id != null &&
            message.author.id === previous.author.id &&
            message.author.type === previous.author.type &&
            new Date(message.createdAt).toDateString() ===
              new Date(previous.createdAt).toDateString() &&
            Date.parse(message.createdAt) - Date.parse(previous.createdAt) <
              5 * 60 * 1000;
          return message.system?.action === "huddle_transcript" ? (
            <HuddleTranscriptMessage key={message.id} message={message} />
          ) : (
            <article
              className={`discussion-message${compact ? " discussion-message--compact" : ""}`}
              key={message.id}
              aria-label={`${message.author.displayName}, ${new Date(message.createdAt).toLocaleString()}`}
            >
              <div className="discussion-message-avatar w-[36px] shrink-0">
                {!compact && (
                  <DiscussionAvatar
                    name={message.author.displayName}
                    src={message.author.avatarUrl}
                    agentId={
                      message.author.type === "agent"
                        ? (message.author.id ?? undefined)
                        : undefined
                    }
                    size={36}
                  />
                )}
              </div>
              <div className="discussion-message-main min-w-0 flex-1 text-[length:var(--workspace-text-chat,_15px)] leading-[var(--workspace-leading-chat,_24px)]">
                {!compact && (
                  <header>
                    <strong>{message.author.displayName}</strong>
                    <time
                      dateTime={message.createdAt}
                      title={new Date(message.createdAt).toLocaleString()}
                    >
                      {new Date(message.createdAt).toLocaleTimeString([], {
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </time>
                  </header>
                )}
                {message.deletedAt ? (
                  <p>This message was deleted.</p>
                ) : (
                  <DiscussionMessageBody body={message.body} />
                )}
              </div>
            </article>
          );
        })}
      </div>
      <div className="discussion-compose-area">
        {error && (
          <p
            className="huddle-chat-status flex items-center justify-between gap-[12px] p-[12px_16px] text-[var(--discussion-muted)] text-[length:var(--workspace-text-caption,_12px)]"
            role="alert"
          >
            {error}
          </p>
        )}
        <form
          className="discussion-composer"
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          <ChatComposerFormatting
            disabled={busy || !canSend}
            onDraftChange={setDraft}
            textareaRef={textarea}
          />
          <textarea
            ref={textarea}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder={
              canSend
                ? "Message Huddle"
                : unavailable
                  ? "Chat unavailable"
                  : "Connecting to chat…"
            }
            aria-label="Message Huddle"
            maxLength={4000}
            rows={2}
            onKeyDown={(event) => {
              if (
                event.key === "Enter" &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault();
                void send();
              }
            }}
          />
          <div className="discussion-composer-tools flex items-center gap-[8px] p-[4px_12px_8px]">
            <div>
              <ChatComposerEmoji
                textareaRef={textarea}
                onDraftChange={setDraft}
                disabled={busy || !canSend}
              />
            </div>
            <button
              type="submit"
              disabled={
                busy || !canSend || !draft.trim() || draft.length > 4000
              }
              className="discussion-send"
              aria-label="Send Huddle message"
              aria-busy={busy}
            >
              <span>{busy ? "Sending…" : "Send"}</span>
              <Send size={15} />
            </button>
          </div>
        </form>
        {draft.length > 4000 && (
          <p role="alert">Keep your message under 4,000 characters.</p>
        )}
      </div>
    </div>
  );
}
