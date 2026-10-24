import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  ArrowLeft,
  X,
  LoaderCircle,
  LockKeyhole,
  MessageSquareText,
} from "@/shared/ui/icons";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Streamdown } from "streamdown";
import { DiscussionMessageBody } from "./discussion-message-body";
import type { components } from "@/shared/api/generated/api.generated";
import { discussionsApi } from "../api/discussions-api";
import { useDiscussionUpdates } from "./discussion-provider";
import { discussionError } from "../model/discussion-runtime";
import { ChannelInputRequests } from "./channel-input-requests";

type Agent = components["schemas"]["ChannelAgentStatus"];
type Invocation = components["schemas"]["ChannelInvocation"];
type Selection = {
  agentId: string;
  invocationId?: string;
  threadRootId?: string;
};
type ChannelAgentsState = {
  agents: Agent[];
  items: Invocation[];
  selected?: Selection;
  open: (agentId: string, invocation?: Invocation) => void;
  close: () => void;
  refresh: () => void;
  retry: (item: Invocation) => Promise<void>;
  retryingId: string | null;
  retryErrors: Record<string, string>;
};
const ChannelAgentsContext = createContext<ChannelAgentsState>({
  agents: [],
  items: [],
  open: () => {},
  close: () => {},
  refresh: () => {},
  retry: async () => {},
  retryingId: null,
  retryErrors: {},
});
export const useChannelAgents = () => useContext(ChannelAgentsContext);

export function ChannelAgentsProvider({
  conversationId,
  enabled,
  children,
}: {
  conversationId: string;
  enabled: boolean;
  children: ReactNode;
}) {
  const runtime = useDiscussionUpdates();
  const args = { ...runtime.session, conversationId };
  const query = discussionsApi.useGetChannelInvocationsQuery(args, {
    skip: !enabled,
  });
  const refetchInvocations = query.refetch;
  const [retryInvocation] = discussionsApi.useRetryChannelInvocationMutation();
  const requestKeys = useRef(new Map<string, string>());
  const retryInFlight = useRef(false);
  const [retryingId, setRetryingId] = useState<string | null>(null);
  const [retryErrors, setRetryErrors] = useState<Record<string, string>>({});
  const retry = useCallback(
    async (item: Invocation) => {
      if (!enabled || !item.canRetry || retryInFlight.current) return;
      retryInFlight.current = true;
      setRetryingId(item.id);
      setRetryErrors((current) => ({ ...current, [item.id]: "" }));
      const requestKey =
        requestKeys.current.get(item.id) ?? crypto.randomUUID();
      requestKeys.current.set(item.id, requestKey);
      try {
        await retryInvocation({
          ...runtime.session,
          conversationId,
          invocationId: item.id,
          requestKey,
        }).unwrap();
        requestKeys.current.delete(item.id);
        void refetchInvocations();
      } catch (cause) {
        setRetryErrors((current) => ({
          ...current,
          [item.id]: discussionError(cause),
        }));
      } finally {
        retryInFlight.current = false;
        setRetryingId(null);
      }
    },
    [
      conversationId,
      enabled,
      refetchInvocations,
      retryInvocation,
      runtime.session,
    ],
  );
  const [selected, setSelected] = useState<Selection>();
  const agents = query.isError ? [] : (query.currentData?.agents ?? []);
  const items = query.isError ? [] : (query.currentData?.items ?? []);
  const viewerId = runtime.session.userId;
  const previous = useRef("");
  useEffect(() => {
    const value = JSON.stringify(
      query.currentData?.items.map((item) => [
        item.id,
        item.status,
        item.publicationStatus,
      ]),
    );
    if (value !== previous.current) {
      previous.current = value;
      runtime.schedule();
    }
  }, [query.currentData, runtime]);
  const value = useMemo<ChannelAgentsState>(
    () => ({
      agents,
      items,
      selected: agents.some(
        (agent) => agent.agentId === selected?.agentId && agent.canViewDetail,
      )
        ? selected
        : undefined,
      open: (agentId, invocation) => {
        if (
          !agents.some(
            (agent) => agent.agentId === agentId && agent.canViewDetail,
          )
        )
          return;
        // The panel lists the viewer's own Runs only, so
        // it opens at one of theirs, never at another member's request.
        const own = items.filter(
          (item) => item.agentId === agentId && item.callerUserId === viewerId,
        );
        const focus =
          (invocation?.callerUserId === viewerId ? invocation : undefined) ??
          own.find((item) =>
            ["active", "waiting_input"].includes(item.status),
          ) ??
          own[0];
        setSelected({
          agentId,
          invocationId: focus?.id,
          threadRootId: focus?.threadRootId,
        });
      },
      close: () => setSelected(undefined),
      refresh: () => {
        if (enabled) void query.refetch();
      },
      retry,
      retryingId,
      retryErrors,
    }),
    [
      agents,
      items,
      selected,
      enabled,
      viewerId,
      query.refetch,
      retry,
      retryingId,
      retryErrors,
    ],
  );
  return (
    <ChannelAgentsContext.Provider value={value}>
      {children}
    </ChannelAgentsContext.Provider>
  );
}

function invocationLabel(item: Invocation) {
  if (item.status === "waiting_input") return "Waiting for input";
  if (
    item.status === "completed" &&
    item.failureReason === "final_answer_too_large"
  )
    return "Completed · reply too long";
  if (item.status === "completed")
    return item.publicationStatus === "published"
      ? "Completed"
      : item.publicationStatus === "no_output"
        ? "Completed · no final answer"
        : "Completed · reply withheld";
  if (item.status === "cancelled")
    return "Cancelled · access or execution ended";
  if (item.status === "failed") return "Failed";
  if (item.runStatus === "running") return "Running";
  if (item.runStatus === "dispatched") return "Starting";
  if (item.runStatus === "deferred") return "Retrying";
  if (item.runStatus === "waiting_local_directory")
    return "Waiting for workspace";
  // A tag runs on its tagger's own computer (ADR 0014); the roster's
  // runtimeStatus is the viewer's, so only the tag's own Run can say it
  // waits, and it says so to the tagger alone — everyone else reads null.
  if (item.runtimeStatus === "offline") return "Waiting for device";
  return "Queued";
}

export function channelAgentActivity(agent: Agent, items: Invocation[]) {
  const current = items.find(
    (item) =>
      item.agentId === agent.agentId &&
      ["active", "waiting_input"].includes(item.status),
  );
  const queued = items.filter(
    (item) => item.agentId === agent.agentId && item.status === "queued",
  ).length;
  return {
    current,
    queued,
    label: current
      ? invocationLabel(current)
      : agent.runtimeStatus === "offline"
        ? // The status is the viewer's own: without a setup of the Agent they
          // cannot tag it, so canInvoke tells the two offline cases apart. An
          // archived chat reads as not set up, where nobody can tag anyway.
          agent.canInvoke
          ? "Computer offline"
          : "Not set up on your computer"
        : queued
          ? "Queued"
          : "Idle",
  };
}

export function ChannelInvocationStatus({
  messageId,
}: {
  messageId: string;
  conversationId: string;
}) {
  const state = useChannelAgents();
  const runtime = useDiscussionUpdates();
  const items = state.items.filter(
    (item) =>
      item.triggerMessageId === messageId &&
      !(item.status === "completed" && item.publicationStatus === "published"),
  );
  if (!items.length) return null;
  return (
    <div
      className="channel-invocation-status mt-[5px] text-[var(--discussion-muted)] text-[length:11px]"
      aria-label="Agent requests"
    >
      {items.map((item) => {
        const agent = state.agents.find(
          (agent) => agent.agentId === item.agentId,
        );
        const label = `${agent?.name ?? "Agent"} · ${invocationLabel(item)}`;
        // Only the member who tagged the Agent opens its Run's activity;
        // another member's request is plain text.
        const own = item.callerUserId === runtime.session.userId;
        return (
          <div key={item.id} data-status={item.status}>
            {agent?.canViewDetail && own ? (
              <button
                className="discussion-thread-link"
                onClick={() => state.open(item.agentId, item)}
              >
                <FoldAvatar agentId={item.agentId} size={18} />
                {["active", "queued"].includes(item.status) &&
                item.runtimeStatus !== "offline" ? (
                  <LoaderCircle size={12} className="animate-spin" />
                ) : null}
                {label}
              </button>
            ) : (
              <span>{label}</span>
            )}
            <ChannelInvocationRetry item={item} />
          </div>
        );
      })}
      <ChannelInputRequests
        ids={items.flatMap((item) => item.inputRequestIds ?? [])}
        onResolved={state.refresh}
      />
    </div>
  );
}

function ChannelInvocationRetry({ item }: { item: Invocation }) {
  const state = useChannelAgents();
  if (!item.canRetry) return null;
  const error = state.retryErrors[item.id];
  return (
    <span className="inline-flex min-w-0 flex-col items-start gap-1">
      <button
        type="button"
        className="discussion-thread-link"
        disabled={state.retryingId !== null}
        onClick={() => void state.retry(item)}
      >
        {state.retryingId === item.id ? "Retrying…" : "Retry"}
      </button>
      {error ? <span role="alert">{error}</span> : null}
    </span>
  );
}

export function ChannelAgentPanel({
  conversationId,
  conversationTitle,
  onBack,
}: {
  conversationId: string;
  conversationTitle: string;
  onBack: (threadRootId?: string) => void;
}) {
  const state = useChannelAgents();
  const runtime = useDiscussionUpdates();
  const selected = state.selected;
  const agent = state.agents.find(
    (agent) => agent.agentId === selected?.agentId,
  );
  const activity = discussionsApi.useGetChannelAgentActivityQuery(
    { ...runtime.session, conversationId, agentId: selected?.agentId ?? "" },
    {
      skip: !selected || !agent?.canViewDetail,
    },
  );
  const panel = useRef<HTMLElement>(null);
  const focused = useRef("");
  useEffect(() => {
    if (
      !selected?.invocationId ||
      focused.current === selected.invocationId ||
      !activity.currentData
    )
      return;
    panel.current
      ?.querySelector(`[data-invocation-id="${selected.invocationId}"]`)
      ?.scrollIntoView({ block: "start" });
    focused.current = selected.invocationId;
  }, [selected?.invocationId, activity.currentData]);
  if (!selected || !agent) return null;
  const invocations = state.items
    .filter(
      (item) =>
        item.agentId === agent.agentId &&
        item.callerUserId === runtime.session.userId,
    )
    .slice()
    .reverse();
  return (
    <aside
      ref={panel}
      className="discussion-thread-panel channel-agent-panel"
      aria-label={`${agent.name} activity`}
    >
      <header className="discussion-thread-header">
        <button
          className="discussion-icon-button"
          aria-label="Back to thread"
          onClick={() => {
            onBack(selected.threadRootId);
            state.close();
          }}
        >
          <ArrowLeft size={18} />
        </button>
        <FoldAvatar agentId={agent.agentId} size={30} />
        <div className="channel-agent-panel-heading">
          <h3>{agent.name}</h3>
          <small>Your runs in {conversationTitle}</small>
        </div>
        <button
          className="discussion-icon-button"
          aria-label="Close Agent activity"
          onClick={state.close}
        >
          <X size={18} />
        </button>
      </header>
      <div className="channel-agent-privacy flex shrink-0 items-center gap-[6px] p-[10px_var(--workspace-thread-inset,_16px)] text-[var(--discussion-muted)] text-[length:11px] leading-[16px] [border-bottom:1px_solid_var(--discussion-border)]">
        <LockKeyhole size={13} />
        <span>Only you can see your runs · Managed by {agent.managerName}</span>
      </div>
      <div className="channel-agent-activity flex-1 min-h-0 overflow-auto p-[18px_var(--workspace-thread-inset,_16px)]">
        {activity.isError ? (
          <p role="alert">{discussionError(activity.error)}</p>
        ) : !activity.currentData ? (
          <p>Loading activity…</p>
        ) : invocations.length === 0 ? (
          <div className="channel-agent-empty flex flex-col items-center justify-center min-h-[260px] gap-[12px] text-center">
            <FoldAvatar agentId={agent.agentId} size={56} />
            <h3>Work with {agent.name}</h3>
            <p>
              Tag @{agent.name} in the conversation or a thread.
              <br />
              Their activity appears here and their reply goes to the thread.
            </p>
          </div>
        ) : (
          invocations.map((item) => (
            <section key={item.id} data-invocation-id={item.id}>
              <header>
                <span>{invocationLabel(item)}</span>
                <div className="flex items-center gap-3">
                  <ChannelInvocationRetry item={item} />
                  <button
                    className="discussion-thread-link"
                    onClick={() => {
                      onBack(item.threadRootId);
                      state.close();
                    }}
                  >
                    <MessageSquareText size={13} />
                    Thread
                  </button>
                </div>
              </header>
              {activity.currentData?.items
                .filter((entry) => entry.invocationId === item.id)
                .map((entry) =>
                  entry.parts.some(
                    (part) =>
                      part.type === "toolCall" || part.type === "toolResult",
                  ) ? (
                    <details
                      className="channel-agent-tool mt-[10px] text-[var(--discussion-muted)] text-[length:11px]"
                      key={entry.id}
                    >
                      <summary>
                        {entry.parts.find(
                          (part) =>
                            part.type === "toolCall" ||
                            part.type === "toolResult",
                        )?.name ??
                          (entry.type === "tool_call"
                            ? "Using tool"
                            : "Tool result")}
                      </summary>
                      <pre>
                        {entry.parts
                          .filter(
                            (part) =>
                              part.type === "toolCall" ||
                              part.type === "toolResult",
                          )
                          .map((part) =>
                            part.type === "toolCall"
                              ? `${part.name}\n${JSON.stringify(part.input, null, 2)}`
                              : part.type === "toolResult"
                                ? `${part.name ?? "Result"}\n${typeof part.output === "string" ? part.output : JSON.stringify(part.output, null, 2)}`
                                : "",
                          )
                          .join("\n\n") || entry.content}
                      </pre>
                    </details>
                  ) : (
                    <div
                      className={`channel-agent-activity-item channel-agent-activity-item--${entry.type}`}
                      key={entry.id}
                    >
                      <header>
                        <strong>
                          {entry.type === "user_message"
                            ? "Request"
                            : agent.name}
                        </strong>
                        <time>
                          {new Date(entry.createdAt).toLocaleTimeString([], {
                            hour: "2-digit",
                            minute: "2-digit",
                          })}
                        </time>
                      </header>
                      {entry.type === "user_message" ? (
                        <Streamdown
                          className="discussion-markdown text-[length:var(--workspace-text-chat,_15px)] leading-[var(--workspace-leading-chat,_24px)] wrap-anywhere min-w-0"
                          mode="static"
                          controls={false}
                          parseIncompleteMarkdown={false}
                          remarkRehypeOptions={{ allowDangerousHtml: false }}
                          components={{
                            img: () => null,
                            a: ({ href, children }) =>
                              /^https?:\/\//i.test(href ?? "") ? (
                                <a
                                  href={href}
                                  target="_blank"
                                  rel="noopener noreferrer"
                                  referrerPolicy="no-referrer"
                                >
                                  {children}
                                </a>
                              ) : (
                                <span>{children}</span>
                              ),
                          }}
                        >
                          {entry.content}
                        </Streamdown>
                      ) : (
                        <DiscussionMessageBody
                          agent
                          body={{
                            version: 1,
                            markdown: entry.content,
                            references: [],
                          }}
                        />
                      )}
                    </div>
                  ),
                )}
            </section>
          ))
        )}
      </div>
    </aside>
  );
}
