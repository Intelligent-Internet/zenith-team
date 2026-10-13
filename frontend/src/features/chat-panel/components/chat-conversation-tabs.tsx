import { useListWorkspaceTasksQuery } from "@/shared/api/task-api";
import {
  LoaderCircle,
  MoreHorizontal,
  Plus,
  Search,
  X,
} from "@/shared/ui/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { readSessionSnapshot } from "@/shared/session/session";
import { chatPanelMenuLayerClassName } from "@/features/chat-panel/constants";
import {
  createDirectConversationDockTab,
  createSearchDirectConversationDockTab,
  createSearchTaskConversationDockTab,
  createTaskConversationSubtitle,
} from "@/features/chat-panel/model/conversation-dock-tab";
import { useConversationPickerSearch } from "@/features/chat-panel/hooks/use-conversation-picker-search";
import type { TeamNodeData } from "@/features/home/types";

import type { TaskDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { useTaskLabel, type LabelTask } from "@/shared/lib/task-label";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  activateConversationDockTab,
  closeConversationDockTab,
  openConversationDockTab,
  selectCompanyConversationDock,
  useAppDispatch,
  useAppSelector,
  type ConversationDockTab,
} from "@/state";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

type ConversationPickerItem = {
  description: string;
  tab: ConversationDockTab;
};

export function ChatConversationTabs({
  currentTab,
  isWideLayout,
  onClosePanel,
  teams,
}: {
  currentTab: ConversationDockTab;
  isWideLayout: boolean;
  onClosePanel: () => void;
  teams: TeamNodeData[];
}) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const dispatch = useAppDispatch();
  const dock = useAppSelector((state) =>
    selectCompanyConversationDock(state, companyId),
  );
  const [isPickerOpen, setIsPickerOpen] = useState(false);
  const [query, setQuery] = useState("");
  const containerRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const pickerSearch = useConversationPickerSearch({
    companyId,
    isOpen: isPickerOpen,
    query,
  });
  const tasksQuery = useListWorkspaceTasksQuery(
    { companyId },
    { skip: !companyId || !isPickerOpen },
  );
  const labelTask = useTaskLabel(companyId || undefined);

  useEffect(() => {
    if (!companyId) {
      return;
    }

    dispatch(openConversationDockTab({ companyId, tab: currentTab }));
  }, [
    companyId,
    currentTab.agentId,
    currentTab.avatarSrc,
    currentTab.conversationId,
    currentTab.id,
    currentTab.kind,
    currentTab.subtitle,
    currentTab.taskId,
    currentTab.teamId,
    currentTab.title,
    dispatch,
  ]);

  useEffect(() => {
    if (!isPickerOpen) {
      setQuery("");
      return;
    }

    searchInputRef.current?.focus();
    const handlePointerDown = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) {
        setIsPickerOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setIsPickerOpen(false);
      }
    };

    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [isPickerOpen]);

  const openTab = useCallback(
    (tab: ConversationDockTab) => {
      if (!companyId) {
        return;
      }

      dispatch(openConversationDockTab({ companyId, tab }));
      if (tab.id === currentTab.id) {
        setIsPickerOpen(false);
        return;
      }

      setIsPickerOpen(false);
    },
    [companyId, currentTab.id, dispatch],
  );

  const closeTab = useCallback(
    (tab: ConversationDockTab) => {
      if (!companyId) {
        return;
      }

      const closingIndex = dock.tabs.findIndex(
        (candidate) => candidate.id === tab.id,
      );
      const isActive = currentTab.id === tab.id;
      const fallback = isActive
        ? (dock.tabs[closingIndex - 1] ?? dock.tabs[closingIndex + 1])
        : undefined;

      dispatch(closeConversationDockTab({ companyId, tabId: tab.id }));
      if (fallback) {
        dispatch(
          activateConversationDockTab({ companyId, tabId: fallback.id }),
        );
      } else if (isActive) {
        onClosePanel();
      }
    },
    [companyId, currentTab.id, dispatch, dock.tabs, onClosePanel],
  );

  const pickerItems = useMemo(
    () =>
      createConversationPickerItems({
        labelTask,
        query,
        results: pickerSearch.results,
        teams,
      }),
    [labelTask, pickerSearch.results, query, teams],
  );
  const suggestedTaskItems = useMemo(
    () =>
      query.trim()
        ? []
        : createSuggestedTaskPickerItems({
            labelTask,
            tasks: tasksQuery.currentData?.items,
            teams,
          }),
    [labelTask, query, tasksQuery.currentData?.items, teams],
  );
  const openTabs = useMemo(() => {
    const tabs = dock.tabs.some((tab) => tab.id === currentTab.id)
      ? dock.tabs
      : [...dock.tabs, currentTab];

    return tabs.map((tab) => {
      if (tab.agentId) return tab;

      const currentAgentId =
        tab.id === currentTab.id ? currentTab.agentId : undefined;
      const task =
        tab.kind === "task"
          ? tasksQuery.currentData?.items.find((item) => item.id === tab.taskId)
          : undefined;
      const team =
        tab.kind === "task" || tab.id === `direct:${tab.teamId}`
          ? teams.find((item) => item.teamId === tab.teamId)
          : undefined;
      const agentId =
        currentAgentId ?? task?.execution.agent?.id ?? team?.agentId;

      return agentId ? { ...tab, agentId } : tab;
    });
  }, [currentTab, dock.tabs, tasksQuery.currentData?.items, teams]);
  const { hiddenTabs, visibleTabs } = useMemo(
    () => splitVisibleTabs(openTabs, currentTab.id, isWideLayout ? 5 : 3),
    [currentTab.id, isWideLayout, openTabs],
  );

  return (
    <div
      className={cx(
        "relative z-30 shrink-0",
        isWideLayout ? "mx-auto w-full max-w-[880px] px-6" : "px-4",
      )}
      ref={containerRef}
    >
      <div className="flex h-11 items-center gap-1.5 border-b border-white/10">
        <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden">
          {visibleTabs.map((tab) => (
            <ConversationTabButton
              isActive={tab.id === currentTab.id}
              key={tab.id}
              tab={tab}
              onClose={closeTab}
              onOpen={openTab}
            />
          ))}
        </div>

        {hiddenTabs.length ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                aria-label={`${hiddenTabs.length} more open chats`}
                className="grid size-7 shrink-0 place-items-center rounded-lg text-white/56 transition hover:bg-white/10 hover:text-white"
                type="button"
              >
                <MoreHorizontal className="size-4" aria-hidden="true" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className={cx(
                chatPanelMenuLayerClassName,
                "!min-w-[230px] !border-white/16 !bg-[#161b22]/96 !text-white backdrop-blur-xl",
              )}
              sideOffset={8}
            >
              {hiddenTabs.map((tab) => (
                <DropdownMenuItem
                  className="cursor-pointer gap-2.5 text-white focus:!bg-white/12 focus:!text-white"
                  key={tab.id}
                  onSelect={() => openTab(tab)}
                >
                  <ConversationAvatar tab={tab} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{tab.title}</span>
                    <span className="block truncate text-[12px] font-medium text-white/48">
                      {tab.subtitle}
                    </span>
                  </span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}

        <button
          aria-expanded={isPickerOpen}
          aria-label="Open another chat"
          className={cx(
            "grid size-7 shrink-0 place-items-center rounded-lg border transition",
            isPickerOpen
              ? "border-white/24 bg-white/16 text-white"
              : "border-white/12 bg-white/6 text-white/62 hover:border-white/20 hover:bg-white/10 hover:text-white",
          )}
          title="Open another chat"
          type="button"
          onClick={() => setIsPickerOpen((open) => !open)}
        >
          <Plus className="size-4" aria-hidden="true" />
        </button>
      </div>

      {isPickerOpen ? (
        <div
          aria-label="Open a chat"
          className="absolute left-4 right-4 top-[46px] overflow-hidden rounded-2xl border border-white/16 bg-[#10151c]/98 p-2 text-white shadow-[0_24px_72px_rgba(0,0,0,0.52),inset_0_1px_0_rgba(255,255,255,0.09)] backdrop-blur-2xl"
          role="dialog"
        >
          <label className="flex h-10 items-center gap-2 rounded-xl border border-white/12 bg-white/[0.055] px-3 focus-within:border-white/24 focus-within:bg-white/[0.075]">
            <Search className="size-4 shrink-0 text-white/48" aria-hidden />
            <span className="sr-only">Search chats</span>
            <input
              className="min-w-0 flex-1 bg-transparent text-xs text-white outline-none placeholder:text-white/38"
              placeholder="Search tasks or agents"
              ref={searchInputRef}
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>

          <div className="mt-2 max-h-[300px] overflow-y-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            {suggestedTaskItems.length ? (
              <>
                <p className="px-2 pb-1.5 pt-1 text-[12px] font-bold uppercase tracking-[0.15em] text-white/38">
                  Active tasks
                </p>
                {suggestedTaskItems.map((item) => (
                  <ConversationPickerRow
                    item={item}
                    key={item.tab.id}
                    onOpen={openTab}
                  />
                ))}
              </>
            ) : null}
            <p className="px-2 pb-1.5 pt-1 text-[12px] font-bold uppercase tracking-[0.15em] text-white/38">
              {query.trim() ? "Matches" : "Agents"}
            </p>
            {pickerItems.map((item) => (
              <ConversationPickerRow
                item={item}
                key={item.tab.id}
                onOpen={openTab}
              />
            ))}

            {pickerSearch.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-6 text-xs text-white/48">
                <LoaderCircle className="size-4 animate-spin" /> Searching…
              </div>
            ) : null}
            {pickerSearch.error ? (
              <p className="px-3 py-4 text-center text-xs text-amber-100/70">
                {pickerSearch.error}
              </p>
            ) : null}
            {!pickerSearch.isLoading &&
            !pickerSearch.error &&
            pickerItems.length === 0 &&
            suggestedTaskItems.length === 0 ? (
              <p className="px-3 py-5 text-center text-xs text-white/44">
                {query.trim() && !pickerSearch.canSearch
                  ? "Type at least 2 characters to search tasks."
                  : "No matching chats."}
              </p>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ConversationPickerRow({
  item,
  onOpen,
}: {
  item: ConversationPickerItem;
  onOpen: (tab: ConversationDockTab) => void;
}) {
  return (
    <button
      className="flex min-h-14 w-full items-center gap-3 rounded-xl px-2.5 py-2 text-left transition hover:bg-white/10 focus-visible:bg-white/12"
      type="button"
      onClick={() => onOpen(item.tab)}
    >
      <ConversationAvatar tab={item.tab} />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <strong className="truncate text-xs font-semibold text-white">
            {item.tab.title}
          </strong>
          <span className="shrink-0 rounded-full bg-white/8 px-1.5 py-0.5 text-[12px] font-bold uppercase tracking-wide text-white/48">
            {item.tab.kind === "task" ? "Task" : "Agent"}
          </span>
        </span>
        <span className="mt-0.5 block truncate text-[12px] leading-[18px] text-white/48">
          {item.description}
        </span>
      </span>
    </button>
  );
}

function ConversationTabButton({
  isActive,
  onClose,
  onOpen,
  tab,
}: {
  isActive: boolean;
  onClose: (tab: ConversationDockTab) => void;
  onOpen: (tab: ConversationDockTab) => void;
  tab: ConversationDockTab;
}) {
  return (
    <div
      className={cx(
        "group flex h-7 min-w-0 max-w-[150px] items-center rounded-lg border transition",
        isActive
          ? "border-white/20 bg-white/13 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.08)]"
          : "border-transparent text-white/48 hover:border-white/10 hover:bg-white/7 hover:text-white/78",
      )}
      title={`${tab.title} — ${tab.subtitle}`}
    >
      <button
        aria-current={isActive ? "page" : undefined}
        className="flex min-w-0 flex-1 items-center gap-1.5 py-1 pl-1.5 text-left"
        type="button"
        onClick={() => onOpen(tab)}
      >
        <ConversationAvatar compact tab={tab} />
        <span className="truncate text-[12px] font-semibold">{tab.title}</span>
      </button>
      <button
        aria-label={`Close ${tab.title}`}
        className={cx(
          "mr-0.5 grid size-5 shrink-0 place-items-center rounded-md transition hover:bg-white/12 hover:text-white",
          isActive
            ? "text-white/48"
            : "text-transparent group-hover:text-white/42 group-focus-within:text-white/42",
        )}
        type="button"
        onClick={() => onClose(tab)}
      >
        <X className="size-3" aria-hidden="true" />
      </button>
    </div>
  );
}

function ConversationAvatar({
  compact = false,
  tab,
}: {
  compact?: boolean;
  tab: ConversationDockTab;
}) {
  if (tab.agentId) {
    return <FoldAvatar agentId={tab.agentId} size={compact ? 16 : 32} />;
  }
  return (
    <span
      aria-hidden="true"
      className={cx(
        "grid shrink-0 place-items-center overflow-hidden rounded-md bg-white",
        compact ? "size-4" : "size-8",
      )}
    >
      <img
        alt=""
        className="size-full max-w-none object-cover object-[50%_12%]"
        draggable={false}
        src={tab.avatarSrc}
      />
    </span>
  );
}

function createConversationPickerItems({
  labelTask,
  query,
  results,
  teams,
}: {
  labelTask: LabelTask;
  query: string;
  results: ReturnType<typeof useConversationPickerSearch>["results"];
  teams: TeamNodeData[];
}) {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const itemsByTabId = new Map<string, ConversationPickerItem>();

  teams
    .filter((team) => team.available !== false && !team.disabled)
    .filter(
      (team) =>
        !normalizedQuery ||
        `${team.label} ${team.agentRole}`
          .toLocaleLowerCase()
          .includes(normalizedQuery),
    )
    .forEach((team) => {
      const tab = createDirectConversationDockTab({ team });
      itemsByTabId.set(tab.id, {
        description: team.agentRole,
        tab,
      });
    });

  results.forEach((result) => {
    const tab =
      result.kind === "task"
        ? createSearchTaskConversationDockTab(result, teams)
        : createSearchDirectConversationDockTab(result, teams);

    if (!tab) {
      return;
    }

    itemsByTabId.set(tab.id, {
      description:
        result.kind === "task"
          ? `${labelTask(result)} · ${result.status.replaceAll("_", " ")}`
          : `${result.agent.name} · Direct conversation`,
      tab,
    });
  });

  return [...itemsByTabId.values()];
}

function createSuggestedTaskPickerItems({
  labelTask,
  tasks,
  teams,
}: {
  labelTask: LabelTask;
  tasks: TaskDto[] | undefined;
  teams: TeamNodeData[];
}) {
  return (tasks ?? [])
    .filter(
      (task) =>
        task.execution.agent &&
        ["in_progress", "in_review", "blocked", "failed", "error"].includes(
          task.status,
        ),
    )
    .sort(
      (first, second) =>
        Date.parse(second.updatedAt) - Date.parse(first.updatedAt),
    )
    .flatMap<ConversationPickerItem>((task) => {
      const team = teams.find(
        (item) => item.agentId === task.execution.agent?.id,
      );
      if (!team) {
        return [];
      }

      return [
        {
          description: `${labelTask(task)} · ${taskStatusLabel(task.status)}`,
          tab: {
            agentId: task.execution.agent?.id,
            avatarSrc: team.iconSrc,
            conversationId: task.conversationId,
            id: `task:${task.id}`,
            kind: "task",
            subtitle: createTaskConversationSubtitle(team.label),
            taskId: task.id,
            teamId: team.teamId,
            title: task.title,
          },
        },
      ];
    })
    .slice(0, 5);
}

function taskStatusLabel(status: TaskDto["status"]) {
  switch (status) {
    case "in_progress":
      return "Running";
    case "in_review":
      return "In review";
    case "blocked":
      return "Blocked";
    case "failed":
      return "Failed";
    case "error":
      return "Error";
    default:
      return status.replaceAll("_", " ");
  }
}

function splitVisibleTabs(
  tabs: ConversationDockTab[],
  currentTabId: string,
  visibleLimit: number,
) {
  if (tabs.length <= visibleLimit) {
    return { hiddenTabs: [], visibleTabs: tabs };
  }

  const visibleTabs = tabs.slice(0, visibleLimit);
  const currentTab = tabs.find((tab) => tab.id === currentTabId);
  if (currentTab && !visibleTabs.some((tab) => tab.id === currentTab.id)) {
    visibleTabs[visibleTabs.length - 1] = currentTab;
  }
  const visibleTabIds = new Set(visibleTabs.map((tab) => tab.id));

  return {
    hiddenTabs: tabs.filter((tab) => !visibleTabIds.has(tab.id)),
    visibleTabs,
  };
}
