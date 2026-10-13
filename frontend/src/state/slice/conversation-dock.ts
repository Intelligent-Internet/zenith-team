import { canonicalUuid } from "@/shared/lib/uuid";
import {
  chatNavigatorSections,
  initialChatNavigatorBrowse,
  initialChatNavigatorPreferences,
  type ChatNavigatorBrowseState,
  type ChatNavigatorPreferences,
} from "@/features/chat-workspace/model/chat-navigator-browse";
import { createSlice, type PayloadAction } from "@reduxjs/toolkit";
import type { ConversationArtifactReference } from "@/features/chat-panel/model/conversation-artifact";
import type { AgentMailDraftSelection } from "@/features/agent-mail/model/agent-mail";
import {
  chatConversationTargetKey,
  isChatConversationTarget,
} from "@/shared/chat/conversation-target";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type {
  ChatConversationTarget,
  DiscussionConversationTarget,
} from "@/shared/chat/types";

export type ConversationDockTab = {
  agentId?: string;
  avatarSrc: string;
  conversationId?: string;
  id: string;
  kind: "direct" | "task";
  subtitle: string;
  taskId?: string;
  /** The chat lane whose messages and draft this tab shows. */
  teamId: ChatTeamId;
  title: string;
};

type CompanyConversationDock = {
  activeTabId: string | null;
  activeTarget?: ChatConversationTarget;
  activeDiscussion?: DiscussionConversationTarget;
  backStack: ChatConversationTarget[];
  draftsByTargetKey: Record<string, string>;
  artifactsByConversationId: Record<string, ConversationArtifactsPanelState>;
  navigatorBrowse: ChatNavigatorBrowseState;
  navigatorPreferences: ChatNavigatorPreferences;
  surfacedTaskIds: string[];
  recentTargets: ChatConversationTarget[];
  visibleDirectMessageKeys: string[];
  tabs: ConversationDockTab[];
};

export type ConversationArtifactsPanelState = {
  open: boolean;
  selection: ConversationArtifactReference | null;
  mailSelection: AgentMailDraftSelection | null;
  feedback: ConversationArtifactReference | null;
  feedbackRevision: number;
};

type ConversationDockState = {
  companies: Record<string, CompanyConversationDock>;
  visibleConversation?: {
    companyId: string;
    conversationId: string;
    panelId: string;
    discussion?: boolean;
    threadRootId?: string;
  };
};

const emptyCompanyDock = createEmptyCompanyDock();

const initialState: ConversationDockState = {
  companies: {},
};

function getCompanyDock(state: ConversationDockState, companyId: string) {
  const existingDock = state.companies[companyId];
  if (!existingDock || !Array.isArray(existingDock.tabs)) {
    state.companies[companyId] = createEmptyCompanyDock();
  } else {
    existingDock.backStack ??= [];
    existingDock.draftsByTargetKey ??= {};
    existingDock.artifactsByConversationId ??= {};
    existingDock.navigatorBrowse ??= { ...initialChatNavigatorBrowse };
    existingDock.navigatorPreferences ??= {
      ...initialChatNavigatorPreferences,
    };
    existingDock.surfacedTaskIds ??= [];
    existingDock.recentTargets ??= [];
    existingDock.visibleDirectMessageKeys ??= [];
  }

  return state.companies[companyId];
}

const conversationDockSlice = createSlice({
  name: "conversationDock",
  initialState,
  reducers: {
    setVisibleConversation: (
      state,
      action: PayloadAction<
        NonNullable<ConversationDockState["visibleConversation"]>
      >,
    ) => {
      state.visibleConversation = action.payload;
    },
    clearVisibleConversation: (state, action: PayloadAction<string>) => {
      if (state.visibleConversation?.panelId === action.payload) {
        delete state.visibleConversation;
      }
    },
    activateConversationDockTab: (
      state,
      action: PayloadAction<{ companyId: string; tabId: string }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);

      if (dock.tabs.some((tab) => tab.id === action.payload.tabId)) {
        dock.activeTabId = action.payload.tabId;
      }
    },
    closeConversationDockTab: (
      state,
      action: PayloadAction<{ companyId: string; tabId: string }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      const closingIndex = dock.tabs.findIndex(
        (tab) => tab.id === action.payload.tabId,
      );

      if (closingIndex < 0) {
        return;
      }

      const wasActive = dock.activeTabId === action.payload.tabId;
      dock.tabs.splice(closingIndex, 1);

      if (!wasActive) {
        return;
      }

      dock.activeTabId = dock.tabs[Math.max(0, closingIndex - 1)]?.id ?? null;
    },
    openConversationDockTab: (
      state,
      action: PayloadAction<{
        companyId: string;
        tab: ConversationDockTab;
      }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      const existingIndex = dock.tabs.findIndex(
        (tab) => tab.id === action.payload.tab.id,
      );

      if (existingIndex >= 0) {
        const existingTab = dock.tabs[existingIndex];
        dock.tabs[existingIndex] = {
          ...existingTab,
          ...action.payload.tab,
          agentId: action.payload.tab.agentId ?? existingTab.agentId,
          conversationId:
            action.payload.tab.conversationId ?? existingTab.conversationId,
        };
      } else {
        dock.tabs.push(action.payload.tab);
      }

      dock.activeTabId = action.payload.tab.id;
    },
    activateConversationWorkspaceDiscussion: (
      state,
      action: PayloadAction<{
        companyId: string;
        target: DiscussionConversationTarget;
      }>,
    ) => {
      getCompanyDock(state, action.payload.companyId).activeDiscussion =
        action.payload.target;
    },
    activateConversationWorkspaceTarget: (
      state,
      action: PayloadAction<{
        companyId: string;
        recordHistory?: boolean;
        target: ChatConversationTarget;
      }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      const nextTarget = mergeKnownTarget(dock, action.payload.target);
      delete dock.activeDiscussion;
      const currentTarget = dock.activeTarget;
      if (
        action.payload.recordHistory !== false &&
        currentTarget &&
        chatConversationTargetKey(currentTarget) !==
          chatConversationTargetKey(nextTarget)
      ) {
        dock.backStack = appendBoundedTarget(dock.backStack, currentTarget);
      }
      if (!sameConversationTargetDetails(currentTarget, nextTarget)) {
        dock.activeTarget = nextTarget;
      }
      const nextRecentTargets = prependBoundedTarget(
        dock.recentTargets,
        nextTarget,
      );
      if (!sameConversationTargetList(dock.recentTargets, nextRecentTargets)) {
        dock.recentTargets = nextRecentTargets;
      }
    },
    rememberConversationWorkspaceTarget: (
      state,
      action: PayloadAction<{
        companyId: string;
        target: ChatConversationTarget;
      }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      const nextTarget = mergeKnownTarget(dock, action.payload.target);
      if (
        dock.activeTarget &&
        chatConversationTargetKey(dock.activeTarget) ===
          chatConversationTargetKey(nextTarget) &&
        !sameConversationTargetDetails(dock.activeTarget, nextTarget)
      ) {
        dock.activeTarget = nextTarget;
      }
      const nextRecentTargets = prependBoundedTarget(
        dock.recentTargets,
        nextTarget,
      );
      if (!sameConversationTargetList(dock.recentTargets, nextRecentTargets)) {
        dock.recentTargets = nextRecentTargets;
      }
    },
    removeConversationWorkspaceBackTarget: (
      state,
      action: PayloadAction<{ companyId: string; targetKey: string }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      for (let index = dock.backStack.length - 1; index >= 0; index -= 1) {
        if (
          chatConversationTargetKey(dock.backStack[index]) ===
          action.payload.targetKey
        ) {
          dock.backStack.splice(index, 1);
          break;
        }
      }
    },
    setConversationWorkspaceDraft: (
      state,
      action: PayloadAction<{
        companyId: string;
        message: string;
        targetKey: string;
      }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      const message = action.payload.message.slice(0, 12_000);
      if (message) {
        dock.draftsByTargetKey[action.payload.targetKey] = message;
      } else {
        delete dock.draftsByTargetKey[action.payload.targetKey];
      }
      const retainedKeys = new Set(
        [dock.activeTarget, ...dock.recentTargets]
          .filter((target): target is ChatConversationTarget => Boolean(target))
          .slice(0, 20)
          .map(chatConversationTargetKey),
      );
      Object.keys(dock.draftsByTargetKey).forEach((targetKey) => {
        if (
          !retainedKeys.has(targetKey) &&
          targetKey !== action.payload.targetKey
        ) {
          delete dock.draftsByTargetKey[targetKey];
        }
      });
    },
    updateConversationArtifactsPanel: (
      state,
      action: PayloadAction<{
        companyId: string;
        conversationId: string;
        defaultOpen: boolean;
        changes: Partial<
          Pick<
            ConversationArtifactsPanelState,
            "open" | "selection" | "mailSelection" | "feedback"
          >
        >;
      }>,
    ) => {
      const { companyId, conversationId, defaultOpen, changes } =
        action.payload;
      const dock = getCompanyDock(state, companyId);
      const panel = dock.artifactsByConversationId[conversationId] ?? {
        open: defaultOpen,
        selection: null,
        mailSelection: null,
        feedback: null,
        feedbackRevision: 0,
      };
      if (changes.open !== undefined) panel.open = changes.open;
      if (changes.selection !== undefined) {
        panel.selection = normalizeArtifactReference(changes.selection);
        panel.mailSelection = null;
      }
      if (changes.mailSelection !== undefined) {
        panel.mailSelection = changes.mailSelection;
        if (panel.mailSelection) panel.selection = null;
      }
      if (changes.feedback !== undefined) {
        panel.feedback = normalizeArtifactReference(changes.feedback);
        panel.feedbackRevision += 1;
      }
      delete dock.artifactsByConversationId[conversationId];
      dock.artifactsByConversationId[conversationId] = panel;
      Object.keys(dock.artifactsByConversationId)
        .slice(0, -20)
        .forEach((oldConversationId) => {
          delete dock.artifactsByConversationId[oldConversationId];
        });
    },
    clearConversationArtifactFeedback: (
      state,
      action: PayloadAction<{
        companyId: string;
        conversationId: string;
        expectedRevision?: number;
      }>,
    ) => {
      const { companyId, conversationId, expectedRevision } = action.payload;
      const panel =
        state.companies[companyId]?.artifactsByConversationId?.[conversationId];
      if (
        !panel ||
        (expectedRevision !== undefined &&
          panel.feedbackRevision !== expectedRevision)
      ) {
        return;
      }
      panel.feedback = null;
      panel.feedbackRevision += 1;
    },
    showConversationWorkspaceDirectMessage: (
      state,
      action: PayloadAction<{ companyId: string; targetKey: string }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      if (!dock.visibleDirectMessageKeys.includes(action.payload.targetKey)) {
        dock.visibleDirectMessageKeys.push(action.payload.targetKey);
      }
    },
    setConversationWorkspaceNavigatorBrowse: (
      state,
      action: PayloadAction<{
        companyId: string;
        browse: ChatNavigatorBrowseState;
      }>,
    ) => {
      getCompanyDock(state, action.payload.companyId).navigatorBrowse =
        action.payload.browse;
    },
    setConversationWorkspaceNavigatorPreferences: (
      state,
      action: PayloadAction<{
        companyId: string;
        preferences: ChatNavigatorPreferences;
      }>,
    ) => {
      getCompanyDock(state, action.payload.companyId).navigatorPreferences =
        action.payload.preferences;
    },
    rememberConversationWorkspaceTaskChats: (
      state,
      action: PayloadAction<{ companyId: string; taskIds: string[] }>,
    ) => {
      const dock = getCompanyDock(state, action.payload.companyId);
      dock.surfacedTaskIds = [
        ...new Set([...action.payload.taskIds, ...dock.surfacedTaskIds]),
      ].slice(0, 100);
    },
    /**
     * Forgets Task chats the viewer can no longer read (ADR 0017): their dock
     * tabs, history, drafts and titles, which are also kept on disk.
     */
    forgetConversationWorkspaceTasks: (
      state,
      action: PayloadAction<{ companyId: string; taskIds: string[] }>,
    ) => {
      const dock = state.companies[action.payload.companyId];
      if (!dock || !Array.isArray(dock.tabs)) return;
      const gone = new Set(action.payload.taskIds);
      const isGone = (target: ChatConversationTarget | undefined) =>
        target?.kind === "task" && gone.has(target.taskId);
      const activeTabGone = dock.tabs.some(
        (tab) =>
          tab.id === dock.activeTabId && tab.taskId && gone.has(tab.taskId),
      );
      dock.tabs = dock.tabs.filter(
        (tab) => !(tab.taskId && gone.has(tab.taskId)),
      );
      if (activeTabGone) dock.activeTabId = dock.tabs.at(-1)?.id ?? null;
      if (isGone(dock.activeTarget)) delete dock.activeTarget;
      dock.backStack = (dock.backStack ?? []).filter(
        (target) => !isGone(target),
      );
      dock.recentTargets = (dock.recentTargets ?? []).filter(
        (target) => !isGone(target),
      );
      dock.surfacedTaskIds = (dock.surfacedTaskIds ?? []).filter(
        (taskId) => !gone.has(taskId),
      );
      for (const taskId of gone) {
        delete dock.draftsByTargetKey?.[
          chatConversationTargetKey({ kind: "task", taskId })
        ];
      }
    },
  },
});

export const {
  clearVisibleConversation,
  setVisibleConversation,
  activateConversationDockTab,
  activateConversationWorkspaceDiscussion,
  activateConversationWorkspaceTarget,
  closeConversationDockTab,
  clearConversationArtifactFeedback,
  forgetConversationWorkspaceTasks,
  openConversationDockTab,
  rememberConversationWorkspaceTarget,
  rememberConversationWorkspaceTaskChats,
  removeConversationWorkspaceBackTarget,
  setConversationWorkspaceDraft,
  setConversationWorkspaceNavigatorBrowse,
  setConversationWorkspaceNavigatorPreferences,
  showConversationWorkspaceDirectMessage,
  updateConversationArtifactsPanel,
} = conversationDockSlice.actions;

export const conversationDockReducer = conversationDockSlice.reducer;

export const selectCompanyConversationDock = (
  state: { conversationDock: ConversationDockState },
  companyId: string,
): CompanyConversationDock => {
  const dock = state.conversationDock?.companies?.[companyId];

  return dock && Array.isArray(dock.tabs) ? dock : emptyCompanyDock;
};

export const selectCompanyConversationWorkspace = selectCompanyConversationDock;

export function selectConversationWorkspaceDraft(
  state: { conversationDock: ConversationDockState },
  companyId: string,
  targetKey: string,
) {
  return (
    state.conversationDock?.companies?.[companyId]?.draftsByTargetKey?.[
      targetKey
    ] ?? ""
  );
}

export function selectConversationArtifactsPanel(
  state: { conversationDock: ConversationDockState },
  companyId: string,
  conversationId: string,
) {
  return state.conversationDock?.companies?.[companyId]
    ?.artifactsByConversationId?.[conversationId];
}

export function normalizeConversationDockState(
  value: unknown,
): ConversationDockState {
  if (!isRecord(value) || !isRecord(value.companies)) {
    return initialState;
  }

  const companies = Object.entries(value.companies).reduce<
    Record<string, CompanyConversationDock>
  >((normalizedCompanies, [companyId, rawDock]) => {
    if (!companyId || !isRecord(rawDock) || !Array.isArray(rawDock.tabs)) {
      return normalizedCompanies;
    }

    const tabsById = new Map<string, ConversationDockTab>();
    rawDock.tabs.slice(-20).forEach((rawTab) => {
      const tab = normalizeConversationDockTab(rawTab);
      if (tab) {
        tabsById.set(tab.id, tab);
      }
    });
    const tabs = [...tabsById.values()];
    const requestedActiveTabId =
      typeof rawDock.activeTabId === "string" ? rawDock.activeTabId : null;
    const activeTabId = tabs.some((tab) => tab.id === requestedActiveTabId)
      ? requestedActiveTabId
      : (tabs.at(-1)?.id ?? null);

    const activeTarget = normalizeConversationTarget(rawDock.activeTarget);
    const migratedTargets = tabs.flatMap((tab) => {
      const target = targetFromDockTab(tab);
      return target ? [target] : [];
    });
    const recentTargets = normalizeTargetList(rawDock.recentTargets);
    const normalizedRecentTargets = recentTargets.length
      ? recentTargets
      : migratedTargets.reverse();
    const normalizedActiveTarget =
      activeTarget ??
      targetFromDockTab(
        tabs.find((tab) => tab.id === activeTabId) ?? tabs.at(-1),
      );

    normalizedCompanies[companyId] = {
      activeTabId,
      ...(normalizedActiveTarget
        ? { activeTarget: normalizedActiveTarget }
        : {}),
      backStack: normalizeTargetList(rawDock.backStack),
      draftsByTargetKey: normalizeDrafts(rawDock.draftsByTargetKey),
      artifactsByConversationId: normalizeArtifactPanels(
        rawDock.artifactsByConversationId,
      ),
      navigatorBrowse: normalizeNavigatorBrowse(rawDock.navigatorBrowse),
      navigatorPreferences: normalizeNavigatorPreferences(
        rawDock.navigatorPreferences,
      ),
      surfacedTaskIds: Array.isArray(rawDock.surfacedTaskIds)
        ? [
            ...new Set(
              rawDock.surfacedTaskIds.flatMap((value) => {
                const id = canonicalUuid(value);
                return id ? [id] : [];
              }),
            ),
          ].slice(0, 100)
        : [],
      recentTargets: normalizedRecentTargets.slice(0, 20),
      visibleDirectMessageKeys: Array.isArray(rawDock.visibleDirectMessageKeys)
        ? [
            ...new Set(
              rawDock.visibleDirectMessageKeys.filter(
                (key): key is string =>
                  typeof key === "string" &&
                  ((key.startsWith("direct-agent:") &&
                    key.length > "direct-agent:".length) ||
                    (key.startsWith("direct-conversation:") &&
                      key.length > "direct-conversation:".length)),
              ),
            ),
          ]
        : [],
      tabs,
    };
    return normalizedCompanies;
  }, {});

  return { companies };
}

function createEmptyCompanyDock(): CompanyConversationDock {
  return {
    activeTabId: null,
    backStack: [],
    draftsByTargetKey: {},
    artifactsByConversationId: {},
    navigatorBrowse: { ...initialChatNavigatorBrowse },
    navigatorPreferences: { ...initialChatNavigatorPreferences },
    surfacedTaskIds: [],
    recentTargets: [],
    visibleDirectMessageKeys: [],
    tabs: [],
  };
}

function normalizeNavigatorBrowse(value: unknown): ChatNavigatorBrowseState {
  if (!isRecord(value)) return { ...initialChatNavigatorBrowse };
  return {
    view:
      value.view === "agents" || value.view === "archived"
        ? value.view
        : "recent",
    agentId: typeof value.agentId === "string" ? value.agentId : null,
    query: typeof value.query === "string" ? value.query.slice(0, 200) : "",
  };
}

function normalizeNavigatorPreferences(
  value: unknown,
): ChatNavigatorPreferences {
  if (!isRecord(value)) return { ...initialChatNavigatorPreferences };
  return {
    collapsedSections: chatNavigatorSections.filter(
      (section) =>
        Array.isArray(value.collapsedSections) &&
        value.collapsedSections.includes(section),
    ),
  };
}

function mergeKnownTarget(
  dock: CompanyConversationDock,
  target: ChatConversationTarget,
) {
  const targetKey = chatConversationTargetKey(target);
  const knownTarget = [dock.activeTarget, ...dock.recentTargets].find(
    (candidate) =>
      candidate && chatConversationTargetKey(candidate) === targetKey,
  );
  if (knownTarget?.kind === "direct" && target.kind === "direct") {
    return { ...knownTarget, ...target };
  }
  if (knownTarget?.kind === "task" && target.kind === "task") {
    return { ...knownTarget, ...target };
  }
  return target;
}

function prependBoundedTarget(
  targets: ChatConversationTarget[],
  target: ChatConversationTarget,
) {
  const targetKey = chatConversationTargetKey(target);
  return [
    target,
    ...targets.filter(
      (candidate) => chatConversationTargetKey(candidate) !== targetKey,
    ),
  ].slice(0, 20);
}

function appendBoundedTarget(
  targets: ChatConversationTarget[],
  target: ChatConversationTarget,
) {
  const targetKey = chatConversationTargetKey(target);
  return [
    ...targets.filter(
      (candidate) => chatConversationTargetKey(candidate) !== targetKey,
    ),
    target,
  ].slice(-20);
}

function sameConversationTargetDetails(
  left: ChatConversationTarget | undefined,
  right: ChatConversationTarget | undefined,
) {
  if (!left || !right) return left === right;
  return (
    chatConversationTargetKey(left) === chatConversationTargetKey(right) &&
    left.agentId === right.agentId &&
    left.conversationId === right.conversationId
  );
}

function sameConversationTargetList(
  left: ChatConversationTarget[],
  right: ChatConversationTarget[],
) {
  return (
    left.length === right.length &&
    left.every((target, index) =>
      sameConversationTargetDetails(target, right[index]),
    )
  );
}

function normalizeConversationTarget(
  value: unknown,
): ChatConversationTarget | undefined {
  if (!isChatConversationTarget(value)) {
    return undefined;
  }
  return value.kind === "task"
    ? {
        ...(value.agentId ? { agentId: value.agentId } : {}),
        ...(value.conversationId
          ? { conversationId: value.conversationId }
          : {}),
        kind: "task",
        taskId: value.taskId,
      }
    : {
        agentId: value.agentId,
        ...(value.conversationId
          ? { conversationId: value.conversationId }
          : {}),
        kind: "direct",
      };
}

function normalizeTargetList(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  const targetsByKey = new Map<string, ChatConversationTarget>();
  value.slice(-20).forEach((candidate) => {
    const target = normalizeConversationTarget(candidate);
    if (target) targetsByKey.set(chatConversationTargetKey(target), target);
  });
  return [...targetsByKey.values()];
}

function normalizeDrafts(value: unknown) {
  if (!isRecord(value)) {
    return {};
  }
  return Object.fromEntries(
    Object.entries(value)
      .filter(
        ([key, message]) =>
          Boolean(key) && typeof message === "string" && message.length > 0,
      )
      .slice(-20)
      .map(([key, message]) => [key, String(message).slice(0, 12_000)]),
  );
}

function normalizeArtifactPanels(value: unknown) {
  if (!isRecord(value) || Array.isArray(value)) return {};
  return Object.fromEntries(
    Object.entries(value)
      .slice(-20)
      .flatMap(([conversationId, panel]) => {
        if (
          canonicalUuid(conversationId) === undefined ||
          !isRecord(panel) ||
          Array.isArray(panel)
        ) {
          return [];
        }
        return [
          [
            conversationId,
            {
              open: panel.open === true,
              selection: normalizeArtifactReference(panel.selection),
              mailSelection: normalizeMailSelection(panel.mailSelection),
              feedback: normalizeArtifactReference(panel.feedback),
              feedbackRevision:
                Number.isSafeInteger(panel.feedbackRevision) &&
                Number(panel.feedbackRevision) >= 0
                  ? Number(panel.feedbackRevision)
                  : 0,
            } satisfies ConversationArtifactsPanelState,
          ],
        ];
      }),
  );
}

function normalizeMailSelection(value: unknown) {
  if (!isRecord(value)) return null;
  const groupId = canonicalUuid(value.groupId);
  const sourceRunId = canonicalUuid(value.sourceRunId);
  return groupId && sourceRunId ? { groupId, sourceRunId } : null;
}

function normalizeArtifactReference(
  value: unknown,
): ConversationArtifactReference | null {
  if (!isRecord(value) || Array.isArray(value)) return null;
  const artifactId = canonicalUuid(value.artifactId);
  // A Whiteboard, or an Artifact kept after its Run, has no Run.
  const runId = value.runId === null ? null : canonicalUuid(value.runId);
  const title =
    typeof value.title === "string" ? value.title.slice(0, 2_048) : "";
  if (
    artifactId === undefined ||
    runId === undefined ||
    !title ||
    (value.type !== "app" &&
      value.type !== "media" &&
      value.type !== "text" &&
      value.type !== "file" &&
      value.type !== "whiteboard")
  ) {
    return null;
  }
  return { artifactId, runId, title, type: value.type };
}

function targetFromDockTab(
  tab: ConversationDockTab | undefined,
): ChatConversationTarget | undefined {
  if (!tab) return undefined;
  if (tab.kind === "task" && tab.taskId) {
    return {
      ...(tab.agentId ? { agentId: tab.agentId } : {}),
      ...(tab.conversationId ? { conversationId: tab.conversationId } : {}),
      kind: "task",
      taskId: tab.taskId,
    };
  }
  if (tab.kind === "direct" && tab.agentId) {
    return {
      agentId: tab.agentId,
      ...(tab.conversationId ? { conversationId: tab.conversationId } : {}),
      kind: "direct",
    };
  }
  return undefined;
}

function normalizeConversationDockTab(value: unknown) {
  if (
    !isRecord(value) ||
    typeof value.id !== "string" ||
    typeof value.avatarSrc !== "string" ||
    typeof value.title !== "string" ||
    typeof value.subtitle !== "string" ||
    (value.kind !== "direct" && value.kind !== "task") ||
    !optionalString(value.teamId)
  ) {
    return null;
  }

  const taskId = optionalString(value.taskId);
  if (value.kind === "task" && !taskId) {
    return null;
  }

  return {
    ...(optionalString(value.agentId)
      ? { agentId: optionalString(value.agentId) }
      : {}),
    avatarSrc: value.avatarSrc,
    ...(optionalString(value.conversationId)
      ? { conversationId: optionalString(value.conversationId) }
      : {}),
    id: value.id,
    kind: value.kind,
    subtitle: value.subtitle,
    ...(taskId ? { taskId } : {}),
    teamId: value.teamId as string,
    title: value.title,
  } satisfies ConversationDockTab;
}

function optionalString(value: unknown) {
  return typeof value === "string" && value ? value : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export type { ConversationDockState };
