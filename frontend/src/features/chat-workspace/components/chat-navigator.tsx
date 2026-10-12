import {
  Archive,
  Ban,
  Circle,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  CircleHelp,
  CirclePause,
  CircleX,
  Clock3,
  Radio,
  RadioIndicator,
  MessageSquareText,
  ChevronRight,
  Users,
  Hash,
  Spinner,
  LockKeyhole,
  ChevronDown,
  Eye,
  EyeOff,
  Info,
  Pin,
  PencilLine,
  Plus,
  ShieldCheck,
  TimerStart,
  type IconComponent,
} from "@/shared/ui/icons";
import {
  createContext,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent as ReactKeyboardEvent,
  type PropsWithChildren,
  type ReactNode,
} from "react";

import { ConversationPinMenu } from "@/features/chat-workspace/components/conversation-pin-menu";
import { useMemberPresence } from "@/features/discussions/model/use-member-presence";
import { ChatNavigatorOptions } from "./chat-navigator-options";
import {
  ChatNavigatorAgents,
  ChatNavigatorBrowseControls,
} from "@/features/chat-workspace/components/chat-navigator-browser";
import {
  matchesChatNavigatorSearch,
  type ChatNavigatorAgent,
  type ChatNavigatorBrowseState,
  type ChatNavigatorPreferences,
  type ChatNavigatorSection,
} from "@/features/chat-workspace/model/chat-navigator-browse";
import { chatConversationTargetKey } from "@/shared/chat/conversation-target";
import type { components } from "@/shared/api/generated/api.generated";
import type {
  ChatConversationTarget,
  DiscussionConversationTarget,
} from "@/shared/chat/types";
import { Avatar, AvatarFallback, AvatarImage } from "@/shared/ui/core/avatar";
import { cx } from "@/shared/lib/cx";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { useChatMemberProfile } from "@/shared/chat/member-profile";
import { Tooltip } from "@/shared/ui/tooltip";

export type ChatNavigatorTarget =
  ChatConversationTarget | DiscussionConversationTarget;

export type ChatNavigatorItem = {
  unreadCount?: number;
  markedUnread?: boolean;
  accentColor?: string;
  // The person a row without an Agent stands for: the peer of a direct
  // message, or the member who does a Task themself. Their photo is
  // avatarSrc and their initials the fallback. Any other row falls back to
  // the first letter of its title.
  avatarName?: string;
  avatarSrc?: string;
  avatarUserId?: string;
  badge?: string;
  description?: string;
  descriptionMono?: boolean;
  // "hash" for the Company-wide general channel (every member is in it),
  // "lock" for every other channel (they are private), "group" for a group
  // conversation.
  iconName?: string;
  archived?: boolean;
  pinned?: boolean;
  canPin?: boolean;
  canRename?: boolean;
  target: ChatNavigatorTarget;
  title: string;
  taskLabel?: string;
  taskState?: {
    active: boolean;
    running: boolean;
    needsAttention: boolean;
    recent: boolean;
    tone: "attention" | "running" | "danger" | "muted";
  };
  updatedAt?: string;
};

type ChatNavigatorProps = {
  activeTarget?: ChatNavigatorTarget;
  agents: ChatNavigatorAgent[];
  avatarMotionPaused?: boolean;
  browse: ChatNavigatorBrowseState;
  preferences: ChatNavigatorPreferences;
  onPreferencesChange: (preferences: ChatNavigatorPreferences) => void;
  directMessages: ChatNavigatorItem[];
  taskChats: ChatNavigatorItem[];
  isLoading?: boolean;
  hasError?: boolean;
  onNewDirectMessage: () => void;
  onBrowseChange: (browse: ChatNavigatorBrowseState) => void;
  onNewAgentChat: (agentId: string) => void;
  onPeekTask: (taskId: string, trigger: HTMLElement) => void;
  onSelect: (target: ChatNavigatorTarget) => void;
  onTogglePin: (item: ChatNavigatorItem) => Promise<boolean>;
  onRename?: (item: ChatNavigatorItem, trigger: HTMLButtonElement) => void;
  updatingTargetKey?: string;
  pinned: ChatNavigatorItem[];
  archivedChats?: ChatNavigatorItem[];
  archivedFooter?: ReactNode;
  onToggleArchive?: (item: ChatNavigatorItem) => Promise<boolean>;
  groups?: ChatNavigatorItem[];
  groupTitle?: string;
  groupFooter?: ReactNode;
  directMessageFooter?: ReactNode;
  onCreateGroup?: () => void;
  options?: ReactNode;
  includeHidden?: boolean;
};

type AvatarAttention = {
  hoveredRowId: string | null;
  focusedRowId: string | null;
  latestInput: "pointer" | "keyboard";
};

function createAvatarAttentionStore() {
  let attention: AvatarAttention = {
    hoveredRowId: null,
    focusedRowId: null,
    latestInput: "pointer",
  };
  const listeners = new Set<() => void>();
  const getGreetingRowId = () =>
    attention.latestInput === "keyboard"
      ? (attention.focusedRowId ?? attention.hoveredRowId)
      : (attention.hoveredRowId ?? attention.focusedRowId);

  return {
    getGreetingRowId,
    setAttention(update: (current: AvatarAttention) => AvatarAttention) {
      const previousGreetingRowId = getGreetingRowId();
      attention = update(attention);
      if (getGreetingRowId() === previousGreetingRowId) return;
      listeners.forEach((listener) => listener());
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

const AvatarAttentionContext = createContext<{
  paused: boolean;
  store: ReturnType<typeof createAvatarAttentionStore>;
} | null>(null);

const MemberPresenceContext = createContext<
  ReadonlyMap<string, components["schemas"]["MemberPresence"]>
>(new Map());

function directMessagePeerId(item: ChatNavigatorItem) {
  return item.target.kind === "discussion" &&
    item.target.discussionType === "human_direct" &&
    item.iconName !== "group"
    ? item.avatarUserId
    : undefined;
}

export function ChatNavigator({
  activeTarget,
  agents,
  avatarMotionPaused = false,
  browse,
  directMessages,
  taskChats,
  preferences,
  onPreferencesChange,
  isLoading = false,
  hasError = false,
  onBrowseChange,
  onNewAgentChat,
  onNewDirectMessage,
  onPeekTask,
  onSelect,
  onTogglePin,
  onRename,
  updatingTargetKey,
  pinned,
  archivedChats = [],
  archivedFooter,
  onToggleArchive,
  groups = [],
  groupTitle = "Channels",
  groupFooter,
  directMessageFooter,
  onCreateGroup,
  options,
  includeHidden,
}: ChatNavigatorProps) {
  const agentById = useMemo(
    () => new Map(agents.map((agent) => [agent.id, agent])),
    [agents],
  );
  const conversations = useMemo(
    () =>
      browse.view === "archived"
        ? archivedChats
        : [
            ...new Map(
              [...pinned, ...taskChats, ...directMessages, ...groups].map(
                (item) => [chatConversationTargetKey(item.target), item],
              ),
            ).values(),
          ],
    [pinned, taskChats, directMessages, groups, archivedChats, browse.view],
  );
  const presence = useMemberPresence(
    conversations.flatMap((item) => {
      const userId = directMessagePeerId(item);
      return userId ? [userId] : [];
    }),
  );
  const presenceByUserId = useMemo(
    () =>
      new Map(
        presence.currentData?.items.map((member) => [member.userId, member]),
      ),
    [presence.currentData],
  );
  const agentCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of conversations) {
      if (item.target.kind !== "discussion" && item.target.agentId) {
        counts.set(
          item.target.agentId,
          (counts.get(item.target.agentId) ?? 0) + 1,
        );
      }
    }
    return counts;
  }, [conversations]);
  const selectedAgent =
    browse.view === "agents" && browse.agentId
      ? agentById.get(browse.agentId)
      : undefined;
  const query = browse.query;
  const showAgents = browse.view === "agents" && !selectedAgent;
  const matchesItem = (item: ChatNavigatorItem) => {
    const agentId =
      item.target.kind !== "discussion" ? item.target.agentId : undefined;
    const agent = agentId ? agentById.get(agentId) : undefined;
    return (
      (!selectedAgent || agentId === selectedAgent.id) &&
      matchesChatNavigatorSearch(
        [
          item.title,
          item.description,
          item.taskLabel,
          agent?.name,
          agent?.title,
        ].join(" "),
        query,
      )
    );
  };
  const visibleAgents = agents
    .filter(
      (agent) =>
        (agent.canStartChat || agentCounts.has(agent.id)) &&
        (matchesChatNavigatorSearch(agent.name + " " + agent.title, query) ||
          conversations.some(
            (item) =>
              item.target.kind !== "discussion" &&
              item.target.agentId === agent.id &&
              matchesItem(item),
          )),
    )
    .sort(
      (left, right) =>
        Number(left.archived) - Number(right.archived) ||
        left.name.localeCompare(right.name),
    );
  const filtered = Boolean(query.trim() || selectedAgent);
  const hasMatchingConversations = conversations.some(matchesItem);
  const [showAllTasks, setShowAllTasks] = useState(false);
  const activeKey = activeTarget
    ? chatConversationTargetKey(activeTarget)
    : undefined;
  const matchingTaskChats = taskChats.filter(matchesItem);
  const unpinnedTaskChats = matchingTaskChats.filter((item) => !item.pinned);
  const recentTaskChats = unpinnedTaskChats
    .filter(
      (item) =>
        item.taskState?.active ||
        chatConversationTargetKey(item.target) === activeKey ||
        item.taskState?.recent,
    )
    .filter(
      (item, index) =>
        index < 6 ||
        item.taskState?.active ||
        chatConversationTargetKey(item.target) === activeKey,
    );
  const visibleTaskChats =
    showAllTasks || filtered ? unpinnedTaskChats : recentTaskChats;
  const runningCount = matchingTaskChats.filter(
    (item) => item.taskState?.running,
  ).length;
  const hasMoreTaskChats = unpinnedTaskChats.length > recentTaskChats.length;
  const sectionProps = (section: ChatNavigatorSection) => ({
    collapsible: !filtered,
    expanded: !preferences.collapsedSections.includes(section),
    onToggleExpanded: () =>
      onPreferencesChange({
        ...preferences,
        collapsedSections: preferences.collapsedSections.includes(section)
          ? preferences.collapsedSections.filter((item) => item !== section)
          : [...preferences.collapsedSections, section],
      }),
  });
  const [avatarAttention] = useState(createAvatarAttentionStore);
  const avatarAttentionContext = useMemo(
    () => ({
      paused: avatarMotionPaused,
      store: avatarAttention,
    }),
    [avatarAttention, avatarMotionPaused],
  );
  const navigatorRef = useRef<HTMLElement | null>(null);
  const [pinFocusTarget, setPinFocusTarget] = useState<{
    key: string;
    pinned?: boolean;
  } | null>(null);
  useEffect(() => {
    if (!pinFocusTarget || updatingTargetKey) return;
    const { key, pinned: expectedPinned } = pinFocusTarget;
    if (
      expectedPinned !== undefined &&
      !conversations.some(
        (item) =>
          chatConversationTargetKey(item.target) === key &&
          Boolean(item.pinned) === expectedPinned,
      )
    )
      return;
    const trigger = navigatorRef.current?.querySelector<HTMLButtonElement>(
      `[data-conversation-actions-key="${CSS.escape(key)}"]`,
    );
    const focused = document.activeElement;
    if (focused === document.body || navigatorRef.current?.contains(focused)) {
      trigger?.focus();
    }
    setPinFocusTarget(null);
  }, [conversations, updatingTargetKey, pinFocusTarget]);
  const handleTogglePin = async (item: ChatNavigatorItem) => {
    const key = chatConversationTargetKey(item.target);
    setPinFocusTarget({
      key,
      pinned: !item.pinned,
    });
    if (!(await onTogglePin(item))) {
      setPinFocusTarget({ key });
      return;
    }
    const section: ChatNavigatorSection = !item.pinned
      ? "pinned"
      : item.target.kind === "task"
        ? "tasks"
        : item.target.kind === "discussion" &&
            item.target.discussionType === "channel"
          ? "channels"
          : "directMessages";
    onPreferencesChange({
      ...preferences,
      collapsedSections: preferences.collapsedSections.filter(
        (value) => value !== section,
      ),
    });
  };
  const handleToggleArchive = async (item: ChatNavigatorItem) => {
    if (await onToggleArchive?.(item)) {
      const focused = document.activeElement;
      if (
        focused === document.body ||
        navigatorRef.current?.contains(focused)
      ) {
        navigatorRef.current
          ?.querySelector<HTMLInputElement>('input[type="search"]')
          ?.focus();
      }
    }
  };
  const navigator = (
    <aside
      ref={navigatorRef}
      aria-label="Conversations"
      onPointerDownCapture={(event) => {
        avatarAttention.setAttention((current) => {
          const hoveredRowId =
            event.pointerType === "touch" ? null : current.hoveredRowId;
          if (
            current.focusedRowId === null &&
            hoveredRowId === current.hoveredRowId
          )
            return current;
          return {
            ...current,
            focusedRowId: null,
            hoveredRowId,
            latestInput: "pointer",
          };
        });
      }}
      className="workspace-chat-navigator flex h-full min-h-0 min-w-0 w-[280px] shrink-0 flex-col border-r border-white/8 bg-[#0A192A] max-[1279px]:w-[248px]"
    >
      <header className="workspace-chat-navigator__header flex h-[58px] shrink-0 items-center gap-2 border-b border-white/8 px-3">
        <div className="min-w-0 flex-1">
          <h1 className="workspace-chat-navigator__title m-0 truncate text-[15px] font-bold text-white">
            Chat
          </h1>
        </div>
        {options ?? (
          <ChatNavigatorOptions
            browse={browse}
            onBrowseChange={onBrowseChange}
          />
        )}
        <button
          aria-label="New chat"
          className="workspace-chat-navigator__icon-button"
          title="New chat"
          type="button"
          onClick={onNewDirectMessage}
        >
          <PencilLine aria-hidden="true" size={18} />
        </button>
      </header>

      <div className="workspace-chat-navigator__content">
        <ChatNavigatorBrowseControls
          browse={browse}
          onChange={onBrowseChange}
          onNewAgentChat={onNewAgentChat}
          selectedAgent={selectedAgent}
          includeHidden={includeHidden}
        />
        <div className="workspace-chat-navigator__list min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-2 py-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {browse.view === "archived" ? (
            <NavigatorItemSection
              activeTarget={activeTarget}
              icon={<Archive size={14} />}
              items={archivedChats.filter(matchesItem)}
              title="Archived chats"
              footer={archivedFooter}
              emptyLabel={
                isLoading
                  ? "Loading archived chats…"
                  : hasError
                    ? "Archived chats could not be fully loaded. Try refreshing."
                    : query.trim()
                      ? "No matching archived chats."
                      : "No archived chats. Archive conversations from their actions menu."
              }
              onTogglePin={handleTogglePin}
              onRename={onRename}
              onToggleArchive={
                onToggleArchive ? handleToggleArchive : undefined
              }
              updatingTargetKey={updatingTargetKey}
              onPeekTask={onPeekTask}
              onSelect={onSelect}
            />
          ) : showAgents ? (
            <ChatNavigatorAgents
              agents={visibleAgents}
              avatarMotionPaused={avatarMotionPaused}
              counts={agentCounts}
              onSelect={(agentId) =>
                onBrowseChange({ view: "agents", agentId, query: "" })
              }
            />
          ) : (
            <>
              <NavigatorItemSection
                activeTarget={activeTarget}
                icon={<Pin size={14} />}
                items={pinned.filter(matchesItem)}
                title="Pinned"
                {...sectionProps("pinned")}
                onTogglePin={handleTogglePin}
                onRename={onRename}
                onToggleArchive={
                  onToggleArchive ? handleToggleArchive : undefined
                }
                updatingTargetKey={updatingTargetKey}
                allowSpacePeek
                onPeekTask={onPeekTask}
                onSelect={onSelect}
              />
              {!selectedAgent &&
              (groups.length > 0 || groupFooter || onCreateGroup) ? (
                <NavigatorItemSection
                  activeTarget={activeTarget}
                  {...sectionProps("channels")}
                  icon={<Hash size={14} />}
                  items={groups.filter(matchesItem)}
                  title={groupTitle}
                  footer={groupFooter}
                  sectionAction={
                    onCreateGroup && !filtered ? (
                      <button
                        className="workspace-chat-navigator__icon-button workspace-chat-navigator__section-action"
                        aria-label="Create channel"
                        title="Create channel"
                        type="button"
                        onClick={onCreateGroup}
                      >
                        <Plus size={15} aria-hidden />
                      </button>
                    ) : undefined
                  }
                  onTogglePin={handleTogglePin}
                  onRename={onRename}
                  onToggleArchive={
                    onToggleArchive ? handleToggleArchive : undefined
                  }
                  updatingTargetKey={updatingTargetKey}
                  onPeekTask={onPeekTask}
                  onSelect={onSelect}
                />
              ) : null}
              <NavigatorItemSection
                activeTarget={activeTarget}
                icon={<MessageSquareText size={14} />}
                items={directMessages.filter(matchesItem)}
                title="Direct messages"
                {...sectionProps("directMessages")}
                footer={!selectedAgent ? directMessageFooter : undefined}
                sectionAction={
                  !filtered ? (
                    <button
                      className="workspace-chat-navigator__icon-button workspace-chat-navigator__section-action"
                      aria-label="New direct message"
                      title="New direct message"
                      type="button"
                      onClick={onNewDirectMessage}
                    >
                      <PencilLine size={16} aria-hidden />
                    </button>
                  ) : undefined
                }
                onTogglePin={handleTogglePin}
                onRename={onRename}
                onToggleArchive={
                  onToggleArchive ? handleToggleArchive : undefined
                }
                updatingTargetKey={updatingTargetKey}
                onPeekTask={onPeekTask}
                onSelect={onSelect}
              />
              <NavigatorItemSection
                activeTarget={activeTarget}
                icon={<Radio size={14} />}
                items={visibleTaskChats}
                title="Task chats"
                {...sectionProps("tasks")}
                keepVisible={Boolean(unpinnedTaskChats.length || runningCount)}
                summary={
                  runningCount ? (
                    <span className="workspace-chat-navigator__running-count">
                      <span aria-hidden="true" />
                      {runningCount} running
                    </span>
                  ) : undefined
                }
                footer={
                  !filtered && hasMoreTaskChats ? (
                    <button
                      className="workspace-chat-navigator__more workspace-chat-navigator__more-tasks"
                      type="button"
                      aria-expanded={showAllTasks}
                      onClick={() => setShowAllTasks((value) => !value)}
                    >
                      <ChevronRight size={14} aria-hidden="true" />
                      {showAllTasks
                        ? "Show fewer task chats"
                        : "Show all task chats"}
                    </button>
                  ) : undefined
                }
                onTogglePin={handleTogglePin}
                onRename={onRename}
                onToggleArchive={
                  onToggleArchive ? handleToggleArchive : undefined
                }
                updatingTargetKey={updatingTargetKey}
                allowSpacePeek
                onPeekTask={onPeekTask}
                onSelect={onSelect}
              />
            </>
          )}
          {browse.view !== "archived" &&
          (showAgents ? !visibleAgents.length : !hasMatchingConversations) &&
          (showAgents ||
            filtered ||
            isLoading ||
            hasError ||
            !conversations.length) ? (
            <p
              className="workspace-chat-navigator__empty m-0 px-3 py-3 text-xs"
              role="status"
            >
              {isLoading
                ? "Loading conversations…"
                : hasError
                  ? "Conversations could not be fully loaded. Try refreshing."
                  : query.trim()
                    ? "No matching conversations or agents."
                    : selectedAgent
                      ? "No conversations with this agent yet."
                      : showAgents
                        ? "No agents available."
                        : "Start a chat with a teammate or agent."}
            </p>
          ) : null}
        </div>
      </div>
    </aside>
  );

  return (
    <AvatarAttentionContext.Provider value={avatarAttentionContext}>
      <MemberPresenceContext.Provider value={presenceByUserId}>
        {navigator}
      </MemberPresenceContext.Provider>
    </AvatarAttentionContext.Provider>
  );
}

function NavigatorItemSection({
  activeTarget,
  allowSpacePeek = false,
  collapsible = false,
  emptyLabel,
  expanded = true,
  onToggleExpanded,
  summary,
  keepVisible = false,
  icon,
  items,
  onPeekTask,
  onSelect,
  onTogglePin,
  onToggleArchive,
  onRename,
  updatingTargetKey,
  sectionAction,
  footer,
  title,
}: {
  activeTarget?: ChatNavigatorTarget;
  allowSpacePeek?: boolean;
  collapsible?: boolean;
  emptyLabel?: string;
  expanded?: boolean;
  onToggleExpanded?: () => void;
  summary?: ReactNode;
  keepVisible?: boolean;
  icon?: ReactNode;
  items: ChatNavigatorItem[];
  onPeekTask: (taskId: string, trigger: HTMLElement) => void;
  onSelect: (target: ChatNavigatorTarget) => void;
  onTogglePin?: (item: ChatNavigatorItem) => void;
  onToggleArchive?: (item: ChatNavigatorItem) => void;
  onRename?: (item: ChatNavigatorItem, trigger: HTMLButtonElement) => void;
  updatingTargetKey?: string;
  sectionAction?: ReactNode;
  footer?: ReactNode;
  title: string;
}) {
  if (!items.length && !emptyLabel && !sectionAction && !footer && !keepVisible)
    return null;

  return (
    <NavigatorSection
      collapsible={collapsible}
      title={title}
      count={items.length}
      icon={icon}
      action={sectionAction}
      expanded={expanded}
      onToggleExpanded={onToggleExpanded}
      summary={summary}
    >
      {items.length ? (
        items.map((item) => (
          <NavigatorRow
            activeTarget={activeTarget}
            allowSpacePeek={allowSpacePeek}
            item={item}
            key={chatConversationTargetKey(item.target)}
            onPeekTask={onPeekTask}
            onSelect={onSelect}
            onTogglePin={onTogglePin}
            onToggleArchive={onToggleArchive}
            onRename={onRename}
            updatingTargetKey={updatingTargetKey}
          />
        ))
      ) : (
        <p
          className="workspace-chat-navigator__empty m-0 px-3 py-2 text-[11px] leading-4 text-white/28"
          hidden={!emptyLabel}
        >
          {emptyLabel}
        </p>
      )}
      {footer}
    </NavigatorSection>
  );
}

function NavigatorSection({
  count,
  icon,
  action,
  children,
  collapsible = false,
  expanded,
  onToggleExpanded,
  summary,
  title,
}: PropsWithChildren<{
  count?: number;
  icon?: ReactNode;
  action?: ReactNode;
  collapsible?: boolean;
  expanded: boolean;
  onToggleExpanded?: () => void;
  summary?: ReactNode;
  title: string;
}>) {
  const contentId = useId();
  const showItems = !collapsible || expanded;

  return (
    <section className="workspace-chat-navigator__section mb-4 min-w-0 last:mb-0">
      <div className="workspace-chat-navigator__section-header mb-1 flex min-w-0 items-center gap-2 pr-2">
        <h2 className="workspace-chat-navigator__section-title m-0 min-w-0 flex-1 px-3 text-[10px] font-bold uppercase tracking-[0.14em] text-white/28">
          {collapsible ? (
            <button
              aria-controls={contentId}
              aria-expanded={showItems}
              aria-label={`${expanded ? "Collapse" : "Expand"} ${title}`}
              className="workspace-chat-navigator__section-toggle"
              onClick={onToggleExpanded}
              type="button"
            >
              {icon}
              <span>{title}</span>
              <ChevronDown
                aria-hidden="true"
                className="workspace-chat-navigator__section-chevron"
                size={12}
              />
            </button>
          ) : (
            <>
              {icon}
              {title}
            </>
          )}
        </h2>
        {count ? (
          <span className="sr-only">
            {count} {count === 1 ? "conversation" : "conversations"}
          </span>
        ) : null}
        {summary}
        {action}
      </div>
      <div
        className={cx(
          "min-w-0 grid-cols-1 gap-0.5",
          showItems ? "grid" : "hidden",
        )}
        id={contentId}
      >
        {showItems ? children : null}
      </div>
    </section>
  );
}

const navigatorBadgeIcons: Record<string, IconComponent> = {
  Backlog: CircleDashed,
  "To do": Circle,
  "In progress": TimerStart,
  Running: Spinner,
  "Up next": Clock3,
  Waiting: Clock3,
  "Needs input": CircleHelp,
  "Needs approval": ShieldCheck,
  "In review": Eye,
  "Needs review": Eye,
  Completed: CircleCheck,
  Blocked: Ban,
  Failed: CircleAlert,
  "Technical error": CircleAlert,
  Cancelled: CircleX,
  Archived: Archive,
  Hidden: EyeOff,
  Inactive: CirclePause,
};

function NavigatorRow({
  activeTarget,
  allowSpacePeek = false,
  item,
  onPeekTask,
  onSelect,
  onTogglePin,
  onToggleArchive,
  onRename,
  updatingTargetKey,
}: {
  activeTarget?: ChatNavigatorTarget;
  allowSpacePeek?: boolean;
  item: ChatNavigatorItem;
  onPeekTask: (taskId: string, trigger: HTMLElement) => void;
  onSelect: (target: ChatNavigatorTarget) => void;
  onTogglePin?: (item: ChatNavigatorItem) => void;
  onToggleArchive?: (item: ChatNavigatorItem) => void;
  onRename?: (item: ChatNavigatorItem, trigger: HTMLButtonElement) => void;
  updatingTargetKey?: string;
}) {
  const rowId = useId();
  const presence = useContext(MemberPresenceContext).get(
    directMessagePeerId(item) ?? "",
  );
  const presenceLabel = presence
    ? presence.online
      ? "Active"
      : "Away"
    : undefined;
  const PresenceIcon = presence?.online ? RadioIndicator : Circle;
  const attention = useContext(AvatarAttentionContext);
  if (!attention) throw new Error("Navigator rows require avatar attention");
  const setAttention = attention.store.setAttention;
  const greeting = useSyncExternalStore(
    attention.store.subscribe,
    () => !attention.paused && attention.store.getGreetingRowId() === rowId,
    () => false,
  );
  const active =
    (activeTarget ? chatConversationTargetKey(activeTarget) : null) ===
      chatConversationTargetKey(item.target) &&
    (activeTarget?.kind !== "discussion" ||
      item.target.kind !== "discussion" ||
      Boolean(activeTarget.manage) === Boolean(item.target.manage));
  const taskId = item.target.kind === "task" ? item.target.taskId : undefined;
  const agentId =
    item.target.kind !== "discussion" ? item.target.agentId : undefined;
  const isChannel =
    item.target.kind === "discussion" &&
    item.target.discussionType === "channel";
  const unread = Boolean(item.unreadCount || item.markedUnread);
  const identity =
    item.target.kind === "direct"
      ? item.description
      : item.target.kind === "task"
        ? item.taskLabel
        : undefined;
  const rowTooltip = [
    item.title,
    presenceLabel,
    item.taskLabel,
    item.description,
    item.badge,
  ]
    .filter(Boolean)
    .join(" · ");
  const StatusIcon = item.badge
    ? (navigatorBadgeIcons[item.badge] ?? Info)
    : undefined;
  const stateTone =
    item.taskState?.tone ?? (item.badge === "Running" ? "running" : "muted");
  const hasAgentAvatar = Boolean(agentId);
  const compactMotion = greeting ? "greet" : active ? "blink" : undefined;
  useEffect(
    () => () => {
      setAttention((current) =>
        current.hoveredRowId === rowId || current.focusedRowId === rowId
          ? {
              ...current,
              hoveredRowId:
                current.hoveredRowId === rowId ? null : current.hoveredRowId,
              focusedRowId:
                current.focusedRowId === rowId ? null : current.focusedRowId,
            }
          : current,
      );
    },
    [hasAgentAvatar, rowId, setAttention],
  );
  const handleOpen = () => onSelect(item.target);
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== " " || !allowSpacePeek || !taskId) return;
    event.preventDefault();
    onPeekTask(taskId, event.currentTarget);
  };

  return (
    <div
      data-active={active || undefined}
      data-kind={item.target.kind}
      onPointerEnter={
        hasAgentAvatar
          ? (event) => {
              if (event.pointerType === "touch") return;
              setAttention((current) => ({
                ...current,
                hoveredRowId: rowId,
                latestInput: "pointer",
              }));
            }
          : undefined
      }
      onPointerLeave={
        hasAgentAvatar
          ? (event) => {
              if (event.pointerType === "touch") return;
              setAttention((current) =>
                current.hoveredRowId === rowId
                  ? { ...current, hoveredRowId: null }
                  : current,
              );
            }
          : undefined
      }
      onFocusCapture={
        hasAgentAvatar
          ? (event) => {
              if (!event.currentTarget.contains(event.target)) return;
              if (!event.target.matches(":focus-visible")) {
                setAttention((current) =>
                  current.focusedRowId === rowId
                    ? { ...current, focusedRowId: null }
                    : current,
                );
                return;
              }
              setAttention((current) => ({
                ...current,
                focusedRowId: rowId,
                latestInput: "keyboard",
              }));
            }
          : undefined
      }
      onBlurCapture={
        hasAgentAvatar
          ? (event) => {
              if (event.currentTarget.contains(event.relatedTarget)) return;
              setAttention((current) =>
                current.focusedRowId === rowId
                  ? { ...current, focusedRowId: null }
                  : current,
              );
            }
          : undefined
      }
      className={cx(
        "workspace-chat-navigator__row group relative flex w-full min-w-0 items-center rounded-xl border pr-1 transition",
        active
          ? "border-[#FFD84D]/22 bg-[#FFD84D]/10 text-white"
          : "border-transparent text-white/66 hover:bg-white/6 hover:text-white",
      )}
    >
      <Tooltip label={rowTooltip}>
        <button
          aria-current={active ? "page" : undefined}
          aria-label={[
            item.title,
            presenceLabel,
            identity,
            item.badge,
            unread
              ? `unread${item.unreadCount ? `, ${item.unreadCount} ${item.unreadCount === 1 ? "message" : "messages"}` : ""}`
              : undefined,
          ]
            .filter(Boolean)
            .join(", ")}
          className="workspace-chat-navigator__row-button flex min-w-0 flex-1 items-center gap-2 px-2 text-left"
          type="button"
          onClick={handleOpen}
          onKeyDown={handleKeyDown}
        >
          <span className="workspace-chat-navigator__avatar-slot">
            <NavigatorAvatar
              agentId={agentId}
              avatarName={item.avatarName}
              avatarSrc={item.avatarSrc}
              avatarUserId={item.avatarUserId}
              compactMotion={compactMotion}
              followPointer={active}
              group={item.iconName === "group"}
              icon={
                item.target.kind === "discussion" &&
                item.target.discussionType === "channel" ? (
                  item.iconName === "hash" ? (
                    <Hash size={17} aria-hidden />
                  ) : (
                    <LockKeyhole size={17} aria-hidden />
                  )
                ) : undefined
              }
              paused={attention.paused}
              title={item.title}
            />
            {presence ? (
              <span
                className="workspace-chat-navigator__presence"
                data-online={presence.online || undefined}
                aria-hidden="true"
              >
                <PresenceIcon size={10} />
              </span>
            ) : null}
          </span>
          <span className="workspace-chat-navigator__row-copy">
            <span className="workspace-chat-navigator__row-text">
              <span
                className="workspace-chat-navigator__row-title"
                data-channel={isChannel || undefined}
                data-unread={unread || undefined}
              >
                {item.title}
              </span>
              {identity ? (
                <span className="workspace-chat-navigator__row-identity">
                  {identity}
                </span>
              ) : null}
            </span>
            {unread ? (
              <span
                className="workspace-chat-navigator__unread"
                data-count={Boolean(item.unreadCount) || undefined}
                aria-hidden="true"
              >
                {item.unreadCount
                  ? item.unreadCount > 99
                    ? "99+"
                    : item.unreadCount
                  : null}
              </span>
            ) : null}
          </span>
        </button>
      </Tooltip>
      <div className="workspace-chat-navigator__row-actions">
        {onTogglePin && item.canPin !== false && item.target.conversationId ? (
          <ConversationPinMenu
            disabled={Boolean(updatingTargetKey)}
            pending={
              updatingTargetKey === chatConversationTargetKey(item.target)
            }
            pinned={Boolean(item.pinned)}
            archived={item.archived}
            onRename={
              item.canRename && item.target.kind === "direct" && onRename
                ? (trigger) => onRename(item, trigger)
                : undefined
            }
            onToggleArchive={
              onToggleArchive &&
              (item.target.kind !== "discussion" ||
                item.target.discussionType === "human_direct")
                ? () => onToggleArchive(item)
                : undefined
            }
            targetKey={chatConversationTargetKey(item.target)}
            title={item.title}
            onTogglePin={() => onTogglePin(item)}
          />
        ) : null}
      </div>
      {item.badge && StatusIcon ? (
        <Tooltip label={item.badge}>
          <span
            className="workspace-chat-navigator__row-state"
            data-tone={stateTone}
            data-review-ready={
              item.badge === "Needs review" ||
              item.badge === "In review" ||
              undefined
            }
            role="img"
            aria-label={item.badge}
          >
            <StatusIcon
              size={16}
              className={
                item.badge === "Running"
                  ? "motion-safe:animate-spin"
                  : undefined
              }
              aria-hidden="true"
            />
          </span>
        </Tooltip>
      ) : null}
    </div>
  );
}

function NavigatorAvatar({
  agentId,
  avatarName,
  avatarSrc,
  avatarUserId,
  compactMotion,
  followPointer,
  group,
  icon,
  paused,
  title,
}: {
  agentId?: string;
  avatarName?: string;
  avatarSrc?: string;
  avatarUserId?: string;
  compactMotion?: "blink" | "greet";
  followPointer?: boolean;
  group?: boolean;
  icon?: ReactNode;
  paused?: boolean;
  title: string;
}) {
  const profile = useChatMemberProfile({
    id: avatarUserId ?? "",
    kind: "human",
    name: avatarName ?? title,
    avatarSrc,
    title: "",
  });
  if (agentId) {
    return (
      <FoldAvatar
        agentId={agentId}
        className="workspace-chat-navigator__avatar"
        motion="still"
        compactMotion={compactMotion}
        followPointer={followPointer}
        paused={paused}
        size={22}
      />
    );
  }
  if (icon) {
    return <span className="workspace-chat-navigator__symbol">{icon}</span>;
  }

  return (
    <Avatar className="workspace-chat-navigator__avatar" aria-hidden="true">
      {profile.avatarSrc ? (
        <AvatarImage src={profile.avatarSrc} alt="" />
      ) : null}
      <AvatarFallback>
        {group ? (
          <Users size={17} aria-hidden />
        ) : avatarName ? (
          personInitials(profile.name)
        ) : (
          Array.from(title)[0]?.toLocaleUpperCase()
        )}
      </AvatarFallback>
    </Avatar>
  );
}
