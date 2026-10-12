import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import {
  Bot,
  ListTodo,
  LoaderCircle,
  MessageSquareText,
  MessagesSquare,
} from "@/shared/ui/icons";
import { useLocation, useNavigate } from "react-router";

import {
  desktopCommandActions,
  getDesktopRouteForMenuCommand,
  type DesktopCommandAction,
} from "@/app/desktop/desktop-menu-commands";
import { subscribeToDesktopCommandPaletteOpen } from "@/app/desktop/desktop-command-palette";
import {
  getDesktopEnvironment,
  subscribeToDesktopMenuCommands,
} from "@/app/desktop/desktop-environment.service";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useCompanyPermissions } from "@/shared/permissions";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { createTaskPeekNavigationState } from "@/features/chat-workspace/model/chat-workspace-route";
import { activateAccountNotification } from "@/features/notifications/lib/account-notification-navigation";
import { companySearchService } from "@/shared/api";
import type {
  AgentDto,
  CompanyConversationSearchResult,
  CompanyMessageSearchResult,
  CompanySearchResult,
  CompanyTaskSearchResult,
} from "@/shared/api/types";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/shared/ui/core/command";
import { cx } from "@/shared/lib/cx";
import {
  formatTaskLabel,
  useTaskLabel,
  type LabelTask,
} from "@/shared/lib/task-label";

export function DesktopMenuCommandRouter() {
  const navigate = useNavigate();
  const location = useLocation();
  const [isPaletteOpen, setIsPaletteOpen] = useState(false);

  const runRoute = useCallback(
    (route: string, state?: unknown) => {
      setIsPaletteOpen(false);
      navigate(route, { state });
    },
    [navigate],
  );

  useEffect(() => {
    return subscribeToDesktopMenuCommands((command) => {
      if (command.type === "command-palette:open") {
        setIsPaletteOpen(true);
        return;
      }

      if (command.type === "app:reload") {
        window.location.reload();
        return;
      }

      if (command.type === "route:path") {
        runRoute(command.path);
        return;
      }

      if (command.type === "notification:activate") {
        void activateAccountNotification({
          notificationId: command.notificationId,
          userId: command.userId,
        }).then((path) => {
          if (path) runRoute(path);
        });
        return;
      }

      const route = getDesktopRouteForMenuCommand(command);

      if (route) {
        runRoute(route);
      }
    });
  }, [runRoute]);

  useEffect(() => {
    return subscribeToDesktopCommandPaletteOpen(() => {
      setIsPaletteOpen(true);
    });
  }, []);

  useEffect(() => {
    const search = new URLSearchParams(location.search);
    if (search.get("desktop_search") !== "1") return;
    const taskId = search.get("taskId")?.trim();
    if (taskId) {
      navigate(
        {
          pathname: "/tasks",
          search: new URLSearchParams({ taskId }).toString(),
        },
        { replace: true },
      );
      return;
    }
    setIsPaletteOpen(true);
    search.delete("desktop_search");
    navigate(
      { pathname: location.pathname, search: search.toString() },
      { replace: true },
    );
  }, [location.pathname, location.search, navigate]);

  return (
    <DesktopCommandPalette
      open={isPaletteOpen}
      onOpenChange={setIsPaletteOpen}
      onRunRoute={runRoute}
    />
  );
}

function DesktopCommandPalette({
  onOpenChange,
  onRunRoute,
  open,
}: {
  onOpenChange: (open: boolean) => void;
  onRunRoute: (route: string, state?: unknown) => void;
  open: boolean;
}) {
  const session = useSessionSnapshot();
  const { can } = useCompanyPermissions();
  const companyId = session.company?.id ?? "";
  const reducedTransparency = useReducedTransparency();
  const [shortcutModifier, setShortcutModifier] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [query, setQuery] = useState("");
  const [searchState, setSearchState] = useState<PaletteSearchState>(
    emptyPaletteSearchState,
  );
  const labelTask = useTaskLabel(open && companyId ? companyId : undefined);
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !open || !companyId,
  });
  const activeSearchController = useRef<AbortController | null>(null);
  const requestVersion = useRef(0);
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const isSearchQuery = [...query.trim()].length >= 2;
  const searchIsSettling =
    isSearchQuery && (debouncedQuery !== query.trim() || searchState.loading);
  const visibleActions = useMemo(() => {
    const allowedActions = desktopCommandActions.filter(
      (action) =>
        (companyId || action.group === "Settings") &&
        (!action.requires || can(action.requires)),
    );
    if (!query.trim()) return allowedActions;
    if (!isSearchQuery) return [];
    return allowedActions.filter((action) =>
      `${action.label} ${action.description} ${action.keywords.join(" ")}`
        .toLocaleLowerCase()
        .includes(normalizedQuery),
    );
  }, [can, companyId, isSearchQuery, normalizedQuery, query]);
  const visibleAgents = useMemo(() => {
    if (!isSearchQuery) return [];
    return (agentsQuery.currentData?.items ?? []).filter((agent) =>
      `${agent.name} ${agent.title}`
        .toLocaleLowerCase()
        .includes(normalizedQuery),
    );
  }, [agentsQuery.currentData?.items, isSearchQuery, normalizedQuery]);

  useEffect(() => {
    let cancelled = false;
    void getDesktopEnvironment().then((environment) => {
      if (!cancelled && environment.isDesktop) {
        setShortcutModifier(environment.platform === "macos" ? "Cmd" : "Ctrl");
      }
    });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const timeout = window.setTimeout(
      () => setDebouncedQuery(query.trim()),
      250,
    );
    return () => window.clearTimeout(timeout);
  }, [query]);

  useEffect(() => {
    activeSearchController.current?.abort();
    requestVersion.current += 1;
  }, [query]);

  useEffect(() => {
    if (!open) {
      requestVersion.current += 1;
      setQuery("");
      setDebouncedQuery("");
      setActionError(null);
      setSearchState(emptyPaletteSearchState);
      return;
    }
    if ([...debouncedQuery].length < 2) {
      requestVersion.current += 1;
      setSearchState(emptyPaletteSearchState);
      return;
    }

    const controller = new AbortController();
    activeSearchController.current = controller;
    const version = requestVersion.current + 1;
    requestVersion.current = version;
    setSearchState({ ...emptyPaletteSearchState, loading: true });
    if (!companyId) {
      setSearchState({
        ...emptyPaletteSearchState,
        serverError: "Company is unavailable.",
      });
      return;
    }

    void companySearchService
      .search(
        companyId,
        {
          includeArchived: false,
          kinds: ["task", "conversation", "message"],
          limit: 50,
          query: debouncedQuery,
        },
        controller.signal,
      )
      .then((response) => {
        if (controller.signal.aborted || version !== requestVersion.current) {
          return;
        }
        setSearchState({
          loading: false,
          results: response.items,
          serverError: null,
        });
      })
      .catch(() => {
        if (controller.signal.aborted || version !== requestVersion.current) {
          return;
        }
        setSearchState({
          loading: false,
          results: [],
          serverError: "Workspace records could not be searched.",
        });
      });

    return () => {
      controller.abort();
      if (activeSearchController.current === controller) {
        activeSearchController.current = null;
      }
    };
  }, [companyId, debouncedQuery, open]);

  const tasks = searchState.results.filter(
    (result): result is CompanyTaskSearchResult => result.kind === "task",
  );
  const conversations = searchState.results.filter(
    (result): result is CompanyConversationSearchResult =>
      result.kind === "conversation",
  );
  const messages = searchState.results.filter(
    (result): result is CompanyMessageSearchResult => result.kind === "message",
  );
  const hasResults =
    visibleActions.length +
      tasks.length +
      conversations.length +
      messages.length +
      visibleAgents.length >
    0;

  const runRecordResult = async (
    result: CompanySearchResult,
    presentation: "primary" | "peek" = "primary",
  ) => {
    setActionError(null);
    try {
      if (result.kind === "task") {
        if (presentation === "peek") {
          onRunRoute(taskPeekRoute(result), createTaskPeekNavigationState());
        } else {
          onRunRoute(taskRoute(result));
        }
        return;
      }
      if (result.kind === "conversation") {
        onRunRoute(directConversationRoute(result));
        return;
      }
      const latestRoute = latestConversationRoute(result);
      onRunRoute(focusedMessageRoute(result, latestRoute));
    } catch {
      setActionError("That result changed before it could be opened.");
    }
  };

  return (
    <CommandDialog
      className={cx(
        "workspace-command-palette force-dark isolate !z-[120] !max-w-[min(640px,calc(100vw-32px))] !overflow-hidden !rounded-[20px] !border-0 !p-0 !text-white !shadow-none [&>[data-slot=command]]:relative [&>[data-slot=command]]:!bg-transparent [&_[data-slot=command-input-wrapper]]:relative [&_[data-slot=command-input-wrapper]]:z-10 [&_[data-slot=command-input-wrapper]]:!border-white/16 [&_[data-slot=command-input-wrapper]]:bg-[linear-gradient(180deg,rgba(255,255,255,0.065),rgba(255,255,255,0.018))] [&_[data-slot=command-input-wrapper]]:shadow-[inset_0_-1px_0_rgba(255,255,255,0.035)] [&_[data-slot=command-input-wrapper]>svg]:text-white/62",
        "!bg-[#0A1D28]",
      )}
      description="Open Zenith Team workspace actions."
      overlayClassName={cx(
        "workspace-command-palette__overlay !z-[118]",
        reducedTransparency
          ? "!bg-[#020817]/82 !backdrop-blur-none"
          : "!bg-[#020817]/48 !backdrop-blur-[7px]",
      )}
      open={open}
      shouldFilter={false}
      showCloseButton={false}
      title="Zenith Team command palette"
      onOpenChange={onOpenChange}
    >
      <CommandInput
        className="relative z-10 text-white placeholder:text-white/48"
        placeholder="Search Zenith Team"
        value={query}
        onValueChange={setQuery}
      />
      <CommandList className="relative z-10 max-h-[380px] p-3">
        {(["Workspace", "Tools", "Settings"] as const).map((group) => {
          const actions = visibleActions.filter(
            (action) => action.group === group,
          );
          if (!actions.length) return null;
          return (
            <CommandGroup
              className={paletteGroupClassName}
              heading={group}
              key={group}
            >
              {actions.map((action) => (
                <DesktopCommandItem
                  action={action}
                  key={action.id}
                  onRunRoute={onRunRoute}
                  shortcutModifier={shortcutModifier}
                />
              ))}
            </CommandGroup>
          );
        })}
        <LocalAgentResultGroup
          heading="Agents"
          icon={Bot}
          items={visibleAgents}
          onSelect={(agentId) => onRunRoute(directAgentRoute(agentId))}
        />
        <SearchResultGroup
          heading="Tasks"
          results={tasks}
          labelTask={labelTask}
          onSelect={runRecordResult}
          onPeekTask={(result) => runRecordResult(result, "peek")}
        />
        <SearchResultGroup
          heading="Conversations"
          results={conversations}
          onSelect={runRecordResult}
        />
        <SearchResultGroup
          heading="Messages"
          results={messages}
          onSelect={runRecordResult}
        />
        {searchIsSettling ? (
          <div className="flex items-center justify-center gap-2 py-7 text-sm text-white/54">
            <LoaderCircle className="size-4 animate-spin" /> Searching…
          </div>
        ) : null}
        {searchState.serverError || actionError ? (
          <div className="space-y-1 px-2 py-3 text-xs text-amber-100/75">
            {searchState.serverError ? <p>{searchState.serverError}</p> : null}
            {actionError ? <p>{actionError}</p> : null}
          </div>
        ) : null}
        {!searchIsSettling && !hasResults ? (
          <CommandEmpty className="py-8 text-center text-sm text-white/60">
            {query.trim() && !isSearchQuery
              ? "Type at least 2 characters to search."
              : "No matching results."}
          </CommandEmpty>
        ) : null}
      </CommandList>
    </CommandDialog>
  );
}

function DesktopCommandItem({
  action,
  onRunRoute,
  shortcutModifier,
}: {
  action: DesktopCommandAction;
  onRunRoute: (route: string, state?: unknown) => void;
  shortcutModifier: string | null;
}) {
  const Icon = action.icon;

  return (
    <CommandItem
      className={paletteItemClassName}
      value={`${action.label} ${action.description} ${action.keywords.join(" ")}`}
      onSelect={() => onRunRoute(action.route)}
    >
      <Icon className="size-4 shrink-0 text-white/62" />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-sm font-semibold leading-5">
          {action.label}
        </span>
        <span className="truncate text-xs leading-4 text-white/54">
          {action.description}
        </span>
      </span>
      {action.shortcut && shortcutModifier ? (
        <CommandShortcut className="ml-3 text-[11px] font-semibold tracking-normal text-white/42">
          {shortcutModifier}+{action.shortcut}
        </CommandShortcut>
      ) : null}
    </CommandItem>
  );
}

const paletteGroupClassName =
  "text-white [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pb-2.5 [&_[cmdk-group-heading]]:pt-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.16em] [&_[cmdk-group-heading]]:text-white/48";
const paletteItemClassName =
  "rounded-xl border border-transparent px-3 py-3 text-white outline-none transition-[border-color,background-color,box-shadow] duration-150 data-[selected=true]:border-white/18 data-[selected=true]:bg-[linear-gradient(135deg,rgba(255,255,255,0.16),rgba(137,199,255,0.075))] data-[selected=true]:text-white data-[selected=true]:shadow-[0_10px_30px_rgba(0,0,0,0.12),inset_0_1px_0_rgba(255,255,255,0.18),inset_0_-1px_0_rgba(255,255,255,0.035)] data-[selected=true]:[&>svg]:text-white/88";

type PaletteSearchState = {
  loading: boolean;
  results: CompanySearchResult[];
  serverError: string | null;
};

const emptyPaletteSearchState: PaletteSearchState = {
  loading: false,
  results: [],
  serverError: null,
};

function LocalAgentResultGroup({
  heading,
  icon: ResultIcon,
  items,
  onSelect,
}: {
  heading: string;
  icon: typeof Bot;
  items: AgentDto[];
  onSelect: (agentId: string) => void;
}) {
  if (!items.length) return null;

  return (
    <CommandGroup className={paletteGroupClassName} heading={heading}>
      {items.map((agent) => (
        <CommandItem
          className={paletteItemClassName}
          key={`${heading}:${agent.id}`}
          value={`${heading}:${agent.id}`}
          onSelect={() => onSelect(agent.id)}
        >
          <ResultIcon className="size-4 shrink-0 text-white/62" />
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="truncate text-sm font-semibold">{agent.name}</span>
            <span className="truncate text-xs text-white/48">
              {agent.title}
            </span>
          </span>
        </CommandItem>
      ))}
    </CommandGroup>
  );
}

function SearchResultGroup({
  heading,
  labelTask,
  onPeekTask,
  onSelect,
  results,
}: {
  heading: string;
  labelTask?: LabelTask;
  onPeekTask?: (result: CompanyTaskSearchResult) => Promise<void>;
  onSelect: (result: CompanySearchResult) => Promise<void>;
  results: CompanySearchResult[];
}) {
  if (!results.length) return null;
  return (
    <CommandGroup className={paletteGroupClassName} heading={heading}>
      {results.map((result) => {
        const Icon =
          result.kind === "task"
            ? ListTodo
            : result.kind === "conversation"
              ? MessagesSquare
              : MessageSquareText;
        const description =
          result.kind === "task"
            ? `${(labelTask ?? formatTaskLabel)(result)} · ${result.status.replace(/_/g, " ")}`
            : result.kind === "conversation"
              ? "Direct conversation"
              : `${result.author.name} · ${result.snippet}`;
        const identity = result.id;
        const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
          if (
            result.kind !== "task" ||
            !onPeekTask ||
            event.key !== "Enter" ||
            (!event.metaKey && !event.ctrlKey)
          ) {
            return;
          }

          event.preventDefault();
          event.stopPropagation();
          void onPeekTask(result);
        };
        return (
          <CommandItem
            className={paletteItemClassName}
            key={`${result.kind}:${identity}`}
            value={`${result.kind}:${identity}`}
            onKeyDown={handleKeyDown}
            onSelect={() => void onSelect(result)}
          >
            <Icon className="size-4 shrink-0 text-white/62" />
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="truncate text-sm font-semibold">
                {result.title}
              </span>
              <span className="truncate text-xs text-white/48">
                {description}
              </span>
            </span>
            {result.kind === "task" && onPeekTask ? (
              <CommandShortcut className="ml-3 text-[11px] font-semibold tracking-normal text-white/42">
                ⌘↵ Peek
              </CommandShortcut>
            ) : null}
          </CommandItem>
        );
      })}
    </CommandGroup>
  );
}

function taskRoute(result: CompanyTaskSearchResult) {
  if (result.routeKind !== "task") {
    throw new Error("Task result does not have an active chat destination");
  }
  return `/app?${new URLSearchParams({
    conversationId: result.conversationId,
    conversationKind: "task",
    taskId: result.id,
    agentId: result.agent.id,
  }).toString()}`;
}

function taskPeekRoute(result: CompanyTaskSearchResult) {
  return `/app?${new URLSearchParams({ peekTaskId: result.id }).toString()}`;
}

function directConversationRoute(result: CompanyConversationSearchResult) {
  if (result.routeKind !== "direct_conversation") {
    throw new Error(
      "Conversation result does not have an active chat destination",
    );
  }
  return `/app?${new URLSearchParams({
    conversationId: result.id,
    conversationKind: "direct",
    agentId: result.agent.id,
  }).toString()}`;
}

function directAgentRoute(agentId: string) {
  return `/app?${new URLSearchParams({
    conversationKind: "direct",
    agentId,
  }).toString()}`;
}

function latestConversationRoute(result: CompanyMessageSearchResult) {
  if (
    result.routeKind === "discussion" &&
    (result.conversationKind === "channel" ||
      result.conversationKind === "human_direct")
  ) {
    const params = new URLSearchParams({
      conversationKind: "discussion",
      discussionType: result.conversationKind,
      conversationId: result.conversationId,
    });
    if (result.threadRootId) params.set("threadRootId", result.threadRootId);
    return `/app?${params}`;
  }

  if (result.routeKind === "direct_conversation" && result.agent) {
    return `/app?${new URLSearchParams({
      conversationId: result.conversationId,
      conversationKind: "direct",
      agentId: result.agent.id,
    }).toString()}`;
  }
  if (result.routeKind === "task_conversation" && result.taskId) {
    const params = new URLSearchParams({
      conversationId: result.conversationId,
      conversationKind: "task",
      taskId: result.taskId,
    });
    if (result.agent) params.set("agentId", result.agent.id);
    return `/app?${params.toString()}`;
  }
  throw new Error("Message result does not have an active chat destination");
}

function focusedMessageRoute(
  result: CompanyMessageSearchResult,
  latestRoute: string,
) {
  const separator = latestRoute.includes("?") ? "&" : "?";
  return `${latestRoute}${separator}${new URLSearchParams({
    messageId: result.id,
    messageSeq: String(result.seq),
  }).toString()}`;
}

function useReducedTransparency() {
  const readPreference = () =>
    document.documentElement.dataset.boardlyReducedTransparency === "true";
  const [reducedTransparency, setReducedTransparency] =
    useState(readPreference);

  useEffect(() => {
    const documentElement = document.documentElement;
    const observer = new MutationObserver(() => {
      setReducedTransparency(readPreference());
    });
    observer.observe(documentElement, {
      attributeFilter: ["data-boardly-reduced-transparency"],
      attributes: true,
    });
    return () => observer.disconnect();
  }, []);

  return reducedTransparency;
}
