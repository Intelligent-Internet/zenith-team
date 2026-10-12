import { ChatRunArtifacts } from "./artifacts/chat-run-artifacts";
import type { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import type { ConversationArtifactReference } from "@/features/chat-panel/model/conversation-artifact";
import {
  Fragment,
  type ReactNode,
  type RefObject,
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  type StickToBottomContext,
  useStickToBottomContext,
} from "use-stick-to-bottom";
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/shared/ui/core/ai-elements/conversation";
import { ChatMessageBubble } from "@/features/chat-panel/components/chat-message-bubble";
import { ChatThinkingIndicator } from "./chat-thinking-indicator";
import { ChatRunToggle } from "@/features/chat-panel/components/chat-run-group";
import type { ChatWorkspaceFileSelection } from "../model/chat-file-source";
import type { ChatCompanyPlanOverviewProps } from "@/features/chat-panel/components/chat-company-plan-dialog";
import type { DiscoveryOtherOptionSelection } from "@/features/chat-panel/components/structured-chat-message";
import type {
  ChatConversationDecorator,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import type { ChatMessage } from "@/shared/chat/types";
import {
  buildChatConversationRenderItems,
  chatRunWaitingMessage,
  type ChatRunDisplayStatus,
  type ChatTurnActivity,
} from "@/features/chat-panel/model/chat-run-groups";
import { cx } from "@/shared/lib/cx";
import { Clock3 } from "@/shared/ui/icons";

const chatPanelLayoutTransitionDurationMs = 350;

function runExpansionStatus(status: ChatRunDisplayStatus) {
  return status === "needs_input" ? "working" : status;
}

type ChatConversationProps = {
  artifacts?: ReturnType<typeof useConversationArtifacts>;
  onArtifactSelect?: (artifact: ConversationArtifactReference) => void;
  emptyState?: ReactNode;
  conversationDecorators?: ChatConversationDecorator[];
  conversationSlot?: ReactNode;
  conversationSlotClassName?: string;
  conversationTimelineItems?: Array<{
    afterMessageId?: number;
    beforeMessageId?: number;
    className?: string;
    content: ReactNode;
    isPending?: boolean;
    key: string;
  }>;
  focusedMessageId?: number | null;
  hasOlderMessages?: boolean;
  hasStreamingMessage?: boolean;
  historyLoadError?: string | null;
  isExpanded: boolean;
  isLoadingOlderMessages?: boolean;
  messageScrollRef: RefObject<HTMLDivElement | null>;
  messages: ChatMessage[];
  readOnly?: boolean;
  scrollToBottomRequestKey?: number;
  showUserIdentity?: boolean;
  showRunThinking?: boolean;
  thinkingAgentId?: string;
  turnActivity?: ChatTurnActivity;
  submittedDiscoveryPlanMessageIds?: ReadonlySet<number>;
  onDiscoveryOtherOptionSelectionChange?: (
    selection: DiscoveryOtherOptionSelection,
  ) => void;
  onDiscoveryPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  onDiscoveryOptionSubmit?: (message: string) => void;
  onGeneratedFileSelect?: (selection: ChatWorkspaceFileSelection) => void;
  onUploadedFileSelect?: (
    file: NonNullable<ChatMessage["files"]>[number],
  ) => void;
  onLoadOlderMessages?: () => Promise<void>;
  onRenderCompanyPlanOverview?: (
    message: ChatMessage,
  ) => Omit<ChatCompanyPlanOverviewProps, "plan"> | null;
};

function ChatConversationComponent({
  artifacts,
  onArtifactSelect,
  emptyState,
  conversationDecorators = [],
  conversationSlot,
  conversationSlotClassName,
  conversationTimelineItems = [],
  focusedMessageId,
  hasOlderMessages = false,
  hasStreamingMessage = false,
  historyLoadError,
  isExpanded,
  isLoadingOlderMessages = false,
  messageScrollRef,
  messages,
  readOnly = false,
  scrollToBottomRequestKey = 0,
  showUserIdentity = false,
  showRunThinking = false,
  thinkingAgentId,
  turnActivity,
  onDiscoveryOtherOptionSelectionChange,
  onDiscoveryPlanHandoff,
  onDiscoveryOptionSubmit,
  onGeneratedFileSelect,
  onUploadedFileSelect,
  onLoadOlderMessages,
  onRenderCompanyPlanOverview,
  submittedDiscoveryPlanMessageIds,
}: ChatConversationProps) {
  const [initialScrollReadyKey, setInitialScrollReadyKey] = useState<
    string | null
  >(null);
  const initialScrollKey = useMemo(
    () =>
      messages.find((message) => message.conversationId)?.conversationId ??
      null,
    [messages],
  );
  const latestUserQueryKey = useMemo(() => {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];
      if (message.author !== "user") continue;
      return `${message.team}:${message.clientMessageId ?? message.id}`;
    }
    return null;
  }, [messages]);
  const readOnlyMessageIds = useMemo(() => {
    const ids = new Set<number>();
    let hasLaterUserMessage = false;

    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];

      if (message.author === "user") {
        hasLaterUserMessage = true;
        continue;
      }

      if (hasLaterUserMessage) {
        ids.add(message.id);
      }
    }

    return ids;
  }, [messages]);
  const { hasMore, isFetching, error, loadMore } = artifacts ?? {};
  useEffect(() => {
    if (hasMore && !isFetching && !error) void loadMore?.();
  }, [hasMore, isFetching, error, loadMore]);

  const renderItems = useMemo(
    () => buildChatConversationRenderItems(messages, turnActivity),
    [messages, turnActivity],
  );
  const artifactsByTurn = useMemo(() => {
    const lastTurnByRun = new Map<string, string>();
    for (const item of renderItems) {
      if (item.kind !== "run") continue;
      for (const message of item.messages) {
        if (message.runId) lastTurnByRun.set(message.runId, item.id);
      }
    }
    const groups = new Map<string, NonNullable<typeof artifacts>["items"]>();
    for (const artifact of artifacts?.items ?? []) {
      if (!artifact.runId) continue;
      const turnId = lastTurnByRun.get(artifact.runId);
      if (!turnId) continue;
      const group = groups.get(turnId) ?? [];
      group.push(artifact);
      groups.set(turnId, group);
    }
    return groups;
  }, [artifacts?.items, renderItems]);
  const latestRenderItem = renderItems[renderItems.length - 1];
  const thinkingGroupId =
    latestRenderItem?.kind === "run" ? latestRenderItem.id : null;
  const thinkingIndicator = showRunThinking ? (
    <div className="mb-5 w-full min-w-0 max-w-full self-start text-xs font-medium leading-[22px] text-white">
      <ChatThinkingIndicator agentId={thinkingAgentId} />
    </div>
  ) : null;
  const waitingMessage = chatRunWaitingMessage(messages, turnActivity);
  const [runExpansion, setRunExpansion] = useState(() => ({
    conversationId: initialScrollKey,
    userQueryKey: latestUserQueryKey,
    overrides: new Map<
      string,
      { expanded: boolean; status: ChatRunDisplayStatus }
    >(),
  }));
  const scrollContextRef = useRef<StickToBottomContext | null>(null);
  const followStreamRef = useRef(true);
  const runScrollAnchorRef = useRef<{
    section: HTMLElement;
    top: number;
    restoreFocus: boolean;
  } | null>(null);
  useEffect(() => {
    const runStatuses = new Map(
      renderItems.flatMap((item) =>
        item.kind === "run"
          ? [[item.id, runExpansionStatus(item.status)] as const]
          : [],
      ),
    );
    setRunExpansion((current) => {
      if (
        current.conversationId !== initialScrollKey ||
        current.userQueryKey !== latestUserQueryKey
      ) {
        return {
          conversationId: initialScrollKey,
          userQueryKey: latestUserQueryKey,
          overrides: new Map(),
        };
      }
      const overrides = new Map(
        [...current.overrides].filter(
          ([groupId, override]) => runStatuses.get(groupId) === override.status,
        ),
      );
      return overrides.size === current.overrides.size
        ? current
        : { ...current, overrides };
    });
  }, [initialScrollKey, latestUserQueryKey, renderItems]);

  const toggleRunExpanded = useCallback(
    (
      groupId: string,
      status: ChatRunDisplayStatus,
      expanded: boolean,
      trigger: HTMLButtonElement,
    ) => {
      // Inspecting a Run must pause both scroll followers before content resizes.
      followStreamRef.current = false;
      scrollContextRef.current?.stopScroll();
      const section = trigger.closest<HTMLElement>("[data-chat-run-id]");
      runScrollAnchorRef.current = section
        ? {
            section,
            top: trigger.getBoundingClientRect().top,
            restoreFocus: document.activeElement === trigger,
          }
        : null;

      setRunExpansion((current) => {
        const overrides = new Map(
          current.conversationId === initialScrollKey &&
            current.userQueryKey === latestUserQueryKey
            ? current.overrides
            : undefined,
        );
        overrides.set(groupId, {
          expanded: !expanded,
          status: runExpansionStatus(status),
        });
        return {
          conversationId: initialScrollKey,
          userQueryKey: latestUserQueryKey,
          overrides,
        };
      });
    },
    [initialScrollKey, latestUserQueryKey],
  );

  useLayoutEffect(() => {
    const anchor = runScrollAnchorRef.current;
    runScrollAnchorRef.current = null;
    const scrollContext = scrollContextRef.current;
    if (!anchor || !scrollContext) return;

    // The summary and expanded messages remount the toggle. Anchor its replacement
    // before paint, and keep keyboard focus without triggering native scrolling.
    const trigger = anchor.section.querySelector<HTMLButtonElement>(
      ".workspace-chat-run-toggle",
    );
    if (!trigger) return;

    scrollContext.state.scrollTop +=
      trigger.getBoundingClientRect().top - anchor.top;
    if (anchor.restoreFocus) {
      trigger.focus({ preventScroll: true });
    }
  }, [runExpansion]);

  const renderTimelineBefore = (messageId: number) =>
    conversationTimelineItems
      .filter(
        (item) =>
          item.afterMessageId === undefined &&
          item.beforeMessageId === messageId,
      )
      .map((item) => (
        <ConversationTimelineItem
          className={item.className}
          content={item.content}
          key={item.key}
        />
      ));
  const renderTimelineAfter = (messageId: number, includePending = true) =>
    conversationTimelineItems
      .filter(
        (item) =>
          item.afterMessageId === messageId &&
          (includePending || !item.isPending),
      )
      .map((item) => (
        <ConversationTimelineItem
          className={item.className}
          content={item.content}
          key={item.key}
        />
      ));
  const renderDecoratorsAfter = (messageId: number) =>
    conversationDecorators
      .filter((decorator) => decorator.afterMessageId === messageId)
      .map((decorator) => (
        <div
          key={decorator.key}
          className={cx("mt-5 text-white", decorator.className)}
        >
          {decorator.content}
        </div>
      ));
  const renderMessage = (
    message: ChatMessage,
    messageIndex: number,
    {
      hideRuntimeActivities = false,
      runToggle,
    }: {
      hideRuntimeActivities?: boolean;
      runToggle?: ReactNode;
    } = {},
  ) => {
    const isDiscoveryReadOnly = readOnly || readOnlyMessageIds.has(message.id);
    const isDiscoveryPlanSubmitted = Boolean(
      submittedDiscoveryPlanMessageIds?.has(message.id),
    );
    const previousMessage = messages[messageIndex - 1];
    const messageDate = message.createdAt ? new Date(message.createdAt) : null;
    const previousDate = previousMessage?.createdAt
      ? new Date(previousMessage.createdAt)
      : null;
    const startsDay =
      messageDate &&
      !Number.isNaN(messageDate.getTime()) &&
      messageDate.toDateString() !== previousDate?.toDateString();
    const isActorGroupStart =
      Boolean(startsDay) ||
      !previousMessage ||
      !message.actor ||
      !previousMessage.actor ||
      message.actor.id !== previousMessage.actor.id ||
      message.author !== previousMessage.author;

    return (
      <>
        {startsDay && previousMessage ? (
          <div className="workspace-chat-day">
            <time dateTime={message.createdAt}>
              {messageDate.toLocaleDateString(undefined, {
                weekday: "long",
                month: "long",
                day: "numeric",
                year: "numeric",
              })}
            </time>
          </div>
        ) : null}
        <ChatMessageBubble
          hideRuntimeActivities={hideRuntimeActivities}
          hideThinking={showRunThinking}
          isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
          isDiscoveryReadOnly={isDiscoveryReadOnly}
          isActorGroupStart={isActorGroupStart}
          isExpanded={isExpanded}
          isFocused={message.id === focusedMessageId}
          message={message}
          runToggle={runToggle}
          showUserIdentity={showUserIdentity}
          thinkingAgentId={thinkingAgentId}
          onDiscoveryOtherOptionSelectionChange={
            onDiscoveryOtherOptionSelectionChange
          }
          onDiscoveryPlanHandoff={onDiscoveryPlanHandoff}
          onDiscoveryOptionSubmit={onDiscoveryOptionSubmit}
          onGeneratedFileSelect={onGeneratedFileSelect}
          onUploadedFileSelect={onUploadedFileSelect}
          onRenderCompanyPlanOverview={onRenderCompanyPlanOverview}
        />
      </>
    );
  };

  return (
    <div
      ref={messageScrollRef}
      className="relative min-h-0 flex-1 overflow-hidden"
    >
      <Conversation
        contextRef={scrollContextRef}
        className="h-full [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        initial="instant"
        resize={hasStreamingMessage ? "instant" : "smooth"}
        aria-live="polite"
      >
        <ConversationContent
          scrollClassName="overscroll-none [overflow-anchor:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
          className={cx(
            "workspace-chat-timeline flex min-h-full flex-col p-0 pt-[18px]",
            isExpanded ? "px-6" : "px-4",
            isExpanded && "mx-auto w-full max-w-[880px]",
          )}
        >
          {messages.length === 0 ? emptyState : null}
          {onLoadOlderMessages ? (
            <ChatHistoryLoader
              enabled={
                initialScrollKey !== null &&
                initialScrollReadyKey === initialScrollKey
              }
              error={historyLoadError}
              hasOlder={hasOlderMessages}
              isLoading={isLoadingOlderMessages}
              onLoadOlder={onLoadOlderMessages}
            />
          ) : null}
          {artifacts?.error ? (
            <div className="my-2 text-xs" role="status">
              Outputs could not be loaded.{" "}
              <button
                type="button"
                className="underline"
                onClick={() => void artifacts.refresh()}
              >
                Try again
              </button>
            </div>
          ) : null}
          {renderItems.map((item) => {
            if (item.kind === "message") {
              return (
                <Fragment key={item.id}>
                  {renderTimelineBefore(item.message.id)}
                  {renderMessage(item.message, item.messageIndex)}
                  {renderTimelineAfter(item.message.id)}
                  {renderDecoratorsAfter(item.message.id)}
                </Fragment>
              );
            }

            const artifactFooter =
              artifacts &&
              onArtifactSelect &&
              (item.status === "finished" || item.status === "stopped") ? (
                <ChatRunArtifacts
                  items={artifactsByTurn.get(item.id) ?? []}
                  onSelect={onArtifactSelect}
                />
              ) : null;
            const turnRequests = conversationTimelineItems.filter((request) =>
              item.messages.some(
                (message) => request.afterMessageId === message.id,
              ),
            );
            if (!item.hasTools && turnRequests.length === 0) {
              return (
                <Fragment key={item.id}>
                  {item.messages.map((message, index) => (
                    <Fragment key={message.id}>
                      {renderTimelineBefore(message.id)}
                      {renderMessage(message, item.firstMessageIndex + index)}
                      {renderTimelineAfter(message.id)}
                      {renderDecoratorsAfter(message.id)}
                    </Fragment>
                  ))}
                  {artifactFooter}
                  {item.id === thinkingGroupId ? thinkingIndicator : null}
                </Fragment>
              );
            }

            const focusedInsideRun = item.messages.some(
              (message) => message.id === focusedMessageId,
            );
            const expansionOverride =
              runExpansion.conversationId === initialScrollKey &&
              runExpansion.userQueryKey === latestUserQueryKey
                ? runExpansion.overrides.get(item.id)
                : undefined;
            // Active work is open on the first render, including after history
            // sync. Only an explicit toggle overrides this lifecycle default.
            const isRunExpanded =
              focusedInsideRun ||
              (expansionOverride?.status === runExpansionStatus(item.status)
                ? expansionOverride.expanded
                : item.status === "working" || item.status === "needs_input");
            const runToggle = (
              <ChatRunToggle
                createdAt={item.runCreatedAt}
                isExpanded={isRunExpanded}
                onToggle={(trigger) =>
                  toggleRunExpanded(
                    item.id,
                    item.status,
                    isRunExpanded,
                    trigger,
                  )
                }
                status={item.status}
              />
            );
            return (
              <Fragment key={item.id}>
                <section data-chat-run-id={item.runId}>
                  {isRunExpanded ? (
                    item.messages.map((message, index) => (
                      <Fragment
                        key={
                          index === 0 ? item.id : `${message.runId}:${index}`
                        }
                      >
                        {renderTimelineBefore(message.id)}
                        {renderMessage(
                          message,
                          item.firstMessageIndex + index,
                          index === 0 ? { runToggle } : undefined,
                        )}
                        {renderTimelineAfter(message.id, false)}
                        {renderDecoratorsAfter(message.id)}
                      </Fragment>
                    ))
                  ) : (
                    <>
                      {renderMessage(
                        item.summaryMessage,
                        item.firstMessageIndex,
                        { hideRuntimeActivities: true, runToggle },
                      )}
                      {item.messages.flatMap((message) =>
                        renderDecoratorsAfter(message.id),
                      )}
                    </>
                  )}
                  {artifactFooter}
                  {/* Decisions remain visible when the tool history closes. */}
                  {!isRunExpanded
                    ? turnRequests
                        .filter((request) => request.isPending === false)
                        .map((request) => (
                          <ConversationTimelineItem
                            className={request.className}
                            content={request.content}
                            key={request.key}
                          />
                        ))
                    : null}
                  {/* Keep input drafts and in-flight submissions mounted when
                      the user collapses the surrounding tool history. */}
                  {turnRequests
                    .filter((request) => request.isPending)
                    .map((request) => (
                      <div key={request.key} hidden={!isRunExpanded}>
                        <ConversationTimelineItem
                          className={request.className}
                          content={request.content}
                        />
                      </div>
                    ))}
                  {item.id === thinkingGroupId ? thinkingIndicator : null}
                </section>
              </Fragment>
            );
          })}
          {conversationTimelineItems
            .filter(
              (item) =>
                item.afterMessageId === undefined &&
                item.beforeMessageId === undefined,
            )
            .map((item) => (
              <ConversationTimelineItem
                className={item.className}
                content={item.content}
                key={item.key}
              />
            ))}
          {!thinkingGroupId ? thinkingIndicator : null}
          {waitingMessage ? (
            <div
              className="my-3 flex items-center gap-2 text-sm text-[var(--workspace-native-text-muted)]"
              role="status"
            >
              <Clock3 aria-hidden="true" className="shrink-0" size={16} />
              <span>{waitingMessage}</span>
            </div>
          ) : null}
          {conversationSlot ? (
            <div
              className={cx(
                "mt-5 text-white",
                conversationSlotClassName ??
                  cx(
                    "rounded-xl border border-white/14 bg-white/8 p-3",
                    isExpanded ? "max-w-[760px]" : "max-w-[360px]",
                  ),
              )}
            >
              {conversationSlot}
            </div>
          ) : null}
        </ConversationContent>
        {focusedMessageId ? (
          <ChatFocusedMessageScroll
            messageCount={messages.length}
            messageId={focusedMessageId}
          />
        ) : (
          <>
            <ChatInitialScroll
              conversationKey={initialScrollKey}
              messageCount={messages.length}
              onComplete={setInitialScrollReadyKey}
            />
            <ChatTurnAutoScroll
              followStreamRef={followStreamRef}
              isExpanded={isExpanded}
              messages={messages}
              requestKey={scrollToBottomRequestKey}
            />
          </>
        )}
        <ConversationScrollButton
          aria-label="Scroll to latest message"
          title="Scroll to latest message"
          className={cx(
            "workspace-chat-scroll-latest z-[60] bottom-4",
            "left-1/2",
          )}
        />
      </Conversation>
    </div>
  );
}

export const ChatConversation = memo(ChatConversationComponent);

function ChatFocusedMessageScroll({
  messageCount,
  messageId,
}: {
  messageCount: number;
  messageId: number;
}) {
  const { scrollRef, stopScroll } = useStickToBottomContext();

  useLayoutEffect(() => {
    if (messageCount === 0) return undefined;

    let innerFrameId = 0;
    const frameId = requestAnimationFrame(() => {
      innerFrameId = requestAnimationFrame(() => {
        const scrollElement = scrollRef.current;
        const target = scrollElement?.querySelector<HTMLElement>(
          `[data-focused-message-id="${messageId}"]`,
        );
        if (!scrollElement || !target) return;

        stopScroll();
        const scrollBounds = scrollElement.getBoundingClientRect();
        const targetBounds = target.getBoundingClientRect();
        scrollElement.scrollTop +=
          targetBounds.top -
          scrollBounds.top -
          (scrollElement.clientHeight - targetBounds.height) / 2;
      });
    });

    return () => {
      cancelAnimationFrame(frameId);
      cancelAnimationFrame(innerFrameId);
    };
  }, [messageCount, messageId, scrollRef, stopScroll]);

  return null;
}

function ConversationTimelineItem({
  className,
  content,
}: {
  className?: string;
  content: ReactNode;
}) {
  return (
    <div
      className={cx(
        "mt-5 w-full min-w-0 max-w-full pb-4 text-white",
        className,
      )}
    >
      {content}
    </div>
  );
}

function ChatTurnAutoScroll({
  followStreamRef,
  isExpanded,
  messages,
  requestKey,
}: {
  followStreamRef: RefObject<boolean>;
  isExpanded: boolean;
  messages: ChatMessage[];
  requestKey: number;
}) {
  const { contentRef, scrollRef, scrollToBottom } = useStickToBottomContext();
  const isLayoutTransitionRef = useRef(false);
  const layoutTransitionTimerRef = useRef<number | null>(null);
  const scheduledFrameRef = useRef<number | null>(null);
  const previousExpandedRef = useRef(isExpanded);
  const previousRequestKeyRef = useRef(requestKey);
  const conversationKey =
    messages.find((message) => message.conversationId)?.conversationId ?? null;
  const previousConversationKeyRef = useRef<string | null>(null);

  const cancelScheduledScroll = useCallback(() => {
    if (scheduledFrameRef.current === null) {
      return;
    }

    cancelAnimationFrame(scheduledFrameRef.current);
    scheduledFrameRef.current = null;
  }, []);

  const scheduleScrollToBottom = useCallback(
    (force = false) => {
      if (force) {
        followStreamRef.current = true;
      }
      if (!followStreamRef.current) {
        return;
      }

      cancelScheduledScroll();
      scheduledFrameRef.current = requestAnimationFrame(() => {
        scheduledFrameRef.current = null;
        if (!followStreamRef.current) {
          return;
        }

        const scrollElement = scrollRef.current;
        if (scrollElement) {
          scrollElement.scrollTop = scrollElement.scrollHeight;
        }
        void scrollToBottom({ animation: "instant" });
      });
    },
    [cancelScheduledScroll, followStreamRef, scrollRef, scrollToBottom],
  );

  useLayoutEffect(() => {
    const scrollElement = scrollRef.current;
    if (!scrollElement) {
      return undefined;
    }

    let previousScrollTop = scrollElement.scrollTop;
    let previousScrollHeight = scrollElement.scrollHeight;
    let previousClientHeight = scrollElement.clientHeight;
    const stopFollowing = () => {
      followStreamRef.current = false;
      isLayoutTransitionRef.current = false;
      cancelScheduledScroll();
    };
    const handleWheel = (event: WheelEvent) => {
      if (event.deltaY < 0) {
        stopFollowing();
      }
    };
    const handleScroll = () => {
      const nextScrollTop = scrollElement.scrollTop;
      const nextScrollHeight = scrollElement.scrollHeight;
      const nextClientHeight = scrollElement.clientHeight;
      const layoutChanged =
        nextScrollHeight !== previousScrollHeight ||
        nextClientHeight !== previousClientHeight;
      const distanceFromBottom =
        nextScrollHeight - nextClientHeight - nextScrollTop;

      if (
        !isLayoutTransitionRef.current &&
        !layoutChanged &&
        nextScrollTop < previousScrollTop - 1
      ) {
        stopFollowing();
      } else if (!layoutChanged && distanceFromBottom <= 2) {
        followStreamRef.current = true;
      }
      previousScrollTop = nextScrollTop;
      previousScrollHeight = nextScrollHeight;
      previousClientHeight = nextClientHeight;
    };

    scrollElement.addEventListener("scroll", handleScroll, { passive: true });
    scrollElement.addEventListener("wheel", handleWheel, { passive: true });
    return () => {
      scrollElement.removeEventListener("scroll", handleScroll);
      scrollElement.removeEventListener("wheel", handleWheel);
    };
  }, [cancelScheduledScroll, followStreamRef, scrollRef]);

  useLayoutEffect(() => {
    const contentElement = contentRef.current;
    if (!contentElement) {
      return undefined;
    }

    const observer = new ResizeObserver(() => {
      scheduleScrollToBottom();
    });
    observer.observe(contentElement);
    return () => observer.disconnect();
  }, [contentRef, scheduleScrollToBottom]);

  useEffect(
    () => () => {
      cancelScheduledScroll();
      if (layoutTransitionTimerRef.current !== null) {
        window.clearTimeout(layoutTransitionTimerRef.current);
      }
    },
    [cancelScheduledScroll],
  );

  useLayoutEffect(() => {
    if (previousExpandedRef.current === isExpanded) {
      return;
    }
    previousExpandedRef.current = isExpanded;

    if (!followStreamRef.current) {
      return;
    }

    isLayoutTransitionRef.current = true;
    if (layoutTransitionTimerRef.current !== null) {
      window.clearTimeout(layoutTransitionTimerRef.current);
    }

    const scrollElement = scrollRef.current;
    if (scrollElement) {
      scrollElement.scrollTop = scrollElement.scrollHeight;
    }
    void scrollToBottom({
      animation: "instant",
      duration: chatPanelLayoutTransitionDurationMs,
    });

    layoutTransitionTimerRef.current = window.setTimeout(() => {
      layoutTransitionTimerRef.current = null;
      isLayoutTransitionRef.current = false;
      scheduleScrollToBottom();
    }, chatPanelLayoutTransitionDurationMs);
  }, [
    followStreamRef,
    isExpanded,
    scheduleScrollToBottom,
    scrollRef,
    scrollToBottom,
  ]);

  useLayoutEffect(() => {
    if (
      !conversationKey ||
      previousConversationKeyRef.current === conversationKey
    ) {
      return;
    }

    previousConversationKeyRef.current = conversationKey;
    scheduleScrollToBottom(true);
  }, [conversationKey, scheduleScrollToBottom]);

  useLayoutEffect(() => {
    if (previousRequestKeyRef.current === requestKey) {
      return;
    }

    previousRequestKeyRef.current = requestKey;
    scheduleScrollToBottom(true);
  }, [requestKey, scheduleScrollToBottom]);

  useLayoutEffect(() => {
    if (messages.length === 0) {
      return;
    }

    scheduleScrollToBottom();
  }, [messages, scheduleScrollToBottom]);

  return null;
}

function ChatInitialScroll({
  conversationKey,
  messageCount,
  onComplete,
}: {
  conversationKey: string | null;
  messageCount: number;
  onComplete: (conversationKey: string) => void;
}) {
  const { scrollRef, scrollToBottom } = useStickToBottomContext();
  const initializedConversationRef = useRef<string | null>(null);

  useLayoutEffect(() => {
    if (
      !conversationKey ||
      messageCount === 0 ||
      initializedConversationRef.current === conversationKey
    ) {
      return undefined;
    }

    let innerFrameId = 0;
    const frameId = requestAnimationFrame(() => {
      innerFrameId = requestAnimationFrame(() => {
        const scrollElement = scrollRef.current;
        if (!scrollElement) return;
        scrollElement.scrollTop = scrollElement.scrollHeight;
        initializedConversationRef.current = conversationKey;
        void scrollToBottom({ animation: "instant", ignoreEscapes: true });
        onComplete(conversationKey);
      });
    });
    return () => {
      cancelAnimationFrame(frameId);
      cancelAnimationFrame(innerFrameId);
    };
  }, [conversationKey, messageCount, onComplete, scrollRef, scrollToBottom]);

  return null;
}

function ChatHistoryLoader({
  enabled,
  error,
  hasOlder,
  isLoading,
  onLoadOlder,
}: {
  enabled: boolean;
  error?: string | null;
  hasOlder: boolean;
  isLoading: boolean;
  onLoadOlder: () => Promise<void>;
}) {
  const { scrollRef, stopScroll } = useStickToBottomContext();
  const sentinelRef = useRef<HTMLDivElement>(null);
  const localLoadRef = useRef(false);

  const loadOlder = useCallback(async () => {
    if (!enabled || !hasOlder || isLoading || localLoadRef.current) {
      return;
    }
    const scrollElement = scrollRef.current;
    if (!scrollElement) {
      return;
    }
    const anchor = scrollElement.querySelector<HTMLElement>(
      "[data-chat-message-id]",
    );
    const anchorId = anchor?.dataset.chatMessageId;
    const anchorTop = anchor?.getBoundingClientRect().top;

    localLoadRef.current = true;
    stopScroll();
    try {
      await onLoadOlder();
      await new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      });
      if (anchorId && anchorTop !== undefined) {
        const nextAnchor = scrollElement.querySelector<HTMLElement>(
          `[data-chat-message-id="${anchorId}"]`,
        );
        if (nextAnchor) {
          scrollElement.scrollTop +=
            nextAnchor.getBoundingClientRect().top - anchorTop;
        }
      }
    } finally {
      localLoadRef.current = false;
    }
  }, [enabled, hasOlder, isLoading, onLoadOlder, scrollRef, stopScroll]);

  useEffect(() => {
    const root = scrollRef.current;
    const sentinel = sentinelRef.current;
    if (!enabled || !root || !sentinel || !hasOlder || error) {
      return undefined;
    }

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) {
          void loadOlder();
        }
      },
      { root, rootMargin: "200px 0px 0px", threshold: 0 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [enabled, error, hasOlder, loadOlder, scrollRef]);

  if (!enabled) {
    return null;
  }

  if (!hasOlder && !isLoading && !error) {
    return null;
  }

  return (
    <div
      ref={sentinelRef}
      className="flex min-h-8 items-center justify-center pb-3 text-xs text-white/60"
    >
      {isLoading ? <span>Loading earlier messages…</span> : null}
      {error ? (
        <button
          className="rounded-full border border-white/15 bg-white/8 px-3 py-1.5 text-white/80 hover:bg-white/12"
          onClick={() => void loadOlder()}
          type="button"
        >
          Retry loading earlier messages
        </button>
      ) : null}
    </div>
  );
}
